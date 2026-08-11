// Package mpegts は、録画とライブ配信で共有するMPEG-TSの最小解析処理を提供する。
// packetの内容は書き換えず、同期、PSI、PCR、再生開始点の検証だけを担う。
package mpegts

import (
	"bytes"
	"errors"
)

const (
	// PacketBytes はMPEG-TS packet一件の固定byte数である。
	PacketBytes = 188
	// MaxSectionBytes は一度に組み立てるPSI sectionの上限である。
	MaxSectionBytes = 4 * 1024
	// MaxElementaryStreams は一つのPMTで受けるelementary stream数の上限である。
	MaxElementaryStreams = 64
	// MaxSyncSearchBytes は入力開始時に同期位置を探す範囲の上限である。
	MaxSyncSearchBytes = 64 * 1024

	syncPacketCount = 5
	pcrWrapTicks    = uint64(1<<33) * 300
)

var (
	// ErrPacket はTS packetのheaderまたはadaptation fieldが不正な場合に返す。
	ErrPacket = errors.New("mpegts: invalid packet")
	// ErrPSI はPSI sectionの長さ、continuity、CRCまたは内容が不正な場合に返す。
	ErrPSI = errors.New("mpegts: invalid PSI")
	// ErrSync は開始から64 KiB以内に確かなpacket同期を得られない場合に返す。
	ErrSync = errors.New("mpegts: packet sync unavailable")
	// ErrTruncated は入力終了時にpacket未満のbyteが残った場合に返す。
	ErrTruncated = errors.New("mpegts: truncated stream")
	// ErrSingleProgram はPATのmedia programが一件ではない場合に返す。
	ErrSingleProgram = errors.New("mpegts: single program required")
	// ErrProgramClock はPMTに有効なPCR PIDがない場合に返す。
	ErrProgramClock = errors.New("mpegts: program clock unavailable")
)

// Packet は検証済みTS packetから得たheader、payload、adaptation情報を表す。
// PayloadはParsePacketへ渡したbyte列を参照するため、呼び出し側は元のbyte列を書き換えてはならない。
type Packet struct {
	PID               uint16
	PayloadUnitStart  bool
	ContinuityCounter byte
	HasPayload        bool
	Payload           []byte
	RandomAccess      bool
	Discontinuity     bool
	HasProgramClock   bool
	ProgramClock27MHz uint64
}

// ParsePacket は188 byteのTS packetを検証し、必要なfieldだけを取り出す。
func ParsePacket(packet []byte) (Packet, error) {
	if len(packet) != PacketBytes || packet[0] != 0x47 || packet[1]&0x80 != 0 {
		return Packet{}, ErrPacket
	}
	control := packet[3] >> 4 & 0x03
	if control == 0 {
		return Packet{}, ErrPacket
	}
	parsed := Packet{
		PID:               uint16(packet[1]&0x1f)<<8 | uint16(packet[2]),
		PayloadUnitStart:  packet[1]&0x40 != 0,
		ContinuityCounter: packet[3] & 0x0f,
		HasPayload:        control&1 != 0,
	}
	offset := 4
	if control&2 != 0 {
		length := int(packet[4])
		if length > PacketBytes-5 {
			return Packet{}, ErrPacket
		}
		if control == 2 && length != PacketBytes-5 {
			return Packet{}, ErrPacket
		}
		offset = 5 + length
		if length > 0 {
			flags := packet[5]
			parsed.Discontinuity = flags&0x80 != 0
			parsed.RandomAccess = flags&0x40 != 0
			if flags&0x10 != 0 {
				if length < 7 {
					return Packet{}, ErrPacket
				}
				value := packet[6:12]
				if value[4]&0x7e != 0x7e {
					return Packet{}, ErrPacket
				}
				base := uint64(value[0])<<25 | uint64(value[1])<<17 | uint64(value[2])<<9 |
					uint64(value[3])<<1 | uint64(value[4]>>7)
				extension := uint64(value[4]&1)<<8 | uint64(value[5])
				if extension > 299 {
					return Packet{}, ErrPacket
				}
				parsed.HasProgramClock = true
				parsed.ProgramClock27MHz = base*300 + extension
			}
		}
	}
	if parsed.HasPayload {
		if offset >= PacketBytes {
			return Packet{}, ErrPacket
		}
		parsed.Payload = packet[offset:]
	}
	return parsed, nil
}

