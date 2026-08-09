package programsearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/channel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/programguide"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

type memorySource struct {
	snapshot       channel.Snapshot
	programs       []catalogmodel.CurrentProgram
	currentErr     error
	programErr     error
	reads          int
	currentReads   int
	changeOnWrite  bool
	omitOnWrite    bool
	reverseOnWrite bool
}

func (source *memorySource) Current(context.Context) (channel.Snapshot, error) {
	source.currentReads++
	return source.snapshot, source.currentErr
}

func (source *memorySource) CurrentProgramsForService(_ context.Context, locator string, limit int, after string) ([]catalogmodel.CurrentProgram, error) {
	source.reads++
	if source.programErr != nil {
		return nil, source.programErr
	}
	result := make([]catalogmodel.CurrentProgram, 0, limit)
	programs := source.programs
	if source.omitOnWrite && source.reads > 1 {
		programs = nil
	}
	if source.reverseOnWrite && source.reads > 1 {
		programs = append([]catalogmodel.CurrentProgram(nil), programs...)
		for left, right := 0, len(programs)-1; left < right; left, right = left+1, right-1 {
			programs[left], programs[right] = programs[right], programs[left]
		}
	}
	for _, original := range programs {
		program := original
		if source.changeOnWrite && source.reads > 1 {
			program.Hash[0]++
		}
		if program.ServiceLocator != locator || program.EventLocator <= after {
			continue
		}
		result = append(result, program)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func TestHandlerSearchesSupportedKonomiConditions(t *testing.T) {
	service := searchService("1003", 1, 2, 3)
	start := time.Date(2026, 8, 9, 10, 30, 0, 0, japanStandardTime).UTC()
	matched := searchProgram("1003", "event:1", 10, start, "Alpha ニュース", "朝の説明", true)
	excluded := searchProgram("1003", "event:2", 11, start.Add(time.Hour), "Alpha ニュース", "skip", true)
	source := &memorySource{
		snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}},
		programs: []catalogmodel.CurrentProgram{matched, excluded},
	}
	search := core.SearchCondition{
		Enabled: true, Keyword: "alpha ニュース", Exclude: "skip", FreeAccess: 1,
		Services:       []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}},
		Dates:          []core.DateRange{{StartDay: 0, StartHour: 9, EndDay: 0, EndHour: 12}},
		MinimumMinutes: 20, MaximumMinutes: 40,
	}
	handler, err := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
	if err != nil {
		t.Fatal(err)
	}
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), searchRequest(t, search, search.Exclude), &response); err != nil {
		t.Fatal(err)
	}
	ids := responseEventIDs(t, response.Bytes())
	if len(ids) != 1 || ids[0] != 10 || source.reads != 2 || source.currentReads != 1 {
		t.Fatalf("ids=%v reads=%d current=%d", ids, source.reads, source.currentReads)
	}
}

func TestHandlerAcceptsFixedKonomiRequestBytes(t *testing.T) {
	const requestHex = "010400004c000000" +
		"4c00000001000000" +
		"44000000" +
		"060000000000060000000000" +
		"0000000000000000" +
		"0800000000000000" +
		"0800000000000000" +
		"0800000000000000" +
		"0800000000000000" +
		"0800000000000000" +
		"00000000"
	request, err := hex.DecodeString(requestHex)
	if err != nil {
		t.Fatal(err)
	}
	source := &memorySource{snapshot: channel.Snapshot{Key: "fixed-konomi-request"}}
	handler, _ := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
	var response bytes.Buffer
	if err := handler.Handle(context.Background(), request, &response); err != nil {
		t.Fatal(err)
	}
	if ids := responseEventIDs(t, response.Bytes()); len(ids) != 0 {
		t.Fatalf("ids=%v", ids)
	}
}

