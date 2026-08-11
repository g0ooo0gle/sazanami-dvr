package recordinghttp

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/mpegts"
)

func TestHLSSegmenterKeepsPacketsAndDropsUnfinishedTail(t *testing.T) {
	for _, streamType := range []byte{0x02, 0x1b} {
		for _, chunkSize := range []int{1, 187, 188, 189, 192512} {
			t.Run(testHLSName(streamType, chunkSize), func(t *testing.T) {
				first := testHLSBoundary(t, 0, 0, 0, streamType, true, []uint16{1})
				second := testHLSBoundary(t, 1, 0, hlsClockHz, streamType, true, []uint16{1})
				third := testHLSBoundary(t, 2, 0, 2*hlsClockHz, streamType, true, []uint16{1})
				input := append(append(append([]byte(nil), first...), second...), third...)
				var got []hlsSegment
				segmenter, err := newHLSSegmenter(func(segment hlsSegment) error {
					got = append(got, segment)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				for offset := 0; offset < len(input); {
					end := min(len(input), offset+chunkSize)
					written, writeErr := segmenter.Write(input[offset:end])
					if writeErr != nil || written != end-offset {
						t.Fatalf("written=%d err=%v", written, writeErr)
					}
					offset = end
				}
				if err := segmenter.Finish(); err != nil {
					t.Fatal(err)
				}
				if len(got) != 2 || got[0].Sequence != 0 || got[1].Sequence != 1 ||
					got[0].Duration != time.Second || got[1].Duration != time.Second ||
					got[0].Discontinuity || got[1].Discontinuity ||
					!bytes.Equal(got[0].Data, first) || !bytes.Equal(got[1].Data, second) {
					t.Fatalf("segments=%+v", segmentSummary(got))
				}
			})
		}
	}
}

func TestHLSSegmenterAcceptsIndependentVersionWrapAndMarksDiscontinuity(t *testing.T) {
	wrap := uint64(1<<33) * 300
	first := testHLSBoundaryVersions(t, 0, 0, 31, 7, wrap-hlsClockHz, 0x1b, true, []uint16{1})
	second := testHLSBoundaryVersions(t, 1, 1, 0, 7, 0, 0x1b, true, []uint16{1})
	third := testHLSBoundaryVersions(t, 2, 2, 0, 7, hlsClockHz, 0x1b, true, []uint16{1})
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	if _, err := segmenter.Write(append(append(first, second...), third...)); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Duration != time.Second || got[0].Discontinuity || !got[1].Discontinuity {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterAcceptsPMTVersionWrapIndependently(t *testing.T) {
	first := testHLSBoundaryVersions(t, 0, 0, 7, 31, 0, 0x1b, true, []uint16{1})
	second := testHLSBoundaryVersions(t, 1, 1, 7, 0, hlsClockHz, 0x1b, true, []uint16{1})
	third := testHLSBoundaryVersions(t, 2, 2, 7, 0, 2*hlsClockHz, 0x1b, true, []uint16{1})
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	if _, err := segmenter.Write(append(append(first, second...), third...)); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Discontinuity || !got[1].Discontinuity ||
		!bytes.Equal(got[0].Data, first) || !bytes.Equal(got[1].Data, second) {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterMarksSupportedComponentChanges(t *testing.T) {
	changes := []struct {
		name       string
		pcrPID     uint16
		videoPID   uint16
		streamType byte
	}{
		{name: "PCR PID", pcrPID: 0x102, videoPID: 0x101, streamType: 0x1b},
		{name: "video PID", pcrPID: 0x101, videoPID: 0x102, streamType: 0x1b},
		{name: "video type", pcrPID: 0x101, videoPID: 0x101, streamType: 0x02},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			first := testHLSBoundaryComponents(t, 0, 0, 0, 0x101, 0x101, 0x1b)
			second := testHLSBoundaryComponents(t, 1, 1, hlsClockHz, change.pcrPID, change.videoPID, change.streamType)
			third := testHLSBoundaryComponents(t, 2, 1, 2*hlsClockHz, change.pcrPID, change.videoPID, change.streamType)
			var got []hlsSegment
			segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
				got = append(got, segment)
				return nil
			})
			if _, err := segmenter.Write(append(append(first, second...), third...)); err != nil {
				t.Fatal(err)
			}
			if err := segmenter.Finish(); err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].Discontinuity || !got[1].Discontinuity ||
				!bytes.Equal(got[0].Data, first) || !bytes.Equal(got[1].Data, second) {
				t.Fatalf("segments=%+v", segmentSummary(got))
			}
		})
	}
}

func TestHLSSegmenterAcceptsMaximumProviderChunk(t *testing.T) {
	first := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
	second := testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})
	fillerPackets := 1024 - len(first)/mpegts.PacketBytes - len(second)/mpegts.PacketBytes
	filler := testHLSNullPackets(fillerPackets)
	input := append(append(first, filler...), second...)
	if len(input) != 192512 {
		t.Fatalf("input bytes=%d", len(input))
	}
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	if written, err := segmenter.Write(input); err != nil || written != len(input) {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !bytes.Equal(got[0].Data, append(first, filler...)) {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterAcceptsRandomAccessBeforePESStart(t *testing.T) {
	first := testHLSBoundarySeparateAccess(t, 0, 0)
	second := testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})
	third := testHLSBoundary(t, 2, 0, 2*hlsClockHz, 0x1b, true, []uint16{1})
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	if _, err := segmenter.Write(append(append(first, second...), third...)); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !bytes.Equal(got[0].Data, first) {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterAcceptsBoundaryPCRAfterPESStart(t *testing.T) {
	first := testHLSBoundaryPCRAfterPES(t, 0, 0)
	second := testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})
	third := testHLSBoundary(t, 2, 0, 2*hlsClockHz, 0x1b, true, []uint16{1})
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	if _, err := segmenter.Write(append(append(first, second...), third...)); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !bytes.Equal(got[0].Data, first) {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterWaitsForOneSecondAndAcceptsTwoSeconds(t *testing.T) {
	first := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
	short := testHLSBoundary(t, 1, 0, hlsClockHz-1, 0x1b, true, []uint16{1})
	twoSeconds := testHLSBoundary(t, 2, 0, 2*hlsClockHz, 0x1b, true, []uint16{1})
	next := testHLSBoundary(t, 3, 0, 3*hlsClockHz, 0x1b, true, []uint16{1})
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	if _, err := segmenter.Write(append(append(append(first, short...), twoSeconds...), next...)); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Duration != 2*time.Second || got[1].Duration != time.Second ||
		!bytes.Equal(got[0].Data, append(append([]byte(nil), first...), short...)) {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterPCRBoundaryValues(t *testing.T) {
	tests := []struct {
		name       string
		firstClock uint64
		nextClock  uint64
		segments   int
		duration   time.Duration
		wantError  error
	}{
		{name: "one second before", nextClock: hlsMinimumTicks - 1},
		{name: "one second", nextClock: hlsMinimumTicks, segments: 1, duration: time.Second},
		{name: "one second after", nextClock: hlsMinimumTicks + 1, segments: 1, duration: time.Second + 37*time.Nanosecond},
		{name: "two seconds before", nextClock: hlsMaximumTicks - 1, segments: 1, duration: 2*time.Second - 38*time.Nanosecond},
		{name: "two seconds", nextClock: hlsMaximumTicks, segments: 1, duration: 2 * time.Second},
		{name: "two seconds after", nextClock: hlsMaximumTicks + 1, wantError: errTimestampInvalid},
		{name: "equal", firstClock: hlsClockHz, nextClock: hlsClockHz, wantError: errTimestampInvalid},
		{name: "reverse", firstClock: hlsClockHz, nextClock: hlsClockHz - 1, wantError: errTimestampInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []hlsSegment
			segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
				got = append(got, segment)
				return nil
			})
			input := append(testHLSBoundary(t, 0, 0, test.firstClock, 0x1b, true, []uint16{1}),
				testHLSBoundary(t, 1, 0, test.nextClock, 0x1b, true, []uint16{1})...)
			_, err := segmenter.Write(input)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("err=%v want=%v", err, test.wantError)
			}
			if len(got) != test.segments {
				t.Fatalf("segments=%+v", segmentSummary(got))
			}
			if len(got) != 0 && got[0].Duration != test.duration {
				t.Fatalf("duration=%s want=%s", got[0].Duration, test.duration)
			}
		})
	}
}