// PID は検証済みpacketからPIDだけを取り出す。
func PID(packet []byte) uint16 {
	return uint16(packet[1]&0x1f)<<8 | uint16(packet[2])
}

// ProgramClockDelta は27 MHz PCRのwrapを考慮した前向きの差を返す。
func ProgramClockDelta(previous, next uint64) uint64 {
	if next >= previous {
		return next - previous
	}
	return pcrWrapTicks - previous + next
}

// PSICollector は一つのPIDについてPSI sectionとpayload continuityを追跡する。
// Feedが返したsectionはcollector内部bufferから独立している。
type PSICollector struct {
	pid             uint16
	pidKnown        bool
	section         []byte
	want            int
	continuity      byte
	continuityKnown bool
}

// Incompleteは入力終了時に未完成のPSI sectionを保持しているかを返す。
func (collector *PSICollector) Incomplete() bool {
	return collector != nil && len(collector.section) != 0
}

// Feed は一件のpacketを取り込み、そのpacketまでに完成したsectionを返す。
func (collector *PSICollector) Feed(packet []byte) ([][]byte, error) {
	parsed, err := ParsePacket(packet)
	if err != nil {
		return nil, err
	}
	if collector.pidKnown && parsed.PID != collector.pid {
		return nil, ErrPSI
	}
	if !collector.pidKnown {
		collector.pid, collector.pidKnown = parsed.PID, true
	}
	if !parsed.HasPayload {
		return nil, nil
	}
	if collector.continuityKnown && !parsed.Discontinuity && parsed.ContinuityCounter != (collector.continuity+1)&0x0f {
		return nil, ErrPSI
	}
	collector.continuity, collector.continuityKnown = parsed.ContinuityCounter, true
	payload := parsed.Payload
	if len(payload) == 0 {
		return nil, ErrPSI
	}
	if !parsed.PayloadUnitStart {
		if len(collector.section) == 0 {
			return nil, nil
		}
		completed, rest, err := collector.append(payload)
		if err != nil {
			return nil, err
		}
		if len(rest) != 0 && !allStuffing(rest) {
			return nil, ErrPSI
		}
		if completed == nil {
			return nil, nil
		}
		return [][]byte{completed}, nil
	}

	pointer := int(payload[0])
	if pointer > len(payload)-1 {
		return nil, ErrPSI
	}
	var sections [][]byte
	if len(collector.section) != 0 {
		completed, rest, appendErr := collector.append(payload[1 : 1+pointer])
		if appendErr != nil || completed == nil || len(rest) != 0 {
			return nil, ErrPSI
		}
		sections = append(sections, completed)
	}
	payload = payload[1+pointer:]
	if len(payload) == 0 || payload[0] == 0xff {
		return nil, ErrPSI
	}
	for len(payload) != 0 && payload[0] != 0xff {
		completed, rest, appendErr := collector.append(payload)
		if appendErr != nil {
			return nil, appendErr
		}
		if completed == nil {
			break
		}
		sections = append(sections, completed)
		payload = rest
	}
	return sections, nil
}

func (collector *PSICollector) append(data []byte) ([]byte, []byte, error) {
	if len(data) == 0 {
		return nil, nil, nil
	}
	needed := len(data)
	if collector.want != 0 && needed > collector.want-len(collector.section) {
		needed = collector.want - len(collector.section)
	}
	collector.section = append(collector.section, data[:needed]...)
	if len(collector.section) >= 3 && collector.want == 0 {
		collector.want = 3 + int(collector.section[1]&0x0f)<<8 + int(collector.section[2])
		if collector.want < 12 || collector.want > MaxSectionBytes {
			return nil, nil, ErrPSI
		}
		if len(collector.section) > collector.want {
			needed -= len(collector.section) - collector.want
			collector.section = collector.section[:collector.want]
		}
	}
	if len(collector.section) > MaxSectionBytes {
		return nil, nil, ErrPSI
	}
	if collector.want == 0 || len(collector.section) < collector.want {
		return nil, nil, nil
	}
	section := append([]byte(nil), collector.section...)
	collector.section, collector.want = nil, 0
	return section, data[needed:], nil
}