func TestHandlerSupportsRegexTitleOnlyAndKonomiNote(t *testing.T) {
	service := searchService("1003", 1, 2, 3)
	start := time.Date(2026, 8, 9, 10, 30, 0, 0, japanStandardTime).UTC()
	source := &memorySource{
		snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}},
		programs: []catalogmodel.CurrentProgram{
			searchProgram("1003", "event:1", 10, start, "NEWS 10", "除外語", true),
			searchProgram("1003", "event:2", 11, start, "OTHER", "NEWS 11", true),
		},
	}
	search := core.SearchCondition{
		Enabled: true, Regex: true, TitleOnly: true, Keyword: `news [0-9]+`, Exclude: "除外語",
		Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}},
	}
	var response bytes.Buffer
	handler, _ := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
	if err := handler.Handle(context.Background(), searchRequest(t, search, `:note:保守\sメモ 除外語`), &response); err != nil {
		t.Fatal(err)
	}
	ids := responseEventIDs(t, response.Bytes())
	if len(ids) != 1 || ids[0] != 10 {
		t.Fatalf("ids=%v", ids)
	}
}

func TestHandlerReturnsEmptyForDisabledOrEmptyServiceCondition(t *testing.T) {
	service := searchService("1003", 1, 2, 3)
	program := searchProgram("1003", "event:1", 10, time.Now().UTC(), "番組", "", true)
	for _, test := range []struct {
		name    string
		search  core.SearchCondition
		service channel.Service
	}{
		{name: "disabled", search: core.SearchCondition{Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}, service: service},
		{name: "empty services", search: core.SearchCondition{Enabled: true}, service: service},
		{name: "unknown service", search: core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 9, TransportStreamID: 9, ServiceID: 9}}}, service: service},
		{name: "search disabled service", search: core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}, service: func() channel.Service {
			value := service
			value.Search = false
			return value
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &memorySource{snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{test.service}}, programs: []catalogmodel.CurrentProgram{program}}
			handler, _ := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
			var response bytes.Buffer
			if err := handler.Handle(context.Background(), searchRequest(t, test.search, ""), &response); err != nil {
				t.Fatal(err)
			}
			if ids := responseEventIDs(t, response.Bytes()); len(ids) != 0 {
				t.Fatalf("ids=%v", ids)
			}
		})
	}
}

func TestHandlerRejectsInvalidRegexpBeforeReadingSource(t *testing.T) {
	base := core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}
	invalidRegex := base
	invalidRegex.Regex, invalidRegex.Keyword = true, "["
	source := &memorySource{snapshot: channel.Snapshot{Key: "search"}}
	handler, _ := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
	if err := handler.Handle(context.Background(), searchRequest(t, invalidRegex, ""), io.Discard); err == nil {
		t.Fatal("不正な正規表現が受理されました")
	}
	if source.currentReads != 0 || source.reads != 0 {
		t.Fatalf("current=%d reads=%d", source.currentReads, source.reads)
	}
}

func TestHandlerRejectsInvalidNoteAndDuration(t *testing.T) {
	base := core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}
	for _, test := range []struct {
		name        string
		search      core.SearchCondition
		wireExclude string
	}{
		{name: "note escape", search: base, wireExclude: `:note:bad\q`},
		{name: "duration order", search: func() core.SearchCondition {
			value := base
			value.MinimumMinutes, value.MaximumMinutes = 60, 30
			return value
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := NewHandler(&memorySource{}, codec.DefaultLimits(), make(chan struct{}, 1))
			if err := handler.Handle(context.Background(), searchRequest(t, test.search, test.wireExclude), io.Discard); err == nil {
				t.Fatal("不正な条件が受理されました")
			}
		})
	}
}

func TestHandlerRejectsBrokenAndOversizedRequests(t *testing.T) {
	base := core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}
	handler, _ := NewHandler(&memorySource{}, codec.DefaultLimits(), make(chan struct{}, 1))
	valid := searchRequest(t, base, "")

	truncated := append([]byte(nil), valid[:len(valid)-1]...)
	requireCodecCategory(t, handler.Handle(context.Background(), truncated, io.Discard), codec.Truncated)

	trailing := append(append([]byte(nil), valid...), 0)
	binary.LittleEndian.PutUint32(trailing[4:8], uint32(len(trailing)-codec.HeaderSize))
	requireCodecCategory(t, handler.Handle(context.Background(), trailing, io.Discard), codec.Malformed)

	invalidBoolean := append([]byte(nil), valid...)
	invalidBoolean[len(invalidBoolean)-4] = 2
	requireCodecReason(t, handler.Handle(context.Background(), invalidBoolean, io.Discard), "program-search-byte")

	invalidFree := append([]byte(nil), valid...)
	invalidFree[len(invalidFree)-1] = 3
	requireCodecReason(t, handler.Handle(context.Background(), invalidFree, io.Discard), "program-search-free-access")

	overKeyword := base
	overKeyword.Keyword = strings.Repeat("a", 4_097)
	requireCodecReason(t, handler.Handle(context.Background(), searchRequest(t, overKeyword, ""), io.Discard), "program-search-keyword")

	overServices := base
	overServices.Services = make([]core.ServiceRange, maxServices+1)
	requireCodecCategory(t, handler.Handle(context.Background(), searchRequest(t, overServices, ""), io.Discard), codec.OverLimit)

	twoConditions := duplicateCondition(valid)
	requireCodecCategory(t, handler.Handle(context.Background(), twoConditions, io.Discard), codec.OverLimit)
}

