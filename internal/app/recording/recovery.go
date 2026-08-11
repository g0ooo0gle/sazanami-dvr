package recording

import (
	"context"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

// RecoveryStoreは再起動時に照合する録画処理を100件ずつ読み、復旧結果を保存するインターフェースである。
type RecoveryStore interface {
	RecoveryAttempts(context.Context, int, catalogmodel.ID) ([]core.RecoveryItem, error)
	MarkFinalPublished(context.Context, catalogmodel.ID, time.Time) error
	MarkDirectorySynced(context.Context, catalogmodel.ID, time.Time) error
	MarkOneSegFinalPublished(context.Context, catalogmodel.ID, time.Time) error
	MarkOneSegDirectorySynced(context.Context, catalogmodel.ID, time.Time) error
	SetOneSegOutcome(context.Context, catalogmodel.ID, core.OneSegResult, time.Time) error
	FinishAttempt(context.Context, core.FinishRequest) error
	SetRecordingAvailability(context.Context, catalogmodel.ID, core.Availability, core.TerminalReason, time.Time) error
	SetOneSegAvailability(context.Context, catalogmodel.ID, core.Availability, core.TerminalReason, time.Time) error
}

// RecoveryFilesはDBに記録したパスの照合と、安全を確認できた完成処理の再開に必要な操作である。
type RecoveryFiles struct {
	FileOperations
	Inspect func(core.FilePlan) (core.FileObservation, error)
}

// Recoveryはプロセス開始前にDBと録画ファイルを照合する。
// 録画保存先全体の走査、既存部分ファイルへの追記、未知ファイルの削除は行わない。
type Recovery struct {
	Store RecoveryStore
	Files RecoveryFiles
	Clock TimeSource
}

// Runはメイン、任意のワンセグの順で状態を照合し、最後に録画処理を確定する。
// 同じ入力へ再実行しても、新しい部分ファイルやストリームは作らない。
func (recovery Recovery) Run(ctx context.Context) error {
	if ctx == nil || recovery.Store == nil || recovery.Clock == nil || !recovery.Files.valid() || recovery.Files.Inspect == nil {
		return errors.New("recording: invalid recovery")
	}
	var after catalogmodel.ID
	for {
		items, err := recovery.Store.RecoveryAttempts(ctx, core.MaxRecoveryPage, after)
		if err != nil {
			return errors.New("recording: read recovery page")
		}
		for _, item := range items {
			mainObservation, err := recovery.Files.Inspect(item.Plan)
			if err != nil {
				return errors.New("recording: inspect recovery file")
			}
			var oneSegObservation *core.FileObservation
			if item.OneSeg != nil {
				observation, inspectErr := recovery.Files.Inspect(item.OneSeg.Plan)
				if inspectErr != nil {
					return errors.New("recording: inspect one-seg recovery file")
				}
				oneSegObservation = &observation
			}
			switch item.State {
			case core.AttemptSucceeded, core.AttemptPartial:
				err = recovery.reconcileSuccess(ctx, item, mainObservation, oneSegObservation)
			case core.AttemptFinalizing:
				err = recovery.recoverFinalizing(ctx, item, mainObservation, oneSegObservation)
			default:
				err = recovery.finishInterrupted(ctx, item, mainObservation, oneSegObservation)
			}
			if err != nil {
				return err
			}
		}
		if len(items) < core.MaxRecoveryPage {
			return nil
		}
		after = items[len(items)-1].ID
	}
}

func (recovery Recovery) finishInterrupted(ctx context.Context, item core.RecoveryItem,
	mainObservation core.FileObservation, oneSegObservation *core.FileObservation,
) error {
	finish := interruptedMainResult(item, mainObservation)
	finish.Now = recovery.now()
	if item.OneSeg != nil {
		finish.OneSeg = oneSegInterruptedResult(*item.OneSeg, *oneSegObservation, core.ReasonProcessInterrupted)
	}
	if err := recovery.Store.FinishAttempt(ctx, finish); err != nil {
		return errors.New("recording: finish interrupted attempt")
	}
	return nil
}

func interruptedMainResult(item core.RecoveryItem, observation core.FileObservation) core.FinishRequest {
	finish := core.FinishRequest{
		AttemptID: item.ID, State: core.AttemptFailed, Reason: core.ReasonProcessInterrupted,
		Availability: core.AvailabilityMissing, Recovered: true,
	}
	switch {
	case observation.Unsafe || invalidFact(observation.Partial) || invalidFact(observation.Final) || observation.Final.Exists:
		finish.Reason = core.ReasonFileIntegrityMismatch
		finish.ByteCount = item.ByteCount
		finish.Availability = core.AvailabilityMismatched
	case observation.Partial.Exists:
		finish.ByteCount = observation.Partial.Size
		finish.Availability = core.AvailabilityPartial
		if finish.ByteCount >= minimumUsefulTS {
			finish.State = core.AttemptPartial
		}
	}
	return finish
}

func oneSegInterruptedResult(segment core.RecoverySegment, observation core.FileObservation,
	reason core.TerminalReason,
) *core.OneSegResult {
	result := core.OneSegResult{Availability: core.AvailabilityMissing, Reason: reason}
	switch {
	case observation.Unsafe || invalidFact(observation.Partial) || invalidFact(observation.Final) || observation.Final.Exists:
		result.ByteCount = segment.ByteCount
		result.Availability = core.AvailabilityMismatched
		result.Reason = core.ReasonFileIntegrityMismatch
	case observation.Partial.Exists:
		result.ByteCount = observation.Partial.Size
		result.Availability = core.AvailabilityPartial
	case segment.Availability == core.AvailabilityMismatched:
		result.Availability = core.AvailabilityMismatched
		result.Reason = core.ReasonFileIntegrityMismatch
	}
	return &result
}

func (recovery Recovery) recoverFinalizing(ctx context.Context, item core.RecoveryItem,
	mainObservation core.FileObservation, oneSegObservation *core.FileObservation,
) error {
	failure, err := recovery.recoverMainFinalization(ctx, item, mainObservation)
	if err != nil {
		return err
	}
	if failure != nil {
		failure.Now = recovery.now()
		if item.OneSeg != nil {
			failure.OneSeg = oneSegInterruptedResult(*item.OneSeg, *oneSegObservation, failure.Reason)
		}
		if err := recovery.Store.FinishAttempt(ctx, *failure); err != nil {
			return errors.New("recording: finish invalid finalization")
		}
		return nil
	}
	if item.OneSeg != nil {
		if err := recovery.recoverOneSegFinalization(ctx, item.ID, *item.OneSeg, *oneSegObservation); err != nil {
			return err
		}
	}
	finish := core.FinishRequest{
		AttemptID: item.ID, State: item.PlannedState, Reason: item.PlannedReason,
		ByteCount: item.ByteCount, Availability: core.AvailabilityFinal, Recovered: true, Now: recovery.now(),
	}
	if err := recovery.Store.FinishAttempt(ctx, finish); err != nil {
		return errors.New("recording: finish recovered publication")
	}
	return nil
}

// recoverMainFinalizationはメインだけを完成状態へ進める。nilはメインの確定完了を表す。
func (recovery Recovery) recoverMainFinalization(ctx context.Context, item core.RecoveryItem,
	observation core.FileObservation,
) (*core.FinishRequest, error) {
	if item.FinalizationToken == (catalogmodel.ID{}) || item.ByteCount < minimumUsefulTS || !item.FileSynced {
		return finalizationFailure(item, core.ReasonFileIntegrityMismatch, core.AvailabilityMismatched), nil
	}
	if observation.Unsafe || invalidFact(observation.Partial) || invalidFact(observation.Final) {
		return finalizationFailure(item, core.ReasonFileIntegrityMismatch, core.AvailabilityMismatched), nil
	}
	partialMatches := observation.Partial.Exists && observation.Partial.Size == item.ByteCount
	finalMatches := observation.Final.Exists && observation.Final.Size == item.ByteCount
	if (observation.Partial.Exists && !partialMatches) || (observation.Final.Exists && !finalMatches) {
		return finalizationFailure(item, core.ReasonFileIntegrityMismatch, core.AvailabilityMismatched), nil
	}
	switch {
	case partialMatches && !observation.Final.Exists && !item.FinalPublished && !item.DirectorySynced:
		if err := recovery.Files.LinkFinal(item.Plan); err != nil {
			return nil, errors.New("recording: resume final publication")
		}
		if err := recovery.Store.MarkFinalPublished(ctx, item.ID, recovery.now()); err != nil {
			return nil, errors.New("recording: record recovered publication")
		}
		return nil, recovery.completeMainPublication(ctx, item, true)
	case partialMatches && finalMatches && observation.SameFile && !item.DirectorySynced:
		if !item.FinalPublished {
			if err := recovery.Store.MarkFinalPublished(ctx, item.ID, recovery.now()); err != nil {
				return nil, errors.New("recording: record observed publication")
			}
		}
		return nil, recovery.completeMainPublication(ctx, item, true)
	case !observation.Partial.Exists && finalMatches && item.FinalPublished:
		return nil, recovery.completeMainPublication(ctx, item, false)
	case !observation.Partial.Exists && !observation.Final.Exists:
		return finalizationFailure(item, core.ReasonFileMissing, core.AvailabilityMissing), nil
	default:
		return finalizationFailure(item, core.ReasonFileIntegrityMismatch, core.AvailabilityMismatched), nil
	}
}

func (recovery Recovery) completeMainPublication(ctx context.Context, item core.RecoveryItem,
	removePartial bool,
) error {
	if err := recovery.Files.SyncDirectory(item.Plan); err != nil {
		return errors.New("recording: sync recovered publication")
	}
	if removePartial {
		if err := recovery.Files.RemovePartial(item.Plan); err != nil {
			return errors.New("recording: remove recovered partial name")
		}
		if err := recovery.Files.SyncDirectory(item.Plan); err != nil {
			return errors.New("recording: sync recovered partial removal")
		}
	}
	if !item.DirectorySynced {
		if err := recovery.Store.MarkDirectorySynced(ctx, item.ID, recovery.now()); err != nil {
			return errors.New("recording: record recovered directory sync")
		}
	}
	return nil
}

func finalizationFailure(item core.RecoveryItem, reason core.TerminalReason,
	availability core.Availability,
) *core.FinishRequest {
	return &core.FinishRequest{
		AttemptID: item.ID, State: core.AttemptFailed, Reason: reason, ByteCount: item.ByteCount,
		Availability: availability, Recovered: true,
	}
}

func (recovery Recovery) recoverOneSegFinalization(ctx context.Context, attemptID catalogmodel.ID,
	segment core.RecoverySegment, observation core.FileObservation,
) error {
	if segment.State == core.SegmentFinalized && segment.Availability == core.AvailabilityFinal {
		return recovery.recoverFinalizedOneSeg(ctx, attemptID, segment, observation)
	}
	if segment.IntegrityReason.Valid() {
		return recovery.reconcileSettledOneSeg(ctx, attemptID, segment, observation, false)
	}
	if segment.State != core.SegmentPartial || segment.Availability != core.AvailabilityPartial ||
		!segment.FileSynced || segment.ByteCount < minimumUsefulTS {
		return recovery.saveOneSegOutcome(ctx, attemptID,
			*oneSegInterruptedResult(segment, observation, core.ReasonProcessInterrupted))
	}
	if observation.Unsafe || invalidFact(observation.Partial) || invalidFact(observation.Final) {
		return recovery.saveOneSegOutcome(ctx, attemptID, oneSegMismatch(segment.ByteCount))
	}
	partialMatches := observation.Partial.Exists && observation.Partial.Size == segment.ByteCount
	finalMatches := observation.Final.Exists && observation.Final.Size == segment.ByteCount
	if (observation.Partial.Exists && !partialMatches) || (observation.Final.Exists && !finalMatches) {
		return recovery.saveOneSegOutcome(ctx, attemptID, oneSegMismatch(segment.ByteCount))
	}
	switch {
	case partialMatches && !observation.Final.Exists && !segment.FinalPublished && !segment.DirectorySynced:
		if err := recovery.Files.LinkFinal(segment.Plan); err != nil {
			return recovery.saveOneSegPublicationFailure(ctx, attemptID, segment, err)
		}
		if err := recovery.Store.MarkOneSegFinalPublished(ctx, attemptID, recovery.now()); err != nil {
			return errors.New("recording: record recovered one-seg publication")
		}
		return recovery.completeOneSegPublication(ctx, attemptID, segment, true)
	case partialMatches && finalMatches && observation.SameFile && !segment.DirectorySynced:
		if !segment.FinalPublished {
			if err := recovery.Store.MarkOneSegFinalPublished(ctx, attemptID, recovery.now()); err != nil {
				return errors.New("recording: record observed one-seg publication")
			}
		}
		return recovery.completeOneSegPublication(ctx, attemptID, segment, true)
	case !observation.Partial.Exists && finalMatches && segment.FinalPublished:
		return recovery.completeOneSegPublication(ctx, attemptID, segment, false)
	case !observation.Partial.Exists && !observation.Final.Exists:
		return recovery.saveOneSegOutcome(ctx, attemptID, core.OneSegResult{
			Availability: core.AvailabilityMissing, Reason: core.ReasonFileMissing,
		})
	default:
		return recovery.saveOneSegOutcome(ctx, attemptID, oneSegMismatch(segment.ByteCount))
	}
}

func (recovery Recovery) recoverFinalizedOneSeg(ctx context.Context, attemptID catalogmodel.ID,
	segment core.RecoverySegment, observation core.FileObservation,
) error {
	if observation.Unsafe || invalidFact(observation.Partial) || invalidFact(observation.Final) ||
		observation.Final.Exists && observation.Final.Size != segment.ByteCount {
		return recovery.saveOneSegOutcome(ctx, attemptID, oneSegMismatch(segment.ByteCount))
	}
	if !observation.Final.Exists {
		return recovery.saveOneSegOutcome(ctx, attemptID, core.OneSegResult{
			Availability: core.AvailabilityMissing, Reason: core.ReasonFileMissing,
		})
	}
	if observation.Partial.Exists {
		if observation.Partial.Size != segment.ByteCount || !observation.SameFile {
			return recovery.saveOneSegOutcome(ctx, attemptID, oneSegMismatch(segment.ByteCount))
		}
		if err := recovery.Files.SyncDirectory(segment.Plan); err != nil {
			return recovery.saveOneSegOutcome(ctx, attemptID, core.OneSegResult{
				ByteCount: segment.ByteCount, Availability: core.AvailabilityPartial,
				Reason: core.ReasonFileSyncFailed, FileSynced: segment.FileSynced,
			})
		}
		if err := recovery.Files.RemovePartial(segment.Plan); err != nil {
			return recovery.saveOneSegOutcome(ctx, attemptID, core.OneSegResult{
				ByteCount: segment.ByteCount, Availability: core.AvailabilityPartial,
				Reason: core.ReasonFinalPublicationFailed, FileSynced: segment.FileSynced,
			})
		}
		if err := recovery.Files.SyncDirectory(segment.Plan); err != nil {
			return recovery.saveOneSegOutcome(ctx, attemptID, core.OneSegResult{
				ByteCount: segment.ByteCount, Availability: core.AvailabilityPartial,
				Reason: core.ReasonFileSyncFailed, FileSynced: segment.FileSynced,
			})
		}
	}
	return nil
}

func (recovery Recovery) completeOneSegPublication(ctx context.Context, attemptID catalogmodel.ID,
	segment core.RecoverySegment, removePartial bool,
) error {
	if err := recovery.Files.SyncDirectory(segment.Plan); err != nil {
		return recovery.saveOneSegOutcome(ctx, attemptID, core.OneSegResult{
			ByteCount: segment.ByteCount, Availability: core.AvailabilityPartial,
			Reason: core.ReasonFileSyncFailed, FileSynced: segment.FileSynced,
		})
	}
	if removePartial {
		if err := recovery.Files.RemovePartial(segment.Plan); err != nil {
			return recovery.saveOneSegOutcome(ctx, attemptID, core.OneSegResult{
				ByteCount: segment.ByteCount, Availability: core.AvailabilityPartial,
				Reason: core.ReasonFinalPublicationFailed, FileSynced: segment.FileSynced,
			})
		}
		if err := recovery.Files.SyncDirectory(segment.Plan); err != nil {
			return recovery.saveOneSegOutcome(ctx, attemptID, core.OneSegResult{
				ByteCount: segment.ByteCount, Availability: core.AvailabilityPartial,
				Reason: core.ReasonFileSyncFailed, FileSynced: segment.FileSynced,
			})
		}
	}
	if !segment.DirectorySynced {
		if err := recovery.Store.MarkOneSegDirectorySynced(ctx, attemptID, recovery.now()); err != nil {
			return errors.New("recording: record recovered one-seg directory sync")
		}
	}
	return nil
}

func (recovery Recovery) saveOneSegPublicationFailure(ctx context.Context, attemptID catalogmodel.ID,
	segment core.RecoverySegment, publicationErr error,
) error {
	result := core.OneSegResult{
		ByteCount: segment.ByteCount, Availability: core.AvailabilityPartial,
		Reason: core.ReasonFinalPublicationFailed, FileSynced: segment.FileSynced,
	}
	if errors.Is(publicationErr, core.ErrFinalExists) {
		result.Availability = core.AvailabilityMismatched
		result.Reason = core.ReasonFinalNameConflict
	}
	return recovery.saveOneSegOutcome(ctx, attemptID, result)
}

func (recovery Recovery) saveOneSegOutcome(ctx context.Context, attemptID catalogmodel.ID,
	result core.OneSegResult,
) error {
	if err := recovery.Store.SetOneSegOutcome(ctx, attemptID, result, recovery.now()); err != nil {
		return errors.New("recording: save recovered one-seg outcome")
	}
	return nil
}

func oneSegMismatch(byteCount int64) core.OneSegResult {
	return core.OneSegResult{
		ByteCount: byteCount, Availability: core.AvailabilityMismatched,
		Reason: core.ReasonFileIntegrityMismatch,
	}
}

func (recovery Recovery) reconcileSuccess(ctx context.Context, item core.RecoveryItem,
	mainObservation core.FileObservation, oneSegObservation *core.FileObservation,
) error {
	availability, reason := completedAvailability(item.ByteCount, mainObservation)
	if item.Availability != availability || item.IntegrityReason != reason {
		if err := recovery.Store.SetRecordingAvailability(ctx, item.ID, availability, reason, recovery.now()); err != nil {
			return errors.New("recording: update completed file availability")
		}
	}
	if item.OneSeg != nil {
		if err := recovery.reconcileSettledOneSeg(ctx, item.ID, *item.OneSeg, *oneSegObservation, true); err != nil {
			return err
		}
	}
	return nil
}

func (recovery Recovery) reconcileSettledOneSeg(ctx context.Context, attemptID catalogmodel.ID,
	segment core.RecoverySegment, observation core.FileObservation, terminal bool,
) error {
	availability, reason := settledOneSegAvailability(segment, observation)
	if segment.Availability == availability && segment.IntegrityReason == reason {
		return nil
	}
	if terminal {
		if err := recovery.Store.SetOneSegAvailability(ctx, attemptID, availability, reason, recovery.now()); err != nil {
			return errors.New("recording: update completed one-seg availability")
		}
		return nil
	}
	result := core.OneSegResult{
		ByteCount: segment.ByteCount, Availability: availability, Reason: reason,
		FileSynced: segment.FileSynced,
	}
	if availability == core.AvailabilityMissing {
		result.ByteCount = 0
	}
	return recovery.saveOneSegOutcome(ctx, attemptID, result)
}

func completedAvailability(byteCount int64, observation core.FileObservation) (core.Availability, core.TerminalReason) {
	switch {
	case !observation.Final.Exists:
		return core.AvailabilityMissing, core.ReasonFileMissing
	case observation.Unsafe || invalidFact(observation.Final) || observation.Final.Size != byteCount || observation.Partial.Exists:
		return core.AvailabilityMismatched, core.ReasonFileIntegrityMismatch
	default:
		return core.AvailabilityFinal, ""
	}
}

func settledOneSegAvailability(segment core.RecoverySegment,
	observation core.FileObservation,
) (core.Availability, core.TerminalReason) {
	if segment.State == core.SegmentFinalized || segment.Availability == core.AvailabilityFinal {
		return completedAvailability(segment.ByteCount, observation)
	}
	if segment.Availability == core.AvailabilityMismatched {
		return core.AvailabilityMismatched, core.ReasonFileIntegrityMismatch
	}
	if observation.Unsafe || invalidFact(observation.Partial) || invalidFact(observation.Final) ||
		observation.Final.Exists || observation.Partial.Exists && observation.Partial.Size != segment.ByteCount {
		return core.AvailabilityMismatched, core.ReasonFileIntegrityMismatch
	}
	if observation.Partial.Exists {
		return core.AvailabilityPartial, segment.IntegrityReason
	}
	if segment.Availability == core.AvailabilityMissing && segment.IntegrityReason.Valid() {
		return core.AvailabilityMissing, segment.IntegrityReason
	}
	return core.AvailabilityMissing, core.ReasonFileMissing
}

func (recovery Recovery) now() time.Time { return recovery.Clock.Now().UTC() }

func invalidFact(fact core.FileFact) bool { return fact.Exists && (!fact.Regular || fact.Size < 0) }