func allStuffing(data []byte) bool {
	for _, value := range data {
		if value != 0xff {
			return false
		}
	}
	return true
}

// VersionTracker は一つのPATまたはPMTについて採用済みversionと内容を保持する。
// 5 bit値の大小を比較しないため、31から0への更新も通常の変更として受理する。
type VersionTracker struct {
	valid   bool
	version byte
	section []byte
}

// Accept は新しいversionならtrue、同じ内容の再送ならfalseを返す。
// 同じversionで内容だけが変わったsectionはErrPSIで拒否する。
func (tracker *VersionTracker) Accept(version byte, section []byte) (bool, error) {
	if version > 31 || len(section) == 0 {
		return false, ErrPSI
	}
	if tracker.valid && tracker.version == version {
		if !bytes.Equal(tracker.section, section) {
			return false, ErrPSI
		}
		return false, nil
	}
	tracker.valid, tracker.version = true, version
	tracker.section = append(tracker.section[:0], section...)
	return true, nil
}

// PAT は一programへ絞ったProgram Association Tableの内容を表す。
type PAT struct {
	TransportStreamID uint16
	ProgramNumber     uint16
	PMTPID            uint16
	Version           byte
}

// ParsePAT はCRCを含むPATを検証し、唯一のmedia programを返す。
func ParsePAT(section []byte) (PAT, error) {
	if len(section) < 16 || section[0] != 0x00 || section[1]&0x80 == 0 ||
		3+int(section[1]&0x0f)<<8+int(section[2]) != len(section) ||
		section[5]&1 == 0 || section[6] != 0 || section[7] != 0 || !ValidCRC(section) ||
		(len(section)-12)%4 != 0 {
		return PAT{}, ErrPSI
	}
	result := PAT{TransportStreamID: uint16(section[3])<<8 | uint16(section[4]), Version: (section[5] >> 1) & 0x1f}
	for offset := 8; offset < len(section)-4; offset += 4 {
		program := uint16(section[offset])<<8 | uint16(section[offset+1])
		if program == 0 {
			continue
		}
		if result.ProgramNumber != 0 {
			return PAT{}, ErrSingleProgram
		}
		result.ProgramNumber = program
		result.PMTPID = uint16(section[offset+2]&0x1f)<<8 | uint16(section[offset+3])
	}
	if result.ProgramNumber == 0 || result.PMTPID == 0 || result.PMTPID == 0x1fff {
		return PAT{}, ErrSingleProgram
	}
	return result, nil
}

// ElementaryStream はPMTに記載されたstream type、PID、descriptorを表す。
type ElementaryStream struct {
	Type       byte
	PID        uint16
	Descriptor []byte
}

// PMT は検証済みProgram Map TableのPCRとelementary stream一覧を表す。
type PMT struct {
	ProgramNumber uint16
	PCRPID        uint16
	Version       byte
	Streams       []ElementaryStream
}

// ParsePMT はCRC、section番号、各可変長fieldを検証してPMTを返す。
func ParsePMT(section []byte) (PMT, error) {
	if len(section) < 16 || section[0] != 0x02 || section[1]&0x80 == 0 ||
		3+int(section[1]&0x0f)<<8+int(section[2]) != len(section) ||
		section[5]&1 == 0 || section[6] != 0 || section[7] != 0 || !ValidCRC(section) {
		return PMT{}, ErrPSI
	}
	result := PMT{
		ProgramNumber: uint16(section[3])<<8 | uint16(section[4]),
		Version:       (section[5] >> 1) & 0x1f,
		PCRPID:        uint16(section[8]&0x1f)<<8 | uint16(section[9]),
	}
	if result.ProgramNumber == 0 {
		return PMT{}, ErrPSI
	}
	if result.PCRPID == 0 || result.PCRPID == 0x1fff {
		return PMT{}, ErrProgramClock
	}
	offset := 12 + int(section[10]&0x0f)<<8 + int(section[11])
	end := len(section) - 4
	if offset > end {
		return PMT{}, ErrPSI
	}
	for offset < end {
		if end-offset < 5 || len(result.Streams) >= MaxElementaryStreams {
			return PMT{}, ErrPSI
		}
		infoLength := int(section[offset+3]&0x0f)<<8 | int(section[offset+4])
		next := offset + 5 + infoLength
		if next > end {
			return PMT{}, ErrPSI
		}
		pid := uint16(section[offset+1]&0x1f)<<8 | uint16(section[offset+2])
		if pid == 0 || pid == 0x1fff {
			return PMT{}, ErrPSI
		}
		result.Streams = append(result.Streams, ElementaryStream{
			Type: section[offset], PID: pid, Descriptor: append([]byte(nil), section[offset+5:next]...),
		})
		offset = next
	}
	return result, nil
}

