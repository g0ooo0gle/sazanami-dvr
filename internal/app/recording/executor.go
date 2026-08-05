package recording

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	progressInterval = 5 * time.Second
	minimumUsefulTS  = 188
)

// AttemptStoreは一回の録画処理を、短いDB更新で順方向へ進めるインターフェースである。
type AttemptStore interface {
	ClaimRecording(context.Context, recording.ClaimRequest) (recording.Attempt, error)
	StartAttempt(context.Context, catalogmodel.ID, time.Time) error
	RecordingStarted(context.Context, catalogmodel.ID, time.Time) error
	UpdateRecordingProgress(context.Context, catalogmodel.ID, int64, time.Time) error
	BeginFinalization(context.Context, recording.FinalizeRequest) error
	MarkFinalPublished(context.Context, catalogmodel.ID, time.Time) error
	MarkDirectorySynced(context.Context, catalogmodel.ID, time.Time) error
	FinishAttempt(context.Context, recording.FinishRequest) error
}

// PartialFileは録画ストリームの書込み、同期、終了だけを公開する部分ファイルのインターフェースである。
type PartialFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// FileOperationsは録画保存先で行う、上書きのないファイル操作をまとめる。
// 関数フィールドにすることで、アダプターの具象型をアプリケーション層へ持ち込まずに接続する。
type FileOperations struct {
	CreatePartial func(recording.FilePlan) (PartialFile, error)
	LinkFinal     func(recording.FilePlan) error
	SyncDirectory func(recording.FilePlan) error
	RemovePartial func(recording.FilePlan) error
}

func (operations FileOperations) valid() bool {
	return operations.CreatePartial != nil && operations.LinkFinal != nil && operations.SyncDirectory != nil &&
		operations.RemovePartial != nil
}

// TimeSourceは録画処理の判定とDB時刻をテスト可能にする。
type TimeSource interface {
	Now() time.Time
}

// Resultは予定した一件の録画処理が到達した終了状態を返す。
type Result struct {
	State  recording.AttemptState
	Reason recording.TerminalReason
}

// Executorは一つの予約の実行権をDBで取得し、ストリームを一度だけ開いて録画ファイルへ保存する。
type Executor struct {
	Store        AttemptStore
	Stream       providerstream.Provider
	Files        FileOperations
	Clock        TimeSource
	NewID        func() (catalogmodel.ID, error)
	OwnerID      catalogmodel.ID
	Generation   int64
	WithDeadline func(context.Context, time.Time) (context.Context, context.CancelFunc)
}

// Missはストリームを開かず、実行できなかった予約を終了状態へ進める。
func (executor Executor) Miss(ctx context.Context, reservation recording.Reservation, reason recording.TerminalReason) (Result, error) {
	if reason != recording.ReasonLateStartExpired && reason != recording.ReasonRecordingSlotUnavailable {
		return Result{}, errors.New("recording: invalid missed reason")
	}
	attempt, err := executor.claim(ctx, reservation)
	if err != nil {
		return Result{}, err
	}
	finish := recording.FinishRequest{
		AttemptID: attempt.ID, State: recording.AttemptMissed, Reason: reason,
		Availability: recording.AvailabilityMissing, Now: executor.now(),
	}
	if err := executor.Store.FinishAttempt(ctx, finish); err != nil {
		return Result{}, errors.New("recording: finish missed attempt")
	}
	return Result{State: finish.State, Reason: finish.Reason}, nil
}

