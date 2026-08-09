package recording

import (
	"errors"
)

const (
	tsPacketBytes     = 188
	maxPSISection     = 1024
	maxElementaryPIDs = 64
	maxPSIBuffer      = 1024 * 1024
)

var errTSFormat = errors.New("recording: invalid MPEG-TS format")

// tsComponentFilterは一つの録画接続に限ってPATとPMTを追跡し、選択外のPIDを除く。
// 入力全体は保持せず、最初のPMTまたは更新中のPMTを待つ間だけ固定上限まで保持する。
type tsComponentFilter struct {
	file              PartialFile
	keepCaptions      bool
	keepData          bool
	initialized       bool
	pmtPID            uint16
	pmtContinuity     byte
	dropped           map[uint16]bool
	tail              []byte
	discovery         []byte
	discoveryScanned  int
	discoveryPAT      psiCollector
	discoveryPMT      psiCollector
	discovered        discoveredPMT
	discoveryPMTKnown bool
	updatePackets     [][]byte
	updatePMTIndexes  []int
	updateCollector   psiCollector
}

type psiCollector struct {
	section []byte
	want    int
}

type discoveredPMT struct {
	pmtPID      uint16
	section     []byte
	firstPacket int
	lastPacket  int
	continuity  byte
}

// newTSComponentFilterは選別後のpacketだけを部分ファイルへ渡す接続単位の状態を作る。
func newTSComponentFilter(file PartialFile, keepCaptions, keepData bool) *tsComponentFilter {
	return &tsComponentFilter{file: file, keepCaptions: keepCaptions, keepData: keepData}
}

// Writeは任意のread境界をTS packetへ組み立て、実際に保存したbyte数を返す。
func (filter *tsComponentFilter) Write(data []byte) (int64, error) {
	if !filter.initialized {
		if len(filter.discovery) > maxPSIBuffer-len(data) {
			return 0, errTSFormat
		}
		filter.discovery = append(filter.discovery, data...)
		found, ok, err := filter.scanDiscovery()
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, nil
		}
		rewritten, dropped, err := rewritePMT(found.section, filter.keepCaptions, filter.keepData)
		if err != nil {
			return 0, err
		}
		filter.pmtPID, filter.dropped, filter.initialized = found.pmtPID, dropped, true
		consumed := (found.lastPacket + 1) * tsPacketBytes
		written, err := filter.replayInitial(filter.discovery[:consumed], found, rewritten)
		if err != nil {
			return written, err
		}
		rest := append([]byte(nil), filter.discovery[consumed:]...)
		filter.discovery = nil
		more, err := filter.writeInitialized(rest)
		return written + more, err
	}
	return filter.writeInitialized(data)
}

// scanDiscoveryは新しく増えた完全なpacketだけを一度ずつ調べ、最初のPATとPMTを探す。
func (filter *tsComponentFilter) scanDiscovery() (discoveredPMT, bool, error) {
	for filter.discoveryScanned+tsPacketBytes <= len(filter.discovery) {
		offset := filter.discoveryScanned
		index := offset / tsPacketBytes
		packet := filter.discovery[offset : offset+tsPacketBytes]
		filter.discoveryScanned += tsPacketBytes
		pid, start, continuity, err := tsPacketHeader(packet)
		if err != nil {
			return discoveredPMT{}, false, err
		}
		if !filter.discoveryPMTKnown && pid == 0 {
			section, done, err := filter.discoveryPAT.feed(packet)
			if err != nil {
				return discoveredPMT{}, false, err
			}
			if done {
				filter.discovered.pmtPID, err = parsePAT(section)
				if err != nil {
					return discoveredPMT{}, false, err
				}
				filter.discoveryPMTKnown = true
			}
		}
		if !filter.discoveryPMTKnown || pid != filter.discovered.pmtPID {
			continue
		}
		if len(filter.discoveryPMT.section) == 0 {
			if !start {
				continue
			}
			filter.discovered.firstPacket, filter.discovered.continuity = index, continuity
		}
		section, done, err := filter.discoveryPMT.feed(packet)
		if err != nil {
			return discoveredPMT{}, false, err
		}
		if done {
			filter.discovered.section, filter.discovered.lastPacket = section, index
			return filter.discovered, true, nil
		}
	}
	return discoveredPMT{}, false, nil
}