func TestPreparedConditionMatchesWeekWrapExclusionDurationAndFreeAccess(t *testing.T) {
	sunday := time.Date(2026, 8, 9, 0, 30, 0, 0, japanStandardTime).UTC()
	free := searchProgram("1003", "event:1", 10, sunday, "番組", "", true)
	paid := searchProgram("1003", "event:2", 11, sunday, "番組", "", false)
	unknown := free
	unknown.Material.FreeAccess = catalogmodel.FreeUnknown

	inside := preparedCondition{search: core.SearchCondition{
		Enabled: true, FreeAccess: 1, MinimumMinutes: 30, MaximumMinutes: 30,
		Dates: []core.DateRange{{StartDay: 6, StartHour: 23, EndDay: 0, EndHour: 1}},
	}}
	if !inside.matches(free) || inside.matches(paid) || inside.matches(unknown) {
		t.Fatal("週またぎ、長さ、無料条件の組合せが一致しません")
	}

	excluded := inside
	excluded.search.ExcludeDates = true
	if excluded.matches(free) {
		t.Fatal("除外時間帯の番組が一致しました")
	}

	paidOnly := preparedCondition{search: core.SearchCondition{Enabled: true, FreeAccess: 2}}
	if paidOnly.matches(free) || !paidOnly.matches(paid) || paidOnly.matches(unknown) {
		t.Fatal("有料条件の三値判定が一致しません")
	}

	missing := free
	missing.Material.StartUTCMS = nil
	if inside.matches(missing) {
		t.Fatal("開始時刻のない番組が時間帯条件に一致しました")
	}
	missing = free
	missing.Material.DurationMS = nil
	if inside.matches(missing) {
		t.Fatal("長さのない番組が長さ条件に一致しました")
	}
}

