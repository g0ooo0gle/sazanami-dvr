package autoreservation

import (
	"context"
	"errors"
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
	ResolveOneSeg(context.Context, recording.ProgramSnapshot) (string, error)
}

// EvaluationStoreは規則読出しと規則に結び付く予約のtransaction保存を提供する。
type EvaluationStore interface {
	AutomaticRules(context.Context, int, int32) ([]autoreservation.Rule, error)
	RecordingHistory(context.Context, int, int32) ([]recording.HistoryItem, error)
	CreateAutomaticReservation(context.Context, int32, recording.Reservation) (recording.Reservation, error)
	DisableAutomaticReservation(context.Context, catalogmodel.ID, time.Time) (bool, error)
}

// DuplicateErrorはDBが同じ番組の予約履歴を検出したかを判定する。
type DuplicateError func(error) bool

// Resultは検索語や番組情報を含まない一回分の評価件数である。
type Result struct {
	Rules, Programs, Comparisons int
	Matched, Created, Duplicates int
	RecordedTitleMatches         int
	UnavailableRules             int
	ForcedTunerUnavailableRules  int
	OneSegUnavailableRules       int
	OneSegUnresolvedPrograms     int
	LimitReached                 bool
}

// Evaluatorは完成済み番組表を事前に数えてから、固定上限内で予約を作る。
type Evaluator struct {
	Store                       EvaluationStore
	Catalog                     Catalog
	Clock                       Clock
	NewID                       func() (catalogmodel.ID, error)
	IsDuplicate                 DuplicateError
	ValidatePostRecordingScript func(string) error
	OnChanged                   func()
}

type preparedRule struct {
	rule              autoreservation.Rule
	matcher           autoreservation.ProgramMatcher
	skip              bool
	unavailable       bool
	forcedTuner       bool
	oneSeg            *recording.OutputSettings
	oneSegUnavailable bool
	post              recording.PostRecordingSettings
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
		prepared[index] = prepareRule(rule, evaluator.ValidatePostRecordingScript)
		if prepared[index].unavailable {
			result.UnavailableRules++
			if prepared[index].forcedTuner {
				result.ForcedTunerUnavailableRules++
			}
			if prepared[index].oneSegUnavailable {
				result.OneSegUnavailableRules++
			}
		}
	}
	recordedTitles, err := loadRecordedTitleIndex(ctx, evaluator.Store, prepared)
	if err != nil {
		return result, err
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
			if candidate.skip || candidate.unavailable || !candidate.matcher.Matches(program) {
				continue
			}
			request, err := evaluator.Catalog.ReservationRequestForProgram(program, candidate.rule.Recording.Priority,
				candidate.rule.Recording.Follow)
			if err != nil {
				continue
			}
			settings := candidate.rule.Recording
			output, supported := automaticOutputSettings(settings)
			if !supported {
				continue
			}
			request.Disabled = settings.Mode == 5
			request.Output = output
			request.OneSegOutput = candidate.oneSeg
			request.PostRecording = candidate.post
			request.Components, supported = automaticComponentMode(settings.ServiceMode)
			if !supported {
				continue
			}
			if settings.StartMargin != nil {
				request.Margins = &recording.RecordingMargins{
					Start: time.Duration(*settings.StartMargin) * time.Second,
					End:   time.Duration(*settings.EndMargin) * time.Second,
				}
			}
			if request.Validate() != nil || !matchService(candidate.rule.Search.Services, request) {
				continue
			}
			recordedTitleMatch := recordedTitles.matches(candidate.rule.Search, request, program)
			if recordedTitleMatch {
				request.Disabled = true
				result.RecordedTitleMatches++
			}
			result.Matched++
			if result.Created == autoreservation.MaxReservationsPerRun {
				result.LimitReached = true
				return nil
			}
			snapshot, oneSeg, oneSegUnresolved, err := evaluator.resolveProgram(ctx, request)
			if err != nil {
				if ctx.Err() != nil {
					return errors.New("autoreservation: context ended")
				}
				if oneSegUnresolved {
					result.OneSegUnresolvedPrograms++
				}
				continue
			}
			id, err := evaluator.NewID()
			if err != nil {
				return errors.New("autoreservation: id generation failed")
			}
			_, err = evaluator.Store.CreateAutomaticReservation(ctx, candidate.rule.Number, recording.Reservation{
				ID: id, Version: 1, State: recording.ReservationActive, Program: snapshot,
				Priority: request.Priority, RequestedFollow: request.RequestedFollow,
				Disabled: request.Disabled, Margins: request.Margins, Output: request.Output, Components: request.Components,
				OneSegOutput:  oneSeg,
				PostRecording: request.PostRecording,
				CreatedAt:     now, UpdatedAt: now,
			})
			if err != nil {
				if evaluator.IsDuplicate(err) {
					result.Duplicates++
					if recordedTitleMatch {
						changed, disableErr := evaluator.Store.DisableAutomaticReservation(
							ctx, snapshot.ProgramInstanceID, now,
						)
						if disableErr != nil {
							return errors.New("autoreservation: disable reservation failed")
						}
						if changed && evaluator.OnChanged != nil {
							evaluator.OnChanged()
						}
					}
					return nil
				}
				return errors.New("autoreservation: create reservation failed")
			}
			result.Created++
			if evaluator.OnChanged != nil {
				evaluator.OnChanged()
			}
			return nil
		}
		return nil
	})
	return result, err
}