// Finishは接続終了時に未完のpacketやPSI sectionが残っていないことを確認する。
func (filter *tsComponentFilter) Finish() error {
	if !filter.initialized || len(filter.tail) != 0 || len(filter.updatePackets) != 0 {
		return errTSFormat
	}
	return nil
}

func (filter *tsComponentFilter) replayInitial(data []byte, found discoveredPMT, rewritten []byte) (int64, error) {
	var written int64
	for index := 0; index <= found.lastPacket; index++ {
		if index == found.firstPacket {
			packets := packetizePMT(found.pmtPID, found.continuity, rewritten)
			count, err := filter.writePacketList(packets)
			filter.pmtContinuity = (found.continuity + byte(len(packets))) & 0x0f
			written += count
			if err != nil {
				return written, err
			}
		}
		if index >= found.firstPacket && index <= found.lastPacket {
			continue
		}
		packet := data[index*tsPacketBytes : (index+1)*tsPacketBytes]
		count, err := filter.writeSelected(packet)
		written += count
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (filter *tsComponentFilter) writeInitialized(data []byte) (int64, error) {
	combined := append(filter.tail, data...)
	complete := len(combined) / tsPacketBytes * tsPacketBytes
	var written int64
	for offset := 0; offset < complete; offset += tsPacketBytes {
		packet := append([]byte(nil), combined[offset:offset+tsPacketBytes]...)
		count, err := filter.processPacket(packet)
		written += count
		if err != nil {
			return written, err
		}
	}
	filter.tail = append(filter.tail[:0], combined[complete:]...)
	return written, nil
}

func (filter *tsComponentFilter) processPacket(packet []byte) (int64, error) {
	pid, start, _, err := tsPacketHeader(packet)
	if err != nil {
		return 0, err
	}
	if len(filter.updatePackets) == 0 && pid != filter.pmtPID {
		return filter.writeSelected(packet)
	}
	if len(filter.updatePackets) == 0 && !start {
		return 0, errTSFormat
	}
	if len(filter.updatePackets) >= maxPSIBuffer/tsPacketBytes {
		return 0, errTSFormat
	}
	filter.updatePackets = append(filter.updatePackets, packet)
	if pid != filter.pmtPID {
		return 0, nil
	}
	filter.updatePMTIndexes = append(filter.updatePMTIndexes, len(filter.updatePackets)-1)
	section, done, err := filter.updateCollector.feed(packet)
	if err != nil {
		return 0, err
	}
	if !done {
		return 0, nil
	}
	rewritten, dropped, err := rewritePMT(section, filter.keepCaptions, filter.keepData)
	if err != nil {
		return 0, err
	}
	first := filter.updatePMTIndexes[0]
	last := filter.updatePMTIndexes[len(filter.updatePMTIndexes)-1]
	packets := packetizePMT(filter.pmtPID, filter.pmtContinuity, rewritten)
	var written int64
	for index, pending := range filter.updatePackets {
		if index == first {
			count, writeErr := filter.writePacketList(packets)
			written += count
			if writeErr != nil {
				return written, writeErr
			}
		}
		if index >= first && index <= last && packetPID(pending) == filter.pmtPID {
			continue
		}
		count, writeErr := filter.writeSelectedWith(pending, dropped)
		written += count
		if writeErr != nil {
			return written, writeErr
		}
	}
	filter.dropped = dropped
	filter.pmtContinuity = (filter.pmtContinuity + byte(len(packets))) & 0x0f
	filter.updatePackets = nil
	filter.updatePMTIndexes = nil
	filter.updateCollector = psiCollector{}
	return written, nil
}

func (filter *tsComponentFilter) writeSelected(packet []byte) (int64, error) {
	return filter.writeSelectedWith(packet, filter.dropped)
}

func (filter *tsComponentFilter) writeSelectedWith(packet []byte, dropped map[uint16]bool) (int64, error) {
	pid, _, _, err := tsPacketHeader(packet)
	if err != nil {
		return 0, err
	}
	if dropped[pid] {
		return 0, nil
	}
	return filter.writePacket(packet)
}

func (filter *tsComponentFilter) writePacketList(packets [][]byte) (int64, error) {
	var written int64
	for _, packet := range packets {
		count, err := filter.writePacket(packet)
		written += count
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (filter *tsComponentFilter) writePacket(packet []byte) (int64, error) {
	written, err := filter.file.Write(packet)
	if written < 0 || written > len(packet) {
		return 0, errors.New("recording: invalid TS write count")
	}
	if err != nil {
		return int64(written), err
	}
	if written != len(packet) {
		return int64(written), errors.New("recording: short TS write")
	}
	return int64(written), nil
}

func (collector *psiCollector) feed(packet []byte) ([]byte, bool, error) {
	_, start, _, err := tsPacketHeader(packet)
	if err != nil {
		return nil, false, err
	}
	payload, err := tsPayload(packet)
	if err != nil {
		return nil, false, err
	}
	if len(payload) == 0 {
		return nil, false, nil
	}
	if start {
		pointer := int(payload[0])
		if pointer > len(payload)-1 {
			return nil, false, errTSFormat
		}
		if len(collector.section) != 0 {
			collector.section = append(collector.section, payload[1:1+pointer]...)
			if collector.want == 0 || len(collector.section) != collector.want {
				return nil, false, errTSFormat
			}
			section := append([]byte(nil), collector.section...)
			*collector = psiCollector{}
			return section, true, nil
		}
		payload = payload[1+pointer:]
		if len(payload) == 0 || payload[0] == 0xff {
			return nil, false, nil
		}
		collector.section = append(collector.section, payload...)
	} else if len(collector.section) != 0 {
		collector.section = append(collector.section, payload...)
	} else {
		return nil, false, nil
	}
	if len(collector.section) >= 3 && collector.want == 0 {
		collector.want = 3 + int(collector.section[1]&0x0f)<<8 + int(collector.section[2])
		if collector.want < 12 || collector.want > maxPSISection {
			return nil, false, errTSFormat
		}
	}
	if collector.want != 0 && len(collector.section) >= collector.want {
		section := append([]byte(nil), collector.section[:collector.want]...)
		*collector = psiCollector{}
		return section, true, nil
	}
	if len(collector.section) > maxPSISection {
		return nil, false, errTSFormat
	}
	return nil, false, nil
}

func parsePAT(section []byte) (uint16, error) {
	if len(section) < 12 || section[0] != 0x00 || section[1]&0x80 == 0 || !validMPEGCRC(section) ||
		section[6] != 0 || section[7] != 0 || (len(section)-12)%4 != 0 {
		return 0, errTSFormat
	}
	programs := 0
	var pmtPID uint16
	for offset := 8; offset < len(section)-4; offset += 4 {
		program := uint16(section[offset])<<8 | uint16(section[offset+1])
		if program == 0 {
			continue
		}
		programs++
		pmtPID = uint16(section[offset+2]&0x1f)<<8 | uint16(section[offset+3])
	}
	if programs != 1 || pmtPID == 0 || pmtPID == 0x1fff {
		return 0, errTSFormat
	}
	return pmtPID, nil
}

func rewritePMT(section []byte, keepCaptions, keepData bool) ([]byte, map[uint16]bool, error) {
	if len(section) < 16 || section[0] != 0x02 || section[1]&0x80 == 0 || !validMPEGCRC(section) ||
		section[6] != 0 || section[7] != 0 {
		return nil, nil, errTSFormat
	}
	pcrPID := uint16(section[8]&0x1f)<<8 | uint16(section[9])
	programInfo := int(section[10]&0x0f)<<8 | int(section[11])
	offset := 12 + programInfo
	end := len(section) - 4
	if offset > end {
		return nil, nil, errTSFormat
	}
	result := append([]byte(nil), section[:offset]...)
	dropped := make(map[uint16]bool)
	streams := 0
	for offset < end {
		if end-offset < 5 {
			return nil, nil, errTSFormat
		}
		info := int(section[offset+3]&0x0f)<<8 | int(section[offset+4])
		next := offset + 5 + info
		if next > end {
			return nil, nil, errTSFormat
		}
		streams++
		if streams > maxElementaryPIDs {
			return nil, nil, errTSFormat
		}
		pid := uint16(section[offset+1]&0x1f)<<8 | uint16(section[offset+2])
		typeValue := section[offset]
		drop := typeValue == 0x06 && !keepCaptions || typeValue == 0x0d && !keepData
		if drop {
			if pid != pcrPID {
				dropped[pid] = true
			}
		} else {
			result = append(result, section[offset:next]...)
		}
		offset = next
	}
	if offset != end {
		return nil, nil, errTSFormat
	}
	sectionLength := len(result) + 4 - 3
	if sectionLength > 0x03ff {
		return nil, nil, errTSFormat
	}
	result[1] = result[1]&0xf0 | byte(sectionLength>>8)
	result[2] = byte(sectionLength)
	version := (result[5] >> 1) & 0x1f
	result[5] = result[5]&0xc1 | (((version + 1) & 0x1f) << 1)
	crc := mpegCRC(result)
	result = append(result, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	return result, dropped, nil
}

func packetizePMT(pid uint16, continuity byte, section []byte) [][]byte {
	remaining := section
	first := true
	var result [][]byte
	for len(remaining) != 0 {
		packet := make([]byte, tsPacketBytes)
		for index := range packet {
			packet[index] = 0xff
		}
		packet[0] = 0x47
		packet[1] = byte(pid >> 8 & 0x1f)
		if first {
			packet[1] |= 0x40
		}
		packet[2] = byte(pid)
		packet[3] = 0x10 | continuity&0x0f
		start := 4
		if first {
			packet[start] = 0
			start++
		}
		count := min(len(remaining), tsPacketBytes-start)
		copy(packet[start:start+count], remaining[:count])
		remaining = remaining[count:]
		result = append(result, packet)
		continuity = (continuity + 1) & 0x0f
		first = false
	}
	return result
}

func tsPacketHeader(packet []byte) (uint16, bool, byte, error) {
	if len(packet) != tsPacketBytes || packet[0] != 0x47 || packet[1]&0x80 != 0 {
		return 0, false, 0, errTSFormat
	}
	control := packet[3] >> 4 & 0x03
	if control == 0 {
		return 0, false, 0, errTSFormat
	}
	pid := uint16(packet[1]&0x1f)<<8 | uint16(packet[2])
	return pid, packet[1]&0x40 != 0, packet[3] & 0x0f, nil
}

func tsPayload(packet []byte) ([]byte, error) {
	_, _, _, err := tsPacketHeader(packet)
	if err != nil {
		return nil, err
	}
	control := packet[3] >> 4 & 0x03
	if control == 2 {
		return nil, nil
	}
	offset := 4
	if control == 3 {
		length := int(packet[4])
		offset = 5 + length
		if offset > len(packet) {
			return nil, errTSFormat
		}
	}
	return packet[offset:], nil
}

func packetPID(packet []byte) uint16 {
	return uint16(packet[1]&0x1f)<<8 | uint16(packet[2])
}

// MPEG-2 CRC-32は初期値を反転せず、上位bitから多項式を適用する。
func mpegCRC(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, value := range data {
		crc ^= uint32(value) << 24
		for range 8 {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func validMPEGCRC(section []byte) bool {
	return len(section) >= 4 && mpegCRC(section) == 0
}