func TestHLSSegmenterRejectsNonIncreasingPCRBetweenBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		middlePCR   uint64
		boundaryPCR uint64
	}{
		{name: "reverse", middlePCR: 3 * hlsClockHz / 2, boundaryPCR: hlsClockHz},
		{name: "equal", middlePCR: hlsClockHz, boundaryPCR: hlsClockHz},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
			middle := testHLSPCROnlyPacket(0x101, 0, test.middlePCR)
			second := testHLSBoundary(t, 1, 0, test.boundaryPCR, 0x1b, true, []uint16{1})
			var got []hlsSegment
			segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
				got = append(got, segment)
				return nil
			})
			if _, err := segmenter.Write(append(append(first, middle...), second...)); !errors.Is(err, errTimestampInvalid) {
				t.Fatalf("err=%v", err)
			}
			if len(got) != 0 {
				t.Fatalf("segments=%+v", segmentSummary(got))
			}
		})
	}
}

func TestHLSSegmenterRejectsNonIncreasingPCRInsideCandidate(t *testing.T) {
	tests := []struct {
		name      string
		firstPCR  uint64
		secondPCR uint64
	}{
		{name: "reverse", firstPCR: 3 * hlsClockHz / 2, secondPCR: hlsClockHz},
		{name: "equal", firstPCR: hlsClockHz, secondPCR: hlsClockHz},
	}
	for _, test := range tests {
		for _, changedPID := range []bool{false, true} {
			name := test.name + "-initial"
			pid := uint16(0x101)
			version := byte(0)
			var input []byte
			if changedPID {
				name = test.name + "-PID-change"
				pid, version = 0x102, 1
				input = testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
			}
			t.Run(name, func(t *testing.T) {
				input := append(input, testHLSBoundaryWithPCRHistory(t, 1, version, test.firstPCR, test.secondPCR, pid)...)
				var got []hlsSegment
				segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
					got = append(got, segment)
					return nil
				})
				if _, err := segmenter.Write(input); !errors.Is(err, errTimestampInvalid) {
					t.Fatalf("err=%v", err)
				}
				if len(got) != 0 {
					t.Fatalf("segments=%+v", segmentSummary(got))
				}
			})
		}
	}
}

