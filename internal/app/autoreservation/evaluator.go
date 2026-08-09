package autoreservation

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const evaluationWindow = 8 * 24 * time.Hour

// Catalogは一つの完成済み番組表世代を順番に読み、予約照合へ変換する。
type Catalog interface {
	CurrentProgramsByService(context.Context, int, catalogmodel.ProgramCursor) ([]catalogmodel.CurrentProgram, error)
	ReservationRequestForProgram(catalogmodel.CurrentProgram, uint8, bool) (recording.ReservationRequest, error)
	FindProgram(context.Context, recording.ReservationRequest) (recording.ProgramSnapshot, error)
}

// EvaluationStoreは規則読出しと規則に結び付く予約のtransaction保存を提供する。
type EvaluationStore interface {
	AutomaticRules(context.Context, int, int32) ([]autoreservation.Rule, error)
	CreateAutomaticReservation(context.Context, int32, recording.Reservation) (recording.Reservation, error)
}

// DuplicateErrorはDBが同じ番組の予約履歴を検出したかを判定する。
type DuplicateError func(error) bool

// Resultは検索語や番組情報を含まない一回分の評価件数である。
type Result struct {
	Rules, Programs, Comparisons int
	Matched, Created, Duplicates int
	UnavailableRules             int
	LimitReached                 bool
}

// Evaluatorは完成済み番組表を事前に数えてから、固定上限内で予約を作る。
type Evaluator struct {
	Store       EvaluationStore
	Catalog     Catalog
	Clock       Clock
	NewID       func() (catalogmodel.ID, error)
	IsDuplicate DuplicateError
	OnCreated   func()
}

type preparedRule struct {
	rule        autoreservation.Rule
	keyword     *regexp.Regexp
	exclude     *regexp.Regexp
	skip        bool
	unavailable bool
}

// Runは規則と対象番組を固定してから評価し、一件ずつtransactionで予約する。
func (evaluator Evaluator) Run(ctx context.Context) (Result, error) {
	var result Result
	if ctx == nil || evaluator.Store == nil || evaluator.Catalog == nil || evaluator.Clock == nil ||
		evaluator.NewID == nil || evaluator.IsDuplicate == nil {
		return result, errors.New("autoreservation: invalid evaluator")
	}
	now := evaluator.Clock.Now().UTC()
	if now.IsZero() || now.UnixMilli() < 0 {
		return result, errors.New("autoreservation: invalid clock")
	}
	rules, err := readRules(ctx, evaluator.Store)
	if err != nil {
		return result, err
	}
	result.Rules = len(rules)
	if len(rules) == 0 {
		return result, nil
	}
	prepared := make([]preparedRule, len(rules))
	for index, rule := range rules {
		prepared[index] = prepareRule(rule)
		if prepared[index].unavailable {
			result.UnavailableRules++
		}
	}
	programs, err := countPrograms(ctx, evaluator.Catalog, now, now.Add(evaluationWindow))
	if err != nil {
		return result, err
	}
	result.Programs = programs
	if programs > autoreservation.MaxProgramsPerRun || len(rules) > 0 && programs > autoreservation.MaxComparisonsPerRun/len(rules) {
		return result, errors.New("autoreservation: evaluation limit exceeded")
	}
	err = forEachProgram(ctx, evaluator.Catalog, func(program catalogmodel.CurrentProgram) error {
		if !programInWindow(program, now, now.Add(evaluationWindow)) {
			return nil
		}
		for _, candidate := range prepared {
			result.Comparisons++
			if candidate.skip || candidate.unavailable || !matchProgram(candidate, program) {
				continue
			}
			request, err := evaluator.Catalog.ReservationRequestForProgram(program, candidate.rule.Recording.Priority,
				candidate.rule.Recording.Follow)
			if err != nil || !matchService(candidate.rule.Search.Services, request) || !matchDate(candidate.rule.Search.Dates,
				candidate.rule.Search.ExcludeDates, request.Start) || !matchDuration(candidate.rule.Search, request.Duration) {
				continue
			}
			result.Matched++
			if result.Created == autoreservation.MaxReservationsPerRun {
				result.LimitReached = true
				return nil
			}
			snapshot, err := evaluator.Catalog.FindProgram(ctx, request)
			if err != nil {
				continue
			}
			id, err := evaluator.NewID()
			if err != nil {
				return errors.New("autoreservation: id generation failed")
			}
			_, err = evaluator.Store.CreateAutomaticReservation(ctx, candidate.rule.Number, recording.Reservation{
				ID: id, Version: 1, State: recording.ReservationActive, Program: snapshot,
				Priority: request.Priority, RequestedFollow: request.RequestedFollow,
				CreatedAt: now, UpdatedAt: now,
			})
			if err != nil {
				if evaluator.IsDuplicate(err) {
					result.Duplicates++
					return nil
				}
				return errors.New("autoreservation: create reservation failed")
			}
			result.Created++
			if evaluator.OnCreated != nil {
				evaluator.OnCreated()
			}
			return nil
		}
		return nil
	})
	return result, err
}

