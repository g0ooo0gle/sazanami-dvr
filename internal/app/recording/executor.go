package recording

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	progressInterval          = 5 * time.Second
	minimumUsefulTS           = 188
	minimumReconnectRemaining = 60 * time.Second
)

var reconnectDelays = [...]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// AttemptStoreは一回の録画処理を、短いDB更新で順方向へ進めるインターフェースである。
type AttemptStore interface {
	ClaimRecording(context.Context, recording.ClaimRequest) (recording.Attempt, error)
	StartAttempt(context.Context, catalogmodel.ID, time.Time) error
	AttemptStopRequested(context.Context, catalogmodel.ID) (bool, error)
	RecordingStarted(context.Context, catalogmodel.ID, time.Time) (time.Time, error)
	OneSegRecordingStarted(context.Context, catalogmodel.ID, time.Time) error
	UpdateRecordingProgress(context.Context, catalogmodel.ID, int64, time.Time) (time.Time, error)
	UpdateOneSegProgress(context.Context, catalogmodel.ID, int64, time.Time) (time.Time, error)
	BeginFinalization(context.Context, recording.FinalizeRequest) error
	MarkFinalPublished(context.Context, catalogmodel.ID, time.Time) error
	MarkDirectorySynced(context.Context, catalogmodel.ID, time.Time) error
	MarkOneSegFinalPublished(context.Context, catalogmodel.ID, time.Time) error
	MarkOneSegDirectorySynced(context.Context, catalogmodel.ID, time.Time) error
	SetOneSegOutcome(context.Context, catalogmodel.ID, recording.OneSegResult, time.Time) error
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
	FinalPath     func(recording.FilePlan) (string, error)
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
	State         recording.AttemptState
	Reason        recording.TerminalReason
	PostRecording recording.PostRecordingMode
}

// PostRecordingRequestは完成済み録画の後処理へ渡す、検証済みの最小情報である。
type PostRecordingRequest struct {
	Script          string
	RecordingNumber int32
	FinalPath       string
	State           recording.AttemptState
	Reason          recording.TerminalReason
}

// Executorは一つの予約の実行権をDBで取得し、同じ部分ファイルへ録画ストリームを保存する。
// 一時的な切断時は古いleaseを閉じた後だけ、固定した小さい上限内で開き直す。
type Executor struct {
	Store                AttemptStore
	Stream               providerstream.Provider
	Files                FileOperations
	Clock                TimeSource
	NewID                func() (catalogmodel.ID, error)
	OwnerID              catalogmodel.ID
	Generation           int64
	WithDeadline         func(context.Context, time.Time) (context.Context, context.CancelFunc)
	Wait                 func(context.Context, time.Duration) error
	PostRecording        func(context.Context, PostRecordingRequest) string
	ObservePostRecording func(string)
	FollowExtensionOnly  bool
}

type streamCopyResult struct {
	ByteCount    int64
	LastProgress time.Time
	PlannedEnd   time.Time
	MaximumEnd   time.Time
	Reason       recording.TerminalReason
	ReachedEnd   bool
	Retryable    bool
}

// Missはストリームを開かず、実行できなかった予約を終了状態へ進める。
func (executor Executor) Miss(ctx context.Context, reservation recording.Reservation, reason recording.TerminalReason) (Result, error) {
	if reason != recording.ReasonLateStartExpired && reason != recording.ReasonRecordingSlotUnavailable {
		return Result{}, errors.New("recording: invalid missed reason")
	}
	attempt, err := executor.Claim(ctx, reservation)
	if err != nil {
		return Result{}, err
	}
	finish := recording.FinishRequest{
		AttemptID: attempt.ID, State: recording.AttemptMissed, Reason: reason,
		Availability: recording.AvailabilityMissing, Now: executor.now(),
	}
	if attempt.OneSegPlan != nil {
		finish.OneSeg = &recording.OneSegResult{Availability: recording.AvailabilityMissing, Reason: reason}
	}
	if err := executor.Store.FinishAttempt(ctx, finish); err != nil {
		return Result{}, errors.New("recording: finish missed attempt")
	}
	return Result{State: finish.State, Reason: finish.Reason}, nil
}

