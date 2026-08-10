package autoreservation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

var errDuplicateForTest = errors.New("duplicate")

type evaluationStore struct {
	rules       []autoreservation.Rule
	created     []recording.Reservation
	ruleNumbers []int32
	seen        map[catalogmodel.ID]struct{}
}

func (store *evaluationStore) AutomaticRules(_ context.Context, limit int, after int32) ([]autoreservation.Rule, error) {
	result := make([]autoreservation.Rule, 0, limit)
	for _, rule := range store.rules {
		if rule.Number > after && len(result) < limit {
			result = append(result, rule)
		}
	}
	return result, nil
}

func (store *evaluationStore) CreateAutomaticReservation(_ context.Context, ruleNumber int32, value recording.Reservation) (recording.Reservation, error) {
	if _, duplicate := store.seen[value.Program.ProgramInstanceID]; duplicate {
		return recording.Reservation{}, errDuplicateForTest
	}
	store.seen[value.Program.ProgramInstanceID] = struct{}{}
	store.created = append(store.created, value)
	store.ruleNumbers = append(store.ruleNumbers, ruleNumber)
	return value, nil
}

type evaluationCatalog struct{ programs []catalogmodel.CurrentProgram }

func (catalog evaluationCatalog) CurrentProgramsByService(_ context.Context, limit int,
	after catalogmodel.ProgramCursor,
) ([]catalogmodel.CurrentProgram, error) {
	result := make([]catalogmodel.CurrentProgram, 0, limit)
	for _, program := range catalog.programs {
		cursor := catalogmodel.ProgramCursor{ServiceLocator: program.ServiceLocator, EventLocator: program.EventLocator}
		if compareCursor(cursor, after) > 0 && len(result) < limit {
			result = append(result, program)
		}
	}
	return result, nil
}

func (evaluationCatalog) ReservationRequestForProgram(program catalogmodel.CurrentProgram, priority uint8,
	follow bool,
) (recording.ReservationRequest, error) {
	return recording.ReservationRequest{
		NetworkID: 1, TransportStreamID: 2, ServiceID: 3, EventID: uint16(*program.RawEventID),
		Start:    time.UnixMilli(*program.Material.StartUTCMS).UTC(),
		Duration: time.Duration(*program.Material.DurationMS) * time.Millisecond,
		Priority: priority, RequestedFollow: follow,
	}, nil
}

func (evaluationCatalog) FindProgram(_ context.Context, request recording.ReservationRequest) (recording.ProgramSnapshot, error) {
	return recording.ProgramSnapshot{
		ProgramInstanceID: catalogmodel.ID{byte(request.EventID)}, ProgramRevisionID: catalogmodel.ID{99},
		BackendID: catalogmodel.ID{98}, ProviderServiceLocator: "1", TuningTarget: "1",
		NetworkID: request.NetworkID, TransportStreamID: request.TransportStreamID, ServiceID: request.ServiceID,
		EventID: request.EventID, Title: "番組", StationName: "局", Start: request.Start, Duration: request.Duration,
	}, nil
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func TestEvaluatorCreatesOnceAndSkipsUnavailableRule(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	rules := []autoreservation.Rule{
		storedRule(1, autoreservation.SearchCondition{Enabled: true, Keyword: "morning news", Exclude: "sports",
			Services:   []autoreservation.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}},
			FreeAccess: 1}),
		storedRule(2, autoreservation.SearchCondition{Enabled: true, Keyword: "news", CheckRecordedTitle: true}),
	}
	programs := []catalogmodel.CurrentProgram{
		currentProgram(1, now.Add(time.Hour), "Morning News", "headlines", catalogmodel.FreeYes),
		currentProgram(2, now.Add(2*time.Hour), "Sports News", "results", catalogmodel.FreeYes),
	}
	store := &evaluationStore{rules: rules, seen: make(map[catalogmodel.ID]struct{})}
	nextID := byte(10)
	evaluator := Evaluator{
		Store: store, Catalog: evaluationCatalog{programs: programs}, Clock: fixedClock{now},
		NewID:       func() (catalogmodel.ID, error) { nextID++; return catalogmodel.ID{nextID}, nil },
		IsDuplicate: func(err error) bool { return errors.Is(err, errDuplicateForTest) },
	}
	result, err := evaluator.Run(context.Background())
	if err != nil || result.Rules != 2 || result.Programs != 2 || result.Comparisons != 3 ||
		result.UnavailableRules != 1 || result.Matched != 1 || result.Created != 1 || len(store.created) != 1 {
		t.Fatalf("result=%+v created=%d err=%v", result, len(store.created), err)
	}
	result, err = evaluator.Run(context.Background())
	if err != nil || result.Created != 0 || result.Duplicates != 1 || len(store.created) != 1 {
		t.Fatalf("second=%+v created=%d err=%v", result, len(store.created), err)
	}
}

