package channel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf16"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
)

type fixedSource struct {
	snapshot Snapshot
	err      error
	after    func()
	calls    atomic.Int32
}

func (s *fixedSource) Current(context.Context) (Snapshot, error) {
	s.calls.Add(1)
	if s.after != nil {
		s.after()
	}
	return s.snapshot, s.err
}

func syntheticService() Service {
	return Service{
		ProviderLocator: "synthetic:1", ProviderName: "提供", ServiceName: "波", NetworkName: "ネット",
		TransportStreamName: "TS-A", NetworkID: 1, TransportStreamID: 2, ServiceID: 3, ServiceType: 1,
		RemoteControlKey: 10, PartialReception: true, EPGCapture: false, Search: true, Verified: true, Selected: true,
	}
}

func appendI32(destination []byte, value int32) []byte {
	var encoded [4]byte
	binary.LittleEndian.PutUint32(encoded[:], uint32(value))
	return append(destination, encoded[:]...)
}

func appendU16(destination []byte, value uint16) []byte {
	var encoded [2]byte
	binary.LittleEndian.PutUint16(encoded[:], value)
	return append(destination, encoded[:]...)
}

func appendTestString(destination []byte, value string) []byte {
	units := utf16.Encode([]rune(value))
	extent := 4 + (len(units)+1)*2
	destination = appendI32(destination, int32(extent))
	for _, unit := range units {
		destination = appendU16(destination, unit)
	}
	return appendU16(destination, 0)
}

func fileRequest(value string) []byte {
	body := appendTestString(nil, value)
	request := appendI32(nil, 1060)
	request = appendI32(request, int32(len(body)))
	return append(request, body...)
}

func enumRequest() []byte {
	request := appendI32(nil, 1021)
	return appendI32(request, 0)
}

func expectedFileResponse() []byte {
	line := []byte("波\tネット\t1\t2\t3\t1\t1\t0\t1\t10\n")
	response := appendI32(nil, 1)
	response = appendI32(response, int32(len(line)))
	return append(response, line...)
}

func expectedServiceResponse(service Service) []byte {
	structure := appendI32(nil, 0)
	structure = appendU16(structure, service.NetworkID)
	structure = appendU16(structure, service.TransportStreamID)
	structure = appendU16(structure, service.ServiceID)
	structure = append(structure, service.ServiceType, 1)
	for _, name := range []string{service.ProviderName, service.ServiceName, service.NetworkName, service.TransportStreamName} {
		structure = appendTestString(structure, name)
	}
	structure = append(structure, service.RemoteControlKey)
	binary.LittleEndian.PutUint32(structure[:4], uint32(len(structure)))
	body := appendI32(nil, int32(8+len(structure)))
	body = appendI32(body, 1)
	body = append(body, structure...)
	response := appendI32(nil, 1)
	response = appendI32(response, int32(len(body)))
	return append(response, body...)
}

func requireCodecCategory(t *testing.T, err error, category codec.Category) *codec.Error {
	t.Helper()
	var codecError *codec.Error
	if !errors.As(err, &codecError) || codecError.Category != category {
		t.Fatalf("err=%v category=%v, want=%s", err, codecError, category)
	}
	return codecError
}

func TestSyntheticGoldenResponses(t *testing.T) {
	service := syntheticService()
	for _, test := range []struct {
		name    string
		request []byte
		want    []byte
	}{
		{"1060", fileRequest("ChSet5.txt"), expectedFileResponse()},
		{"1021", enumRequest(), expectedServiceResponse(service)},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &fixedSource{snapshot: Snapshot{Key: "generation-1", Services: []Service{service}}}
			var destination bytes.Buffer
			if err := (Handler{Source: source}).Handle(context.Background(), test.request, &destination); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(destination.Bytes(), test.want) {
				t.Fatalf("response\n got=%x\nwant=%x", destination.Bytes(), test.want)
			}
			if source.calls.Load() != 1 {
				t.Fatalf("source calls=%d", source.calls.Load())
			}
			sum := sha256.Sum256(test.want)
			t.Logf("response_bytes=%d sha256=%x", len(test.want), sum)
		})
	}
}

func TestCanonicalEmptyResponses(t *testing.T) {
	for _, test := range []struct {
		name    string
		request []byte
		want    []byte
	}{
		{"1060", fileRequest("ChSet5.txt"), []byte{1, 0, 0, 0, 0, 0, 0, 0}},
		{"1021", enumRequest(), []byte{1, 0, 0, 0, 8, 0, 0, 0, 8, 0, 0, 0, 0, 0, 0, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &fixedSource{snapshot: Snapshot{Key: "empty"}}
			var destination bytes.Buffer
			if err := (Handler{Source: source}).Handle(context.Background(), test.request, &destination); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(destination.Bytes(), test.want) {
				t.Fatalf("got=%x want=%x", destination.Bytes(), test.want)
			}
		})
	}
}