func readRules(ctx context.Context, store EvaluationStore) ([]autoreservation.Rule, error) {
	result := make([]autoreservation.Rule, 0, autoreservation.MaxRules)
	var after int32
	for {
		page, err := store.AutomaticRules(ctx, autoreservation.MaxPage, after)
		if err != nil || len(page) > autoreservation.MaxPage {
			return nil, errors.New("autoreservation: read rules failed")
		}
		for _, rule := range page {
			if rule.Number <= after || len(result) == autoreservation.MaxRules || rule.ValidateStored() != nil {
				return nil, errors.New("autoreservation: invalid rule order")
			}
			after = rule.Number
			result = append(result, rule)
		}
		if len(page) < autoreservation.MaxPage {
			return result, nil
		}
	}
}

func countPrograms(ctx context.Context, catalog Catalog, from, to time.Time) (int, error) {
	count := 0
	err := forEachProgram(ctx, catalog, func(program catalogmodel.CurrentProgram) error {
		if programInWindow(program, from, to) {
			count++
			if count > autoreservation.MaxProgramsPerRun {
				return errors.New("autoreservation: program limit exceeded")
			}
		}
		return nil
	})
	return count, err
}

func forEachProgram(ctx context.Context, catalog Catalog, visit func(catalogmodel.CurrentProgram) error) error {
	var cursor catalogmodel.ProgramCursor
	seen := 0
	for {
		if err := ctx.Err(); err != nil {
			return errors.New("autoreservation: context ended")
		}
		page, err := catalog.CurrentProgramsByService(ctx, catalogmodel.MaxQueryPage, cursor)
		if err != nil || len(page) > catalogmodel.MaxQueryPage {
			return errors.New("autoreservation: read programs failed")
		}
		for _, program := range page {
			next := catalogmodel.ProgramCursor{ServiceLocator: program.ServiceLocator, EventLocator: program.EventLocator}
			if compareCursor(next, cursor) <= 0 || seen == provider.MaxProgramOperation {
				return errors.New("autoreservation: invalid program order")
			}
			cursor, seen = next, seen+1
			if err := visit(program); err != nil {
				return err
			}
		}
		if len(page) < catalogmodel.MaxQueryPage {
			return nil
		}
	}
}

func compareCursor(left, right catalogmodel.ProgramCursor) int {
	if value := strings.Compare(left.ServiceLocator, right.ServiceLocator); value != 0 {
		return value
	}
	return strings.Compare(left.EventLocator, right.EventLocator)
}

func programInWindow(program catalogmodel.CurrentProgram, from, to time.Time) bool {
	material := program.Material
	return material.StartUTCMS != nil && material.DurationMS != nil && material.Title != nil &&
		material.Validation == catalogmodel.ValidationValid && *material.StartUTCMS >= from.UnixMilli() &&
		*material.StartUTCMS < to.UnixMilli() && *material.DurationMS >= 1_000 &&
		*material.DurationMS <= int64((24*time.Hour)/time.Millisecond)
}

