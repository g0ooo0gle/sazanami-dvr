package recorded

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestHandlerListsAndGetsCompletedRecordings(t *testing.T) {
	items := []recording.HistoryItem{historyItem(1), historyItem(2)}
	operations := &fakeOperations{items: items}
	handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), requestFrame(CommandList, versionBody()), &response); err != nil {
		t.Fatal(err)
	}
	body := responseBody(t, response.Bytes(), resultSuccess)
	reader, err := codec.NewReader(body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	version, err := reader.U16()
	if err != nil || version != Version {
		t.Fatalf("version=%d err=%v", version, err)
	}
	count := 0
	if err := reader.Vector(4, recording.MaxHistoryItems, func(item *codec.Reader, index int) error {
		count++
		return item.Structure(func(value *codec.Reader) error {
			id, err := value.I32()
			if err != nil || id != int32(index+1) {
				t.Fatalf("id=%d err=%v", id, err)
			}
			path, err := value.String()
			if err != nil || path != "/recordings/"+string(rune('1'+index))+".ts" {
				t.Fatalf("path=%q err=%v", path, err)
			}
			return skipRecordedItem(value)
		})
	}); err != nil {
		t.Fatal(err)
	}
	if count != 2 || reader.Exact() != nil || operations.maximumPage > pageSize {
		t.Fatalf("count=%d page=%d", count, operations.maximumPage)
	}

	response.Reset()
	bodyRequest := append(versionBody(), []byte{2, 0, 0, 0}...)
	if err := handler.Handle(context.Background(), requestFrame(CommandGet, bodyRequest), &response); err != nil {
		t.Fatal(err)
	}
	body = responseBody(t, response.Bytes(), resultSuccess)
	reader, _ = codec.NewReader(body, codec.DefaultLimits())
	version, _ = reader.U16()
	if version != Version {
		t.Fatal(version)
	}
	if err := reader.Structure(func(value *codec.Reader) error {
		id, _ := value.I32()
		if id != 2 {
			t.Fatal(id)
		}
		if _, err := value.String(); err != nil {
			return err
		}
		return skipRecordedItem(value)
	}); err != nil {
		t.Fatal(err)
	}
	if reader.Exact() != nil {
		t.Fatal("trailing response")
	}
}

func TestHandlerRejectsMalformedAndUnavailableItems(t *testing.T) {
	bad := historyItem(1)
	bad.State = recording.AttemptFailed
	bad.Reason = recording.ReasonStreamUnavailable
	handler := Handler{Operations: &fakeOperations{items: []recording.HistoryItem{bad}}, Limits: codec.DefaultLimits()}
	cases := [][]byte{{}, {5}, {4, 0}, {5, 0, 1}, {5, 0, 0, 0, 0, 0}, {5, 0, 1, 0, 0, 0, 0}}
	for _, body := range cases {
		var response bytes.Buffer
		if err := handler.Handle(context.Background(), requestFrame(CommandGet, body), &response); err != nil {
			t.Fatal(err)
		}
		responseBody(t, response.Bytes(), resultFailure)
	}
}

