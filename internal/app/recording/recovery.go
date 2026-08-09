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
	FinishAttempt(context.Context, core.FinishRequest) error
	SetRecordingAvailability(context.Context, catalogmodel.ID, core.Availability, core.TerminalReason, time.Time) error
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

// Runは100件ずつ録画状態を照合し、同じ入力への再実行で新しいファイルやストリームを作らない。
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
			observation, err := recovery.Files.Inspect(item.Plan)
			if err != nil {
				return errors.New("recording: inspect recovery file")
			}
			switch item.State {
			case core.AttemptSucceeded, core.AttemptPartial:
				err = recovery.reconcileSuccess(ctx, item, observation)
			case core.AttemptFinalizing:
				err = recovery.recoverFinalizing(ctx, item, observation)
			default:
				err = recovery.finishInterrupted(ctx, item, observation)
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

func (recovery Recovery) finishInterrupted(ctx context.Context, item core.RecoveryItem, observation core.FileObservation) error {
	finish := core.FinishRequest{
		AttemptID: item.ID, State: core.AttemptFailed, Reason: core.ReasonProcessInterrupted,
		Availability: core.AvailabilityMissing, Recovered: true, Now: recovery.now(),
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
	if err := recovery.Store.FinishAttempt(ctx, finish); err != nil {
		return errors.New("recording: finish interrupted attempt")
	}
	return nil
}

func (recovery Recovery) recoverFinalizing(ctx context.Context, item core.RecoveryItem, observation core.FileObservation) error {
	if item.FinalizationToken == (catalogmodel.ID{}) || item.ByteCount < minimumUsefulTS || !item.FileSynced {
		return recovery.finishFinalizationFailure(ctx, item, core.ReasonFileIntegrityMismatch, core.AvailabilityMismatched)
	}
	if observation.Unsafe || invalidFact(observation.Partial) || invalidFact(observation.Final) {
		return recovery.finishFinalizationFailure(ctx, item, core.ReasonFileIntegrityMismatch, core.AvailabilityMismatched)
	}
	partialMatches := observation.Partial.Exists && observation.Partial.Size == item.ByteCount
	finalMatches := observation.Final.Exists && observation.Final.Size == item.ByteCount
	if (observation.Partial.Exists && !partialMatches) || (observation.Final.Exists && !finalMatches) {
		return recovery.finishFinalizationFailure(ctx, item, core.ReasonFileIntegrityMismatch, core.AvailabilityMismatched)
	}
	switch {
	case partialMatches && !observation.Final.Exists && !item.FinalPublished && !item.DirectorySynced:
		if err := recovery.Files.LinkFinal(item.Plan); err != nil {
			return errors.New("recording: resume final publication")
		}
		if err := recovery.Store.MarkFinalPublished(ctx, item.ID, recovery.now()); err != nil {
			return errors.New("recording: record recovered publication")
		}
		item.FinalPublished = true
		return recovery.finishPublication(ctx, item, true)
	case partialMatches && finalMatches && observation.SameFile && !item.DirectorySynced:
		if !item.FinalPublished {
			if err := recovery.Store.MarkFinalPublished(ctx, item.ID, recovery.now()); err != nil {
				return errors.New("recording: record observed publication")
			}
			item.FinalPublished = true
		}
		return recovery.finishPublication(ctx, item, true)
	case !observation.Partial.Exists && finalMatches && item.FinalPublished:
		return recovery.finishPublication(ctx, item, false)
	case !observation.Partial.Exists && !observation.Final.Exists:
		return recovery.finishFinalizationFailure(ctx, item, core.ReasonFileMissing, core.AvailabilityMissing)
	default:
		return recovery.finishFinalizationFailure(ctx, item, core.ReasonFileIntegrityMismatch, core.AvailabilityMismatched)
	}
}

func (recovery Recovery) finishPublication(ctx context.Context, item core.RecoveryItem, removePartial bool) error {
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
	finish := core.FinishRequest{
		AttemptID: item.ID, State: item.PlannedState, Reason: item.PlannedReason,
		ByteCount: item.ByteCount, Availability: core.AvailabilityFinal, Recovered: true, Now: recovery.now(),
	}
	if err := recovery.Store.FinishAttempt(ctx, finish); err != nil {
		return errors.New("recording: finish recovered publication")
	}
	return nil
}

func (recovery Recovery) finishFinalizationFailure(ctx context.Context, item core.RecoveryItem, reason core.TerminalReason, availability core.Availability) error {
	finish := core.FinishRequest{
		AttemptID: item.ID, State: core.AttemptFailed, Reason: reason, ByteCount: item.ByteCount,
		Availability: availability, Recovered: true, Now: recovery.now(),
	}
	if err := recovery.Store.FinishAttempt(ctx, finish); err != nil {
		return errors.New("recording: finish invalid finalization")
	}
	return nil
}

func (recovery Recovery) reconcileSuccess(ctx context.Context, item core.RecoveryItem, observation core.FileObservation) error {
	availability := core.AvailabilityFinal
	var reason core.TerminalReason
	switch {
	case !observation.Final.Exists:
		availability = core.AvailabilityMissing
		reason = core.ReasonFileMissing
	case observation.Unsafe || invalidFact(observation.Final) || observation.Final.Size != item.ByteCount || observation.Partial.Exists:
		availability = core.AvailabilityMismatched
		reason = core.ReasonFileIntegrityMismatch
	}
	if item.Availability == availability {
		return nil
	}
	if err := recovery.Store.SetRecordingAvailability(ctx, item.ID, availability, reason, recovery.now()); err != nil {
		return errors.New("recording: update completed file availability")
	}
	return nil
}

func (recovery Recovery) now() time.Time { return recovery.Clock.Now().UTC() }

func invalidFact(fact core.FileFact) bool { return fact.Exists && (!fact.Regular || fact.Size < 0) }