// Executeは一つの予約を予定終了まで録画し、正常終了時だけ完成ファイルを公開する。
func (executor Executor) Execute(ctx context.Context, reservation recording.Reservation) (Result, error) {
	attempt, err := executor.Claim(ctx, reservation)
	if err != nil {
		return Result{}, err
	}
	return executor.ExecuteClaimed(ctx, reservation, attempt)
}

// ExecuteClaimedはDBへ確保済みの一件を実行する。
// SchedulerはClaimの完了後だけこの処理をGo routineで起動し、同じ予約の二重起動を防ぐ。
func (executor Executor) ExecuteClaimed(ctx context.Context, reservation recording.Reservation, attempt recording.Attempt) (Result, error) {
	if attempt.OneSegPlan != nil {
		return executor.executeClaimedWithOneSeg(ctx, reservation, attempt)
	}
	return executor.executeClaimed(ctx, reservation, attempt)
}

func (executor Executor) executeClaimed(ctx context.Context, reservation recording.Reservation, attempt recording.Attempt) (Result, error) {
	if err := executor.validateClaimed(ctx, reservation, attempt); err != nil {
		return Result{}, err
	}
	if err := executor.Store.StartAttempt(ctx, attempt.ID, executor.now()); err != nil {
		return Result{}, errors.New("recording: persist starting attempt")
	}
	stopRequested, err := executor.Store.AttemptStopRequested(context.WithoutCancel(ctx), attempt.ID)
	if err != nil {
		return Result{}, errors.New("recording: read initial stop request")
	}
	if stopRequested {
		return executor.finishWithoutFile(context.WithoutCancel(ctx), attempt.ID, recording.ReasonUserRequestedStop)
	}
	partial, err := executor.Files.CreatePartial(attempt.Plan)
	if err != nil {
		return executor.finishWithoutFile(ctx, attempt.ID, recording.ReasonFileCreateFailed)
	}
	target, err := provider.NewTuningTarget(reservation.Program.ProviderServiceLocator)
	if err != nil {
		return executor.finishBeforeRecording(ctx, partial, attempt.ID, recording.ReasonStreamNotFound)
	}
	maximumEnd := attempt.PlannedStart.Add(recording.MaxEffectiveDuration)
	if attempt.PlannedEnd.After(maximumEnd) {
		maximumEnd = attempt.PlannedEnd
	}
	streamContext, cancel := executor.deadline(ctx, maximumEnd)
	defer cancel()
	copyResult := streamCopyResult{LastProgress: executor.now(), PlannedEnd: attempt.PlannedEnd, MaximumEnd: maximumEnd}
	started := false
	reconnected := false
	for connection := 0; ; connection++ {
		lease, openErr := executor.Stream.OpenStream(streamContext, providerstream.Request{
			Target: target, Usage: providerstream.UsageRecording, PriorityPolicy: "0", RequireDescrambled: true,
			CorrelationID: streamCorrelationID(attempt.ID, connection),
		})
		if openErr != nil {
			copyResult.Reason = streamFailureReason(openErr)
			if ctx.Err() != nil {
				copyResult.Reason, err = executor.cancelReason(ctx, attempt.ID)
				if err != nil {
					_ = partial.Close()
					return Result{}, err
				}
			}
			copyResult.Retryable = retryableStreamFailure(openErr, providerstream.Terminal{})
			if !executor.prepareReconnect(streamContext, copyResult.PlannedEnd, connection, copyResult.Retryable) {
				if ctx.Err() != nil {
					copyResult.Reason, err = executor.cancelReason(ctx, attempt.ID)
					if err != nil {
						_ = partial.Close()
						return Result{}, err
					}
				} else if copyResult.Retryable && connection == len(reconnectDelays) {
					copyResult.Reason = recording.ReasonStreamReconnectExhausted
				}
				return executor.finishStreamFailure(ctx, partial, attempt.ID, copyResult.ByteCount, copyResult.Reason, started)
			}
			continue
		}
		if connection > 0 {
			reconnected = true
		}
		if !started {
			stopRequested, stopErr := executor.Store.AttemptStopRequested(context.WithoutCancel(ctx), attempt.ID)
			if stopErr != nil {
				_ = lease.Cancel()
				_ = lease.Close()
				_ = partial.Close()
				return Result{}, errors.New("recording: read stop request before stream start")
			}
			if stopRequested {
				_ = lease.Cancel()
				_ = lease.Close()
				return executor.finishBeforeRecording(ctx, partial, attempt.ID, recording.ReasonUserRequestedStop)
			}
			plannedEnd, err := executor.Store.RecordingStarted(ctx, attempt.ID, executor.now())
			if err != nil || plannedEnd.IsZero() || plannedEnd.Location() != time.UTC ||
				!plannedEnd.After(attempt.PlannedStart) || plannedEnd.After(copyResult.MaximumEnd) ||
				executor.FollowExtensionOnly && plannedEnd.Before(copyResult.PlannedEnd) {
				_ = lease.Cancel()
				_ = lease.Close()
				_ = partial.Close()
				return Result{}, errors.New("recording: persist recording start")
			}
			copyResult.PlannedEnd = plannedEnd
			started = true
		}
		copyResult = executor.copy(streamContext, lease, partial, attempt, reservation.Components, copyResult, false)
		_ = lease.Cancel()
		_ = lease.Close()
		if copyResult.Reason == recording.ReasonUserRequestedStop {
			return executor.finishUserStop(ctx, partial, reservation, attempt, copyResult.ByteCount)
		}
		if copyResult.ReachedEnd {
			break
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			copyResult.Reason, err = executor.cancelReason(ctx, attempt.ID)
			if err != nil {
				_ = partial.Close()
				return Result{}, err
			}
			if copyResult.Reason == recording.ReasonUserRequestedStop {
				return executor.finishUserStop(ctx, partial, reservation, attempt, copyResult.ByteCount)
			}
			return executor.finishPartial(ctx, partial, attempt.ID, copyResult.ByteCount, copyResult.Reason)
		}
		if !executor.prepareReconnect(streamContext, copyResult.PlannedEnd, connection, copyResult.Retryable) {
			if errors.Is(ctx.Err(), context.Canceled) {
				copyResult.Reason, err = executor.cancelReason(ctx, attempt.ID)
				if err != nil {
					_ = partial.Close()
					return Result{}, err
				}
				if copyResult.Reason == recording.ReasonUserRequestedStop {
					return executor.finishUserStop(ctx, partial, reservation, attempt, copyResult.ByteCount)
				}
			} else if copyResult.Retryable && connection == len(reconnectDelays) {
				copyResult.Reason = recording.ReasonStreamReconnectExhausted
			}
			return executor.finishPartial(ctx, partial, attempt.ID, copyResult.ByteCount, copyResult.Reason)
		}
	}
	byteCount := copyResult.ByteCount
	if err := partial.Sync(); err != nil {
		return executor.finishPartialAfterClose(ctx, partial, attempt.ID, byteCount, recording.ReasonFileSyncFailed)
	}
	if err := partial.Close(); err != nil {
		return executor.finishByCount(ctx, attempt.ID, byteCount, recording.ReasonFileSyncFailed, true)
	}
	if byteCount < minimumUsefulTS {
		return executor.finishByCount(ctx, attempt.ID, byteCount, recording.ReasonStreamEndedEarly, true)
	}
	reason := recording.ReasonCompleted
	if reconnected {
		reason = recording.ReasonCompletedAfterReconnect
	}
	return executor.publishAndPostProcess(ctx, reservation, attempt, byteCount, recording.AttemptSucceeded, reason)
}