func TestHandlerEnforcesGateResponseLimitCancellationAndStableGeneration(t *testing.T) {
	service := searchService("1003", 1, 2, 3)
	program := searchProgram("1003", "event:1", 10, time.Now().UTC(), "番組", "説明", true)
	search := core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}
	request := searchRequest(t, search, "")

	busyGate := make(chan struct{}, 1)
	busyGate <- struct{}{}
	busySource := &memorySource{snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}}, programs: []catalogmodel.CurrentProgram{program}}
	busy, _ := NewHandler(busySource, codec.DefaultLimits(), busyGate)
	if err := busy.Handle(context.Background(), request, io.Discard); err == nil || busySource.currentReads != 0 {
		t.Fatalf("busy err=%v current=%d", err, busySource.currentReads)
	}

	limitedSource := &memorySource{snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}}, programs: []catalogmodel.CurrentProgram{program}}
	limits := codec.DefaultLimits()
	limits.ResponseBody = 8
	limited, _ := NewHandler(limitedSource, limits, make(chan struct{}, 1))
	if err := limited.Handle(context.Background(), request, io.Discard); err == nil {
		t.Fatal("応答上限超過が受理されました")
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, _ := NewHandler(limitedSource, codec.DefaultLimits(), make(chan struct{}, 1))
	if err := cancelled.Handle(cancelledContext, request, io.Discard); err == nil {
		t.Fatal("取消し済み要求が受理されました")
	}

	changedSource := &memorySource{snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}}, programs: []catalogmodel.CurrentProgram{program}, changeOnWrite: true}
	changed, _ := NewHandler(changedSource, codec.DefaultLimits(), make(chan struct{}, 1))
	var changedResponse bytes.Buffer
	if err := changed.Handle(context.Background(), request, &changedResponse); err == nil {
		t.Fatal("二回走査の差が受理されました")
	}
	if _, err := codec.ParseRequestFrame(changedResponse.Bytes(), codec.DefaultLimits()); err == nil {
		t.Fatal("二回走査が異なる応答を完全な成功frameとして出力しました")
	}

	for name, changedSource := range map[string]*memorySource{
		"count": {
			snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}},
			programs: []catalogmodel.CurrentProgram{program}, omitOnWrite: true,
		},
		"order": {
			snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}},
			programs: []catalogmodel.CurrentProgram{
				searchProgram("1003", "event:1", 10, time.Now().UTC(), "番組1", "", true),
				searchProgram("1003", "event:2", 11, time.Now().UTC(), "番組2", "", true),
			},
			reverseOnWrite: true,
		},
	} {
		t.Run("generation "+name, func(t *testing.T) {
			changed, _ := NewHandler(changedSource, codec.DefaultLimits(), make(chan struct{}, 1))
			var response bytes.Buffer
			if err := changed.Handle(context.Background(), request, &response); err == nil {
				t.Fatal("二回走査の差が受理されました")
			}
			if _, err := codec.ParseRequestFrame(response.Bytes(), codec.DefaultLimits()); err == nil {
				t.Fatal("二回走査が異なる応答を完全な成功frameとして出力しました")
			}
		})
	}

	writerSource := &memorySource{snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}}, programs: []catalogmodel.CurrentProgram{program}}
	writerHandler, _ := NewHandler(writerSource, codec.DefaultLimits(), make(chan struct{}, 1))
	if err := writerHandler.Handle(context.Background(), request, failingWriter{}); err == nil {
		t.Fatal("書込み失敗が成功になりました")
	}
}

func TestHandlerDoesNotHideCatalogFailures(t *testing.T) {
	service := searchService("1003", 1, 2, 3)
	search := core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}
	request := searchRequest(t, search, "")
	for name, source := range map[string]*memorySource{
		"snapshot": {currentErr: errors.New("snapshot failed")},
		"programs": {
			snapshot:   channel.Snapshot{Key: "search", Services: []channel.Service{service}},
			programErr: errors.New("program read failed"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler, _ := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
			if err := handler.Handle(context.Background(), request, io.Discard); err == nil {
				t.Fatal("番組表の読取り失敗が成功になりました")
			}
		})
	}
}

func TestHandlerReadsProgramsInPagesAndRejectsOneOverSourceLimit(t *testing.T) {
	service := searchService("1003", 1, 2, 3)
	programs := make([]catalogmodel.CurrentProgram, pageSize+1)
	for index := range programs {
		programs[index] = searchProgram("1003", fmt.Sprintf("%06d", index), int64(index), time.Now().UTC(), "番組", "", true)
	}
	search := core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}
	source := &memorySource{snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}}, programs: programs}
	handler, _ := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
	if err := handler.Handle(context.Background(), searchRequest(t, search, ""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if source.reads != 4 {
		t.Fatalf("page reads=%d", source.reads)
	}

	generated := &generatedSource{count: maxPrograms + 1, snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}}}
	over, _ := NewHandler(generated, codec.DefaultLimits(), make(chan struct{}, 1))
	if err := over.Handle(context.Background(), searchRequest(t, search, ""), io.Discard); err == nil {
		t.Fatal("番組数の一件超過が受理されました")
	}
	if generated.maximumLimit != pageSize {
		t.Fatalf("page limit=%d", generated.maximumLimit)
	}
	if _, err := addSize(responseCap, 1, responseCap); err == nil {
		t.Fatal("応答本文の一byte超過が受理されました")
	}
}

