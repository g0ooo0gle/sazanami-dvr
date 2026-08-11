package recording

import (
	"errors"

	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

const (
	tsPacketBytes     = mpegts.PacketBytes
	maxElementaryPIDs = mpegts.MaxElementaryStreams
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
	discoveryPAT      mpegts.PSICollector
	discoveryPMT      mpegts.PSICollector
	discovered        discoveredPMT
	discoveryPMTKnown bool
	updatePackets     [][]byte
	updatePMTIndexes  []int
	updateCollector   mpegts.PSICollector
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
		filter.updateCollector = filter.discoveryPMT
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
		parsed, err := mpegts.ParsePacket(packet)
		if err != nil {
			return discoveredPMT{}, false, invalidTS(err)
		}
		if !filter.discoveryPMTKnown && parsed.PID == 0 {
			sections, feedErr := filter.discoveryPAT.Feed(packet)
			if feedErr != nil {
				return discoveredPMT{}, false, invalidTS(feedErr)
			}
			if len(sections) != 0 {
				pat, parseErr := mpegts.ParsePAT(sections[len(sections)-1])
				if parseErr != nil {
					return discoveredPMT{}, false, invalidTS(parseErr)
				}
				filter.discovered.pmtPID = pat.PMTPID
				filter.discoveryPMTKnown = true
			}
		}
		if !filter.discoveryPMTKnown || parsed.PID != filter.discovered.pmtPID {
			continue
		}
		if parsed.PayloadUnitStart {
			filter.discovered.firstPacket = index
			filter.discovered.continuity = parsed.ContinuityCounter
		}
		sections, feedErr := filter.discoveryPMT.Feed(packet)
		if feedErr != nil {
			return discoveredPMT{}, false, invalidTS(feedErr)
		}
		if len(sections) != 0 {
			filter.discovered.section = sections[len(sections)-1]
			filter.discovered.lastPacket = index
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
			packets, err := mpegts.PacketizeSection(found.pmtPID, found.continuity, rewritten)
			if err != nil {
				return written, invalidTS(err)
			}
			count, writeErr := filter.writePacketList(packets)
			filter.pmtContinuity = (found.continuity + byte(len(packets))) & 0x0f
			written += count
			if writeErr != nil {
				return written, writeErr
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
	parsed, err := mpegts.ParsePacket(packet)
	if err != nil {
		return 0, invalidTS(err)
	}
	if len(filter.updatePackets) == 0 && parsed.PID != filter.pmtPID {
		return filter.writeSelected(packet)
	}
	if len(filter.updatePackets) == 0 && !parsed.PayloadUnitStart {
		return 0, errTSFormat
	}
	if len(filter.updatePackets) >= maxPSIBuffer/tsPacketBytes {
		return 0, errTSFormat
	}
	filter.updatePackets = append(filter.updatePackets, packet)
	if parsed.PID != filter.pmtPID {
		return 0, nil
	}
	filter.updatePMTIndexes = append(filter.updatePMTIndexes, len(filter.updatePackets)-1)
	sections, feedErr := filter.updateCollector.Feed(packet)
	if feedErr != nil {
		return 0, invalidTS(feedErr)
	}
	if len(sections) == 0 {
		return 0, nil
	}
	section := sections[len(sections)-1]
	rewritten, dropped, err := rewritePMT(section, filter.keepCaptions, filter.keepData)
	if err != nil {
		return 0, err
	}
	first := filter.updatePMTIndexes[0]
	last := filter.updatePMTIndexes[len(filter.updatePMTIndexes)-1]
	packets, err := mpegts.PacketizeSection(filter.pmtPID, filter.pmtContinuity, rewritten)
	if err != nil {
		return 0, invalidTS(err)
	}
	var written int64
	for index, pending := range filter.updatePackets {
		if index == first {
			count, writeErr := filter.writePacketList(packets)
			written += count
			if writeErr != nil {
				return written, writeErr
			}
		}
		if index >= first && index <= last && mpegts.PID(pending) == filter.pmtPID {
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
	return written, nil
}

func (filter *tsComponentFilter) writeSelected(packet []byte) (int64, error) {
	return filter.writeSelectedWith(packet, filter.dropped)
}

func (filter *tsComponentFilter) writeSelectedWith(packet []byte, dropped map[uint16]bool) (int64, error) {
	parsed, err := mpegts.ParsePacket(packet)
	if err != nil {
		return 0, invalidTS(err)
	}
	if dropped[parsed.PID] {
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

func rewritePMT(section []byte, keepCaptions, keepData bool) ([]byte, map[uint16]bool, error) {
	parsed, err := mpegts.ParsePMT(section)
	if err != nil {
		return nil, nil, invalidTS(err)
	}
	programInfo := int(section[10]&0x0f)<<8 | int(section[11])
	offset := 12 + programInfo
	end := len(section) - 4
	result := append([]byte(nil), section[:offset]...)
	dropped := make(map[uint16]bool)
	for offset < end {
		info := int(section[offset+3]&0x0f)<<8 | int(section[offset+4])
		next := offset + 5 + info
		pid := uint16(section[offset+1]&0x1f)<<8 | uint16(section[offset+2])
		typeValue := section[offset]
		drop := typeValue == 0x06 && !keepCaptions || typeValue == 0x0d && !keepData
		if drop {
			if pid != parsed.PCRPID {
				dropped[pid] = true
			}
		} else {
			result = append(result, section[offset:next]...)
		}
		offset = next
	}
	sectionLength := len(result) + 4 - 3
	if sectionLength > 0x0fff {
		return nil, nil, errTSFormat
	}
	result[1] = result[1]&0xf0 | byte(sectionLength>>8)
	result[2] = byte(sectionLength)
	version := (result[5] >> 1) & 0x1f
	result[5] = result[5]&0xc1 | (((version + 1) & 0x1f) << 1)
	crc := mpegts.CRC32(result)
	result = append(result, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	return result, dropped, nil
}

func invalidTS(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(errTSFormat, err)
}
