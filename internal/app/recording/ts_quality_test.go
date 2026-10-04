package recording

import (
	"bytes"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
	"testing"
)

func TestQualityArbitrarySplits(t *testing.T) {
	input := bytes.Repeat(makePayloadPacket(0x101), 5)
	for _, size := range []int{1, 187, 188, 189, 65536} {
		file := &tsBufferFile{}
		f := newTSComponentFilter(file, true, true, core.QualitySummary{})
		var written int64
		for offset := 0; offset < len(input); offset += size {
			n, err := f.Write(input[offset:min(offset+size, len(input))])
			if err != nil {
				t.Fatal(err)
			}
			written += n
		}
		n, err := f.Finish(true)
		written += n
		if err != nil || written != int64(len(input)) || !bytes.Equal(file.Bytes(), input) {
			t.Fatalf("size=%d written=%d err=%v", size, written, err)
		}
		if f.Quality().Validate() != nil {
			t.Fatal("invalid quality")
		}
	}
}

func TestQualityTEILeavesOriginalPacket(t *testing.T) {
	packet := makePayloadPacket(0x101)
	packet[1] |= 0x80
	file := &tsBufferFile{}
	f := newTSComponentFilter(file, true, true, core.QualitySummary{})
	if _, err := f.Write(packet); err != nil {
		t.Fatal(err)
	}
	if _, err := mpegts.ParsePacket(packet); err == nil {
		t.Fatal("shared parser became tolerant")
	}
	if !bytes.Equal(file.Bytes(), packet) || f.Quality().TEIPackets != 1 || f.Quality().Status != core.QualityDegraded {
		t.Fatal("TEI lost or repaired")
	}
	bad := makePCRPacketForTest(0)
	bad[10] |= 1
	bad[11] = 0xff
	if _, err := f.Write(bad); err != nil || f.Quality().MalformedPacketEvents != 1 || file.Len() != 188 {
		t.Fatal("invalid PCR accepted or recording stopped")
	}
}