func TestWrongFileReturnsUnsupportedWithoutSourceAccess(t *testing.T) {
	for _, name := range []string{"ChSet4.txt", "chset5.txt", "../foo.txt", "/foo/a.txt"} {
		t.Run(name, func(t *testing.T) {
			source := &fixedSource{}
			var destination bytes.Buffer
			if err := (Handler{Source: source}).Handle(context.Background(), fileRequest(name), &destination); err != nil {
				t.Fatal(err)
			}
			want := []byte{203, 0, 0, 0, 0, 0, 0, 0}
			if !bytes.Equal(destination.Bytes(), want) || source.calls.Load() != 0 {
				t.Fatalf("response=%x calls=%d", destination.Bytes(), source.calls.Load())
			}
		})
	}
}

func TestMalformedRequestsHaveNoResponseOrSourceAccess(t *testing.T) {
	validFile := fileRequest("ChSet5.txt")
	invalidTerminator := append([]byte(nil), validFile...)
	invalidTerminator[len(invalidTerminator)-2] = 1
	embeddedNUL := append([]byte(nil), validFile...)
	embeddedNUL[12] = 0
	embeddedNUL[13] = 0
	unpairedSurrogate := append([]byte(nil), validFile...)
	binary.LittleEndian.PutUint16(unpairedSurrogate[12:14], 0xD800)
	shortStringExtent := append([]byte(nil), validFile...)
	binary.LittleEndian.PutUint32(shortStringExtent[8:12], 24)
	longStringExtent := append([]byte(nil), validFile...)
	binary.LittleEndian.PutUint32(longStringExtent[8:12], 28)
	wrongFileLength := fileRequest("/ChSet5.txt")
	nonEmptyEnum := append(enumRequest(), 1)
	binary.LittleEndian.PutUint32(nonEmptyEnum[4:8], 1)
	mutations := map[string][]byte{
		"short outer":          validFile[:7],
		"truncated body":       validFile[:len(validFile)-1],
		"trailing byte":        append(append([]byte(nil), validFile...), 0),
		"invalid terminator":   invalidTerminator,
		"embedded nul":         embeddedNUL,
		"unpaired surrogate":   unpairedSurrogate,
		"string extent minus":  shortStringExtent,
		"string extent plus":   longStringExtent,
		"absolute path length": wrongFileLength,
		"non-empty enum":       nonEmptyEnum,
	}
	for name, request := range mutations {
		t.Run(name, func(t *testing.T) {
			source := &fixedSource{}
			var destination bytes.Buffer
			err := (Handler{Source: source}).Handle(context.Background(), request, &destination)
			if err == nil || destination.Len() != 0 || source.calls.Load() != 0 {
				t.Fatalf("err=%v response=%x calls=%d", err, destination.Bytes(), source.calls.Load())
			}
		})
	}
}

func TestUnsupportedCommandAndNilWriterDoNotReadSource(t *testing.T) {
	unsupported := appendI32(nil, 999)
	unsupported = appendI32(unsupported, 0)
	for _, test := range []struct {
		name        string
		request     []byte
		destination io.Writer
	}{
		{"unsupported command", unsupported, io.Discard},
		{"nil writer", enumRequest(), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &fixedSource{}
			err := (Handler{Source: source}).Handle(context.Background(), test.request, test.destination)
			if err == nil || source.calls.Load() != 0 {
				t.Fatalf("err=%v calls=%d", err, source.calls.Load())
			}
		})
	}
}

func TestProjectionFiltersSortsAndDoesNotMutateSource(t *testing.T) {
	first := syntheticService()
	first.ProviderLocator, first.ServiceName, first.NetworkName, first.ServiceID = "z", "後", "N", 20
	second := syntheticService()
	second.ProviderLocator, second.ServiceName, second.NetworkName, second.ServiceID = "a", "先", "N", 10
	unverified := syntheticService()
	unverified.Verified, unverified.ServiceName = false, "除外1"
	unselected := syntheticService()
	unselected.Selected, unselected.ServiceName = false, "除外2"
	services := []Service{first, unverified, second, unselected}
	wantSource := append([]Service(nil), services...)
	source := &fixedSource{snapshot: Snapshot{Key: "sorted", Services: services}}
	var destination bytes.Buffer
	if err := (Handler{Source: source}).Handle(context.Background(), fileRequest("ChSet5.txt"), &destination); err != nil {
		t.Fatal(err)
	}
	body := string(destination.Bytes()[8:])
	wantBody := "先\tN\t1\t2\t10\t1\t1\t0\t1\t10\n" +
		"後\tN\t1\t2\t20\t1\t1\t0\t1\t10\n"
	if body != wantBody {
		t.Fatalf("body=%q want=%q", body, wantBody)
	}
	if !reflect.DeepEqual(services, wantSource) {
		t.Fatalf("source sliceが変更されました\n got=%+v\nwant=%+v", services, wantSource)
	}
}