func (executor Executor) publishAndPostProcess(ctx context.Context, reservation recording.Reservation, attempt recording.Attempt,
	byteCount int64, state recording.AttemptState, reason recording.TerminalReason,
) (Result, error) {
	result, err := executor.publishFinal(ctx, attempt, byteCount, state, reason)
	if err != nil {
		return result, err
	}
	if reservation.PostRecording.Script != "" {
		postReason := "post-recording-script-invalid"
		if executor.Files.FinalPath != nil && executor.PostRecording != nil {
			finalPath, pathErr := executor.Files.FinalPath(attempt.Plan)
			if pathErr == nil {
				postReason = executor.PostRecording(ctx, PostRecordingRequest{
					Script: reservation.PostRecording.Script, RecordingNumber: reservation.Number,
					FinalPath: finalPath, State: state, Reason: reason,
				})
			}
		}
		if postReason != "" && executor.ObservePostRecording != nil {
			executor.ObservePostRecording(postReason)
		}
	}
	if reservation.PostRecording.Mode.ChangesPower() {
		result.PostRecording = reservation.PostRecording.Mode
	}
	return result, nil
}

// publishFinalは同期して閉じた部分ファイルを、DBへ保存した予定結果どおり完成名へ公開する。
func (executor Executor) publishFinal(ctx context.Context, attempt recording.Attempt, byteCount int64, state recording.AttemptState, reason recording.TerminalReason) (Result, error) {
	token, err := executor.NewID()
	if err != nil {
		return Result{}, errors.New("recording: finalization token generation failed")
	}
	if err := executor.Store.BeginFinalization(ctx, recording.FinalizeRequest{
		AttemptID: attempt.ID, Token: token, ByteCount: byteCount, State: state, Reason: reason, Now: executor.now(),
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
		AttemptID: attempt.ID, State: state, Reason: reason,
		ByteCount: byteCount, Availability: recording.AvailabilityFinal, Now: executor.now(),
	}
	if err := executor.Store.FinishAttempt(ctx, finish); err != nil {
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFinalDatabaseFailed}, err
	}
	return Result{State: finish.State, Reason: finish.Reason}, nil
}