func TestQualityContinuityClassification(t *testing.T) {
	first := makePayloadPacket(0x101)
	changed := append([]byte(nil), first...)
	changed[10] = 1
	gap := makePayloadPacket(0x101)
	gap[3] = 0x13
	noPayload := makePayloadPacket(0x101)
	noPayload[3] = 0x2b
	noPayload[4] = 183
	noPayload[5] = 0
	next := makePayloadPacket(0x101)
	next[3] = 0x14
	discontinuity := makePayloadPacket(0x101)
	discontinuity[3] = 0x3c
	discontinuity[4] = 1
	discontinuity[5] = 0x80
	after := makePayloadPacket(0x101)
	after[3] = 0x1d
	file := &tsBufferFile{}
	f := newTSComponentFilter(file, true, true, core.QualitySummary{})
	for _, p := range [][]byte{first, first, changed, gap, noPayload, next, discontinuity, after} {
		if _, err := f.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	q := f.Quality()
	if q.CCDuplicateEvents != 1 || q.CCGapEvents != 2 || file.Len() != 8*188 {
		t.Fatalf("quality=%+v bytes=%d", q, file.Len())
	}
}

func TestQualityPMTWithoutPayloadDoesNotLoseSelection(t *testing.T) {
	file := &tsBufferFile{}
	f := newTSComponentFilter(file, true, false, core.QualitySummary{})
	if _, err := f.Write(testTransportStream(t, []testStream{{0x1b, 0x101, nil}, {0x0d, 0x103, nil}})); err != nil {
		t.Fatal(err)
	}
	adaptation := makePayloadPacket(0x100)
	adaptation[3], adaptation[4], adaptation[5] = 0x20, 183, 0
	if _, err := f.Write(adaptation); err != nil {
		t.Fatal(err)
	}
	before := file.Len()
	if _, err := f.Write(makePayloadPacket(0x103)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Finish(true); err != nil {
		t.Fatal(err)
	}
	if q := f.Quality(); q.SelectionUnverified || q.PSIStructureEvents != 0 || file.Len() != before {
		t.Fatalf("payloadのないPMT packetで選別を失いました: quality=%+v", q)
	}
}

func TestQualityPSIRecoveryAndFallback(t *testing.T) {
	t.Run("initial CRC recovery", func(t *testing.T) {
		bad := packetizeSectionForTest(0, 0, makePATSection(t, []uint16{1}))
		bad[20] ^= 1
		input := append(bad, testTransportStream(t, []testStream{{0x1b, 0x101, nil}, {0x0d, 0x103, nil}})...)
		file := &tsBufferFile{}
		f := newTSComponentFilter(file, true, false, core.QualitySummary{})
		if _, err := f.Write(input); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Finish(true); err != nil {
			t.Fatal(err)
		}
		q := f.Quality()
		if q.PSICRCEvents != 1 || q.SelectionUnverified || containsPID(packetPIDsForTest(t, file.Bytes()), 0x103) {
			t.Fatalf("quality=%+v", q)
		}
	})
	t.Run("duplicate PSI", func(t *testing.T) {
		pat := packetizeSectionForTest(0, 0, makePATSection(t, []uint16{1}))
		pmt := packetizeSectionForTest(0x100, 3, makePMTSection(t, []testStream{{0x1b, 0x101, bytes.Repeat([]byte{0xaa}, 200)}, {0x0d, 0x103, nil}}))
		input := append(pat, pmt[:188]...)
		input = append(input, pmt...)
		file := &tsBufferFile{}
		f := newTSComponentFilter(file, true, false, core.QualitySummary{})
		if _, err := f.Write(input); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Finish(true); err != nil {
			t.Fatal(err)
		}
		q := f.Quality()
		if q.CCDuplicateEvents != 1 || q.SelectionUnverified || q.PSIContinuityEvents != 0 || !mpegts.ValidCRC(pmtSectionFromPackets(t, file.Bytes(), 0x100)) {
			t.Fatalf("quality=%+v", q)
		}
	})
	for _, cause := range []string{"CRC update", "PMT PID change", "TEI update"} {
		t.Run(cause, func(t *testing.T) {
			file := &tsBufferFile{}
			f := newTSComponentFilter(file, false, false, core.QualitySummary{})
			if _, err := f.Write(testTransportStream(t, []testStream{{0x1b, 0x101, nil}, {0x0d, 0x103, nil}})); err != nil {
				t.Fatal(err)
			}
			update := packetizeSectionForTest(0x100, 4, makePMTSection(t, []testStream{{0x1b, 0x101, nil}}))
			switch cause {
			case "CRC update":
				update[20] ^= 1
			case "PMT PID change":
				pat := makePATSection(t, []uint16{1})
				pat[11] = 1
				pat = finishSection(pat[:len(pat)-4])
				update = packetizeSectionForTest(0, 1, pat)
			case "TEI update":
				update[1] |= 0x80
			}
			payload := makePayloadPacket(0x103)
			update = append(update, payload...)
			if _, err := f.Write(update); err != nil {
				t.Fatal(err)
			}
			q := f.Quality()
			if !q.SelectionUnverified || q.FallbackEvents != 1 || q.Status != core.QualityDegraded || !bytes.HasSuffix(file.Bytes(), payload) {
				t.Fatalf("quality=%+v", q)
			}
			next := newTSComponentFilter(file, false, false, q)
			if _, err := next.Write(payload); err != nil || next.Quality().FallbackEvents != 1 || !bytes.HasSuffix(file.Bytes(), payload) {
				t.Fatal("fallback lost after reconnect")
			}
		})
	}
}

func TestQualitySyncRecoveryBounds(t *testing.T) {
	good := makePayloadPacket(0x101)
	for _, candidates := range []int{4, 5} {
		file := &tsBufferFile{}
		f := newTSComponentFilter(file, true, true, core.QualitySummary{})
		input := append(append([]byte(nil), good...), bytes.Repeat([]byte{0}, 80000)...)
		input = append(input, bytes.Repeat(good, candidates)...)
		for offset := 0; offset < len(input); offset += 187 {
			if _, err := f.Write(input[offset:min(offset+187, len(input))]); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.Finish(true); err != nil {
			t.Fatal(err)
		}
		q := f.Quality()
		wantPackets := 1
		wantRecover := int64(0)
		if candidates == 5 {
			wantPackets = 6
			wantRecover = 1
		}
		if file.Len() != wantPackets*188 || q.SyncLossEvents != 1 || q.SyncRecoveredEvents != wantRecover || q.SyncDiscardedBytes < 80000 {
			t.Fatalf("candidates=%d q=%+v bytes=%d", candidates, q, file.Len())
		}
	}
}

func TestQualityPSILimitsAndEarlyFinish(t *testing.T) {
	for _, cause := range []string{"missing PMT", "section limit", "stream limit", "early finish", "no flush"} {
		t.Run(cause, func(t *testing.T) {
			file := &tsBufferFile{}
			f := newTSComponentFilter(file, true, false, core.QualitySummary{})
			input := makePayloadPacket(0x101)
			switch cause {
			case "missing PMT":
				input = bytes.Repeat(input, (1024*1024)/188+1)
			case "section limit":
				input = append(packetizeSectionForTest(0, 0, makePATSection(t, []uint16{1})), oversizedSectionPacket()...)
			case "stream limit":
				streams := make([]testStream, 65)
				for i := range streams {
					streams[i] = testStream{0x1b, uint16(0x101 + i), nil}
				}
				input = testTransportStream(t, streams)
			}
			if _, err := f.Write(input); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Finish(cause != "no flush"); err != nil {
				t.Fatal(err)
			}
			q := f.Quality()
			if cause == "no flush" {
				if file.Len() != 0 {
					t.Fatal("cancel flushed held bytes")
				}
				return
			}
			if q.FallbackEvents != 1 || !q.SelectionUnverified || file.Len()%188 != 0 || file.Len() == 0 {
				t.Fatalf("q=%+v bytes=%d", q, file.Len())
			}
		})
	}
}

func TestQualityCountersSaturate(t *testing.T) {
	initial := core.QualitySummary{CCGapEvents: 2147483646}
	file := &tsBufferFile{}
	f := newTSComponentFilter(file, true, true, initial)
	for _, cc := range []byte{0, 3, 7, 10} {
		p := makePayloadPacket(0x101)
		p[3] = 0x10 | cc
		if _, err := f.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	q := f.Quality()
	if q.CCGapEvents != 2147483647 || !q.CountersSaturated || q.Validate() != nil {
		t.Fatalf("q=%+v", q)
	}
	for i := 0; i < 100; i++ {
		if _, err := f.Write(makePayloadPacket(uint16(0x200 + i))); err != nil {
			t.Fatal(err)
		}
	}
	if !f.Quality().ObservationLimited {
		t.Fatal("PID tracking was not bounded")
	}
}

func TestQualityPriorityPIDTracking(t *testing.T) {
	file := &tsBufferFile{}
	f := newTSComponentFilter(file, true, false, core.QualitySummary{})
	for i := 0; i < 80; i++ {
		if _, err := f.Write(makePayloadPacket(uint16(0x300 + i))); err != nil {
			t.Fatal(err)
		}
	}
	pat := packetizeSectionForTest(0, 0, makePATSection(t, []uint16{1}))
	pmt := packetizeSectionForTest(0x100, 3, makePMTSection(t, []testStream{{0x1b, 0x101, bytes.Repeat([]byte{0xaa}, 200)}}))
	for _, data := range [][]byte{pat, pmt[:188], pmt[:188], pmt[188:]} {
		if _, err := f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	q := f.Quality()
	if q.CCDuplicateEvents != 1 || !q.ObservationLimited || q.SelectionUnverified {
		t.Fatalf("priority quality=%+v", q)
	}
}

func TestQualitySectionAndEntryBoundaries(t *testing.T) {
	for _, entries := range []int{64, 65} {
		streams := make([]testStream, entries)
		for i := range streams {
			streams[i] = testStream{0x1b, uint16(0x101 + i), nil}
		}
		f := newTSComponentFilter(&tsBufferFile{}, true, false, core.QualitySummary{})
		if _, err := f.Write(testTransportStream(t, streams)); err != nil {
			t.Fatal(err)
		}
		if f.Quality().SelectionUnverified != (entries == 65) {
			t.Fatalf("entries=%d quality=%+v", entries, f.Quality())
		}
	}
	for _, descriptor := range []int{1003, 1004} {
		f := newTSComponentFilter(&tsBufferFile{}, true, false, core.QualitySummary{})
		if _, err := f.Write(testTransportStream(t, []testStream{{0x1b, 0x101, bytes.Repeat([]byte{0xaa}, descriptor)}})); err != nil {
			t.Fatal(err)
		}
		if f.Quality().SelectionUnverified != (descriptor == 1004) {
			t.Fatalf("descriptor=%d quality=%+v", descriptor, f.Quality())
		}
	}
}