func TestVerifiedServiceValidationAndDuplicateIdentity(t *testing.T) {
	invalid := []struct {
		name   string
		mutate func(*Service)
	}{
		{"locator", func(s *Service) { s.ProviderLocator = string([]byte{0xff}) }},
		{"empty locator", func(s *Service) { s.ProviderLocator = "" }},
		{"locator over cap", func(s *Service) { s.ProviderLocator = strings.Repeat("a", maxProviderLocator+1) }},
		{"empty service name", func(s *Service) { s.ServiceName = "" }},
		{"invalid name utf8", func(s *Service) { s.ServiceName = string([]byte{0xff}) }},
		{"name over cap", func(s *Service) { s.NetworkName = strings.Repeat("a", maxNameBytes+1) }},
		{"nul", func(s *Service) { s.NetworkName = "a\x00b" }},
		{"tab", func(s *Service) { s.ProviderName = "a\tb" }},
		{"carriage return", func(s *Service) { s.ProviderName = "a\rb" }},
		{"line feed", func(s *Service) { s.TransportStreamName = "a\nb" }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			service := syntheticService()
			test.mutate(&service)
			source := &fixedSource{snapshot: Snapshot{Key: "invalid", Services: []Service{service}}}
			var destination bytes.Buffer
			err := (Handler{Source: source}).Handle(context.Background(), enumRequest(), &destination)
			requireCodecCategory(t, err, codec.Malformed)
			leaksValue := (service.ProviderLocator != "" && strings.Contains(err.Error(), service.ProviderLocator)) ||
				(service.ServiceName != "" && strings.Contains(err.Error(), service.ServiceName))
			if destination.Len() != 0 || leaksValue {
				t.Fatalf("response=%x err=%v", destination.Bytes(), err)
			}
		})
	}

	t.Run("verified but unselected is still validated", func(t *testing.T) {
		service := syntheticService()
		service.Selected = false
		service.ServiceName = ""
		err := (Handler{Source: &fixedSource{snapshot: Snapshot{Key: "invalid", Services: []Service{service}}}}).
			Handle(context.Background(), enumRequest(), io.Discard)
		requireCodecCategory(t, err, codec.Malformed)
	})
	t.Run("unverified invalid service is excluded", func(t *testing.T) {
		service := syntheticService()
		service.Verified = false
		service.ServiceName = ""
		var destination bytes.Buffer
		err := (Handler{Source: &fixedSource{snapshot: Snapshot{Key: "excluded", Services: []Service{service}}}}).
			Handle(context.Background(), enumRequest(), &destination)
		if err != nil || len(destination.Bytes()) != 16 {
			t.Fatalf("err=%v response=%x", err, destination.Bytes())
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		first := syntheticService()
		second := first
		second.ProviderLocator = "synthetic:2"
		var destination bytes.Buffer
		err := (Handler{Source: &fixedSource{snapshot: Snapshot{Key: "duplicate", Services: []Service{first, second}}}}).
			Handle(context.Background(), enumRequest(), &destination)
		requireCodecCategory(t, err, codec.Malformed)
		if destination.Len() != 0 {
			t.Fatalf("response=%x", destination.Bytes())
		}
	})
}

