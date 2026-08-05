package programguide

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

type memorySource struct {
	snapshot  channel.Snapshot
	programs  []catalogmodel.CurrentProgram
	reads     int
	requested []string
}

func (source *memorySource) Current(context.Context) (channel.Snapshot, error) {
	return source.snapshot, nil
}

func (source *memorySource) CurrentProgramsForService(_ context.Context, serviceLocator string, limit int, afterEvent string) ([]catalogmodel.CurrentProgram, error) {
	source.reads++
	source.requested = append(source.requested, serviceLocator)
	result := make([]catalogmodel.CurrentProgram, 0, limit)
	for _, program := range source.programs {
		if program.ServiceLocator != serviceLocator || program.EventLocator <= afterEvent {
			continue
		}
		result = append(result, program)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func TestHandlerWritesOneServiceAndFiltersUnknownProgram(t *testing.T) {
	service := guideService("1003", 1, 2, 3)
	valid := guideProgram("1003", "event:1", 4)
	unknown := guideProgram("9999", "event:2", 5)
	source := &memorySource{snapshot: channel.Snapshot{Key: "guide", Services: []channel.Service{service}}, programs: []catalogmodel.CurrentProgram{valid, unknown}}
	handler, err := NewHandler(source, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), guideRequest(acceptedSelectors), &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
	reader, err := codec.NewReader(frame.Body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	if err := reader.Vector(1, maxServices, func(serviceReader *codec.Reader, _ int) error {
		return serviceReader.Structure(func(item *codec.Reader) error {
			if err := readService(item); err != nil {
				return err
			}
			return item.Vector(1, maxPrograms, func(eventReader *codec.Reader, _ int) error {
				events++
				return readEvent(eventReader, valid)
			})
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.Exact(); err != nil || events != 1 || source.reads != 2 ||
		len(source.requested) != 2 || source.requested[0] != "1003" || source.requested[1] != "1003" {
		t.Fatalf("events=%d reads=%d requested=%v exact=%v", events, source.reads, source.requested, err)
	}
}

func TestHandlerRejectsSelectorBeforeReadingSource(t *testing.T) {
	source := &memorySource{snapshot: channel.Snapshot{Key: "guide"}}
	handler, _ := NewHandler(source, codec.DefaultLimits())
	wrong := acceptedSelectors
	wrong[2] = 2
	if err := handler.Handle(context.Background(), guideRequest(wrong), io.Discard); err == nil {
		t.Fatal("未対応の検索条件が受理されました")
	}
	if source.reads != 0 {
		t.Fatalf("source reads=%d", source.reads)
	}
	truncated := guideRequest(acceptedSelectors)
	truncated = truncated[:len(truncated)-1]
	if err := handler.Handle(context.Background(), truncated, io.Discard); err == nil {
		t.Fatal("途中で切れたrequestが受理されました")
	}
}

func TestMeasureProgramCountBoundary(t *testing.T) {
	service := guideService("1003", 1, 2, 3)
	for _, test := range []struct {
		name    string
		count   int
		wantErr bool
	}{
		{name: "upper bound", count: maxPrograms},
		{name: "one over", count: maxPrograms + 1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &generatedSource{count: test.count, snapshot: channel.Snapshot{Key: "guide", Services: []channel.Service{service}}}
			_, _, err := measure(context.Background(), source, []channel.Service{service}, programQuery{}, codec.DefaultLimits())
			if (err != nil) != test.wantErr {
				t.Fatalf("err=%v", err)
			}
			if source.maximumLimit != pageSize {
				t.Fatalf("page limit=%d", source.maximumLimit)
			}
		})
	}
}

func TestHandlerSupportsKonomiExactServiceAndTimeRange(t *testing.T) {
	start := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	target := guideService("1003", 1, 2, 3)
	other := guideService("1004", 1, 2, 4)
	before := guideProgram("1003", "event:1", 4)
	inRange := guideProgram("1003", "event:2", 5)
	atEnd := guideProgram("1003", "event:3", 6)
	otherService := guideProgram("1004", "event:4", 7)
	for program, value := range map[*catalogmodel.CurrentProgram]time.Time{
		&before:       start.Add(-time.Second),
		&inRange:      start,
		&atEnd:        start.Add(time.Minute),
		&otherService: start,
	} {
		unixMS := value.UnixMilli()
		program.Material.StartUTCMS = &unixMS
	}
	source := &memorySource{
		snapshot: channel.Snapshot{Key: "guide", Services: []channel.Service{target, other}},
		programs: []catalogmodel.CurrentProgram{before, inRange, atEnd, otherService},
	}
	handler, err := NewHandler(source, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	serviceKey := int64(target.NetworkID)<<32 | int64(target.TransportStreamID)<<16 | int64(target.ServiceID)
	selectors := [4]int64{0, serviceKey, edcbFileTime(start), edcbFileTime(start.Add(time.Minute))}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), guideRequest(selectors), &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
	reader, err := codec.NewReader(frame.Body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	services, events := 0, 0
	if err := reader.Vector(1, maxServices, func(serviceReader *codec.Reader, _ int) error {
		services++
		return serviceReader.Structure(func(item *codec.Reader) error {
			if err := readService(item); err != nil {
				return err
			}
			return item.Vector(1, maxPrograms, func(eventReader *codec.Reader, _ int) error {
				events++
				return readEvent(eventReader, inRange)
			})
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.Exact(); err != nil || services != 1 || events != 1 || source.reads != 2 ||
		len(source.requested) != 2 || source.requested[0] != "1003" || source.requested[1] != "1003" {
		t.Fatalf("services=%d events=%d reads=%d requested=%v exact=%v", services, events, source.reads, source.requested, err)
	}
}

func edcbFileTime(value time.Time) int64 {
	return value.UnixMilli()*10_000 + fileTimeUnixEpoch + fileTimeJSTOffset
}

type generatedSource struct {
	count        int
	maximumLimit int
	snapshot     channel.Snapshot
}

func (source *generatedSource) Current(context.Context) (channel.Snapshot, error) {
	return source.snapshot, nil
}

func (source *generatedSource) CurrentProgramsForService(_ context.Context, serviceLocator string, limit int, afterEvent string) ([]catalogmodel.CurrentProgram, error) {
	if limit > source.maximumLimit {
		source.maximumLimit = limit
	}
	start := 0
	if afterEvent != "" {
		parsed, err := strconv.Atoi(afterEvent)
		if err != nil {
			return nil, err
		}
		start = parsed + 1
	}
	end := min(start+limit, source.count)
	result := make([]catalogmodel.CurrentProgram, 0, end-start)
	for index := start; index < end; index++ {
		program := guideProgram(serviceLocator, fmt.Sprintf("%06d", index), int64(index%65_536))
		// 件数上限だけを確認するため、応答サイズへ加算しない不完全な番組を使う。
		program.Material.StartUTCMS = nil
		result = append(result, program)
	}
	return result, nil
}

func guideService(locator string, network, transport, service uint16) channel.Service {
	return channel.Service{
		ProviderLocator: locator, ProviderName: "provider", ServiceName: "テスト局", NetworkName: "network",
		TransportStreamName: "transport", NetworkID: network, TransportStreamID: transport, ServiceID: service,
		ServiceType: 1, RemoteControlKey: 1, Verified: true, Selected: true,
	}
}

func guideProgram(locator, eventLocator string, eventID int64) catalogmodel.CurrentProgram {
	start := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC).UnixMilli()
	duration := int64((30 * time.Minute) / time.Millisecond)
	title, description := "テスト番組", "説明"
	return catalogmodel.CurrentProgram{
		ServiceLocator: locator, EventLocator: eventLocator, RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &start, DurationMS: &duration, Title: &title,
			Description: &description, FreeAccess: catalogmodel.FreeYes, Validation: catalogmodel.ValidationValid},
	}
}

func guideRequest(selectors [4]int64) []byte {
	request := make([]byte, codec.HeaderSize+40)
	binary.LittleEndian.PutUint32(request[0:4], uint32(Command))
	binary.LittleEndian.PutUint32(request[4:8], 40)
	binary.LittleEndian.PutUint32(request[8:12], 40)
	binary.LittleEndian.PutUint32(request[12:16], 4)
	for index, selector := range selectors {
		binary.LittleEndian.PutUint64(request[16+index*8:24+index*8], uint64(selector))
	}
	return request
}

func readService(reader *codec.Reader) error {
	return reader.Structure(func(item *codec.Reader) error {
		for range 3 {
			if _, err := item.U16(); err != nil {
				return err
			}
		}
		for range 2 {
			if _, err := item.U8(); err != nil {
				return err
			}
		}
		for range 4 {
			if _, err := item.String(); err != nil {
				return err
			}
		}
		_, err := item.U8()
		return err
	})
}

func readEvent(reader *codec.Reader, want catalogmodel.CurrentProgram) error {
	return reader.Structure(func(item *codec.Reader) error {
		for _, expected := range []uint16{1, 2, 3, uint16(*want.RawEventID)} {
			value, err := item.U16()
			if err != nil || value != expected {
				return fmt.Errorf("id=%d want=%d err=%w", value, expected, err)
			}
		}
		flag, err := item.U8()
		if err != nil || flag != 1 {
			return fmt.Errorf("start flag=%d err=%w", flag, err)
		}
		start, err := item.SystemTime()
		if err != nil || start.UnixMilli() != *want.Material.StartUTCMS {
			return fmt.Errorf("start=%v err=%w", start, err)
		}
		flag, err = item.U8()
		if err != nil || flag != 1 {
			return fmt.Errorf("duration flag=%d err=%w", flag, err)
		}
		duration, err := item.I32()
		if err != nil || duration != 1800 {
			return fmt.Errorf("duration=%d err=%w", duration, err)
		}
		if err := item.Structure(func(short *codec.Reader) error {
			for range 2 {
				if _, err := short.String(); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		for range 6 {
			if err := item.Structure(func(*codec.Reader) error { return nil }); err != nil {
				return err
			}
		}
		free, err := item.U8()
		if err != nil || free != 0 {
			return fmt.Errorf("free=%d err=%w", free, err)
		}
		return nil
	})
}
