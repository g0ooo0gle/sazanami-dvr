package autoreservation

import (
	"bytes"
	"context"
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

type fakeOperations struct {
	rules   []core.Rule
	changed core.Rule
	deleted int32
}

func (operations *fakeOperations) Add(_ context.Context, search core.SearchCondition, settings core.RecordingSettings) (core.Rule, error) {
	rule := core.Rule{ID: catalogmodel.ID{1}, Number: 1, Version: 1, Search: search, Recording: settings,
		CreatedAtUTCMS: 1, UpdatedAtUTCMS: 1}
	operations.rules = []core.Rule{rule}
	return rule, nil
}

func (operations *fakeOperations) List(_ context.Context, limit int, after int32) ([]core.Rule, error) {
	result := make([]core.Rule, 0, limit)
	for _, rule := range operations.rules {
		if rule.Number > after && len(result) < limit {
			result = append(result, rule)
		}
	}
	return result, nil
}

func (operations *fakeOperations) Change(_ context.Context, number int32, search core.SearchCondition, settings core.RecordingSettings) error {
	operations.changed = core.Rule{Number: number, Search: search, Recording: settings}
	return nil
}

func (operations *fakeOperations) Delete(_ context.Context, number int32) error {
	operations.deleted = number
	operations.rules = nil
	return nil
}

func TestAutomaticRuleCommandsRoundTrip(t *testing.T) {
	limits := codec.DefaultLimits()
	start, end := int32(-5), int32(10)
	rule := core.Rule{
		Search: core.SearchCondition{
			Enabled: true, CaseSensitive: true, Regex: true, TitleOnly: true, Keyword: "番組.*", Exclude: ":note:memo 除外",
			Contents: []core.ContentRange{{Content: 0x01ff, User: 2}},
			Dates:    []core.DateRange{{StartDay: 1, StartHour: 2, StartMinute: 3, EndDay: 4, EndHour: 5, EndMinute: 6}},
			Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}},
			Video:    []uint16{4}, Audio: []uint16{5}, FreeAccess: 1, CheckRecordedTitle: true,
			CheckRecordedAllServices: true, CheckRecordedDays: 6, MinimumMinutes: 10, MaximumMinutes: 90,
		},
		Recording: core.RecordingSettings{
			Mode: 1, Priority: 4, Follow: true, ServiceMode: 16, Exact: true, Batch: "after.bat",
			Folders: []core.Folder{{Path: "recordings", Writer: "Write_Default.dll", Name: "name"}},
			Suspend: 2, Reboot: true, StartMargin: &start, EndMargin: &end, Continue: true,
			PartialMode: 1, TunerID: 7, PartialFolders: []core.Folder{{Path: "partial"}},
		},
	}
	operations := &fakeOperations{}
	handler := Handler{Operations: operations, Limits: limits}
	response := handleRequest(t, handler, requestForRule(t, CommandAdd, rule, limits))
	if response.Code != resultSuccess || len(response.Body) != 2 || len(operations.rules) != 1 {
		t.Fatalf("add response=%+v rules=%+v", response, operations.rules)
	}
	stored := operations.rules[0]
	if stored.Search.Keyword != rule.Search.Keyword || stored.Search.MinimumMinutes != 10 ||
		stored.Recording.StartMargin == nil || *stored.Recording.StartMargin != start || len(stored.Recording.PartialFolders) != 1 {
		t.Fatalf("stored=%+v", stored)
	}

	response = handleRequest(t, handler, versionRequest(t, CommandList, limits))
	if response.Code != resultSuccess {
		t.Fatalf("list response=%+v", response)
	}
	reader, err := codec.NewReader(response.Body, limits)
	if err != nil {
		t.Fatal(err)
	}
	version, err := reader.U16()
	var listed core.Rule
	count := 0
	if err == nil {
		err = reader.Vector(1, core.MaxRules, func(item *codec.Reader, _ int) error {
			count++
			return decodeAutoAdd(item, &listed)
		})
	}
	if err != nil || version != Version || count != 1 || reader.Exact() != nil || listed.Number != 1 ||
		listed.Search.Keyword != rule.Search.Keyword || listed.Recording.TunerID != 7 {
		t.Fatalf("listed=%+v version=%d count=%d err=%v", listed, version, count, err)
	}

	changed := rule
	changed.Number = 1
	changed.Search.Keyword = "変更"
	response = handleRequest(t, handler, requestForRule(t, CommandChange, changed, limits))
	if response.Code != resultSuccess || operations.changed.Number != 1 || operations.changed.Search.Keyword != "変更" {
		t.Fatalf("change response=%+v changed=%+v", response, operations.changed)
	}
	response = handleRequest(t, handler, deleteRequest(t, 1, limits))
	if response.Code != resultSuccess || operations.deleted != 1 {
		t.Fatalf("delete response=%+v deleted=%d", response, operations.deleted)
	}
}