func (executor Executor) copy(ctx context.Context, lease providerstream.Lease, file PartialFile, attempt recording.Attempt,
	componentMode recording.ComponentMode, result streamCopyResult, oneSeg bool,
) streamCopyResult {
	result.ReachedEnd = false
	result.Retryable = false
	buffer := make([]byte, provider.MaxStreamChunk)
	components := componentMode.Effective()
	var filter *tsComponentFilter
	if !components.Captions || !components.Data {
		filter = newTSComponentFilter(file, components.Captions, components.Data)
	}
	for {
		if !executor.now().Before(result.PlannedEnd) {
			if filter != nil && filter.Finish() != nil {
				result.Reason = recording.ReasonStreamFormatInvalid
				return result
			}
			result.Reason = recording.ReasonCompleted
			result.ReachedEnd = true
			return result
		}
		read, terminal, err := lease.Read(ctx, buffer)
		if read < 0 || read > len(buffer) {
			result.Reason = recording.ReasonStreamUnavailable
			return result
		}
		if read > 0 {
			var written int64
			var writeErr error
			if filter == nil {
				count, err := file.Write(buffer[:read])
				if count < 0 || count > read {
					writeErr = errors.New("recording: invalid file write count")
				} else {
					written, writeErr = int64(count), err
					if writeErr == nil && count != read {
						writeErr = errors.New("recording: short file write")
					}
				}
			} else {
				written, writeErr = filter.Write(buffer[:read])
			}
			if result.ByteCount > math.MaxInt64-written {
				result.Reason = recording.ReasonFileWriteFailed
				return result
			}
			result.ByteCount += written
			if writeErr != nil {
				if errors.Is(writeErr, errTSFormat) {
					result.Reason = recording.ReasonStreamFormatInvalid
					return result
				}
				result.Reason = recording.ReasonFileWriteFailed
				return result
			}
		}
		now := executor.now()
		if now.Sub(result.LastProgress) >= progressInterval {
			var plannedEnd time.Time
			var err error
			if oneSeg {
				plannedEnd, err = executor.Store.UpdateOneSegProgress(context.WithoutCancel(ctx), attempt.ID, result.ByteCount, now)
			} else {
				plannedEnd, err = executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attempt.ID, result.ByteCount, now)
			}
			if err != nil || plannedEnd.IsZero() || plannedEnd.Location() != time.UTC ||
				!plannedEnd.After(attempt.PlannedStart) || plannedEnd.After(result.MaximumEnd) ||
				executor.FollowExtensionOnly && plannedEnd.Before(result.PlannedEnd) {
				result.Reason = recording.ReasonProcessInterrupted
				return result
			}
			result.PlannedEnd = plannedEnd
			result.LastProgress = now
			stopRequested, stopErr := executor.Store.AttemptStopRequested(context.WithoutCancel(ctx), attempt.ID)
			if stopErr != nil {
				result.Reason = recording.ReasonProcessInterrupted
				return result
			}
			if stopRequested {
				result.Reason = recording.ReasonUserRequestedStop
				return result
			}
		}
		if !now.Before(result.PlannedEnd) {
			if filter != nil && filter.Finish() != nil {
				result.Reason = recording.ReasonStreamFormatInvalid
				return result
			}
			result.Reason = recording.ReasonCompleted
			result.ReachedEnd = true
			return result
		}
		if err != nil || terminal.Done {
			if filter != nil && ctx.Err() == nil && filter.Finish() != nil {
				result.Reason = recording.ReasonStreamFormatInvalid
				return result
			}
			result.Reason = streamTerminalReason(err, terminal)
			result.Retryable = retryableStreamFailure(err, terminal)
			return result
		}
		if read == 0 {
			result.Reason = recording.ReasonStreamUnavailable
			result.Retryable = true
			return result
		}
	}
}