func TestSnapshotAndCountLimits(t *testing.T) {
	for _, snapshot := range []Snapshot{
		{Key: ""},
		{Key: strings.Repeat("a", maxSnapshotKey+1)},
		{Key: string([]byte{0xff})},
	} {
		var destination bytes.Buffer
		err := (Handler{Source: &fixedSource{snapshot: snapshot}}).Handle(context.Background(), enumRequest(), &destination)
		requireCodecCategory(t, err, codec.Malformed)
		if destination.Len() != 0 {
			t.Fatalf("response=%x", destination.Bytes())
		}
	}

	services := make([]Service, maxServices)
	for i := range services {
		services[i] = syntheticService()
		services[i].ProviderLocator = "synthetic:" + string(rune(0x1000+i))
		services[i].ServiceID = uint16(i)
	}
	t.Run("exact", func(t *testing.T) {
		var destination bytes.Buffer
		err := (Handler{Source: &fixedSource{snapshot: Snapshot{Key: "exact", Services: services}}}).
			Handle(context.Background(), enumRequest(), &destination)
		if err != nil {
			t.Fatal(err)
		}
		if got := binary.LittleEndian.Uint32(destination.Bytes()[12:16]); got != maxServices {
			t.Fatalf("count=%d", got)
		}
	})
	t.Run("one over", func(t *testing.T) {
		over := append(append([]Service(nil), services...), syntheticService())
		var destination bytes.Buffer
		err := (Handler{Source: &fixedSource{snapshot: Snapshot{Key: "over", Services: over}}}).
			Handle(context.Background(), enumRequest(), &destination)
		requireCodecCategory(t, err, codec.OverLimit)
		if destination.Len() != 0 {
			t.Fatalf("response=%x", destination.Bytes())
		}
	})
}

func TestResponseBodyCapsAndCheckedArithmetic(t *testing.T) {
	service := syntheticService()
	for _, test := range []struct {
		name    string
		request []byte
		want    []byte
	}{
		{"1060", fileRequest("ChSet5.txt"), expectedFileResponse()},
		{"1021", enumRequest(), expectedServiceResponse(service)},
	} {
		t.Run(test.name+" exact", func(t *testing.T) {
			limit := len(test.want) - codec.HeaderSize
			var destination bytes.Buffer
			err := (Handler{Source: &fixedSource{snapshot: Snapshot{Key: "cap", Services: []Service{service}}}, Limits: codec.Limits{ResponseBody: limit}}).
				Handle(context.Background(), test.request, &destination)
			if err != nil || !bytes.Equal(destination.Bytes(), test.want) {
				t.Fatalf("err=%v response=%x", err, destination.Bytes())
			}
		})
		t.Run(test.name+" one over", func(t *testing.T) {
			limit := len(test.want) - codec.HeaderSize - 1
			var destination bytes.Buffer
			err := (Handler{Source: &fixedSource{snapshot: Snapshot{Key: "cap", Services: []Service{service}}}, Limits: codec.Limits{ResponseBody: limit}}).
				Handle(context.Background(), test.request, &destination)
			requireCodecCategory(t, err, codec.OverLimit)
			if destination.Len() != 0 {
				t.Fatalf("response=%x", destination.Bytes())
			}
		})
	}
	if _, err := checkedSize(math.MaxInt64-1, 2, math.MaxInt64, "test-overflow"); err == nil {
		t.Fatal("overflow相当の加算が受理されました")
	}
	if got, err := checkedSize(math.MaxInt64-1, 1, math.MaxInt64, "test-exact"); err != nil || got != math.MaxInt64 {
		t.Fatalf("got=%d err=%v", got, err)
	}
}

func TestCommandHardBodyCaps(t *testing.T) {
	t.Run("1060 exact and minimum over", func(t *testing.T) {
		services := make([]Service, 2_048)
		for i := range services {
			services[i] = Service{ServiceName: strings.Repeat("a", 4_078)}
		}
		if got, err := fileBodySize(context.Background(), services, maxFileBody); err != nil || got != maxFileBody {
			t.Fatalf("size=%d err=%v", got, err)
		}
		services[0].ServiceName += "a"
		if _, err := fileBodySize(context.Background(), services, maxFileBody); err == nil {
			t.Fatal("8 MiBを1 byte越えるbodyが受理されました")
		}
	})
	t.Run("1021 exact and minimum over", func(t *testing.T) {
		services := make([]Service, maxServices)
		baseName := strings.Repeat("a", 2_029)
		for i := range services {
			services[i] = Service{ServiceName: baseName}
		}
		services[0].ServiceName = strings.Repeat("a", 4_073)
		limits := codec.Limits{ResponseBody: maxServiceBody}
		if got, err := serviceBodySize(context.Background(), services, limits); err != nil || got != maxServiceBody {
			t.Fatalf("size=%d err=%v", got, err)
		}
		services[0].ServiceName += "a"
		if _, err := serviceBodySize(context.Background(), services, limits); err == nil {
			t.Fatal("16 MiBを最小単位で越えるbodyが受理されました")
		}
	})
}