func prepareRule(rule autoreservation.Rule) preparedRule {
	prepared := preparedRule{rule: rule}
	search, settings := rule.Search, rule.Recording
	if !search.Enabled {
		prepared.skip = true
		return prepared
	}
	if search.Fuzzy || len(search.Contents) != 0 || search.ExcludeContents || len(search.Video) != 0 || len(search.Audio) != 0 ||
		search.CheckRecordedTitle || settings.Mode != 1 || settings.ServiceMode != 0 || settings.Exact || settings.Batch != "" ||
		len(settings.Folders) != 0 || settings.Suspend != 0 || settings.Reboot || settings.StartMargin != nil ||
		settings.Continue || settings.PartialMode != 0 || settings.TunerID != 0 || len(settings.PartialFolders) != 0 {
		prepared.unavailable = true
		return prepared
	}
	if search.Regex {
		keyword, err := compilePattern(search.Keyword, search.CaseSensitive)
		if err != nil {
			prepared.unavailable = true
			return prepared
		}
		exclude, err := compilePattern(search.Exclude, search.CaseSensitive)
		if err != nil {
			prepared.unavailable = true
			return prepared
		}
		prepared.keyword, prepared.exclude = keyword, exclude
	}
	return prepared
}

func compilePattern(value string, caseSensitive bool) (*regexp.Regexp, error) {
	if value == "" {
		return nil, nil
	}
	if !caseSensitive {
		value = "(?i)" + value
	}
	return regexp.Compile(value)
}

func matchProgram(rule preparedRule, program catalogmodel.CurrentProgram) bool {
	material := program.Material
	if material.Title == nil {
		return false
	}
	target := *material.Title
	if !rule.rule.Search.TitleOnly && material.Description != nil {
		target += "\n" + *material.Description
	}
	if !matchText(rule, target) {
		return false
	}
	if rule.rule.Search.FreeAccess != 0 {
		if material.FreeAccess == catalogmodel.FreeUnknown ||
			rule.rule.Search.FreeAccess == 1 && material.FreeAccess != catalogmodel.FreeYes ||
			rule.rule.Search.FreeAccess == 2 && material.FreeAccess != catalogmodel.FreeNo {
			return false
		}
	}
	return true
}

func matchText(rule preparedRule, target string) bool {
	search := rule.rule.Search
	if search.Regex {
		return (rule.keyword == nil || rule.keyword.MatchString(target)) && (rule.exclude == nil || !rule.exclude.MatchString(target))
	}
	keyword, exclude := search.Keyword, search.Exclude
	if !search.CaseSensitive {
		target, keyword, exclude = strings.ToLower(target), strings.ToLower(keyword), strings.ToLower(exclude)
	}
	for _, word := range strings.Fields(keyword) {
		if !strings.Contains(target, word) {
			return false
		}
	}
	for _, word := range strings.Fields(exclude) {
		if strings.Contains(target, word) {
			return false
		}
	}
	return true
}

func matchService(services []autoreservation.ServiceRange, request recording.ReservationRequest) bool {
	if len(services) == 0 {
		return true
	}
	for _, service := range services {
		if service.NetworkID == request.NetworkID && service.TransportStreamID == request.TransportStreamID &&
			service.ServiceID == request.ServiceID {
			return true
		}
	}
	return false
}

func matchDate(ranges []autoreservation.DateRange, exclude bool, start time.Time) bool {
	if len(ranges) == 0 {
		return true
	}
	local := start.In(time.FixedZone("Asia/Tokyo", 9*60*60))
	minute := int(local.Weekday())*24*60 + local.Hour()*60 + local.Minute()
	matched := false
	for _, value := range ranges {
		from := int(value.StartDay)*24*60 + int(value.StartHour)*60 + int(value.StartMinute)
		to := int(value.EndDay)*24*60 + int(value.EndHour)*60 + int(value.EndMinute)
		if from == to || from < to && minute >= from && minute < to || from > to && (minute >= from || minute < to) {
			matched = true
			break
		}
	}
	return matched != exclude
}

func matchDuration(search autoreservation.SearchCondition, duration time.Duration) bool {
	minutes := uint64(duration / time.Minute)
	return (search.MinimumMinutes == 0 || minutes >= uint64(search.MinimumMinutes)) &&
		(search.MaximumMinutes == 0 || minutes <= uint64(search.MaximumMinutes))
}