func TestHLSSegmenterKeepsLatestCandidatePCR(t *testing.T) {
	tests := []struct {
		name        string
		prefix      func(*testing.T) []byte
		boundary    uint64
		latest      uint64
		reverse     uint64
		pid         uint16
		version     byte
		wantEmitted int
	}{
		{
			name: "initial", prefix: func(*testing.T) []byte { return nil },
			boundary: 0, latest: hlsClockHz / 2, reverse: hlsClockHz / 4, pid: 0x101,
		},
		{
			name: "PID change",
			prefix: func(t *testing.T) []byte {
				return testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
			},
			boundary: hlsClockHz, latest: 3 * hlsClockHz / 2, reverse: 5 * hlsClockHz / 4,
			pid: 0x102, version: 1, wantEmitted: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := test.prefix(t)
			input = append(input, testHLSBoundaryWithPCRHistory(t, 1, test.version, test.boundary, test.latest, test.pid)...)
			input = append(input, testHLSPCROnlyPacket(test.pid, 4, test.reverse)...)
			var got []hlsSegment
			segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
				got = append(got, segment)
				return nil
			})
			if _, err := segmenter.Write(input); !errors.Is(err, errTimestampInvalid) {
				t.Fatalf("err=%v", err)
			}
			if len(got) != test.wantEmitted {
				t.Fatalf("segments=%+v", segmentSummary(got))
			}
		})
	}
}

func TestHLSSegmenterTracksCandidatePCRAcrossWrap(t *testing.T) {
	wrap := uint64(1<<33) * 300
	first := testHLSBoundaryWithPCRHistory(t, 0, 0, wrap-hlsClockHz/2, 0, 0x101)
	second := testHLSBoundary(t, 1, 0, hlsClockHz/2, 0x1b, true, []uint16{1})
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	if _, err := segmenter.Write(append(first, second...)); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Duration != time.Second || !bytes.Equal(got[0].Data, first) {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterRejectsCandidatePCRSpanOverTwoSeconds(t *testing.T) {
	for _, changedPID := range []bool{false, true} {
		name := "initial"
		pid := uint16(0x101)
		version := byte(0)
		start := uint64(0)
		var input []byte
		if changedPID {
			name, pid, version = "PCR PID change", 0x102, 1
			start = hlsClockHz
			input = testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
		}
		t.Run(name, func(t *testing.T) {
			input := append(input, testHLSCandidateClocks(t, 1, version, pid,
				start, start+hlsClockHz, start+2*hlsClockHz, start+2*hlsClockHz+1)...)
			segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
			if _, err := segmenter.Write(input); !errors.Is(err, errRandomAccess) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestHLSSegmenterAcceptsCandidatePCRSpanAtTwoSecondsAcrossWrites(t *testing.T) {
	first := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
	candidate := testHLSBoundaryWithPCRHistory(t, 1, 1, hlsClockHz, 3*hlsClockHz, 0x102)
	pesOffset := len(candidate) - mpegts.PacketBytes
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	if _, err := segmenter.Write(append(first, candidate[:pesOffset]...)); err != nil {
		t.Fatal(err)
	}
	if _, err := segmenter.Write(candidate[pesOffset:]); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Duration != time.Second || !bytes.Equal(got[0].Data, first) {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterTracksPCRAcrossWrap(t *testing.T) {
	wrap := uint64(1<<33) * 300
	first := testHLSBoundary(t, 0, 0, wrap-hlsClockHz, 0x1b, true, []uint16{1})
	middle := testHLSPCROnlyPacket(0x101, 0, wrap-hlsClockHz/2)
	second := testHLSBoundary(t, 1, 0, 0, 0x1b, true, []uint16{1})
	third := testHLSBoundary(t, 2, 0, hlsClockHz, 0x1b, true, []uint16{1})
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	input := append(append(append(first, middle...), second...), third...)
	if _, err := segmenter.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Duration != time.Second || got[1].Duration != time.Second ||
		!bytes.Equal(got[0].Data, append(append([]byte(nil), first...), middle...)) ||
		!bytes.Equal(got[1].Data, second) {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterRejectsDiscontinuityBeforeCandidate(t *testing.T) {
	first := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
	discontinuity := testHLSPCROnlyPacket(0x101, 0, hlsClockHz/2)
	discontinuity[5] |= 0x80
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	input := testHLSPadToSync(append(first, discontinuity...))
	if _, err := segmenter.Write(input); !errors.Is(err, errTimestampInvalid) {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
	if segmenter.data != nil || segmenter.active != nil || segmenter.candidate != nil || segmenter.emit != nil {
		t.Fatal("失敗後にbufferが残りました")
	}
	if _, err := segmenter.Write(nil); !errors.Is(err, errTimestampInvalid) {
		t.Fatalf("second write err=%v", err)
	}
	if err := segmenter.Finish(); !errors.Is(err, errTimestampInvalid) {
		t.Fatalf("finish err=%v", err)
	}
}

func TestHLSSegmenterRejectsDiscontinuityInsideCandidate(t *testing.T) {
	input := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
	input = append(input, testHLSPacketize(t, 0, 1, testHLSPAT([]uint16{1}, 0))...)
	input = append(input, testHLSPacketize(t, 0x100, 1, testHLSPMT(0x101, 0x101, 0x1b, 0))...)
	clock := testHLSPCRPacket(0x101, 1, hlsClockHz, true)
	clock[5] |= 0x80
	input = append(input, clock...)
	segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
	if _, err := segmenter.Write(input); !errors.Is(err, errTimestampInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateHLSSegmentRejectsDiscontinuityInsideSegment(t *testing.T) {
	data := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
	data[2*mpegts.PacketBytes+5] |= 0x80
	if err := validateHLSSegment(data); !errors.Is(err, errTimestampInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestHLSSegmenterReturnsStableMediaReasons(t *testing.T) {
	tests := []struct {
		name   string
		input  func(*testing.T) []byte
		reason error
	}{
		{
			name: "multiple programs",
			input: func(t *testing.T) []byte {
				return append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1, 2}),
					testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1, 2})...)
			},
			reason: errSingleProgramRequired,
		},
		{
			name: "unsupported video",
			input: func(t *testing.T) []byte {
				return append(testHLSBoundary(t, 0, 0, 0, 0x24, true, []uint16{1}),
					testHLSBoundary(t, 1, 0, hlsClockHz, 0x24, true, []uint16{1})...)
			},
			reason: errVideoUnsupported,
		},
		{
			name: "over two seconds",
			input: func(t *testing.T) []byte {
				return append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}),
					testHLSBoundary(t, 1, 0, 2*hlsClockHz+1, 0x1b, true, []uint16{1})...)
			},
			reason: errTimestampInvalid,
		},
		{
			name: "random access missing",
			input: func(t *testing.T) []byte {
				input := append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}),
					testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, false, []uint16{1})...)
				return append(input, testHLSPCRPacket(0x101, 2, 2*hlsClockHz+1, false)...)
			},
			reason: errRandomAccess,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
			if _, err := segmenter.Write(test.input(t)); !errors.Is(err, test.reason) {
				t.Fatalf("err=%v want=%v", err, test.reason)
			}
		})
	}
}

func TestHLSSegmenterRejectsSyncLossTruncationAndSameVersionChange(t *testing.T) {
	segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
	garbage := bytes.Repeat([]byte{0}, mpegts.MaxSyncSearchBytes+1+(5-1)*mpegts.PacketBytes+1)
	if _, err := segmenter.Write(garbage); !errors.Is(err, errTSSyncUnavailable) {
		t.Fatalf("sync err=%v", err)
	}

	segmenter, _ = newHLSSegmenter(func(hlsSegment) error { return nil })
	input := append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}),
		testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})...)
	if _, err := segmenter.Write(append(input, 0x47)); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); !errors.Is(err, errTSSyncUnavailable) {
		t.Fatalf("truncated err=%v", err)
	}

	segmenter, _ = newHLSSegmenter(func(hlsSegment) error { return nil })
	first := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
	changed := testHLSBoundaryWithPID(t, 1, 0, hlsClockHz, 0x1b, 0x102)
	if _, err := segmenter.Write(append(first, changed...)); !errors.Is(err, errPSIInvalid) {
		t.Fatalf("same version changed err=%v", err)
	}
}

