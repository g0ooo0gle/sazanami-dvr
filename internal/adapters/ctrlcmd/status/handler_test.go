package status

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
)

type fixedSource struct {
	status StartupStatus
	err    error
	calls  int
}

func (s *fixedSource) Current(context.Context) (StartupStatus, error) {
	s.calls++
	return s.status, s.err
}

type fixedClock struct {
	instant time.Time
	calls   int
}

func (c *fixedClock) Now() time.Time {
	c.calls++
	return c.instant
}

func request(version uint16, target uint32) []byte {
	value := make([]byte, 14)
	binary.LittleEndian.PutUint32(value[0:4], uint32(Command))
	binary.LittleEndian.PutUint32(value[4:8], RequestBodySize)
	binary.LittleEndian.PutUint16(value[8:10], version)
	binary.LittleEndian.PutUint32(value[10:14], target)
	return value
}

func expectedResponse() []byte {
	value := make([]byte, ResponseFrameSize)
	binary.LittleEndian.PutUint32(value[0:4], uint32(ResultSuccess))
	binary.LittleEndian.PutUint32(value[4:8], ResponseBodySize)
	binary.LittleEndian.PutUint16(value[8:10], Version)
	binary.LittleEndian.PutUint32(value[10:14], StructureExtent)
	binary.LittleEndian.PutUint32(value[14:18], StatusNotificationID)
	fields := []uint16{2026, 7, 5, 31, 9, 0, 0, 123}
	position := 18
	for _, field := range fields {
		binary.LittleEndian.PutUint16(value[position:position+2], field)
		position += 2
	}
	position += 12 // param1〜param3はzero value。
	for range 3 {
		binary.LittleEndian.PutUint32(value[position:position+4], 6)
		position += 6 // extentとUTF-16 NUL。
	}
	return value
}

func TestHandleSyntheticGolden(t *testing.T) {
	source := &fixedSource{status: StartupNormal}
	clock := &fixedClock{instant: time.Date(2026, 7, 31, 0, 0, 0, 123_000_000, time.UTC)}
	handler := Handler{Source: source, Clock: clock}
	var destination bytes.Buffer
	if err := handler.Handle(context.Background(), request(Version, 0), &destination); err != nil {
		t.Fatal(err)
	}
	want := expectedResponse()
	if !bytes.Equal(destination.Bytes(), want) {
		t.Fatalf("response\n got=%x\nwant=%x", destination.Bytes(), want)
	}
	if len(want) != ResponseFrameSize || source.calls != 1 || clock.calls != 1 {
		t.Fatalf("len=%d source=%d clock=%d", len(want), source.calls, clock.calls)
	}
	if got := sha256.Sum256(want); got != sha256.Sum256(destination.Bytes()) {
		t.Fatal("synthetic golden hash mismatch")
	}
}

func TestUnsupportedVersionOrTarget(t *testing.T) {
	for _, test := range []struct {
		name    string
		version uint16
		target  uint32
	}{{"version", 4, 0}, {"target", 5, 1}} {
		t.Run(test.name, func(t *testing.T) {
			source := &fixedSource{}
			clock := &fixedClock{}
			var destination bytes.Buffer
			err := (Handler{Source: source, Clock: clock}).Handle(context.Background(), request(test.version, test.target), &destination)
			if err != nil {
				t.Fatal(err)
			}
			want := []byte{203, 0, 0, 0, 0, 0, 0, 0}
			if !bytes.Equal(destination.Bytes(), want) || source.calls != 0 || clock.calls != 0 {
				t.Fatalf("response=%x source=%d clock=%d", destination.Bytes(), source.calls, clock.calls)
			}
		})
	}
}

func TestMalformedRequestHasNoSideEffectOrResponse(t *testing.T) {
	mutations := map[string][]byte{
		"short":           request(5, 0)[:13],
		"trailing":        append(request(5, 0), 1),
		"wrong body size": append([]byte(nil), request(5, 0)...),
		"wrong command":   append([]byte(nil), request(5, 0)...),
	}
	binary.LittleEndian.PutUint32(mutations["wrong body size"][4:8], 5)
	binary.LittleEndian.PutUint32(mutations["wrong command"][0:4], 2201)
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			source := &fixedSource{}
			clock := &fixedClock{}
			var destination bytes.Buffer
			err := (Handler{Source: source, Clock: clock}).Handle(context.Background(), mutation, &destination)
			if err == nil || destination.Len() != 0 || source.calls != 0 || clock.calls != 0 {
				t.Fatalf("err=%v response=%x source=%d clock=%d", err, destination.Bytes(), source.calls, clock.calls)
			}
		})
	}
}

func TestSourceFailureAndOutOfProfileStatusFailClosed(t *testing.T) {
	for _, source := range []*fixedSource{
		{err: errors.New("source unavailable")},
		{status: StartupRecording},
		{status: StartupEPGGathering},
	} {
		var destination bytes.Buffer
		err := (Handler{Source: source, Clock: &fixedClock{instant: time.Now()}}).Handle(context.Background(), request(5, 0), &destination)
		if err == nil || destination.Len() != 0 {
			t.Fatalf("err=%v response=%x", err, destination.Bytes())
		}
	}
}

func TestMissingDependenciesFailClosed(t *testing.T) {
	var destination bytes.Buffer
	err := (Handler{}).Handle(context.Background(), request(5, 0), &destination)
	var codecError *codec.Error
	if !errors.As(err, &codecError) || codecError.Category != codec.Internal || destination.Len() != 0 {
		t.Fatalf("err=%v response=%x", err, destination.Bytes())
	}
}