// Executeは一つの予約を予定終了まで録画し、正常終了時だけ完成ファイルを公開する。
func (executor Executor) Execute(ctx context.Context, reservation recording.Reservation) (Result, error) {
	if err := executor.validate(ctx, reservation); err != nil {
		return Result{}, err
	}
	attempt, err := executor.claim(ctx, reservation)
	if err != nil {
		return Result{}, err
	}
	if err := executor.Store.StartAttempt(ctx, attempt.ID, executor.now()); err != nil {
		return Result{}, errors.New("recording: persist starting attempt")
	}
	partial, err := executor.Files.CreatePartial(attempt.Plan)
	if err != nil {
		return executor.finishWithoutFile(ctx, attempt.ID, recording.ReasonFileCreateFailed)
	}
	target, err := provider.NewTuningTarget(reservation.Program.ProviderServiceLocator)
	if err != nil {
		return executor.finishOpenFailure(ctx, partial, attempt.ID, recording.ReasonStreamNotFound)
	}
	streamContext, cancel := executor.deadline(ctx, attempt.PlannedEnd)
	defer cancel()
	lease, err := executor.Stream.OpenStream(streamContext, providerstream.Request{
		Target: target, Usage: providerstream.UsageRecording, PriorityPolicy: "0", RequireDescrambled: true,
		CorrelationID: attempt.ID.String(),
	})
	if err != nil {
		return executor.finishOpenFailure(ctx, partial, attempt.ID, streamFailureReason(err))
	}
	defer lease.Close()
	if err := executor.Store.RecordingStarted(ctx, attempt.ID, executor.now()); err != nil {
		_ = lease.Cancel()
		_ = partial.Close()
		return Result{}, errors.New("recording: persist recording start")
	}
	byteCount, reason, reachedEnd, _ := executor.copy(streamContext, lease, partial, attempt)
	if !reachedEnd {
		if errors.Is(ctx.Err(), context.Canceled) {
			reason = recording.ReasonProcessShutdown
		}
		return executor.finishPartial(ctx, partial, attempt.ID, byteCount, reason)
	}
	_ = lease.Cancel()
	if err := partial.Sync(); err != nil {
		return executor.finishPartialAfterClose(ctx, partial, attempt.ID, byteCount, recording.ReasonFileSyncFailed)
	}
	if err := partial.Close(); err != nil {
		return executor.finishByCount(ctx, attempt.ID, byteCount, recording.ReasonFileSyncFailed, true)
	}
	if byteCount < minimumUsefulTS {
		return executor.finishByCount(ctx, attempt.ID, byteCount, recording.ReasonStreamEndedEarly, true)
	}
	token, err := executor.NewID()
	if err != nil {
		return Result{}, errors.New("recording: finalization token generation failed")
	}
	if err := executor.Store.BeginFinalization(ctx, recording.FinalizeRequest{
		AttemptID: attempt.ID, Token: token, ByteCount: byteCount, Now: executor.now(),
	}); err != nil {
		return Result{}, errors.New("recording: persist finalization start")
	}
	if err := executor.Files.LinkFinal(attempt.Plan); err != nil {
		if errors.Is(err, recording.ErrFinalExists) {
			return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFinalNameConflict}, err
		}
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFinalPublicationFailed}, err
	}
	if err := executor.Store.MarkFinalPublished(ctx, attempt.ID, executor.now()); err != nil {
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFinalDatabaseFailed}, err
	}
	if err := executor.Files.SyncDirectory(attempt.Plan); err != nil {
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFileSyncFailed}, err
	}
	if err := executor.Files.RemovePartial(attempt.Plan); err != nil {
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFinalPublicationFailed}, err
	}
	if err := executor.Files.SyncDirectory(attempt.Plan); err != nil {
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFileSyncFailed}, err
	}
	if err := executor.Store.MarkDirectorySynced(ctx, attempt.ID, executor.now()); err != nil {
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFinalDatabaseFailed}, err
	}
	finish := recording.FinishRequest{
		AttemptID: attempt.ID, State: recording.AttemptSucceeded, Reason: recording.ReasonCompleted,
		ByteCount: byteCount, Availability: recording.AvailabilityFinal, Now: executor.now(),
	}
	if err := executor.Store.FinishAttempt(ctx, finish); err != nil {
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFinalDatabaseFailed}, err
	}
	return Result{State: finish.State, Reason: finish.Reason}, nil
}

func (executor Executor) copy(ctx context.Context, lease providerstream.Lease, file PartialFile, attempt recording.Attempt) (int64, recording.TerminalReason, bool, error) {
	buffer := make([]byte, provider.MaxStreamChunk)
	var total int64
	lastProgress := executor.now()
	for {
		if !executor.now().Before(attempt.PlannedEnd) {
			return total, recording.ReasonCompleted, true, nil
		}
		read, terminal, err := lease.Read(ctx, buffer)
		if read < 0 || read > len(buffer) {
			return total, recording.ReasonStreamUnavailable, false, errors.New("recording: invalid stream read count")
		}
		if read > 0 {
			if total > math.MaxInt64-int64(read) {
				return total, recording.ReasonFileWriteFailed, false, errors.New("recording: byte count overflow")
			}
			written, writeErr := file.Write(buffer[:read])
			if written > 0 && written <= read {
				total += int64(written)
			}
			if writeErr != nil || written != read {
				return total, recording.ReasonFileWriteFailed, false, errors.New("recording: partial write failed")
			}
		}
		now := executor.now()
		if now.Sub(lastProgress) >= progressInterval {
			if err := executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attempt.ID, total, now); err != nil {
				return total, recording.ReasonProcessInterrupted, false, errors.New("recording: persist progress")
			}
			lastProgress = now
		}
		if !now.Before(attempt.PlannedEnd) {
			return total, recording.ReasonCompleted, true, nil
		}
		if err != nil || terminal.Done {
			return total, streamTerminalReason(err, terminal), false, err
		}
		if read == 0 {
			return total, recording.ReasonStreamUnavailable, false, errors.New("recording: stream made no progress")
		}
	}
}