func TestEvaluatorInheritsDisabledPriorityFollowAndMargins(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	startMargin, endMargin := int32(-10), int32(20)
	rule := storedRule(1, autoreservation.SearchCondition{Enabled: true})
	rule.Recording = autoreservation.RecordingSettings{
		Mode: 5, Priority: 1, Follow: false, ServiceMode: 0x21, StartMargin: &startMargin, EndMargin: &endMargin,
		Folders: []autoreservation.Folder{{Path: "ドラマ", Writer: "Write_Default.dll", Name: "RecName_Macro.dll?$Title$"}},
		Batch:   "/allowed/finish.sh", Suspend: 4,
	}
	store := &evaluationStore{rules: []autoreservation.Rule{rule}, seen: make(map[catalogmodel.ID]struct{})}
	result, err := (Evaluator{
		Store: store, Catalog: evaluationCatalog{programs: []catalogmodel.CurrentProgram{
			currentProgram(1, now.Add(time.Hour), "番組", "", catalogmodel.FreeYes),
		}}, Clock: fixedClock{now},
		NewID:       func() (catalogmodel.ID, error) { return catalogmodel.ID{10}, nil },
		IsDuplicate: func(error) bool { return false },
		ValidatePostRecordingScript: func(path string) error {
			if path != "/allowed/finish.sh" {
				return errors.New("unexpected script")
			}
			return nil
		},
	}).Run(context.Background())
	if err != nil || result.Created != 1 || len(store.created) != 1 {
		t.Fatalf("result=%+v created=%+v err=%v", result, store.created, err)
	}
	created := store.created[0]
	if !created.Disabled || created.Priority != 1 || created.RequestedFollow || created.Margins == nil ||
		*created.Margins != (recording.RecordingMargins{Start: -10 * time.Second, End: 20 * time.Second}) ||
		created.Output != (recording.OutputSettings{Folder: "ドラマ", Template: "$Title$"}) ||
		created.Components != recording.ComponentDataOnly ||
		created.PostRecording != (recording.PostRecordingSettings{Mode: recording.PostRecordingNothing, Script: "/allowed/finish.sh"}) {
		t.Fatalf("created=%+v", created)
	}
}

func TestEvaluatorPreflightsProgramLimit(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	programs := make([]catalogmodel.CurrentProgram, autoreservation.MaxProgramsPerRun+1)
	for index := range programs {
		programs[index] = currentProgram(index+1, now.Add(time.Hour), "番組", "", catalogmodel.FreeYes)
	}
	store := &evaluationStore{rules: []autoreservation.Rule{storedRule(1, autoreservation.SearchCondition{Enabled: true})},
		seen: make(map[catalogmodel.ID]struct{})}
	evaluator := Evaluator{
		Store: store, Catalog: evaluationCatalog{programs: programs}, Clock: fixedClock{now},
		NewID:       func() (catalogmodel.ID, error) { return catalogmodel.ID{1}, nil },
		IsDuplicate: func(error) bool { return false },
	}
	if result, err := evaluator.Run(context.Background()); err == nil || result.Created != 0 || len(store.created) != 0 {
		t.Fatalf("result=%+v created=%d err=%v", result, len(store.created), err)
	}
}