func TestHLSSegmenterRejectsByteLossAndInsertionAfterSync(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func([]byte, []byte) []byte
	}{
		{
			name: "one byte missing",
			corrupt: func(null, boundary []byte) []byte {
				input := append(append([]byte(nil), null...), null[:len(null)-1]...)
				return append(input, boundary...)
			},
		},
		{
			name: "one byte inserted",
			corrupt: func(null, boundary []byte) []byte {
				input := append(append([]byte(nil), null...), 0)
				return append(input, boundary...)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
			initial := append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}),
				testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})...)
			if _, err := segmenter.Write(initial); err != nil {
				t.Fatal(err)
			}
			null := testHLSNullPackets(1)
			boundary := testHLSBoundary(t, 2, 0, 2*hlsClockHz, 0x1b, true, []uint16{1})
			if _, err := segmenter.Write(test.corrupt(null, boundary)); !errors.Is(err, errTSSyncUnavailable) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestHLSSegmenterDoesNotOverwritePendingFormatChange(t *testing.T) {
	tests := []struct {
		name   string
		second func(*testing.T) []byte
	}{
		{
			name: "version",
			second: func(t *testing.T) []byte {
				return testHLSBoundary(t, 1, 1, hlsClockHz, 0x1b, false, []uint16{1})
			},
		},
		{
			name: "PID",
			second: func(t *testing.T) []byte {
				return testHLSBoundaryWithPIDAccess(t, 1, 1, hlsClockHz, 0x1b, 0x102, false)
			},
		},
		{
			name: "discontinuity",
			second: func(t *testing.T) []byte {
				value := testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, false, []uint16{1})
				testHLSMarkDiscontinuity(value[:mpegts.PacketBytes])
				return value
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
			second := test.second(t)
			var got []hlsSegment
			segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
				got = append(got, segment)
				return nil
			})
			input := append(append([]byte(nil), first...), second...)
			input = append(input, testHLSBoundary(t, 2, 1, 2*hlsClockHz, 0x1b, true, []uint16{1})...)
			if _, err := segmenter.Write(input); !errors.Is(err, errRandomAccess) {
				t.Fatalf("err=%v", err)
			}
			if len(got) != 0 {
				t.Fatalf("segments=%+v", segmentSummary(got))
			}

			got = nil
			segmenter, _ = newHLSSegmenter(func(segment hlsSegment) error {
				got = append(got, segment)
				return nil
			})
			if _, err := segmenter.Write(append(append([]byte(nil), first...), second...)); err != nil {
				t.Fatal(err)
			}
			if err := segmenter.Finish(); !errors.Is(err, errRandomAccess) {
				t.Fatalf("finish err=%v", err)
			}
			if len(got) != 0 {
				t.Fatalf("finish segments=%+v", segmentSummary(got))
			}
		})
	}
}

