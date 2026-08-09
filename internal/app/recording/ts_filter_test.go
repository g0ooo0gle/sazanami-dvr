package recording

import (
	"bytes"
	"errors"
	"testing"
)

type tsBufferFile struct{ bytes.Buffer }

func (file *tsBufferFile) Sync() error  { return nil }
func (file *tsBufferFile) Close() error { return nil }

type testStream struct {
	typeValue  byte
	pid        uint16
	descriptor []byte
}

func TestTSComponentFilterSelectsPIDsWithArbitraryReads(t *testing.T) {
	streams := []testStream{{0x1b, 0x101, nil}, {0x06, 0x102, nil}, {0x0d, 0x103, nil}, {0x0f, 0x104, nil}}
	input := testTransportStream(t, streams)
	file := &tsBufferFile{}
	filter := newTSComponentFilter(file, true, false)
	for offset, sizeIndex := 0, 0; offset < len(input); sizeIndex++ {
		sizes := [...]int{1, 187, 23, 401, 17}
		size := min(sizes[sizeIndex%len(sizes)], len(input)-offset)
		if _, err := filter.Write(input[offset : offset+size]); err != nil {
			t.Fatal(err)
		}
		offset += size
	}
	if err := filter.Finish(); err != nil {
		t.Fatal(err)
	}
	got := file.Bytes()
	if len(got)%tsPacketBytes != 0 {
		t.Fatalf("bytes=%d", len(got))
	}
	pids := packetPIDsForTest(t, got)
	want := []uint16{0, 0x100, 0x101, 0x102, 0x104}
	if !equalPIDs(pids, want) {
		t.Fatalf("pids=%#v", pids)
	}
	section := pmtSectionFromPackets(t, got, 0x100)
	if !validMPEGCRC(section) || containsStreamPID(section, 0x103) || !containsStreamPID(section, 0x102) {
		t.Fatalf("rewritten PMT=%x", section)
	}
}

func TestTSComponentFilterFourSelectionsAndPMTUpdate(t *testing.T) {
	base := []testStream{{0x1b, 0x101, nil}, {0x06, 0x102, nil}, {0x0d, 0x103, nil}}
	for _, test := range []struct {
		name                string
		captions, data      bool
		captionPID, dataPID bool
	}{
		{"neither", false, false, false, false},
		{"captions", true, false, true, false},
		{"data", false, true, false, true},
		{"both", true, true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := &tsBufferFile{}
			filter := newTSComponentFilter(file, test.captions, test.data)
			if _, err := filter.Write(testTransportStream(t, base)); err != nil || filter.Finish() != nil {
				t.Fatal(err)
			}
			pids := packetPIDsForTest(t, file.Bytes())
			if containsPID(pids, 0x102) != test.captionPID || containsPID(pids, 0x103) != test.dataPID {
				t.Fatalf("pids=%#v", pids)
			}
		})
	}

	file := &tsBufferFile{}
	filter := newTSComponentFilter(file, false, false)
	initial := testTransportStream(t, base)
	updatedSection := makePMTSection(t, []testStream{{0x1b, 0x101, nil}, {0x06, 0x105, nil}, {0x0d, 0x106, nil}})
	updated := append(packetizeSectionForTest(0x100, 7, updatedSection), makePayloadPacket(0x105)...)
	updated = append(updated, makePayloadPacket(0x106)...)
	if _, err := filter.Write(append(initial, updated...)); err != nil || filter.Finish() != nil {
		t.Fatal(err)
	}
	pids := packetPIDsForTest(t, file.Bytes())
	if containsPID(pids, 0x102) || containsPID(pids, 0x103) || containsPID(pids, 0x105) || containsPID(pids, 0x106) {
		t.Fatalf("更新後の除外PIDが残りました: %#v", pids)
	}
}

func TestTSComponentFilterHandlesMultiPacketPMT(t *testing.T) {
	descriptor := bytes.Repeat([]byte{0xaa}, 180)
	streams := []testStream{{0x1b, 0x101, descriptor}, {0x06, 0x102, nil}, {0x0d, 0x103, nil}}
	input := testTransportStream(t, streams)
	file := &tsBufferFile{}
	filter := newTSComponentFilter(file, true, false)
	if _, err := filter.Write(input); err != nil || filter.Finish() != nil {
		t.Fatal(err)
	}
	section := pmtSectionFromPackets(t, file.Bytes(), 0x100)
	if !validMPEGCRC(section) || !bytes.Contains(section, descriptor) || containsStreamPID(section, 0x103) {
		t.Fatal("複数packetのPMTを正しく再生成できませんでした")
	}
	continuity := byte(3)
	for offset := 0; offset < len(file.Bytes()); offset += tsPacketBytes {
		packet := file.Bytes()[offset : offset+tsPacketBytes]
		if packetPID(packet) != 0x100 {
			continue
		}
		if packet[3]&0x0f != continuity {
			t.Fatalf("continuity=%d want=%d", packet[3]&0x0f, continuity)
		}
		continuity = (continuity + 1) & 0x0f
	}
}