func TestHandlerHonorsResponseAndContextLimits(t *testing.T) {
	limits := codec.DefaultLimits()
	limits.ResponseBody = 32
	handler := Handler{Operations: &fakeOperations{items: []recording.HistoryItem{historyItem(1)}}, Limits: limits}
	if err := handler.Handle(context.Background(), requestFrame(CommandList, versionBody()), &bytes.Buffer{}); err == nil {
		t.Fatal("small response limit accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := handler.Handle(ctx, requestFrame(CommandList, versionBody()), &bytes.Buffer{}); err == nil {
		t.Fatal("cancel accepted")
	}
}

func TestHandlerPagesWithoutBuildingAWholeHistorySlice(t *testing.T) {
	for _, count := range []int{0, 1, 256, 257} {
		items := make([]recording.HistoryItem, count)
		for index := range items {
			items[index] = historyItem(int32(index + 1))
		}
		operations := &fakeOperations{items: items}
		handler := Handler{Operations: operations, Limits: codec.DefaultLimits()}
		var response bytes.Buffer
		if err := handler.Handle(context.Background(), requestFrame(CommandList, versionBody()), &response); err != nil {
			t.Fatalf("count=%d err=%v", count, err)
		}
		body := responseBody(t, response.Bytes(), resultSuccess)
		reader, err := codec.NewReader(body, codec.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reader.U16(); err != nil {
			t.Fatal(err)
		}
		seen := 0
		if err := reader.Vector(4, recording.MaxHistoryItems, func(item *codec.Reader, _ int) error {
			seen++
			return item.Structure(func(value *codec.Reader) error {
				if _, err := value.I32(); err != nil {
					return err
				}
				if _, err := value.String(); err != nil {
					return err
				}
				return skipRecordedItem(value)
			})
		}); err != nil || seen != count || reader.Exact() != nil || operations.maximumPage > pageSize {
			t.Fatalf("count=%d seen=%d page=%d err=%v", count, seen, operations.maximumPage, err)
		}
	}
}

func TestHandlerRejectsCountOneOverBeforeWriting(t *testing.T) {
	items := make([]recording.HistoryItem, recording.MaxHistoryItems+1)
	for index := range items {
		items[index] = historyItem(int32(index + 1))
	}
	var response bytes.Buffer
	err := (Handler{Operations: &fakeOperations{items: items}, Limits: codec.DefaultLimits()}).Handle(
		context.Background(), requestFrame(CommandList, versionBody()), &response)
	var codecErr *codec.Error
	if !errors.As(err, &codecErr) || codecErr.Category != codec.OverLimit || response.Len() != 0 {
		t.Fatalf("err=%v bytes=%d", err, response.Len())
	}
}

func TestHandlerReportsWriterDisconnect(t *testing.T) {
	err := (Handler{Operations: &fakeOperations{items: []recording.HistoryItem{historyItem(1)}}, Limits: codec.DefaultLimits()}).Handle(
		context.Background(), requestFrame(CommandList, versionBody()), &shortWriter{remaining: 12})
	var codecErr *codec.Error
	if !errors.As(err, &codecErr) || codecErr.Category != codec.PeerDisconnect {
		t.Fatalf("err=%v", err)
	}
}

type fakeOperations struct {
	items       []recording.HistoryItem
	maximumPage int
	err         error
}

type shortWriter struct{ remaining int }

func (writer *shortWriter) Write(data []byte) (int, error) {
	if writer.remaining <= 0 {
		return 0, io.ErrClosedPipe
	}
	if len(data) > writer.remaining {
		written := writer.remaining
		writer.remaining = 0
		return written, io.ErrClosedPipe
	}
	writer.remaining -= len(data)
	return len(data), nil
}

func (operations *fakeOperations) CompletedRecordings(_ context.Context, limit int, after int32) ([]recording.HistoryItem, error) {
	if operations.err != nil {
		return nil, operations.err
	}
	if limit > operations.maximumPage {
		operations.maximumPage = limit
	}
	result := make([]recording.HistoryItem, 0, limit)
	for _, item := range operations.items {
		if item.Number > after && item.Playable() {
			result = append(result, item)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}
func (operations *fakeOperations) RecordingHistoryItem(_ context.Context, id int32) (*recording.HistoryItem, error) {
	if operations.err != nil {
		return nil, operations.err
	}
	for i := range operations.items {
		if operations.items[i].Number == id {
			return &operations.items[i], nil
		}
	}
	return nil, nil
}

func historyItem(number int32) recording.HistoryItem {
	start := time.Date(2026, 8, 9, 0, 0, int(number), 0, time.UTC)
	end := start.Add(time.Hour)
	return recording.HistoryItem{Number: number, State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted, Title: "番組", StationName: "放送局", NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: 4, PlannedStart: start, PlannedEnd: end, ActualStart: &start, ActualEnd: &end, ByteCount: 188, Plan: recording.FilePlan{PartialPath: "2026/08/x.ts.partial", FinalPath: "2026/08/x.ts"}, SegmentState: recording.SegmentFinalized, Availability: recording.AvailabilityFinal, FileSynced: true, FinalPublished: true, DirectorySynced: true}
}
func versionBody() []byte { return []byte{5, 0} }
func requestFrame(command int32, body []byte) []byte {
	result := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(result, uint32(command))
	binary.LittleEndian.PutUint32(result[4:], uint32(len(body)))
	copy(result[8:], body)
	return result
}
func responseBody(t *testing.T, response []byte, code int32) []byte {
	t.Helper()
	if len(response) < 8 || int32(binary.LittleEndian.Uint32(response)) != code {
		t.Fatalf("response=%x", response)
	}
	size := int(binary.LittleEndian.Uint32(response[4:]))
	if len(response) != 8+size {
		t.Fatalf("size=%d len=%d", size, len(response))
	}
	return response[8:]
}
func skipRecordedItem(reader *codec.Reader) error {
	if _, err := reader.String(); err != nil {
		return err
	}
	if _, err := reader.SystemTime(); err != nil {
		return err
	}
	if _, err := reader.I32(); err != nil {
		return err
	}
	if _, err := reader.String(); err != nil {
		return err
	}
	for range 4 {
		if _, err := reader.U16(); err != nil {
			return err
		}
	}
	for range 2 {
		if _, err := reader.I64(); err != nil {
			return err
		}
	}
	if _, err := reader.I32(); err != nil {
		return err
	}
	if _, err := reader.SystemTime(); err != nil {
		return err
	}
	for range 3 {
		if _, err := reader.String(); err != nil {
			return err
		}
	}
	_, err := reader.U8()
	return err
}