func TestHLSSegmenterRejectsPMTChangeOutsideCandidate(t *testing.T) {
	changes := []struct {
		name     string
		pcrPID   uint16
		videoPID uint16
	}{
		{name: "version", pcrPID: 0x101, videoPID: 0x101},
		{name: "PCR PID", pcrPID: 0x102, videoPID: 0x101},
		{name: "video PID", pcrPID: 0x101, videoPID: 0x102},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			first := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
			updated := testHLSPacketize(t, 0x100, 1, testHLSPMT(change.pcrPID, change.videoPID, 0x1b, 1))
			segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
			if _, err := segmenter.Write(testHLSPadToSync(append(first, updated...))); !errors.Is(err, errPSIInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestHLSSegmenterRejectsSecondPMTChangeInsideCandidate(t *testing.T) {
	changes := []struct {
		name     string
		pcrPID   uint16
		videoPID uint16
	}{
		{name: "version", pcrPID: 0x101, videoPID: 0x101},
		{name: "PCR PID", pcrPID: 0x102, videoPID: 0x101},
		{name: "video PID", pcrPID: 0x101, videoPID: 0x102},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			input := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
			input = append(input, testHLSPacketize(t, 0, 1, testHLSPAT([]uint16{1}, 1))...)
			input = append(input, testHLSPacketize(t, 0x100, 1, testHLSPMT(0x101, 0x101, 0x1b, 1))...)
			input = append(input, testHLSPacketize(t, 0x100, 2, testHLSPMT(change.pcrPID, change.videoPID, 0x1b, 2))...)
			segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
			if _, err := segmenter.Write(input); !errors.Is(err, errPSIInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestValidateHLSSegmentRejectsPMTChange(t *testing.T) {
	changes := []struct {
		name     string
		pcrPID   uint16
		videoPID uint16
	}{
		{name: "version", pcrPID: 0x101, videoPID: 0x101},
		{name: "PCR PID", pcrPID: 0x102, videoPID: 0x101},
		{name: "video PID", pcrPID: 0x101, videoPID: 0x102},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			data := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
			data = append(data, testHLSPacketize(t, 0x100, 1, testHLSPMT(change.pcrPID, change.videoPID, 0x1b, 1))...)
			if err := validateHLSSegment(data); !errors.Is(err, errPSIInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestHLSSegmenterMarksPIDAndPCRDiscontinuities(t *testing.T) {
	tests := []struct {
		name   string
		second func(*testing.T) []byte
		third  func(*testing.T) []byte
	}{
		{
			name: "PID",
			second: func(t *testing.T) []byte {
				return testHLSBoundaryWithPID(t, 1, 1, hlsClockHz, 0x1b, 0x102)
			},
			third: func(t *testing.T) []byte {
				return testHLSBoundaryWithPID(t, 2, 1, 2*hlsClockHz, 0x1b, 0x102)
			},
		},
		{
			name: "PAT discontinuity",
			second: func(t *testing.T) []byte {
				value := testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})
				testHLSMarkDiscontinuity(value[:mpegts.PacketBytes])
				return value
			},
			third: func(t *testing.T) []byte {
				return testHLSBoundary(t, 2, 0, 2*hlsClockHz, 0x1b, true, []uint16{1})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got []hlsSegment
			segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
				got = append(got, segment)
				return nil
			})
			input := append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}), test.second(t)...)
			input = append(input, test.third(t)...)
			if _, err := segmenter.Write(input); err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].Discontinuity || !got[1].Discontinuity {
				t.Fatalf("segments=%+v", segmentSummary(got))
			}
		})
	}
}

func TestSelectHLSVideoRejectsUnsupportedVideoBesideH264(t *testing.T) {
	if _, _, err := selectHLSVideo([]mpegts.ElementaryStream{
		{Type: 0x1b, PID: 0x101}, {Type: 0x24, PID: 0x102},
	}); !errors.Is(err, errVideoUnsupported) {
		t.Fatalf("H.264 + HEVC err=%v", err)
	}
	pid, streamType, err := selectHLSVideo([]mpegts.ElementaryStream{
		{Type: 0x1b, PID: 0x101}, {Type: 0x0f, PID: 0x102}, {Type: 0x06, PID: 0x103}, {Type: 0x0d, PID: 0x104},
	})
	if err != nil || pid != 0x101 || streamType != 0x1b {
		t.Fatalf("pid=%x type=%x err=%v", pid, streamType, err)
	}
	if _, _, err := selectHLSVideo([]mpegts.ElementaryStream{{Type: 0x99, PID: 0x101}}); !errors.Is(err, errVideoUnsupported) {
		t.Fatalf("unknown stream err=%v", err)
	}
	if _, _, err := selectHLSVideo([]mpegts.ElementaryStream{{Type: 0x0f, PID: 0x101}}); !errors.Is(err, errVideoUnsupported) {
		t.Fatalf("video missing err=%v", err)
	}
	if _, _, err := selectHLSVideo([]mpegts.ElementaryStream{
		{Type: 0x02, PID: 0x101}, {Type: 0x1b, PID: 0x102},
	}); !errors.Is(err, errSingleProgramRequired) {
		t.Fatalf("multiple video err=%v", err)
	}
}

func TestHLSSegmenterPropagatesSinkFailure(t *testing.T) {
	want := errors.New("cache-write-failed")
	segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return want })
	input := append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}),
		testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})...)
	if _, err := segmenter.Write(input); !errors.Is(err, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestHLSSegmenterRejectsSegmentSizeOneOver(t *testing.T) {
	alignedMaximum := hlsMaximumSegment - hlsMaximumSegment%mpegts.PacketBytes
	data := make([]byte, alignedMaximum+mpegts.PacketBytes)
	segmenter := &hlsSegmenter{
		active: &hlsBoundary{}, candidate: &hlsCandidate{start: alignedMaximum / 2},
		data: data[:alignedMaximum-mpegts.PacketBytes],
	}
	packet := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
	packet[0], packet[1], packet[2], packet[3] = 0x47, 0x1f, 0xff, 0x10
	if err := segmenter.processPacket(packet); err != nil || len(segmenter.data) != alignedMaximum {
		t.Fatalf("exact err=%v", err)
	}
	if err := segmenter.processPacket(packet); !errors.Is(err, errRandomAccess) {
		t.Fatalf("err=%v", err)
	}
}

func TestHLSSegmenterFinishRejectsMissingInitialBoundary(t *testing.T) {
	tests := []struct {
		name   string
		input  func(*testing.T) []byte
		reason error
	}{
		{
			name: "PAT missing",
			input: func(*testing.T) []byte {
				return testHLSPadToSync(nil)
			},
			reason: errPSIInvalid,
		},
		{
			name: "PMT missing",
			input: func(t *testing.T) []byte {
				return testHLSPadToSync(testHLSPacketize(t, 0, 0, testHLSPAT([]uint16{1}, 0)))
			},
			reason: errPSIInvalid,
		},
		{
			name: "PCR missing",
			input: func(t *testing.T) []byte {
				input := testHLSPacketize(t, 0, 0, testHLSPAT([]uint16{1}, 0))
				input = append(input, testHLSPacketize(t, 0x100, 0, testHLSPMT(0x101, 0x101, 0x1b, 0))...)
				return testHLSPadToSync(input)
			},
			reason: errTimestampInvalid,
		},
		{
			name: "random access missing",
			input: func(t *testing.T) []byte {
				return testHLSPadToSync(testHLSBoundary(t, 0, 0, 0, 0x1b, false, []uint16{1}))
			},
			reason: errRandomAccess,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
			if _, err := segmenter.Write(test.input(t)); err != nil {
				t.Fatal(err)
			}
			if err := segmenter.Finish(); !errors.Is(err, test.reason) {
				t.Fatalf("err=%v want=%v", err, test.reason)
			}
		})
	}
}