func TestTSComponentFilterRejectsMalformedAndBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"wrong sync", bytes.Repeat([]byte{0}, tsPacketBytes)},
		{"two programs", append(packetizeSectionForTest(0, 0, makePATSection(t, []uint16{1, 2})), makePayloadPacket(0x101)...)},
		{"pointer over", invalidPointerPacket()},
		{"section over", oversizedSectionPacket()},
	} {
		t.Run(test.name, func(t *testing.T) {
			filter := newTSComponentFilter(&tsBufferFile{}, true, false)
			if _, err := filter.Write(test.data); !errors.Is(err, errTSFormat) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	filter := newTSComponentFilter(&tsBufferFile{}, true, false)
	if _, err := filter.Write(make([]byte, 187)); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(filter.Finish(), errTSFormat) {
		t.Fatal("truncated packetが受理されました")
	}

	badCRC := testTransportStream(t, []testStream{{0x1b, 0x101, nil}})
	badCRC[20] ^= 1
	filter = newTSComponentFilter(&tsBufferFile{}, true, false)
	if _, err := filter.Write(badCRC); !errors.Is(err, errTSFormat) {
		t.Fatalf("CRC err=%v", err)
	}

	many := make([]testStream, maxElementaryPIDs+1)
	for index := range many {
		many[index] = testStream{0x1b, uint16(0x101 + index), nil}
	}
	filter = newTSComponentFilter(&tsBufferFile{}, true, false)
	if _, err := filter.Write(testTransportStream(t, many)); !errors.Is(err, errTSFormat) {
		t.Fatalf("stream one-over err=%v", err)
	}

	nullPacket := makePayloadPacket(0x1fff)
	data := bytes.Repeat(nullPacket, maxPSIBuffer/tsPacketBytes)
	data = append(data, bytes.Repeat([]byte{0xff}, maxPSIBuffer-len(data))...)
	filter = newTSComponentFilter(&tsBufferFile{}, true, false)
	if _, err := filter.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := filter.Write([]byte{0}); !errors.Is(err, errTSFormat) {
		t.Fatalf("one-over err=%v", err)
	}
}

func invalidPointerPacket() []byte {
	packet := makePayloadPacket(0)
	packet[1] |= 0x40
	packet[4] = 184
	return packet
}

func oversizedSectionPacket() []byte {
	packet := makePayloadPacket(0)
	packet[1] |= 0x40
	packet[4], packet[5], packet[6] = 0, 0x00, 0xb3
	packet[7] = 0xff
	return packet
}

func testTransportStream(t *testing.T, streams []testStream) []byte {
	t.Helper()
	result := packetizeSectionForTest(0, 0, makePATSection(t, []uint16{1}))
	result = append(result, packetizeSectionForTest(0x100, 3, makePMTSection(t, streams))...)
	for _, stream := range streams {
		result = append(result, makePayloadPacket(stream.pid)...)
	}
	return result
}

func makePATSection(t *testing.T, programs []uint16) []byte {
	t.Helper()
	section := []byte{0x00, 0xb0, 0, 0, 1, 0xc1, 0, 0}
	for index, program := range programs {
		pid := uint16(0x100 + index)
		section = append(section, byte(program>>8), byte(program), 0xe0|byte(pid>>8), byte(pid))
	}
	return finishSection(section)
}

func makePMTSection(t *testing.T, streams []testStream) []byte {
	t.Helper()
	section := []byte{0x02, 0xb0, 0, 0, 1, 0xc1, 0, 0, 0xe1, 0x01, 0xf0, 0}
	for _, stream := range streams {
		if len(stream.descriptor) > 0x0fff {
			t.Fatal("descriptor too large")
		}
		section = append(section, stream.typeValue, 0xe0|byte(stream.pid>>8), byte(stream.pid),
			0xf0|byte(len(stream.descriptor)>>8), byte(len(stream.descriptor)))
		section = append(section, stream.descriptor...)
	}
	return finishSection(section)
}

func finishSection(section []byte) []byte {
	length := len(section) + 4 - 3
	section[1] = section[1]&0xf0 | byte(length>>8)
	section[2] = byte(length)
	crc := mpegCRC(section)
	return append(section, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func packetizeSectionForTest(pid uint16, continuity byte, section []byte) []byte {
	packets := packetizePMT(pid, continuity, section)
	var result []byte
	for _, packet := range packets {
		result = append(result, packet...)
	}
	return result
}

func makePayloadPacket(pid uint16) []byte {
	packet := bytes.Repeat([]byte{0xff}, tsPacketBytes)
	packet[0], packet[1], packet[2], packet[3] = 0x47, byte(pid>>8)&0x1f, byte(pid), 0x10
	return packet
}

func packetPIDsForTest(t *testing.T, data []byte) []uint16 {
	t.Helper()
	if len(data)%tsPacketBytes != 0 {
		t.Fatalf("invalid output bytes=%d", len(data))
	}
	result := make([]uint16, 0, len(data)/tsPacketBytes)
	for offset := 0; offset < len(data); offset += tsPacketBytes {
		result = append(result, packetPID(data[offset:offset+tsPacketBytes]))
	}
	return result
}

func pmtSectionFromPackets(t *testing.T, data []byte, pid uint16) []byte {
	t.Helper()
	var collector psiCollector
	for offset := 0; offset < len(data); offset += tsPacketBytes {
		packet := data[offset : offset+tsPacketBytes]
		if packetPID(packet) != pid {
			continue
		}
		section, done, err := collector.feed(packet)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return section
		}
	}
	t.Fatal("PMT not found")
	return nil
}

func containsStreamPID(section []byte, target uint16) bool {
	offset := 12 + int(section[10]&0x0f)<<8 + int(section[11])
	for offset < len(section)-4 {
		pid := uint16(section[offset+1]&0x1f)<<8 | uint16(section[offset+2])
		if pid == target {
			return true
		}
		offset += 5 + int(section[offset+3]&0x0f)<<8 + int(section[offset+4])
	}
	return false
}

func containsPID(values []uint16, target uint16) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func equalPIDs(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