func TestAutomaticRuleRejectsTwoItems(t *testing.T) {
	limits := codec.DefaultLimits()
	rule := core.Rule{Recording: core.RecordingSettings{Mode: 1, Priority: 3}}
	itemSize, err := autoAddSize(rule, limits)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer, _ := codec.NewWriter(&body, limits)
	_ = writer.U16(Version)
	_ = writer.I32(int32(8 + itemSize*2))
	_ = writer.I32(2)
	_ = writeAutoAdd(writer, rule, limits)
	_ = writeAutoAdd(writer, rule, limits)
	request := frameWithBody(t, CommandAdd, body.Bytes(), limits)
	operations := &fakeOperations{}
	response := handleRequest(t, Handler{Operations: operations, Limits: limits}, request)
	if response.Code != resultFailure || len(operations.rules) != 0 {
		t.Fatalf("response=%+v rules=%+v", response, operations.rules)
	}
}

func requestForRule(t *testing.T, command int32, rule core.Rule, limits codec.Limits) []byte {
	t.Helper()
	size, err := autoAddSize(rule, limits)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer, _ := codec.NewWriter(&body, limits)
	if err := writer.U16(Version); err != nil {
		t.Fatal(err)
	}
	if err := writer.I32(int32(8 + size)); err != nil {
		t.Fatal(err)
	}
	if err := writer.I32(1); err != nil {
		t.Fatal(err)
	}
	if err := writeAutoAdd(writer, rule, limits); err != nil {
		t.Fatal(err)
	}
	return frameWithBody(t, command, body.Bytes(), limits)
}

func versionRequest(t *testing.T, command int32, limits codec.Limits) []byte {
	t.Helper()
	var body bytes.Buffer
	writer, _ := codec.NewWriter(&body, limits)
	if err := writer.U16(Version); err != nil {
		t.Fatal(err)
	}
	return frameWithBody(t, command, body.Bytes(), limits)
}

func deleteRequest(t *testing.T, number int32, limits codec.Limits) []byte {
	t.Helper()
	var body bytes.Buffer
	writer, _ := codec.NewWriter(&body, limits)
	_ = writer.I32(12)
	_ = writer.I32(1)
	_ = writer.I32(number)
	return frameWithBody(t, CommandDelete, body.Bytes(), limits)
}

func frameWithBody(t *testing.T, command int32, body []byte, limits codec.Limits) []byte {
	t.Helper()
	var request bytes.Buffer
	if err := codec.WriteFrame(&request, command, int64(len(body)), limits, func(writer *codec.Writer) error {
		return writer.Bytes(body)
	}); err != nil {
		t.Fatal(err)
	}
	return request.Bytes()
}

func handleRequest(t *testing.T, handler Handler, request []byte) codec.Frame {
	t.Helper()
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	frame, err := codec.ParseRequestFrame(response.Bytes(), handler.Limits)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}