func TestHandlerWritesMaximumResultsWithoutWholeArray(t *testing.T) {
	service := searchService("1003", 1, 2, 3)
	search := core.SearchCondition{Enabled: true, Services: []core.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}}}
	source := &generatedSource{
		count: maxPrograms, eligible: true,
		snapshot: channel.Snapshot{Key: "search", Services: []channel.Service{service}},
	}
	handler, _ := NewHandler(source, codec.DefaultLimits(), make(chan struct{}, 1))
	destination := &countingWriter{}
	if err := handler.Handle(context.Background(), searchRequest(t, search, ""), destination); err != nil {
		t.Fatal(err)
	}
	program := generatedProgram("1003", "000000")
	eventSize, eligible, err := programguide.EventStructureSize(program, codec.DefaultLimits())
	if err != nil || !eligible {
		t.Fatalf("event size=%d eligible=%v err=%v", eventSize, eligible, err)
	}
	want := int64(codec.HeaderSize+8) + int64(maxPrograms)*eventSize
	if destination.written != want || source.maximumLimit != pageSize {
		t.Fatalf("written=%d want=%d page=%d", destination.written, want, source.maximumLimit)
	}
}

type generatedSource struct {
	count        int
	maximumLimit int
	snapshot     channel.Snapshot
	eligible     bool
}

func (source *generatedSource) Current(context.Context) (channel.Snapshot, error) {
	return source.snapshot, nil
}

func (source *generatedSource) CurrentProgramsForService(_ context.Context, locator string, limit int, after string) ([]catalogmodel.CurrentProgram, error) {
	if limit > source.maximumLimit {
		source.maximumLimit = limit
	}
	start := 0
	if after != "" {
		value, err := strconv.Atoi(after)
		if err != nil {
			return nil, err
		}
		start = value + 1
	}
	end := min(start+limit, source.count)
	result := make([]catalogmodel.CurrentProgram, 0, end-start)
	for index := start; index < end; index++ {
		eventLocator := fmt.Sprintf("%06d", index)
		program := catalogmodel.CurrentProgram{ServiceLocator: locator, EventLocator: eventLocator}
		if source.eligible {
			program = generatedProgram(locator, eventLocator)
		}
		result = append(result, program)
	}
	return result, nil
}

func generatedProgram(locator, eventLocator string) catalogmodel.CurrentProgram {
	eventID := int64(1)
	startMS := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC).UnixMilli()
	durationMS := int64(time.Minute / time.Millisecond)
	title := ""
	return catalogmodel.CurrentProgram{
		ServiceLocator: locator, EventLocator: eventLocator, RawEventID: &eventID,
		Material: catalogmodel.RevisionMaterial{
			StartUTCMS: &startMS, DurationMS: &durationMS, Title: &title,
			FreeAccess: catalogmodel.FreeYes, Validation: catalogmodel.ValidationValid,
		},
	}
}

type countingWriter struct{ written int64 }