func TestSupportedSearchConditions(t *testing.T) {
	start := time.Date(2026, 8, 9, 1, 0, 0, 0, time.FixedZone("Asia/Tokyo", 9*60*60))
	program := currentProgram(1, start, "Alpha News", "beta details", catalogmodel.FreeNo)
	target := prepareRule(storedRule(1, autoreservation.SearchCondition{
		Enabled: true, CaseSensitive: true, Keyword: "Alpha beta", TitleOnly: false, FreeAccess: 2,
	}), nil)
	if target.unavailable || !target.matcher.Matches(program) {
		t.Fatal("AND、説明、大文字小文字、有料指定が一致しません")
	}
	target = prepareRule(storedRule(1, autoreservation.SearchCondition{
		Enabled: true, CaseSensitive: true, Keyword: "Alpha beta", Exclude: "details", FreeAccess: 2,
	}), nil)
	if target.matcher.Matches(program) {
		t.Fatal("除外語を含む番組が一致しました")
	}
	target = prepareRule(storedRule(1, autoreservation.SearchCondition{
		Enabled: true, Regex: true, TitleOnly: true, Keyword: `^Alpha News$`,
	}), nil)
	if target.unavailable || !target.matcher.Matches(program) {
		t.Fatal("正規表現と番組名限定が一致しません")
	}
	request := recording.ReservationRequest{
		NetworkID: 1, TransportStreamID: 2, ServiceID: 3, Start: start, Duration: 30 * time.Minute,
	}
	search := autoreservation.SearchCondition{
		Enabled:        true,
		Services:       []autoreservation.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}},
		Dates:          []autoreservation.DateRange{{StartDay: 6, StartHour: 23, EndDay: 0, EndHour: 2}},
		MinimumMinutes: 30, MaximumMinutes: 30,
	}
	target = prepareRule(storedRule(1, search), nil)
	if target.unavailable || !target.matcher.Matches(program) || !matchService(search.Services, request) {
		t.Fatal("サービス、日時、番組時間が一致しません")
	}
	request.ServiceID = 4
	if matchService(search.Services, request) {
		t.Fatal("別サービスが一致しました")
	}
}

func TestUnsupportedRulesAreUnavailable(t *testing.T) {
	base := storedRule(1, autoreservation.SearchCondition{Enabled: true})
	cases := map[string]autoreservation.Rule{}
	invalidRegex := base
	invalidRegex.Search.Regex, invalidRegex.Search.Keyword = true, "["
	cases["invalid-regexp"] = invalidRegex
	recorded := base
	recorded.Search.CheckRecordedTitle = true
	cases["recorded-title"] = recorded
	unsafeRecording := base
	unsafeRecording.Recording.Folders = []autoreservation.Folder{{Path: "custom"}}
	cases["recording-setting"] = unsafeRecording
	powerAction := base
	powerAction.Recording.Suspend = 1
	cases["power-action"] = powerAction
	unsafeScript := base
	unsafeScript.Recording.Batch = "/outside/finish.sh"
	cases["script-without-validator"] = unsafeScript
	for name, rule := range cases {
		t.Run(name, func(t *testing.T) {
			if prepared := prepareRule(rule, nil); !prepared.unavailable {
				t.Fatalf("rule=%+v", rule)
			}
		})
	}
}

func TestEvaluatorMatchesMetadataAndFuzzyConditions(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	program := currentProgram(1, now.Add(time.Hour), "テスト本組", "", catalogmodel.FreeYes)
	video := catalogmodel.Video{StreamContent: 1, ComponentType: 0xb3}
	program.Material.Metadata = catalogmodel.ProgramMetadata{
		Genres: []catalogmodel.Genre{{Level1: 1, Level2: 2}}, Video: &video,
		Audios: []catalogmodel.Audio{{ComponentType: 3, SamplingRate: 48_000}},
	}
	rule := storedRule(1, autoreservation.SearchCondition{
		Enabled: true, Fuzzy: true, Keyword: "テスト番組",
		Services: []autoreservation.ServiceRange{{NetworkID: 1, TransportStreamID: 2, ServiceID: 3}},
		Contents: []autoreservation.ContentRange{{Content: 0xff01}},
		Video:    []uint16{0x01b3}, Audio: []uint16{0x0203},
	})
	store := &evaluationStore{rules: []autoreservation.Rule{rule}, seen: make(map[catalogmodel.ID]struct{})}
	result, err := (Evaluator{
		Store: store, Catalog: evaluationCatalog{programs: []catalogmodel.CurrentProgram{program}}, Clock: fixedClock{now},
		NewID:       func() (catalogmodel.ID, error) { return catalogmodel.ID{10}, nil },
		IsDuplicate: func(error) bool { return false },
	}).Run(context.Background())
	if err != nil || result.UnavailableRules != 0 || result.Matched != 1 || result.Created != 1 || len(store.created) != 1 {
		t.Fatalf("result=%+v created=%d err=%v", result, len(store.created), err)
	}
}