func TestHLSSegmenterFinishRejectsIncompletePSI(t *testing.T) {
	tests := []struct {
		name  string
		input func(*testing.T) []byte
	}{
		{
			name: "PAT",
			input: func(*testing.T) []byte {
				return testHLSPadToSync(testHLSTruncatedSectionPacket(0, 0))
			},
		},
		{
			name: "PMT",
			input: func(t *testing.T) []byte {
				input := testHLSPacketize(t, 0, 0, testHLSPAT([]uint16{1}, 0))
				return testHLSPadToSync(append(input, testHLSTruncatedSectionPacket(0x100, 0)...))
			},
		},
		{
			name: "after active boundary",
			input: func(t *testing.T) []byte {
				input := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
				return testHLSPadToSync(append(input, testHLSTruncatedSectionPacket(0, 1)...))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
			if _, err := segmenter.Write(test.input(t)); err != nil {
				t.Fatal(err)
			}
			if err := segmenter.Finish(); !errors.Is(err, errPSIInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestValidateHLSSegmentRejectsIncompletePSI(t *testing.T) {
	data := testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1})
	data = append(data, testHLSTruncatedSectionPacket(0x100, 1)...)
	if err := validateHLSSegment(data); !errors.Is(err, errPSIInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestHLSSegmenterFinishEndsWritesAndReleasesBuffers(t *testing.T) {
	valid := append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}),
		testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})...)
	segmenter, _ := newHLSSegmenter(func(hlsSegment) error { return nil })
	if _, err := segmenter.Write(valid); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); err != nil {
		t.Fatal(err)
	}
	if segmenter.data != nil || segmenter.active != nil || segmenter.candidate != nil ||
		segmenter.pmtKnown || segmenter.lastPCRKnown || segmenter.emit != nil {
		t.Fatal("正常終了後にbufferが残りました")
	}
	if _, err := segmenter.Write(valid); !errors.Is(err, errSessionEnded) {
		t.Fatalf("write after finish err=%v", err)
	}

	segmenter, _ = newHLSSegmenter(func(hlsSegment) error { return nil })
	if _, err := segmenter.Write(append(valid, 0x47)); err != nil {
		t.Fatal(err)
	}
	if err := segmenter.Finish(); !errors.Is(err, errTSSyncUnavailable) {
		t.Fatalf("finish err=%v", err)
	}
	if segmenter.data != nil || segmenter.active != nil || segmenter.candidate != nil ||
		segmenter.pmtKnown || segmenter.lastPCRKnown || segmenter.emit != nil {
		t.Fatal("失敗終了後にbufferが残りました")
	}
}