func (evaluator Evaluator) resolveProgram(ctx context.Context, request recording.ReservationRequest) (
	recording.ProgramSnapshot, *recording.OneSegOutput, bool, error,
) {
	snapshot, err := evaluator.Catalog.FindProgram(ctx, request)
	if err != nil || request.OneSegOutput == nil {
		return snapshot, nil, false, err
	}
	locator, err := evaluator.Catalog.ResolveOneSeg(ctx, snapshot)
	if err != nil {
		return recording.ProgramSnapshot{}, nil, true, err
	}
	oneSeg, err := recording.ResolveOneSegOutput(request, locator)
	if err != nil {
		return recording.ProgramSnapshot{}, nil, true, err
	}
	return snapshot, oneSeg, false, nil
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

func prepareRule(rule autoreservation.Rule, validateScript func(string) error) preparedRule {
	prepared := preparedRule{rule: rule}
	search, settings := rule.Search, rule.Recording
	if !search.Enabled {
		prepared.skip = true
		return prepared
	}
	if settings.TunerID != 0 {
		prepared.unavailable = true
		prepared.forcedTuner = true
		return prepared
	}
	if (settings.Mode != 1 && settings.Mode != 5) || settings.Exact || settings.Continue {
		prepared.unavailable = true
		return prepared
	}
	oneSeg, supported := automaticOneSegOutputSettings(settings)
	if !supported {
		prepared.unavailable = true
		prepared.oneSegUnavailable = true
		return prepared
	}
	prepared.oneSeg = oneSeg
	postMode, ok := automaticPostRecordingMode(settings.Suspend, settings.Reboot)
	if !ok {
		prepared.unavailable = true
		return prepared
	}
	prepared.post = recording.PostRecordingSettings{Mode: postMode, Script: settings.Batch}
	if prepared.post.Validate() != nil || prepared.post.Script != "" && (validateScript == nil || validateScript(prepared.post.Script) != nil) {
		prepared.unavailable = true
		return prepared
	}
	if _, ok := automaticOutputSettings(settings); !ok {
		prepared.unavailable = true
		return prepared
	}
	if _, ok := automaticComponentMode(settings.ServiceMode); !ok {
		prepared.unavailable = true
		return prepared
	}
	if settings.StartMargin != nil && (*settings.StartMargin < -3600 || *settings.StartMargin > 3600 ||
		*settings.EndMargin < -3600 || *settings.EndMargin > 3600) {
		prepared.unavailable = true
		return prepared
	}
	matcher, err := autoreservation.PrepareProgramMatcher(search)
	if err != nil {
		prepared.unavailable = true
		return prepared
	}
	prepared.matcher = matcher
	return prepared
}

// automaticPostRecordingModeは自動予約のCtrlCmd値を通常予約と同じ選択へ変換する。
func automaticPostRecordingMode(suspend uint8, reboot bool) (recording.PostRecordingMode, bool) {
	switch {
	case suspend == 0 && !reboot:
		return recording.PostRecordingDefault, true
	case suspend == 4 && !reboot:
		return recording.PostRecordingNothing, true
	case suspend == 1 && !reboot:
		return recording.PostRecordingStandby, true
	case suspend == 1 && reboot:
		return recording.PostRecordingStandbyReboot, true
	case suspend == 2 && !reboot:
		return recording.PostRecordingSuspend, true
	case suspend == 2 && reboot:
		return recording.PostRecordingSuspendReboot, true
	case suspend == 3 && !reboot:
		return recording.PostRecordingShutdown, true
	default:
		return recording.PostRecordingDefault, false
	}
}

// automaticComponentModeは自動予約に保存したCtrlCmd値を通常予約と同じ選択へ変換する。
func automaticComponentMode(value uint32) (recording.ComponentMode, bool) {
	if value&^uint32(0x31) != 0 {
		return recording.ComponentDefault, false
	}
	if value&0x01 == 0 {
		return recording.ComponentDefault, true
	}
	return recording.ExplicitComponentMode(value&0x10 != 0, value&0x20 != 0), true
}

func automaticOutputSettings(settings autoreservation.RecordingSettings) (recording.OutputSettings, bool) {
	if len(settings.Folders) == 0 {
		return recording.OutputSettings{}, true
	}
	if len(settings.Folders) != 1 {
		return recording.OutputSettings{}, false
	}
	folder := settings.Folders[0]
	if folder.Writer != "Write_Default.dll" {
		return recording.OutputSettings{}, false
	}
	const plugin = "RecName_Macro.dll"
	template := ""
	switch {
	case folder.Name == plugin:
	case strings.HasPrefix(folder.Name, plugin+"?") && len(folder.Name) > len(plugin)+1:
		template = folder.Name[len(plugin)+1:]
	default:
		return recording.OutputSettings{}, false
	}
	output := recording.OutputSettings{Folder: folder.Path, Template: template}
	return output, output.Validate() == nil
}

// automaticOneSegOutputSettingsは保存済みのワンセグ設定を、単発予約と同じ実行可能範囲へ変換する。
func automaticOneSegOutputSettings(settings autoreservation.RecordingSettings) (*recording.OutputSettings, bool) {
	switch settings.PartialMode {
	case 0:
		return nil, len(settings.PartialFolders) == 0
	case 1:
	default:
		return nil, false
	}
	if len(settings.PartialFolders) == 0 {
		return &recording.OutputSettings{}, true
	}
	if len(settings.PartialFolders) != 1 {
		return nil, false
	}
	folder := settings.PartialFolders[0]
	if folder.Writer != "" && folder.Writer != "Write_Default.dll" {
		return nil, false
	}
	const plugin = "RecName_Macro.dll"
	template := ""
	switch {
	case folder.Name == "" || folder.Name == plugin:
	case strings.HasPrefix(folder.Name, plugin+"?") && len(folder.Name) > len(plugin)+1:
		template = folder.Name[len(plugin)+1:]
	default:
		return nil, false
	}
	output := recording.OutputSettings{Folder: folder.Path, Template: template}
	if output.Validate() != nil {
		return nil, false
	}
	return &output, true
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