func (writer *countingWriter) Write(data []byte) (int, error) {
	writer.written += int64(len(data))
	return len(data), nil
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func searchService(locator string, networkID, transportID, serviceID uint16) channel.Service {
	return channel.Service{
		ProviderLocator: locator, ServiceName: "テスト局", NetworkID: networkID, TransportStreamID: transportID,
		ServiceID: serviceID, ServiceType: 1, Verified: true, Selected: true, Search: true,
	}
}

func searchProgram(locator, eventLocator string, eventID int64, start time.Time, title, description string, free bool) catalogmodel.CurrentProgram {
	startMS := start.UnixMilli()
	durationMS := int64((30 * time.Minute) / time.Millisecond)
	access := catalogmodel.FreeNo
	if free {
		access = catalogmodel.FreeYes
	}
	return catalogmodel.CurrentProgram{
		ServiceLocator: locator, EventLocator: eventLocator, RawEventID: &eventID,
		Hash: sha256.Sum256([]byte(title + "\x00" + description)),
		Material: catalogmodel.RevisionMaterial{
			StartUTCMS: &startMS, DurationMS: &durationMS, Title: &title, Description: &description,
			FreeAccess: access, Validation: catalogmodel.ValidationValid,
		},
	}
}

func searchRequest(t *testing.T, search core.SearchCondition, wireExclude string) []byte {
	t.Helper()
	condition := structure(t, func(writer *codec.Writer) {
		mustWrite(t, writer.String(wireKeyword(search)))
		mustWrite(t, writer.String(wireExclude))
		mustWrite(t, writer.I32(boolI32(search.Regex)))
		mustWrite(t, writer.I32(boolI32(search.TitleOnly)))
		contents := make([][]byte, len(search.Contents))
		for index, content := range search.Contents {
			content := content
			contents[index] = structure(t, func(writer *codec.Writer) {
				mustWrite(t, writer.U16(content.Content))
				mustWrite(t, writer.U16(content.User))
			})
		}
		writeVector(t, writer, contents)
		dates := make([][]byte, len(search.Dates))
		for index, date := range search.Dates {
			date := date
			dates[index] = structure(t, func(writer *codec.Writer) {
				mustWrite(t, writer.U8(date.StartDay))
				mustWrite(t, writer.U16(date.StartHour))
				mustWrite(t, writer.U16(date.StartMinute))
				mustWrite(t, writer.U8(date.EndDay))
				mustWrite(t, writer.U16(date.EndHour))
				mustWrite(t, writer.U16(date.EndMinute))
			})
		}
		writeVector(t, writer, dates)
		services := make([][]byte, len(search.Services))
		for index, service := range search.Services {
			packed := int64(service.NetworkID)<<32 | int64(service.TransportStreamID)<<16 | int64(service.ServiceID)
			services[index] = primitive(t, func(writer *codec.Writer) { mustWrite(t, writer.I64(packed)) })
		}
		writeVector(t, writer, services)
		video := make([][]byte, len(search.Video))
		for index, value := range search.Video {
			value := value
			video[index] = primitive(t, func(writer *codec.Writer) { mustWrite(t, writer.U16(value)) })
		}
		writeVector(t, writer, video)
		audio := make([][]byte, len(search.Audio))
		for index, value := range search.Audio {
			value := value
			audio[index] = primitive(t, func(writer *codec.Writer) { mustWrite(t, writer.U16(value)) })
		}
		writeVector(t, writer, audio)
		mustWrite(t, writer.U8(boolU8(search.Fuzzy)))
		mustWrite(t, writer.U8(boolU8(search.ExcludeContents)))
		mustWrite(t, writer.U8(boolU8(search.ExcludeDates)))
		mustWrite(t, writer.U8(search.FreeAccess))
	})
	var body bytes.Buffer
	writer, err := codec.NewWriter(&body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	writeVector(t, writer, [][]byte{condition})
	request := make([]byte, codec.HeaderSize+body.Len())
	binary.LittleEndian.PutUint32(request[0:4], uint32(Command))
	binary.LittleEndian.PutUint32(request[4:8], uint32(body.Len()))
	copy(request[8:], body.Bytes())
	return request
}

func wireKeyword(search core.SearchCondition) string {
	var value strings.Builder
	if !search.Enabled {
		value.WriteString(disabledPrefix)
	}
	if search.CaseSensitive {
		value.WriteString(casePrefix)
	}
	if search.MinimumMinutes > 0 || search.MaximumMinutes > 0 {
		fmt.Fprintf(&value, "D!{1%08d}", uint32(search.MinimumMinutes)*10_000+uint32(search.MaximumMinutes))
	}
	value.WriteString(search.Keyword)
	return value.String()
}

func structure(t *testing.T, write func(*codec.Writer)) []byte {
	t.Helper()
	body := primitive(t, write)
	var result bytes.Buffer
	writer, _ := codec.NewWriter(&result, codec.DefaultLimits())
	mustWrite(t, writer.I32(int32(4+len(body))))
	mustWrite(t, writer.Bytes(body))
	return result.Bytes()
}

func primitive(t *testing.T, write func(*codec.Writer)) []byte {
	t.Helper()
	var result bytes.Buffer
	writer, err := codec.NewWriter(&result, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	write(writer)
	return result.Bytes()
}

func writeVector(t *testing.T, writer *codec.Writer, elements [][]byte) {
	t.Helper()
	size := 8
	for _, element := range elements {
		size += len(element)
	}
	mustWrite(t, writer.I32(int32(size)))
	mustWrite(t, writer.I32(int32(len(elements))))
	for _, element := range elements {
		mustWrite(t, writer.Bytes(element))
	}
}

func mustWrite(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func boolI32(value bool) int32 {
	if value {
		return 1
	}
	return 0
}

func boolU8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

func duplicateCondition(request []byte) []byte {
	body := request[codec.HeaderSize:]
	condition := body[8:]
	duplicatedBody := make([]byte, 8+len(condition)*2)
	binary.LittleEndian.PutUint32(duplicatedBody[0:4], uint32(len(duplicatedBody)))
	binary.LittleEndian.PutUint32(duplicatedBody[4:8], 2)
	copy(duplicatedBody[8:], condition)
	copy(duplicatedBody[8+len(condition):], condition)
	return commandRequest(Command, duplicatedBody)
}

func commandRequest(command int32, body []byte) []byte {
	request := make([]byte, codec.HeaderSize+len(body))
	binary.LittleEndian.PutUint32(request[0:4], uint32(command))
	binary.LittleEndian.PutUint32(request[4:8], uint32(len(body)))
	copy(request[codec.HeaderSize:], body)
	return request
}

func requireCodecCategory(t *testing.T, err error, category codec.Category) {
	t.Helper()
	var codecError *codec.Error
	if !errors.As(err, &codecError) || codecError.Category != category {
		t.Fatalf("error=%v, want category=%s", err, category)
	}
}

func requireCodecReason(t *testing.T, err error, reason string) {
	t.Helper()
	var codecError *codec.Error
	if !errors.As(err, &codecError) || codecError.Reason != reason {
		t.Fatalf("error=%v, want reason=%s", err, reason)
	}
}

func responseEventIDs(t *testing.T, response []byte) []uint16 {
	t.Helper()
	frame, err := codec.ParseRequestFrame(response, codec.DefaultLimits())
	if err != nil || frame.Code != ResultSuccess {
		t.Fatalf("frame=%+v err=%v", frame, err)
	}
	reader, err := codec.NewReader(frame.Body, codec.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]uint16, 0)
	if err := reader.Vector(1, maxPrograms, func(event *codec.Reader, _ int) error {
		return event.Structure(func(item *codec.Reader) error {
			for range 3 {
				if _, err := item.U16(); err != nil {
					return err
				}
			}
			eventID, err := item.U16()
			if err != nil {
				return err
			}
			ids = append(ids, eventID)
			if _, err := item.U8(); err != nil {
				return err
			}
			if _, err := item.SystemTime(); err != nil {
				return err
			}
			if _, err := item.U8(); err != nil {
				return err
			}
			if _, err := item.I32(); err != nil {
				return err
			}
			if err := item.Structure(func(short *codec.Reader) error {
				if _, err := short.String(); err != nil {
					return err
				}
				_, err := short.String()
				return err
			}); err != nil {
				return err
			}
			if err := item.Structure(func(value *codec.Reader) error {
				if value.Remaining() == 0 {
					return nil
				}
				_, err := value.String()
				return err
			}); err != nil {
				return err
			}
			if err := item.Structure(func(value *codec.Reader) error {
				if value.Remaining() == 0 {
					return nil
				}
				return value.Vector(8, 64, func(element *codec.Reader, _ int) error {
					return element.Structure(func(fields *codec.Reader) error {
						for range 4 {
							if _, err := fields.U8(); err != nil {
								return err
							}
						}
						return nil
					})
				})
			}); err != nil {
				return err
			}
			if err := item.Structure(func(value *codec.Reader) error {
				if value.Remaining() == 0 {
					return nil
				}
				for range 3 {
					if _, err := value.U8(); err != nil {
						return err
					}
				}
				_, err := value.String()
				return err
			}); err != nil {
				return err
			}
			if err := item.Structure(func(value *codec.Reader) error {
				if value.Remaining() == 0 {
					return nil
				}
				return value.Vector(19, 16, func(element *codec.Reader, _ int) error {
					return element.Structure(func(fields *codec.Reader) error {
						for range 9 {
							if _, err := fields.U8(); err != nil {
								return err
							}
						}
						_, err := fields.String()
						return err
					})
				})
			}); err != nil {
				return err
			}
			for range 2 {
				if err := item.Structure(func(*codec.Reader) error { return nil }); err != nil {
					return err
				}
			}
			_, err = item.U8()
			return err
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.Exact(); err != nil {
		t.Fatal(err)
	}
	return ids
}