func TestHLSSegmenterRejectsSequenceOverflow(t *testing.T) {
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	segmenter.sequence = math.MaxUint64
	input := append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}),
		testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})...)
	if _, err := segmenter.Write(input); !errors.Is(err, errSessionEnded) {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

func TestHLSSegmenterStopsBeforeSequenceWrap(t *testing.T) {
	var got []hlsSegment
	segmenter, _ := newHLSSegmenter(func(segment hlsSegment) error {
		got = append(got, segment)
		return nil
	})
	segmenter.sequence = math.MaxUint64 - 1
	input := append(testHLSBoundary(t, 0, 0, 0, 0x1b, true, []uint16{1}),
		testHLSBoundary(t, 1, 0, hlsClockHz, 0x1b, true, []uint16{1})...)
	input = append(input, testHLSBoundary(t, 2, 0, 2*hlsClockHz, 0x1b, true, []uint16{1})...)
	if _, err := segmenter.Write(input); !errors.Is(err, errSessionEnded) {
		t.Fatalf("err=%v", err)
	}
	if len(got) != 1 || got[0].Sequence != math.MaxUint64-1 {
		t.Fatalf("segments=%+v", segmentSummary(got))
	}
}

type hlsSegmentSummary struct {
	sequence      uint64
	bytes         int
	duration      time.Duration
	discontinuity bool
}

func segmentSummary(segments []hlsSegment) []hlsSegmentSummary {
	result := make([]hlsSegmentSummary, 0, len(segments))
	for _, segment := range segments {
		result = append(result, hlsSegmentSummary{segment.Sequence, len(segment.Data), segment.Duration, segment.Discontinuity})
	}
	return result
}

func testHLSName(streamType byte, chunk int) string {
	return testHLSUint(uint64(streamType)) + "-" + testHLSUint(uint64(chunk))
}

func testHLSUint(value uint64) string {
	return strconv.FormatUint(value, 10)
}

func testHLSBoundary(t *testing.T, continuity byte, version byte, clock uint64, streamType byte, randomAccess bool, programs []uint16) []byte {
	t.Helper()
	return testHLSBoundaryVersions(t, continuity, continuity, version, version, clock, streamType, randomAccess, programs)
}

func testHLSBoundaryVersions(t *testing.T, patContinuity, pmtContinuity, patVersion, pmtVersion byte, clock uint64, streamType byte, randomAccess bool, programs []uint16) []byte {
	t.Helper()
	pat := testHLSPAT(programs, patVersion)
	pmt := testHLSPMT(0x101, 0x101, streamType, pmtVersion)
	result := testHLSPacketize(t, 0, patContinuity, pat)
	result = append(result, testHLSPacketize(t, 0x100, pmtContinuity, pmt)...)
	result = append(result, testHLSPCRPacket(0x101, patContinuity, clock, randomAccess)...)
	return result
}

func testHLSBoundaryWithPID(t *testing.T, continuity, version byte, clock uint64, streamType byte, videoPID uint16) []byte {
	return testHLSBoundaryWithPIDAccess(t, continuity, version, clock, streamType, videoPID, true)
}

func testHLSBoundaryWithPIDAccess(t *testing.T, continuity, version byte, clock uint64, streamType byte, videoPID uint16, randomAccess bool) []byte {
	t.Helper()
	result := testHLSPacketize(t, 0, continuity, testHLSPAT([]uint16{1}, version))
	result = append(result, testHLSPacketize(t, 0x100, continuity, testHLSPMT(videoPID, videoPID, streamType, version))...)
	result = append(result, testHLSPCRPacket(videoPID, continuity, clock, randomAccess)...)
	return result
}

func testHLSBoundaryComponents(t *testing.T, continuity, version byte, clock uint64, pcrPID, videoPID uint16, streamType byte) []byte {
	t.Helper()
	result := testHLSPacketize(t, 0, continuity, testHLSPAT([]uint16{1}, version))
	result = append(result, testHLSPacketize(t, 0x100, continuity, testHLSPMT(pcrPID, videoPID, streamType, version))...)
	result = append(result, testHLSPCROnlyPacket(pcrPID, continuity, clock)...)
	return append(result, testHLSRandomAccessPacket(videoPID, continuity)...)
}

func testHLSBoundarySeparateAccess(t *testing.T, continuity byte, clock uint64) []byte {
	t.Helper()
	result := testHLSPacketize(t, 0, continuity, testHLSPAT([]uint16{1}, 0))
	result = append(result, testHLSPacketize(t, 0x100, continuity, testHLSPMT(0x101, 0x101, 0x1b, 0))...)
	randomAccess := testHLSPCRPacket(0x101, continuity, clock, true)
	randomAccess[1] &^= 0x40
	result = append(result, randomAccess...)
	pes := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
	pes[0], pes[1], pes[2], pes[3] = 0x47, 0x41, 0x01, 0x10|(continuity+1)&0x0f
	copy(pes[4:], []byte{0, 0, 1, 0xe0})
	return append(result, pes...)
}

func testHLSBoundaryPCRAfterPES(t *testing.T, continuity byte, clock uint64) []byte {
	t.Helper()
	result := testHLSPacketize(t, 0, continuity, testHLSPAT([]uint16{1}, 0))
	result = append(result, testHLSPacketize(t, 0x100, continuity, testHLSPMT(0x101, 0x101, 0x1b, 0))...)
	pes := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
	pes[0], pes[1], pes[2], pes[3], pes[4], pes[5] = 0x47, 0x41, 0x01, 0x30|continuity&0x0f, 1, 0x40
	copy(pes[6:], []byte{0, 0, 1, 0xe0})
	result = append(result, pes...)
	return append(result, testHLSPCROnlyPacket(0x101, continuity, clock)...)
}

func testHLSBoundaryWithPCRHistory(t *testing.T, continuity, version byte, firstClock, lastClock uint64, videoPID uint16) []byte {
	t.Helper()
	result := testHLSPacketize(t, 0, continuity, testHLSPAT([]uint16{1}, version))
	result = append(result, testHLSPacketize(t, 0x100, continuity, testHLSPMT(videoPID, videoPID, 0x1b, version))...)
	access := testHLSPCRPacket(videoPID, continuity, firstClock, true)
	access[1] &^= 0x40
	result = append(result, access...)
	result = append(result, testHLSPCROnlyPacket(videoPID, continuity+1, lastClock)...)
	pes := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
	pes[0], pes[1], pes[2], pes[3] = 0x47, 0x40|byte(videoPID>>8)&0x1f, byte(videoPID), 0x10|(continuity+2)&0x0f
	copy(pes[4:], []byte{0, 0, 1, 0xe0})
	return append(result, pes...)
}

func testHLSCandidateClocks(t *testing.T, continuity, version byte, pcrPID uint16, clocks ...uint64) []byte {
	t.Helper()
	result := testHLSPacketize(t, 0, continuity, testHLSPAT([]uint16{1}, version))
	result = append(result, testHLSPacketize(t, 0x100, continuity, testHLSPMT(pcrPID, pcrPID, 0x1b, version))...)
	for index, clock := range clocks {
		result = append(result, testHLSPCROnlyPacket(pcrPID, continuity+byte(index), clock)...)
	}
	return result
}

func testHLSPAT(programs []uint16, version byte) []byte {
	section := []byte{0x00, 0xb0, 0, 0, 1, 0xc1 | version<<1, 0, 0}
	for index, program := range programs {
		pid := uint16(0x100 + index)
		section = append(section, byte(program>>8), byte(program), 0xe0|byte(pid>>8), byte(pid))
	}
	return testHLSFinishSection(section)
}

func testHLSPMT(pcrPID, videoPID uint16, streamType, version byte) []byte {
	section := []byte{
		0x02, 0xb0, 0, 0, 1, 0xc1 | version<<1, 0, 0,
		0xe0 | byte(pcrPID>>8), byte(pcrPID), 0xf0, 0,
		streamType, 0xe0 | byte(videoPID>>8), byte(videoPID), 0xf0, 0,
	}
	return testHLSFinishSection(section)
}

func testHLSFinishSection(section []byte) []byte {
	length := len(section) + 4 - 3
	section[1] = section[1]&0xf0 | byte(length>>8)
	section[2] = byte(length)
	crc := mpegts.CRC32(section)
	return append(section, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func testHLSPacketize(t *testing.T, pid uint16, continuity byte, section []byte) []byte {
	t.Helper()
	packets, err := mpegts.PacketizeSection(pid, continuity, section)
	if err != nil {
		t.Fatal(err)
	}
	var result []byte
	for _, packet := range packets {
		result = append(result, packet...)
	}
	return result
}

func testHLSPadToSync(input []byte) []byte {
	result := append([]byte(nil), input...)
	for len(result) < 5*mpegts.PacketBytes {
		result = append(result, testHLSNullPackets(1)...)
	}
	return result
}

func testHLSNullPackets(count int) []byte {
	result := make([]byte, 0, count*mpegts.PacketBytes)
	for range count {
		packet := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
		packet[0], packet[1], packet[2], packet[3] = 0x47, 0x1f, 0xff, 0x10
		result = append(result, packet...)
	}
	return result
}

func testHLSTruncatedSectionPacket(pid uint16, continuity byte) []byte {
	packet := bytes.Repeat([]byte{0}, mpegts.PacketBytes)
	packet[0], packet[1], packet[2], packet[3], packet[4] =
		0x47, 0x40|byte(pid>>8)&0x1f, byte(pid), 0x10|continuity&0x0f, 0
	packet[5], packet[6], packet[7] = 0x00, 0xb1, 0x2c
	return packet
}

func testHLSMarkDiscontinuity(packet []byte) {
	payload := append([]byte(nil), packet[4:mpegts.PacketBytes-2]...)
	packet[3] = packet[3]&0x0f | 0x30
	packet[4], packet[5] = 1, 0x80
	copy(packet[6:], payload)
	packet[mpegts.PacketBytes-2], packet[mpegts.PacketBytes-1] = 0xff, 0xff
}

func testHLSPCRPacket(pid uint16, continuity byte, clock uint64, randomAccess bool) []byte {
	packet := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
	packet[0], packet[1], packet[2], packet[3] = 0x47, 0x40|byte(pid>>8)&0x1f, byte(pid), 0x30|continuity&0x0f
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

func testHLSRandomAccessPacket(pid uint16, continuity byte) []byte {
	packet := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
	packet[0], packet[1], packet[2], packet[3], packet[4], packet[5] =
		0x47, 0x40|byte(pid>>8)&0x1f, byte(pid), 0x30|continuity&0x0f, 1, 0x40
	copy(packet[6:], []byte{0, 0, 1, 0xe0})
	return packet
}

func testHLSPCROnlyPacket(pid uint16, continuity byte, clock uint64) []byte {
	packet := bytes.Repeat([]byte{0xff}, mpegts.PacketBytes)
	packet[0], packet[1], packet[2], packet[3], packet[4], packet[5] = 0x47, byte(pid>>8)&0x1f, byte(pid), 0x20|continuity&0x0f, 183, 0x10
	base, extension := clock/300, clock%300
	packet[6] = byte(base >> 25)
	packet[7] = byte(base >> 17)
	packet[8] = byte(base >> 9)
	packet[9] = byte(base >> 1)
	packet[10] = byte(base&1)<<7 | 0x7e | byte(extension>>8)
	packet[11] = byte(extension)
	return packet
}