func TestEvaluatorUsesFirstRuleAndStopsAtCreationLimit(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	programs := make([]catalogmodel.CurrentProgram, autoreservation.MaxReservationsPerRun+1)
	for index := range programs {
		programs[index] = currentProgram(index+1, now.Add(time.Duration(index+1)*time.Minute), "番組", "", catalogmodel.FreeYes)
	}
	store := &evaluationStore{rules: []autoreservation.Rule{
		storedRule(1, autoreservation.SearchCondition{Enabled: true}),
		storedRule(2, autoreservation.SearchCondition{Enabled: true}),
	}, seen: make(map[catalogmodel.ID]struct{})}
	nextID := 0
	result, err := (Evaluator{
		Store: store, Catalog: evaluationCatalog{programs: programs}, Clock: fixedClock{now},
		NewID:       func() (catalogmodel.ID, error) { nextID++; return catalogmodel.ID{byte(nextID), 9}, nil },
		IsDuplicate: func(error) bool { return false },
	}).Run(context.Background())
	if err != nil || result.Created != autoreservation.MaxReservationsPerRun || !result.LimitReached ||
		len(store.created) != autoreservation.MaxReservationsPerRun {
		t.Fatalf("result=%+v created=%d err=%v", result, len(store.created), err)
	}
	for _, number := range store.ruleNumbers {
		if number != 1 {
			t.Fatalf("後順位の規則が先に使われました: %d", number)
		}
	}
}

func TestEvaluatorRejectsRuleLimitAndCancellationBeforeWrites(t *testing.T) {
	now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	rules := make([]autoreservation.Rule, autoreservation.MaxRules+1)
	for index := range rules {
		rules[index] = storedRule(int32(index+1), autoreservation.SearchCondition{Enabled: true})
	}
	store := &evaluationStore{rules: rules, seen: make(map[catalogmodel.ID]struct{})}
	evaluator := Evaluator{
		Store: store, Catalog: evaluationCatalog{programs: []catalogmodel.CurrentProgram{
			currentProgram(1, now.Add(time.Hour), "番組", "", catalogmodel.FreeYes),
		}}, Clock: fixedClock{now},
		NewID:       func() (catalogmodel.ID, error) { return catalogmodel.ID{1}, nil },
		IsDuplicate: func(error) bool { return false },
	}
	if result, err := evaluator.Run(context.Background()); err == nil || result.Created != 0 || len(store.created) != 0 {
		t.Fatalf("rule limit result=%+v created=%d err=%v", result, len(store.created), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store.rules = rules[:1]
	if result, err := evaluator.Run(ctx); err == nil || result.Created != 0 || len(store.created) != 0 {
		t.Fatalf("cancel result=%+v created=%d err=%v", result, len(store.created), err)
	}
}

func storedRule(number int32, search autoreservation.SearchCondition) autoreservation.Rule {
	return autoreservation.Rule{
		ID: catalogmodel.ID{byte(number)}, Number: number, Version: 1, Search: search,
		Recording:      autoreservation.RecordingSettings{Mode: 1, Priority: 3, Follow: true},
		CreatedAtUTCMS: 1, UpdatedAtUTCMS: 1,
	}
}

func currentProgram(event int, start time.Time, title, description string,
	free catalogmodel.FreeAccess,
) catalogmodel.CurrentProgram {
	startMS, durationMS, rawEvent := start.UnixMilli(), int64((30*time.Minute)/time.Millisecond), int64(event)
	return catalogmodel.CurrentProgram{
		InstanceID: catalogmodel.ID{byte(event)}, RevisionID: catalogmodel.ID{byte(event), 1},
		ServiceLocator: "1", EventLocator: fmt.Sprintf("%06d", event), RawEventID: &rawEvent,
		Material: catalogmodel.RevisionMaterial{StartUTCMS: &startMS, DurationMS: &durationMS,
			Title: &title, Description: &description, FreeAccess: free, Validation: catalogmodel.ValidationValid},
	}
}
