package mpegts

import (
	"bytes"
	"errors"
	"testing"
)

func TestPacketizerFindsBoundedSyncAndKeepsPackets(t *testing.T) {
	packets := bytes.Repeat(testPayloadPacket(0x101, 0), syncPacketCount)
	input := append(bytes.Repeat([]byte{0xaa}, MaxSyncSearchBytes), packets...)
	for _, chunkSize := range []int{1, 187, 188, 189, len(input)} {
		t.Run(testSizeName(chunkSize), func(t *testing.T) {
			var packetizer Packetizer
			var got []byte
			for offset := 0; offset < len(input); {
				end := min(len(input), offset+chunkSize)
				if err := packetizer.Feed(input[offset:end], func(packet []byte) error {
					got = append(got, packet...)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				offset = end
			}
			if err := packetizer.Finish(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, packets) {
				t.Fatalf("bytes=%d want=%d", len(got), len(packets))
			}
		})
	}
}

func TestPacketizerRejectsOneOverFalseSyncAndTruncation(t *testing.T) {
	falseSync := bytes.Repeat([]byte{0}, MaxSyncSearchBytes+(syncPacketCount-1)*PacketBytes+1)
	for index := 0; index < syncPacketCount-1; index++ {
		falseSync[index*PacketBytes] = 0x47
	}
	var packetizer Packetizer
	if err := packetizer.Feed(falseSync, func([]byte) error { return nil }); !errors.Is(err, ErrSync) {
		t.Fatalf("false sync err=%v", err)
	}

	packetizer = Packetizer{}
	oneOver := append(bytes.Repeat([]byte{0}, MaxSyncSearchBytes+1), bytes.Repeat(testPayloadPacket(0x101, 0), syncPacketCount)...)
	if err := packetizer.Feed(oneOver, func([]byte) error { return nil }); !errors.Is(err, ErrSync) {
		t.Fatalf("one over err=%v", err)
	}

	packetizer = Packetizer{}
	input := append(bytes.Repeat(testPayloadPacket(0x101, 0), syncPacketCount), 0x47)
	if err := packetizer.Feed(input, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := packetizer.Finish(); !errors.Is(err, ErrTruncated) {
		t.Fatalf("truncated err=%v", err)
	}

	packetizer = Packetizer{}
	input = bytes.Repeat(testPayloadPacket(0x101, 0), syncPacketCount+1)
	input[PacketBytes*syncPacketCount] = 0
	if err := packetizer.Feed(input, func([]byte) error { return nil }); !errors.Is(err, ErrSync) {
		t.Fatalf("lost sync err=%v", err)
	}
}

func TestPSICollectorChecksSplitSectionContinuityAndLimit(t *testing.T) {
	descriptor := bytes.Repeat([]byte{0xaa}, 300)
	section := testPMTSection(31, []ElementaryStream{{Type: 0x1b, PID: 0x101, Descriptor: descriptor}})
	packets, err := PacketizeSection(0x100, 14, section)
	if err != nil {
		t.Fatal(err)
	}
	var collector PSICollector
	var got [][]byte
	for _, packet := range packets {
		sections, feedErr := collector.Feed(packet)
		if feedErr != nil {
			t.Fatal(feedErr)
		}
		got = append(got, sections...)
	}
	if len(got) != 1 || !bytes.Equal(got[0], section) {
		t.Fatalf("sections=%d", len(got))
	}

	broken := append([][]byte(nil), packets...)
	broken[1] = append([]byte(nil), broken[1]...)
	broken[1][3] = broken[1][3]&0xf0 | broken[0][3]&0x0f
	collector = PSICollector{}
	if _, err := collector.Feed(broken[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Feed(broken[1]); !errors.Is(err, ErrPSI) {
		t.Fatalf("continuity err=%v", err)
	}

	over := testPayloadPacket(0, 0)
	over[1] |= 0x40
	over[4], over[5], over[6], over[7] = 0, 0, 0xbf, 0xff
	collector = PSICollector{}
	if _, err := collector.Feed(over); !errors.Is(err, ErrPSI) {
		t.Fatalf("section one over err=%v", err)
	}
}

func TestPSICollectorAcceptsFourKiBAndRejectsOneOver(t *testing.T) {
	section := testPMTSection(0, []ElementaryStream{{Type: 0x1b, PID: 0x101, Descriptor: bytes.Repeat([]byte{0xaa}, 4075)}})
	if len(section) != MaxSectionBytes {
		t.Fatalf("section bytes=%d", len(section))
	}
	packets, err := PacketizeSection(0x100, 0, section)
	if err != nil {
		t.Fatal(err)
	}
	var collector PSICollector
	var got [][]byte
	for _, packet := range packets {
		sections, feedErr := collector.Feed(packet)
		if feedErr != nil {
			t.Fatal(feedErr)
		}
		got = append(got, sections...)
	}
	if len(got) != 1 || !bytes.Equal(got[0], section) {
		t.Fatalf("sections=%d", len(got))
	}
	if _, err := PacketizeSection(0x100, 0, append(section, 0)); !errors.Is(err, ErrPSI) {
		t.Fatalf("one over err=%v", err)
	}
}

func TestPATPMTAndVersionTracking(t *testing.T) {
	patSection := testPATSection(31, []uint16{1})
	pat, err := ParsePAT(patSection)
	if err != nil || pat.Version != 31 || pat.ProgramNumber != 1 || pat.PMTPID != 0x100 {
		t.Fatalf("pat=%+v err=%v", pat, err)
	}
	pmtSection := testPMTSection(7, []ElementaryStream{{Type: 0x02, PID: 0x101}, {Type: 0x0f, PID: 0x102}})
	pmt, err := ParsePMT(pmtSection)
	if err != nil || pmt.Version != 7 || pmt.PCRPID != 0x101 || len(pmt.Streams) != 2 {
		t.Fatalf("pmt=%+v err=%v", pmt, err)
	}
	if _, err := ParsePAT(testPATSection(0, []uint16{1, 2})); !errors.Is(err, ErrSingleProgram) {
		t.Fatalf("two programs err=%v", err)
	}

	var tracker VersionTracker
	changed, err := tracker.Accept(31, patSection)
	if err != nil || !changed {
		t.Fatalf("first changed=%v err=%v", changed, err)
	}
	changed, err = tracker.Accept(31, append([]byte(nil), patSection...))
	if err != nil || changed {
		t.Fatalf("repeat changed=%v err=%v", changed, err)
	}
	wrapped := testPATSection(0, []uint16{1})
	changed, err = tracker.Accept(0, wrapped)
	if err != nil || !changed {
		t.Fatalf("wrap changed=%v err=%v", changed, err)
	}
	modified := append([]byte(nil), wrapped...)
	modified[3] ^= 1
	if _, err := tracker.Accept(0, modified); !errors.Is(err, ErrPSI) {
		t.Fatalf("same version changed content err=%v", err)
	}
}

func TestParsePacketReadsPCRRandomAccessAndWrap(t *testing.T) {
	const clock = uint64(123456789)
	packet := testPCRPacket(0x101, 3, clock, true, true)
	parsed, err := ParsePacket(packet)
	if err != nil || parsed.PID != 0x101 || !parsed.PayloadUnitStart || !parsed.RandomAccess ||
		!parsed.HasProgramClock || parsed.ProgramClock27MHz != clock || !bytes.HasPrefix(parsed.Payload, []byte{0, 0, 1}) {
		t.Fatalf("packet=%+v err=%v", parsed, err)
	}
	wrap := uint64(1<<33) * 300
	if delta := ProgramClockDelta(wrap-27_000_000, 27_000_000); delta != 54_000_000 {
		t.Fatalf("wrap delta=%d", delta)
	}
	packet[10] &^= 0x7e
	if _, err := ParsePacket(packet); !errors.Is(err, ErrPacket) {
		t.Fatalf("reserved PCR bits err=%v", err)
	}
}

func TestParsePacketRequiresFullAdaptationOnlyPacket(t *testing.T) {
	packet := bytes.Repeat([]byte{0xff}, PacketBytes)
	packet[0], packet[1], packet[2], packet[3], packet[4], packet[5] = 0x47, 0x01, 0x01, 0x20, 183, 0
	parsed, err := ParsePacket(packet)
	if err != nil || parsed.HasPayload {
		t.Fatalf("packet=%+v err=%v", parsed, err)
	}
	packet[4] = 182
	if _, err := ParsePacket(packet); !errors.Is(err, ErrPacket) {
		t.Fatalf("short adaptation err=%v", err)
	}
}

func TestPSICollectorRejectsPUSIWithoutNewSection(t *testing.T) {
	for _, pointer := range []byte{0, 183} {
		packet := testPayloadPacket(0, 0)
		packet[1] |= 0x40
		packet[4] = pointer
		var collector PSICollector
		if _, err := collector.Feed(packet); !errors.Is(err, ErrPSI) {
			t.Fatalf("pointer=%d err=%v", pointer, err)
		}
	}
}

func testSizeName(size int) string {
	const digits = "0123456789"
	if size == 0 {
		return "0"
	}
	var result [20]byte
	position := len(result)
	for size > 0 {
		position--
		result[position] = digits[size%10]
		size /= 10
	}
	return string(result[position:])
}

func testPayloadPacket(pid uint16, continuity byte) []byte {
	packet := bytes.Repeat([]byte{0xff}, PacketBytes)
	packet[0], packet[1], packet[2], packet[3] = 0x47, byte(pid>>8)&0x1f, byte(pid), 0x10|continuity&0x0f
	return packet
}

func testPCRPacket(pid uint16, continuity byte, clock uint64, randomAccess, payloadStart bool) []byte {
	packet := testPayloadPacket(pid, continuity)
	packet[3] = 0x30 | continuity&0x0f
	if payloadStart {
		packet[1] |= 0x40
	}
	packet[4], packet[5] = 7, 0x10
	if randomAccess {
		packet[5] |= 0x40
	}
	base, extension := clock/300, clock%300
	packet[6] = byte(base >> 25)
	packet[7] = byte(base >> 17)
	packet[8] = byte(base >> 9)
	packet[9] = byte(base >> 1)
	packet[10] = byte(base&1)<<7 | 0x7e | byte(extension>>8)
	packet[11] = byte(extension)
	copy(packet[12:], []byte{0, 0, 1, 0xe0})
	return packet
}

func testPATSection(version byte, programs []uint16) []byte {
	section := []byte{0x00, 0xb0, 0, 0, 1, 0xc1 | version<<1, 0, 0}
	for index, program := range programs {
		pid := uint16(0x100 + index)
		section = append(section, byte(program>>8), byte(program), 0xe0|byte(pid>>8), byte(pid))
	}
	return testFinishSection(section)
}

func testPMTSection(version byte, streams []ElementaryStream) []byte {
	section := []byte{0x02, 0xb0, 0, 0, 1, 0xc1 | version<<1, 0, 0, 0xe1, 0x01, 0xf0, 0}
	for _, stream := range streams {
		section = append(section, stream.Type, 0xe0|byte(stream.PID>>8), byte(stream.PID),
			0xf0|byte(len(stream.Descriptor)>>8), byte(len(stream.Descriptor)))
		section = append(section, stream.Descriptor...)
	}
	return testFinishSection(section)
}

func testFinishSection(section []byte) []byte {
	length := len(section) + 4 - 3
	section[1] = section[1]&0xf0 | byte(length>>8)
	section[2] = byte(length)
	crc := CRC32(section)
	return append(section, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}