// PacketizeSection はPSI sectionを指定PIDとcontinuityからTS packetへ格納する。
func PacketizeSection(pid uint16, continuity byte, section []byte) ([][]byte, error) {
	if pid > 0x1ffe || len(section) < 12 || len(section) > MaxSectionBytes {
		return nil, ErrPSI
	}
	remaining := section
	first := true
	var result [][]byte
	for len(remaining) != 0 {
		packet := bytes.Repeat([]byte{0xff}, PacketBytes)
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
		count := min(len(remaining), PacketBytes-start)
		copy(packet[start:start+count], remaining[:count])
		remaining = remaining[count:]
		result = append(result, packet)
		continuity = (continuity + 1) & 0x0f
		first = false
	}
	return result, nil
}

// CRC32 はMPEG-2 PSIで使うCRC-32を計算する。
func CRC32(data []byte) uint32 {
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

// ValidCRC は末尾のCRCを含むsection全体がMPEG-2 CRC-32として正しいかを返す。
func ValidCRC(section []byte) bool {
	return len(section) >= 4 && CRC32(section) == 0
}

// Packetizer は任意の入力chunkから188 byte packetを順に取り出す。
// 同期後に欠落や余分なbyteを見つけても再同期せず、入力を失敗させる。
type Packetizer struct {
	buffer []byte
	synced bool
}

// Feed は入力を追加し、完成packetをbyte順のままemitへ渡す。
// emitへ渡すsliceは呼び出し中だけ有効で、保持する場合はcopyが必要である。
func (packetizer *Packetizer) Feed(data []byte, emit func([]byte) error) error {
	if emit == nil {
		return ErrPacket
	}
	packetizer.buffer = append(packetizer.buffer, data...)
	if !packetizer.synced {
		position := findSync(packetizer.buffer)
		if position >= 0 {
			packetizer.buffer = packetizer.buffer[position:]
			packetizer.synced = true
		} else if len(packetizer.buffer) >= MaxSyncSearchBytes+(syncPacketCount-1)*PacketBytes+1 {
			return ErrSync
		} else {
			return nil
		}
	}
	for len(packetizer.buffer) >= PacketBytes {
		packet := packetizer.buffer[:PacketBytes]
		if packet[0] != 0x47 {
			return ErrSync
		}
		if err := emit(packet); err != nil {
			return err
		}
		packetizer.buffer = packetizer.buffer[PacketBytes:]
	}
	if len(packetizer.buffer) == 0 {
		packetizer.buffer = nil
	} else {
		packetizer.buffer = append([]byte(nil), packetizer.buffer...)
	}
	return nil
}

func findSync(data []byte) int {
	last := min(MaxSyncSearchBytes, len(data)-(syncPacketCount-1)*PacketBytes-1)
	for position := 0; position <= last; position++ {
		matched := true
		for index := 0; index < syncPacketCount; index++ {
			if data[position+index*PacketBytes] != 0x47 {
				matched = false
				break
			}
		}
		if matched {
			return position
		}
	}
	return -1
}

// Finish は入力終了時に同期済みで、packet未満のbyteが残っていないことを確認する。
func (packetizer *Packetizer) Finish() error {
	if !packetizer.synced {
		return ErrSync
	}
	if len(packetizer.buffer) != 0 {
		return ErrTruncated
	}
	return nil
}