func (executor Executor) finishStreamFailure(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason, started bool) (Result, error) {
	if !started {
		return executor.finishBeforeRecording(ctx, file, attemptID, reason)
	}
	return executor.finishPartial(ctx, file, attemptID, byteCount, reason)
}

func (executor Executor) finishBeforeRecording(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, reason recording.TerminalReason) (Result, error) {
	if err := file.Sync(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	if err := file.Close(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	return executor.finishByCount(context.WithoutCancel(ctx), attemptID, 0, reason, true)
}

func (executor Executor) prepareReconnect(ctx context.Context, plannedEnd time.Time, connection int, retryable bool) bool {
	if !retryable || connection >= len(reconnectDelays) || ctx.Err() != nil ||
		plannedEnd.Sub(executor.now()) < minimumReconnectRemaining {
		return false
	}
	return executor.wait(ctx, reconnectDelays[connection]) == nil
}

func (executor Executor) wait(ctx context.Context, delay time.Duration) error {
	if executor.Wait != nil {
		return executor.Wait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func streamCorrelationID(attemptID catalogmodel.ID, connection int) string {
	if connection == 0 {
		return attemptID.String()
	}
	return fmt.Sprintf("%s-reconnect-%d", attemptID.String(), connection)
}

func retryableStreamFailure(err error, terminal providerstream.Terminal) bool {
	if provider.IsReason(err, provider.ReasonUnavailable) || provider.IsReason(err, provider.ReasonTimeout) ||
		provider.IsReason(err, provider.ReasonEarlyEOF) {
		return true
	}
	switch terminal.Reason {
	case providerstream.TerminalCleanEnd, providerstream.TerminalEarlyEOF,
		providerstream.TerminalTimeout, providerstream.TerminalPeer:
		return true
	default:
		return false
	}
}

func (executor Executor) finishPartial(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason) (Result, error) {
	if _, err := executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attemptID, byteCount, executor.now()); err != nil {
		_ = file.Sync()
		_ = file.Close()
		return Result{}, errors.New("recording: persist final progress")
	}
	if err := file.Sync(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	return executor.finishPartialAfterClose(ctx, file, attemptID, byteCount, reason)
}

func (executor Executor) finishUserStop(ctx context.Context, file PartialFile, reservation recording.Reservation,
	attempt recording.Attempt, byteCount int64,
) (Result, error) {
	if _, err := executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attempt.ID, byteCount, executor.now()); err != nil {
		_ = file.Sync()
		_ = file.Close()
		return Result{}, errors.New("recording: persist stopped progress")
	}
	if err := file.Sync(); err != nil {
		return executor.finishPartialAfterClose(ctx, file, attempt.ID, byteCount, recording.ReasonFileSyncFailed)
	}
	if err := file.Close(); err != nil {
		return executor.finishByCount(context.WithoutCancel(ctx), attempt.ID, byteCount, recording.ReasonFileSyncFailed, true)
	}
	if byteCount < minimumUsefulTS {
		return executor.finishByCount(context.WithoutCancel(ctx), attempt.ID, byteCount, recording.ReasonUserRequestedStop, true)
	}
	return executor.publishAndPostProcess(context.WithoutCancel(ctx), reservation, attempt, byteCount,
		recording.AttemptPartial, recording.ReasonUserRequestedStop)
}

func (executor Executor) cancelReason(ctx context.Context, attemptID catalogmodel.ID) (recording.TerminalReason, error) {
	requested, err := executor.Store.AttemptStopRequested(context.WithoutCancel(ctx), attemptID)
	if err != nil {
		return "", errors.New("recording: read stop request after cancellation")
	}
	if requested {
		return recording.ReasonUserRequestedStop, nil
	}
	return recording.ReasonProcessShutdown, nil
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
	if reason == recording.ReasonUserRequestedStop {
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

// Claimは一件の予約へ録画処理とファイル計画を同期的に割り当てる。
// 呼び出しが成功するまで録画用Go routineやstreamを開始してはいけない。
func (executor Executor) Claim(ctx context.Context, reservation recording.Reservation) (recording.Attempt, error) {
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
	plan, err := recording.NewReservationFilePlan(reservation, attemptID)
	if err != nil {
		return recording.Attempt{}, err
	}
	claim := recording.ClaimRequest{
		ReservationID: reservation.ID, ReservationVersion: reservation.Version,
		AttemptID: attemptID, SegmentID: segmentID, OwnerID: executor.OwnerID,
		OwnerGeneration: executor.Generation, Now: executor.now(), Plan: plan,
	}
	if reservation.OneSegOutput != nil {
		claim.OneSegSegmentID, err = executor.NewID()
		if err != nil {
			return recording.Attempt{}, errors.New("recording: one-seg segment id generation failed")
		}
		oneSegPlan, planErr := recording.NewOneSegFilePlan(reservation, attemptID)
		if planErr != nil {
			return recording.Attempt{}, planErr
		}
		claim.OneSegPlan = &oneSegPlan
	}
	attempt, err := executor.Store.ClaimRecording(ctx, claim)
	if err != nil {
		return recording.Attempt{}, err
	}
	return attempt, nil
}

func (executor Executor) validateClaimed(ctx context.Context, reservation recording.Reservation, attempt recording.Attempt) error {
	if err := executor.validate(ctx, reservation); err != nil {
		return err
	}
	if attempt.ID == (catalogmodel.ID{}) || attempt.ReservationID != reservation.ID ||
		attempt.State != recording.AttemptClaimed || attempt.PlannedStart.IsZero() || attempt.PlannedEnd.IsZero() ||
		attempt.PlannedStart.Location() != time.UTC || attempt.PlannedEnd.Location() != time.UTC ||
		!attempt.PlannedEnd.After(attempt.PlannedStart) || attempt.Plan.Validate() != nil ||
		(attempt.OneSegPlan == nil) != (reservation.OneSegOutput == nil) ||
		attempt.OneSegPlan != nil && (attempt.OneSegPlan.Validate() != nil ||
			attempt.OneSegPlan.PartialPath == attempt.Plan.PartialPath || attempt.OneSegPlan.PartialPath == attempt.Plan.FinalPath ||
			attempt.OneSegPlan.FinalPath == attempt.Plan.PartialPath || attempt.OneSegPlan.FinalPath == attempt.Plan.FinalPath) {
		return errors.New("recording: invalid claimed attempt")
	}
	return nil
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