func (executor Executor) finishOpenFailure(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, reason recording.TerminalReason) (Result, error) {
	if err := file.Sync(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	if err := file.Close(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	return executor.finishByCount(context.WithoutCancel(ctx), attemptID, 0, reason, true)
}

func (executor Executor) finishPartial(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason) (Result, error) {
	if err := executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attemptID, byteCount, executor.now()); err != nil {
		_ = file.Sync()
		_ = file.Close()
		return Result{}, errors.New("recording: persist final progress")
	}
	if err := file.Sync(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	return executor.finishPartialAfterClose(ctx, file, attemptID, byteCount, reason)
}

func (executor Executor) finishPartialAfterClose(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason) (Result, error) {
	if err := file.Close(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	return executor.finishByCount(context.WithoutCancel(ctx), attemptID, byteCount, reason, true)
}

func (executor Executor) finishByCount(ctx context.Context, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason, fileExists bool) (Result, error) {
	state := recording.AttemptFailed
	availability := recording.AvailabilityMissing
	if fileExists {
		availability = recording.AvailabilityPartial
	}
	if byteCount >= minimumUsefulTS {
		state = recording.AttemptPartial
	}
	if reason == recording.ReasonProcessShutdown {
		state = recording.AttemptCancelled
	}
	finish := recording.FinishRequest{
		AttemptID: attemptID, State: state, Reason: reason, ByteCount: byteCount,
		Availability: availability, Now: executor.now(),
	}
	if err := executor.Store.FinishAttempt(ctx, finish); err != nil {
		return Result{}, errors.New("recording: persist unsuccessful finish")
	}
	return Result{State: state, Reason: reason}, nil
}

func (executor Executor) finishWithoutFile(ctx context.Context, attemptID catalogmodel.ID, reason recording.TerminalReason) (Result, error) {
	return executor.finishByCount(context.WithoutCancel(ctx), attemptID, 0, reason, false)
}

func (executor Executor) claim(ctx context.Context, reservation recording.Reservation) (recording.Attempt, error) {
	if err := executor.validate(ctx, reservation); err != nil {
		return recording.Attempt{}, err
	}
	attemptID, err := executor.NewID()
	if err != nil {
		return recording.Attempt{}, errors.New("recording: attempt id generation failed")
	}
	segmentID, err := executor.NewID()
	if err != nil {
		return recording.Attempt{}, errors.New("recording: segment id generation failed")
	}
	plan, err := recording.NewFilePlan(reservation.Program.Start, attemptID)
	if err != nil {
		return recording.Attempt{}, err
	}
	attempt, err := executor.Store.ClaimRecording(ctx, recording.ClaimRequest{
		ReservationID: reservation.ID, AttemptID: attemptID, SegmentID: segmentID, OwnerID: executor.OwnerID,
		OwnerGeneration: executor.Generation, Now: executor.now(), Plan: plan,
	})
	if err != nil {
		return recording.Attempt{}, err
	}
	return attempt, nil
}

func (executor Executor) validate(ctx context.Context, reservation recording.Reservation) error {
	if ctx == nil || executor.Store == nil || executor.Stream == nil || !executor.Files.valid() || executor.Clock == nil ||
		executor.NewID == nil || executor.OwnerID == (catalogmodel.ID{}) || executor.Generation < 1 ||
		reservation.ID == (catalogmodel.ID{}) || reservation.State != recording.ReservationActive {
		return errors.New("recording: invalid executor")
	}
	now := executor.now()
	if now.IsZero() || now.Location() != time.UTC || now.UnixMilli() < 0 {
		return errors.New("recording: invalid executor clock")
	}
	return nil
}

func (executor Executor) now() time.Time {
	return executor.Clock.Now().UTC()
}

func (executor Executor) deadline(ctx context.Context, end time.Time) (context.Context, context.CancelFunc) {
	if executor.WithDeadline != nil {
		return executor.WithDeadline(ctx, end)
	}
	return context.WithDeadline(ctx, end)
}

func streamFailureReason(err error) recording.TerminalReason {
	switch {
	case provider.IsReason(err, provider.ReasonNotFound):
		return recording.ReasonStreamNotFound
	case provider.IsReason(err, provider.ReasonTimeout):
		return recording.ReasonStreamTimeout
	case provider.IsReason(err, provider.ReasonCancelled):
		return recording.ReasonStreamCancelled
	default:
		return recording.ReasonStreamUnavailable
	}
}

func streamTerminalReason(err error, terminal providerstream.Terminal) recording.TerminalReason {
	switch terminal.Reason {
	case providerstream.TerminalTimeout:
		return recording.ReasonStreamTimeout
	case providerstream.TerminalCancelled:
		return recording.ReasonStreamCancelled
	case providerstream.TerminalEarlyEOF, providerstream.TerminalCleanEnd:
		return recording.ReasonStreamEndedEarly
	default:
		return streamFailureReason(err)
	}
}