func TestDependencyFailuresAndCancellationHaveNoResponse(t *testing.T) {
	tests := []struct {
		name    string
		handler Handler
		ctx     context.Context
	}{
		{"nil source", Handler{}, context.Background()},
		{"source error", Handler{Source: &fixedSource{err: errors.New("secret source detail")}}, context.Background()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var destination bytes.Buffer
			err := test.handler.Handle(test.ctx, enumRequest(), &destination)
			if err == nil || destination.Len() != 0 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("err=%v response=%x", err, destination.Bytes())
			}
		})
	}

	t.Run("canceled before source", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		source := &fixedSource{}
		var destination bytes.Buffer
		err := (Handler{Source: source}).Handle(ctx, enumRequest(), &destination)
		requireCodecCategory(t, err, codec.Timeout)
		if source.calls.Load() != 0 || destination.Len() != 0 {
			t.Fatalf("calls=%d response=%x", source.calls.Load(), destination.Bytes())
		}
	})
	t.Run("canceled after source", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		source := &fixedSource{snapshot: Snapshot{Key: "cancel", Services: []Service{syntheticService()}}, after: cancel}
		var destination bytes.Buffer
		err := (Handler{Source: source}).Handle(ctx, enumRequest(), &destination)
		requireCodecCategory(t, err, codec.Timeout)
		if source.calls.Load() != 1 || destination.Len() != 0 {
			t.Fatalf("calls=%d response=%x", source.calls.Load(), destination.Bytes())
		}
	})
}

type controlledWriter struct {
	data     bytes.Buffer
	maxWrite int
	zero     bool
	cancel   context.CancelFunc
	writes   int
}

type failedWriter struct{ err error }

func (w failedWriter) Write([]byte) (int, error) { return 0, w.err }

func (w *controlledWriter) Write(value []byte) (int, error) {
	if w.zero {
		return 0, nil
	}
	if w.maxWrite > 0 && len(value) > w.maxWrite {
		value = value[:w.maxWrite]
	}
	n, err := w.data.Write(value)
	w.writes++
	if w.cancel != nil && w.writes == 1 {
		w.cancel()
	}
	return n, err
}

func TestPartialZeroProgressAndCancellationDuringWrite(t *testing.T) {
	handler := Handler{Source: &fixedSource{snapshot: Snapshot{Key: "write", Services: []Service{syntheticService()}}}}
	t.Run("partial write completes", func(t *testing.T) {
		destination := &controlledWriter{maxWrite: 1}
		if err := handler.Handle(context.Background(), enumRequest(), destination); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(destination.data.Bytes(), expectedServiceResponse(syntheticService())) {
			t.Fatalf("response=%x", destination.data.Bytes())
		}
	})
	t.Run("zero progress", func(t *testing.T) {
		destination := &controlledWriter{zero: true}
		err := handler.Handle(context.Background(), enumRequest(), destination)
		requireCodecCategory(t, err, codec.PeerDisconnect)
	})
	t.Run("disconnect", func(t *testing.T) {
		err := handler.Handle(context.Background(), enumRequest(), failedWriter{err: errors.New("secret peer detail")})
		requireCodecCategory(t, err, codec.PeerDisconnect)
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("cancel during write", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		destination := &controlledWriter{cancel: cancel}
		err := handler.Handle(ctx, enumRequest(), destination)
		requireCodecCategory(t, err, codec.Timeout)
		if destination.data.Len() == 0 {
			t.Fatalf("err=%v written=%d", err, destination.data.Len())
		}
	})
}

func TestSameInputAndParallelHandlerSafety(t *testing.T) {
	source := &fixedSource{snapshot: Snapshot{Key: "parallel", Services: []Service{syntheticService()}}}
	handler := Handler{Source: source}
	want := expectedServiceResponse(syntheticService())
	const workers = 32
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var destination bytes.Buffer
			if err := handler.Handle(context.Background(), enumRequest(), &destination); err != nil {
				errorsFound <- err
				return
			}
			if !bytes.Equal(destination.Bytes(), want) {
				errorsFound <- errors.New("response mismatch")
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if source.calls.Load() != workers {
		t.Fatalf("source calls=%d", source.calls.Load())
	}
}

func FuzzHandlerRejectsUnknownInputWithoutPanic(f *testing.F) {
	validFile := fileRequest("ChSet5.txt")
	validEnum := enumRequest()
	for _, seed := range [][]byte{validFile, validEnum, validFile[:7], append(validEnum, 1), nil} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, request []byte) {
		source := &fixedSource{snapshot: Snapshot{Key: "fuzz"}}
		var destination bytes.Buffer
		_ = (Handler{Source: source}).Handle(context.Background(), request, &destination)
		if source.calls.Load() > 0 && !bytes.Equal(request, validFile) && !bytes.Equal(request, validEnum) {
			t.Fatalf("不正requestでSourceが%d回呼ばれました: %x", source.calls.Load(), request)
		}
	})
}
