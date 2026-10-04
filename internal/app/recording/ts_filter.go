package recording

import (
	"bytes"
	"errors"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

const (
	tsPacketBytes     = mpegts.PacketBytes
	maxElementaryPIDs = mpegts.MaxElementaryStreams
	maxPSIBuffer      = 1024 * 1024
)

var errTSFormat = errors.New("recording: invalid MPEG-TS format")

// tsComponentFilterは録画接続ごとに回復・選別を行い、segmentの品質だけを引き継ぐ。
type tsComponentFilter struct {
	file                                                         PartialFile
	keepCaptions, keepData, initialized, pmtKnown, raw, finished bool
	pmtPID                                                       uint16
	pmtContinuity                                                byte
	dropped                                                      map[uint16]bool
	framer                                                       tsFramer
	generation                                                   uint64
	quality                                                      core.QualitySummary
	tracker                                                      tsQualityTracker
	pat, pmt                                                     recordingPSI
	discovery                                                    []byte
	firstPMT                                                     int
	pending                                                      []byte
}

func newTSComponentFilter(file PartialFile, keepCaptions, keepData bool, initial core.QualitySummary) *tsComponentFilter {
	return &tsComponentFilter{file: file, keepCaptions: keepCaptions, keepData: keepData, quality: initial,
		raw: initial.SelectionUnverified, firstPMT: -1}
}

// Qualityは現在までに観測した品質の要約を返す。
func (filter *tsComponentFilter) Quality() core.QualitySummary { return filter.quality }
func (filter *tsComponentFilter) selectionRequired() bool {
	return !filter.keepCaptions || !filter.keepData
}

// Writeは入力を188-byte単位で選別し、実際に保存したbyte数を返す。
func (filter *tsComponentFilter) Write(data []byte) (int64, error) {
	return filter.framer.Write(data, &filter.quality, filter.processPacket)
}

func (filter *tsComponentFilter) issue(issue psiIssue) {
	q := &filter.quality
	switch issue {
	case psiContinuity:
		addQuality(q, &q.PSIContinuityEvents, 1)
	case psiCRC:
		addQuality(q, &q.PSICRCEvents, 1)
	case psiStructure:
		addQuality(q, &q.PSIStructureEvents, 1)
	case psiLimit:
		addQuality(q, &q.PSILimitEvents, 1)
	}
}

func (filter *tsComponentFilter) resetPSI() {
	if filter.pat.incomplete() {
		addQuality(&filter.quality, &filter.quality.UnfinishedPSIEvents, 1)
	}
	if filter.pmt.incomplete() {
		addQuality(&filter.quality, &filter.quality.UnfinishedPSIEvents, 1)
	}
	filter.pat.reset()
	filter.pmt.reset()
	filter.tracker.reset()
}

func (filter *tsComponentFilter) processPacket(packet []byte) (int64, error) {
	var written int64
	if filter.generation != filter.framer.generation {
		filter.generation = filter.framer.generation
		filter.resetPSI()
		if len(filter.pending) > 0 {
			n, err := filter.fallback()
			written += n
			if err != nil {
				return written, err
			}
		}
	}
	parsed, tei, err := parseRecordingPacket(packet)
	if err != nil {
		addQuality(&filter.quality, &filter.quality.MalformedPacketEvents, 1)
		pid := uint16(packet[1]&0x1f)<<8 | uint16(packet[2])
		control := pid == 0 || filter.pmtKnown && pid == filter.pmtPID
		if control {
			if pid == 0 {
				filter.pat.reset()
			} else {
				filter.pmt.reset()
			}
			if filter.initialized && filter.selectionRequired() && !filter.raw {
				n, fallbackErr := filter.fallback()
				return written + n, fallbackErr
			}
		}
		return written, nil
	}
	if filter.quality.Status == core.QualityUnknown {
		filter.quality.Status = core.QualityNoIssuesObserved
	}
	if tei {
		addQuality(&filter.quality, &filter.quality.TEIPackets, 1)
	}
	filter.tracker.observe(packet, parsed, &filter.quality)
	if filter.raw {
		if tei && (parsed.PID == 0 || filter.pmtKnown && parsed.PID == filter.pmtPID) {
			filter.issue(psiStructure)
			if parsed.PID == 0 {
				filter.pat.reset()
			} else {
				filter.pmt.reset()
			}
		} else {
			filter.observeTables(packet, parsed)
		}
		n, err := filter.writePacket(packet)
		return written + n, err
	}
	control := parsed.PID == 0 || filter.pmtKnown && parsed.PID == filter.pmtPID
	if tei && control {
		filter.issue(psiStructure)
		if parsed.PID == 0 {
			filter.pat.reset()
		} else {
			filter.pmt.reset()
		}
		if filter.selectionRequired() {
			if filter.initialized {
				n, err := filter.fallback()
				return written + n, err
			}
			return written, nil
		}
		n, err := filter.writePacket(packet)
		return written + n, err
	}
	if !filter.selectionRequired() {
		filter.observeTables(packet, parsed)
		n, err := filter.writePacket(packet)
		return written + n, err
	}
	if !filter.initialized {
		if len(filter.discovery)+188 > maxPSIBuffer {
			n, err := filter.fallback()
			written += n
			if err != nil {
				return written, err
			}
			n, err = filter.writePacket(packet)
			return written + n, err
		}
		if filter.discovery == nil {
			filter.discovery = make([]byte, 0, maxPSIBuffer)
		}
		filter.discovery = append(filter.discovery, packet...)
		n, err := filter.discover(packet, parsed)
		return written + n, err
	}
	if parsed.PID == 0 {
		sections, _, issue := filter.pat.Feed(packet, parsed)
		filter.issue(issue)
		invalid := issue != psiOK
		for _, section := range sections {
			pat, err := mpegts.ParsePAT(section)
			if err != nil {
				filter.issue(psiStructure)
				invalid = true
				break
			}
			if pat.PMTPID != filter.pmtPID {
				invalid = true
			}
		}
		if invalid {
			n, err := filter.fallback()
			written += n
			if err != nil {
				return written, err
			}
			n, err = filter.writePacket(packet)
			return written + n, err
		}
	}
	n, err := filter.writeInitialized(packet, parsed)
	return written + n, err
}

// observeTablesは全component保存時にもPSI品質を観測するが、選別設定には使わない。
func (filter *tsComponentFilter) observeTables(packet []byte, parsed mpegts.Packet) {
	if parsed.PID == 0 {
		sections, _, issue := filter.pat.Feed(packet, parsed)
		filter.issue(issue)
		for _, section := range sections {
			pat, err := mpegts.ParsePAT(section)
			if err != nil {
				filter.issue(psiStructure)
				continue
			}
			if !filter.pmtKnown || filter.pmtPID != pat.PMTPID {
				filter.pmt.reset()
			}
			filter.pmtPID, filter.pmtKnown = pat.PMTPID, true
			filter.tracker.prioritize(pat.PMTPID, &filter.quality)
		}
	} else if filter.pmtKnown && parsed.PID == filter.pmtPID {
		sections, _, issue := filter.pmt.Feed(packet, parsed)
		filter.issue(issue)
		for _, section := range sections {
			if issue := pmtIssue(section); issue != psiOK {
				filter.issue(issue)
				continue
			}
			pmt, err := mpegts.ParsePMT(section)
			if err != nil {
				filter.issue(psiStructure)
				continue
			}
			filter.tracker.configure(filter.pmtPID, pmt, &filter.quality)
		}
	}
}

func (filter *tsComponentFilter) discover(packet []byte, parsed mpegts.Packet) (int64, error) {
	if parsed.PID == 0 {
		sections, _, issue := filter.pat.Feed(packet, parsed)
		filter.issue(issue)
		if issue == psiLimit {
			return filter.fallback()
		}
		for _, section := range sections {
			pat, err := mpegts.ParsePAT(section)
			if err != nil {
				filter.issue(psiStructure)
				continue
			}
			if !filter.pmtKnown || pat.PMTPID != filter.pmtPID {
				filter.pmt.reset()
				filter.firstPMT = -1
			}
			filter.pmtPID, filter.pmtKnown = pat.PMTPID, true
			filter.tracker.prioritize(pat.PMTPID, &filter.quality)
		}
	}
	if !filter.pmtKnown || parsed.PID != filter.pmtPID {
		return 0, nil
	}
	sections, duplicate, issue := filter.pmt.Feed(packet, parsed)
	filter.issue(issue)
	if issue == psiLimit {
		return filter.fallback()
	}
	if issue != psiOK {
		filter.firstPMT = -1
		return 0, nil
	}
	if parsed.PayloadUnitStart && !duplicate {
		filter.firstPMT = len(filter.discovery)/188 - 1
	}
	if len(sections) == 0 {
		return 0, nil
	}
	section := sections[len(sections)-1]
	if issue := pmtIssue(section); issue != psiOK {
		filter.issue(issue)
		if issue == psiLimit {
			return filter.fallback()
		}
		return 0, nil
	}
	pmt, err := mpegts.ParsePMT(section)
	if err != nil {
		filter.issue(psiStructure)
		return 0, nil
	}
	rewritten, dropped, err := rewritePMT(section, filter.keepCaptions, filter.keepData)
	if err != nil {
		filter.issue(psiStructure)
		return 0, nil
	}
	filter.tracker.configure(filter.pmtPID, pmt, &filter.quality)
	filter.dropped = dropped
	filter.initialized = true
	filter.pmtContinuity = packet[3] & 15
	// PMTの最初の連番を出力の起点にする。
	if filter.firstPMT >= 0 {
		filter.pmtContinuity = filter.discovery[filter.firstPMT*188+3] & 15
	}
	written, err := filter.replay(filter.discovery, filter.firstPMT, rewritten, dropped)
	filter.discovery = nil
	return written, err
}

func (filter *tsComponentFilter) writeInitialized(packet []byte, parsed mpegts.Packet) (int64, error) {
	if len(filter.pending) == 0 && parsed.PID != filter.pmtPID {
		return filter.writeSelectedWith(packet, filter.dropped)
	}
	if len(filter.pending) == 0 && !parsed.HasPayload {
		// adaptationだけのpacketはPMT更新の開始ではない。
		filter.pmt.Feed(packet, parsed)
		return filter.writePacket(packet)
	}
	// 完全同一の再送はcollectorにも更新bufferにも二重に追加しない。
	if parsed.PID == filter.pmtPID && filter.pmt.known && parsed.ContinuityCounter == filter.pmt.cc && !parsed.Discontinuity {
		if bytes.Equal(packet, filter.pmt.previous[:]) {
			return 0, nil
		}
	}
	if len(filter.pending) == 0 && !parsed.PayloadUnitStart {
		filter.issue(psiStructure)
		n, err := filter.fallback()
		if err != nil {
			return n, err
		}
		more, err := filter.writePacket(packet)
		return n + more, err
	}
	if len(filter.pending)+188 > maxPSIBuffer {
		filter.issue(psiLimit)
		n, err := filter.fallback()
		if err != nil {
			return n, err
		}
		more, err := filter.writePacket(packet)
		return n + more, err
	}
	if filter.pending == nil {
		filter.pending = make([]byte, 0, maxPSIBuffer)
	}
	filter.pending = append(filter.pending, packet...)
	if parsed.PID != filter.pmtPID {
		return 0, nil
	}
	sections, _, issue := filter.pmt.Feed(packet, parsed)
	filter.issue(issue)
	if issue != psiOK {
		return filter.fallback()
	}
	if len(sections) == 0 {
		return 0, nil
	}
	section := sections[len(sections)-1]
	if issue := pmtIssue(section); issue != psiOK {
		filter.issue(issue)
		return filter.fallback()
	}
	pmt, err := mpegts.ParsePMT(section)
	if err != nil {
		filter.issue(psiStructure)
		return filter.fallback()
	}
	rewritten, dropped, err := rewritePMT(section, filter.keepCaptions, filter.keepData)
	if err != nil {
		filter.issue(psiStructure)
		return filter.fallback()
	}
	filter.tracker.configure(filter.pmtPID, pmt, &filter.quality)
	written, err := filter.replay(filter.pending, 0, rewritten, dropped)
	filter.pending = filter.pending[:0]
	filter.dropped = dropped
	return written, err
}

func (filter *tsComponentFilter) replay(data []byte, first int, section []byte, dropped map[uint16]bool) (int64, error) {
	var written int64
	packets, err := mpegts.PacketizeSection(filter.pmtPID, filter.pmtContinuity, section)
	if err != nil {
		return 0, err
	}
	for index := 0; index < len(data)/188; index++ {
		if index == first {
			n, err := filter.writePacketList(packets)
			written += n
			if err != nil {
				return written, err
			}
			filter.pmtContinuity = (filter.pmtContinuity + byte(len(packets))) & 15
		}
		packet := data[index*188 : (index+1)*188]
		if index >= first && mpegts.PID(packet) == filter.pmtPID {
			continue
		}
		n, err := filter.writeSelectedWith(packet, dropped)
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

func (filter *tsComponentFilter) fallback() (int64, error) {
	if !filter.raw {
		filter.raw = true
		filter.quality.SelectionUnverified = true
		filter.quality.FallbackEvents = 1
		filter.quality.Status = core.QualityDegraded
	}
	var written int64
	for _, data := range [][]byte{filter.discovery, filter.pending} {
		for offset := 0; offset < len(data); offset += 188 {
			n, err := filter.writePacket(data[offset : offset+188])
			written += n
			if err != nil {
				filter.discovery = nil
				filter.pending = nil
				return written, err
			}
		}
	}
	filter.discovery = nil
	filter.pending = nil
	return written, nil
}

// Finishは末尾の観測を確定する。許可された場合だけ保持済みpacketを保存する。
func (filter *tsComponentFilter) Finish(allowBufferedWrite bool) (int64, error) {
	if filter.finished {
		return 0, nil
	}
	filter.finished = true
	filter.framer.Finish(&filter.quality)
	if filter.pat.incomplete() {
		addQuality(&filter.quality, &filter.quality.UnfinishedPSIEvents, 1)
	}
	if filter.pmt.incomplete() {
		addQuality(&filter.quality, &filter.quality.UnfinishedPSIEvents, 1)
	}
	if filter.selectionRequired() && (!filter.initialized || len(filter.pending) > 0) && allowBufferedWrite {
		return filter.fallback()
	}
	filter.discovery = nil
	filter.pending = nil
	return 0, nil
}

func (filter *tsComponentFilter) writeSelectedWith(packet []byte, dropped map[uint16]bool) (int64, error) {
	if dropped[mpegts.PID(packet)] {
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
