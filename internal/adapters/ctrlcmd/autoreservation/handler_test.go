package autoreservation

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
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
	changed.Recording.TunerID = 9
	response = handleRequest(t, handler, requestForRule(t, CommandChange, changed, limits))
	if response.Code != resultSuccess || operations.changed.Number != 1 || operations.changed.Search.Keyword != "変更" ||
		operations.changed.Recording.TunerID != 9 {
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

func TestAutomaticRuleEmptyListUsesDefaultLimits(t *testing.T) {
	request := versionRequest(t, CommandList, codec.DefaultLimits())
	response := handleRequest(t, Handler{Operations: &fakeOperations{}}, request)
	if response.Code != resultSuccess || len(response.Body) != 10 {
		t.Fatalf("response=%+v", response)
	}
}

func TestAutomaticRuleRejectsMalformedValues(t *testing.T) {
	limits := codec.DefaultLimits()
	base := core.Rule{Search: core.SearchCondition{Enabled: true},
		Recording: core.RecordingSettings{Mode: 1, Priority: 3}}
	cases := map[string]core.Rule{
		"oversize-string": base,
		"unknown-free":    base,
		"invalid-date":    base,
		"unknown-mode":    base,
		"invalid-number":  base,
	}
	oversize := cases["oversize-string"]
	oversize.Search.Keyword = strings.Repeat("a", 4_097)
	cases["oversize-string"] = oversize
	unknownFree := cases["unknown-free"]
	unknownFree.Search.FreeAccess = 3
	cases["unknown-free"] = unknownFree
	invalidDate := cases["invalid-date"]
	invalidDate.Search.Dates = []core.DateRange{{StartDay: 7}}
	cases["invalid-date"] = invalidDate
	unknownMode := cases["unknown-mode"]
	unknownMode.Recording.Mode = 10
	cases["unknown-mode"] = unknownMode
	invalidNumber := cases["invalid-number"]
	invalidNumber.Number = -1
	cases["invalid-number"] = invalidNumber

	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			command := CommandAdd
			if name == "invalid-number" {
				command = CommandChange
			}
			response := handleRequest(t, Handler{Operations: &fakeOperations{}, Limits: limits},
				requestForRule(t, command, rule, limits))
			if response.Code != resultFailure {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}

func TestAutomaticRuleRejectsMalformedStructures(t *testing.T) {
	limits := codec.DefaultLimits()
	base := core.Rule{Recording: core.RecordingSettings{Mode: 1, Priority: 3}}
	valid := requestForRule(t, CommandAdd, base, limits)
	cases := map[string][]byte{}
	wrongVersion := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(wrongVersion[8:10], Version-1)
	cases["version"] = wrongVersion
	wrongVector := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(wrongVector[14:18], 2)
	cases["vector"] = wrongVector
	wrongStructure := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(wrongStructure[18:22], 4)
	cases["structure"] = wrongStructure
	trailingBody := append(append([]byte(nil), valid[8:]...), 0)
	cases["trailing"] = frameWithBody(t, CommandAdd, trailingBody, limits)

	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			response := handleRequest(t, Handler{Operations: &fakeOperations{}, Limits: limits}, request)
			if response.Code != resultFailure {
				t.Fatalf("response=%+v", response)
			}
		})
	}

	wrongBody := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(wrongBody[4:8], uint32(len(wrongBody)))
	var response bytes.Buffer
	if err := (Handler{Operations: &fakeOperations{}, Limits: limits}).Handle(context.Background(), wrongBody, &response); err == nil || response.Len() != 0 {
		t.Fatalf("body err=%v response=%d", err, response.Len())
	}
}

func TestAutomaticRuleHonorsResponseLimitAndCancellation(t *testing.T) {
	rule := core.Rule{ID: catalogmodel.ID{1}, Number: 1, Version: 1,
		Recording: core.RecordingSettings{Mode: 1, Priority: 3}, CreatedAtUTCMS: 1, UpdatedAtUTCMS: 1}
	limits := codec.DefaultLimits()
	limits.ResponseBody = 16
	request := versionRequest(t, CommandList, limits)
	var response bytes.Buffer
	err := (Handler{Operations: &fakeOperations{rules: []core.Rule{rule}}, Limits: limits}).Handle(
		context.Background(), request, &response)
	var codecErr *codec.Error
	if !errors.As(err, &codecErr) || codecErr.Category != codec.OverLimit || response.Len() != 0 {
		t.Fatalf("limit err=%v response=%d", err, response.Len())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response.Reset()
	err = (Handler{Operations: &fakeOperations{}, Limits: codec.DefaultLimits()}).Handle(
		ctx, versionRequest(t, CommandList, codec.DefaultLimits()), &response)
	if !errors.As(err, &codecErr) || codecErr.Category != codec.Timeout || response.Len() != 0 {
		t.Fatalf("cancel err=%v response=%d", err, response.Len())
	}
}

func TestKeywordPrefixesRoundTrip(t *testing.T) {
	cases := []core.SearchCondition{
		{Enabled: true, Keyword: "通常"},
		{Enabled: false, CaseSensitive: true, Keyword: "停止"},
		{Enabled: true, MinimumMinutes: 10, MaximumMinutes: 90, Keyword: "時間"},
	}
	for _, want := range cases {
		var got core.SearchCondition
		if err := decodeKeyword(wireKeyword(want), &got); err != nil || got.Enabled != want.Enabled ||
			got.CaseSensitive != want.CaseSensitive || got.MinimumMinutes != want.MinimumMinutes ||
			got.MaximumMinutes != want.MaximumMinutes || got.Keyword != want.Keyword {
			t.Fatalf("want=%+v got=%+v err=%v", want, got, err)
		}
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
