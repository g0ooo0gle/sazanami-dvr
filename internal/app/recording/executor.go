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
	UpdateRecordingProgress(context.Context, catalogmodel.ID, int64, time.Time, recording.QualitySummary) (time.Time, error)
	UpdateOneSegProgress(context.Context, catalogmodel.ID, int64, time.Time, recording.QualitySummary) (time.Time, error)
	BeginFinalization(context.Context, recording.FinalizeRequest) (recording.FinalizeRequest, error)
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

// QualityObservationはsegmentごとの固定分類と集約品質だけを観測先へ渡す。
type QualityObservation struct {
	Ordinal int
	Phase   string
	Quality recording.QualitySummary
}

type qualityObservationState struct {
	observe     func(QualityObservation)
	ordinal     int
	lastTime    time.Time
	lastQuality recording.QualitySummary
}

func (state *qualityObservationState) emit(phase string, q recording.QualitySummary, now time.Time) {
	if state == nil || state.observe == nil {
		return
	}
	if phase == "continue" || phase == "fallback" || phase == "reconnect" {
		if q == state.lastQuality || now.Sub(state.lastTime) < 30*time.Second {
			return
		}
		state.lastTime, state.lastQuality = now, q
	}
	state.observe(QualityObservation{Ordinal: state.ordinal, Phase: phase, Quality: q})
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
	ObserveQuality       func(QualityObservation)
	FollowExtensionOnly  bool
}

type streamCopyResult struct {
	ByteCount    int64
	ActualStart  time.Time
	LastProgress time.Time
	PlannedEnd   time.Time
	MaximumEnd   time.Time
	Reason       recording.TerminalReason
	ReachedEnd   bool
	Retryable    bool
	Quality      recording.QualitySummary
	observation  *qualityObservationState
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
		return executor.finishBeforeRecording(ctx, partial, attempt.ID, recording.ReasonStreamNotFound, recording.QualitySummary{})
	}
	maximumEnd := attempt.PlannedStart.Add(recording.MaxEffectiveDuration)
	if attempt.PlannedEnd.After(maximumEnd) {
		maximumEnd = attempt.PlannedEnd
	}
	streamContext, cancel := executor.deadline(ctx, maximumEnd)
	defer cancel()
	copyResult := streamCopyResult{LastProgress: executor.now(), PlannedEnd: attempt.PlannedEnd, MaximumEnd: maximumEnd}
	copyResult.observation = &qualityObservationState{observe: executor.ObserveQuality, lastTime: executor.now()}
	copyResult.observation.emit("start", copyResult.Quality, executor.now())
	defer func() { copyResult.observation.emit("final", copyResult.Quality, executor.now()) }()
	started := false
	reconnected := false
	for connection := 0; ; connection++ {
		if ctx.Err() != nil {
			copyResult.Reason, err = executor.cancelReason(ctx, attempt.ID)
			if err != nil {
				_ = partial.Close()
				return Result{}, err
			}
			return executor.finishCopyFailure(ctx, partial, reservation, attempt, copyResult, started)
		}
		stop, stopErr := executor.Store.AttemptStopRequested(context.WithoutCancel(ctx), attempt.ID)
		if stopErr != nil {
			_ = partial.Close()
			return Result{}, errors.New("recording: read stop request before connection")
		}
		if stop {
			copyResult.Reason = recording.ReasonUserRequestedStop
			return executor.finishCopyFailure(ctx, partial, reservation, attempt, copyResult, started)
		}
		if !executor.now().Before(copyResult.PlannedEnd) {
			copyResult.ReachedEnd = true
			break
		}
		if connection > 0 && copyResult.PlannedEnd.Sub(executor.now()) < minimumReconnectRemaining {
			return executor.finishCopyFailure(ctx, partial, reservation, attempt, copyResult, started)
		}
		if connection > 0 {
			reconnected = true
			copyResult.Quality.ReconnectCount = int64(connection)
			copyResult.Quality.Status = recording.QualityDegraded
			copyResult.observation.emit("reconnect", copyResult.Quality, executor.now())
		}
		lease, openErr := executor.Stream.OpenStream(streamContext, providerstream.Request{
			Target: target, Usage: providerstream.UsageRecording, PriorityPolicy: "0", RequireDescrambled: true,
			CorrelationID: streamCorrelationID(attempt.ID, connection),
		})
		if openErr != nil {
			copyResult.Quality.Status = recording.QualityDegraded
			copyResult.observation.emit("connection-end", copyResult.Quality, executor.now())
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
				if ctx.Err() == nil && copyResult.Retryable && !executor.now().Before(copyResult.PlannedEnd) {
					copyResult = executor.endCopy(ctx, attempt, copyResult)
					if copyResult.ReachedEnd {
						break
					}
					return executor.finishCopyFailure(ctx, partial, reservation, attempt, copyResult, started)
				}
				if ctx.Err() != nil {
					copyResult.Reason, err = executor.cancelReason(ctx, attempt.ID)
					if err != nil {
						_ = partial.Close()
						return Result{}, err
					}
				} else if copyResult.Retryable && connection == len(reconnectDelays) {
					copyResult.Reason = recording.ReasonStreamReconnectExhausted
				}
				return executor.finishCopyFailure(ctx, partial, reservation, attempt, copyResult, started)
			}
			continue
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
				return executor.finishBeforeRecording(ctx, partial, attempt.ID, recording.ReasonUserRequestedStop, copyResult.Quality)
			}
			copyResult.ActualStart = executor.now()
			plannedEnd, err := executor.Store.RecordingStarted(ctx, attempt.ID, copyResult.ActualStart)
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
		copyResult = executor.copy(ctx, streamContext, lease, partial, attempt, reservation.Components, copyResult, false)
		_ = lease.Cancel()
		_ = lease.Close()
		copyResult.observation.emit("connection-end", copyResult.Quality, executor.now())
		if copyResult.Reason == recording.ReasonUserRequestedStop {
			return executor.finishUserStop(ctx, partial, reservation, attempt, copyResult.ByteCount, copyResult.Quality)
		}
		if copyResult.ReachedEnd {
			break
		}
		if ctx.Err() != nil {
			copyResult.Reason, err = executor.cancelReason(ctx, attempt.ID)
			if err != nil {
				_ = partial.Close()
				return Result{}, err
			}
			if copyResult.Reason == recording.ReasonUserRequestedStop {
				return executor.finishUserStop(ctx, partial, reservation, attempt, copyResult.ByteCount, copyResult.Quality)
			}
			return executor.finishPartial(ctx, partial, attempt.ID, copyResult.ByteCount, copyResult.Reason, copyResult.Quality)
		}
		if !executor.prepareReconnect(streamContext, copyResult.PlannedEnd, connection, copyResult.Retryable) {
			if ctx.Err() == nil && copyResult.Retryable && !executor.now().Before(copyResult.PlannedEnd) {
				copyResult = executor.endCopy(ctx, attempt, copyResult)
				if copyResult.ReachedEnd {
					break
				}
				return executor.finishCopyFailure(ctx, partial, reservation, attempt, copyResult, started)
			}
			if ctx.Err() != nil {
				copyResult.Reason, err = executor.cancelReason(ctx, attempt.ID)
				if err != nil {
					_ = partial.Close()
					return Result{}, err
				}
				if copyResult.Reason == recording.ReasonUserRequestedStop {
					return executor.finishUserStop(ctx, partial, reservation, attempt, copyResult.ByteCount, copyResult.Quality)
				}
			} else if copyResult.Retryable && connection == len(reconnectDelays) {
				copyResult.Reason = recording.ReasonStreamReconnectExhausted
			}
			return executor.finishCopyFailure(ctx, partial, reservation, attempt, copyResult, started)
		}
	}
	byteCount := copyResult.ByteCount
	if err := partial.Sync(); err != nil {
		return executor.finishPartialAfterClose(ctx, partial, attempt.ID, byteCount, recording.ReasonFileSyncFailed, copyResult.Quality)
	}
	if err := partial.Close(); err != nil {
		return executor.finishByCount(ctx, attempt.ID, byteCount, recording.ReasonFileSyncFailed, true, copyResult.Quality)
	}
	if byteCount < minimumUsefulTS {
		return executor.finishByCount(ctx, attempt.ID, byteCount, recording.ReasonStreamEndedEarly, true, copyResult.Quality)
	}
	reason := recording.ReasonCompleted
	if reconnected {
		reason = recording.ReasonCompletedAfterReconnect
	}
	return executor.publishAndPostProcess(ctx, reservation, attempt, byteCount, recording.AttemptSucceeded, reason, copyResult.Quality)
}

func (executor Executor) publishAndPostProcess(ctx context.Context, reservation recording.Reservation, attempt recording.Attempt,
	byteCount int64, state recording.AttemptState, reason recording.TerminalReason,
	quality recording.QualitySummary,
) (Result, error) {
	result, err := executor.publishFinal(ctx, attempt, byteCount, state, reason, quality)
	if err != nil {
		return result, err
	}
	if result.State != recording.AttemptSucceeded &&
		(result.State != recording.AttemptPartial || result.Reason != recording.ReasonUserRequestedStop) {
		return result, nil
	}
	if reservation.PostRecording.Script != "" {
		postReason := "post-recording-script-invalid"
		if executor.Files.FinalPath != nil && executor.PostRecording != nil {
			finalPath, pathErr := executor.Files.FinalPath(attempt.Plan)
			if pathErr == nil {
				postContext := ctx
				if result.State == recording.AttemptPartial {
					postContext = context.WithoutCancel(ctx)
				}
				postReason = executor.PostRecording(postContext, PostRecordingRequest{
					Script: reservation.PostRecording.Script, RecordingNumber: reservation.Number,
					FinalPath: finalPath, State: result.State, Reason: result.Reason,
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
func (executor Executor) publishFinal(ctx context.Context, attempt recording.Attempt, byteCount int64, state recording.AttemptState, reason recording.TerminalReason, quality recording.QualitySummary) (Result, error) {
	if ctx.Err() != nil {
		cancelReason, err := executor.cancelReason(ctx, attempt.ID)
		if err != nil {
			return Result{}, err
		}
		if cancelReason != recording.ReasonUserRequestedStop {
			return executor.finishByCount(context.WithoutCancel(ctx), attempt.ID, byteCount, cancelReason, true, quality)
		}
		state, reason = recording.AttemptPartial, cancelReason
	}
	token, err := executor.NewID()
	if err != nil {
		return Result{}, errors.New("recording: finalization token generation failed")
	}
	if ctx.Err() != nil {
		cancelReason, err := executor.cancelReason(ctx, attempt.ID)
		if err != nil {
			return Result{}, err
		}
		if cancelReason != recording.ReasonUserRequestedStop {
			return executor.finishByCount(context.WithoutCancel(ctx), attempt.ID, byteCount, cancelReason, true, quality)
		}
		state, reason = recording.AttemptPartial, cancelReason
	}
	finalizationContext := ctx
	if reason == recording.ReasonUserRequestedStop {
		finalizationContext = context.WithoutCancel(ctx)
	}
	request := recording.FinalizeRequest{
		AttemptID: attempt.ID, Token: token, ByteCount: byteCount, State: state, Reason: reason, Now: executor.now(), Quality: quality,
	}
	finalization, err := executor.Store.BeginFinalization(finalizationContext, request)
	if errors.Is(err, recording.ErrFinalizationUnavailable) && ctx.Err() != nil {
		cancelReason, cancelErr := executor.cancelReason(ctx, attempt.ID)
		if cancelErr != nil {
			return Result{}, cancelErr
		}
		if cancelReason != recording.ReasonUserRequestedStop {
			return executor.finishByCount(context.WithoutCancel(ctx), attempt.ID, byteCount,
				cancelReason, true, quality)
		}
		request.State, request.Reason = recording.AttemptPartial, recording.ReasonUserRequestedStop
		finalization, err = executor.Store.BeginFinalization(context.WithoutCancel(ctx), request)
	}
	if err != nil {
		return Result{}, errors.New("recording: persist finalization start")
	}
	if finalization.Validate() != nil || finalization.AttemptID != attempt.ID || finalization.Token != token ||
		finalization.ByteCount != byteCount {
		return Result{}, errors.New("recording: invalid finalization result")
	}
	state, reason = finalization.State, finalization.Reason
	ctx = context.WithoutCancel(ctx)
	if err := executor.Files.LinkFinal(attempt.Plan); err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			finish := recording.FinishRequest{AttemptID: attempt.ID, State: recording.AttemptFailed,
				Reason: recording.ReasonFinalPublicationFailed, ByteCount: byteCount,
				Availability: recording.AvailabilityPartial, Now: executor.now(), Quality: quality}
			if err := executor.Store.FinishAttempt(ctx, finish); err != nil {
				return Result{}, errors.New("recording: persist unsupported publication")
			}
			return Result{State: finish.State, Reason: finish.Reason}, nil
		}
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
		ByteCount: byteCount, Availability: recording.AvailabilityFinal, Now: executor.now(), Quality: quality,
	}
	if err := executor.Store.FinishAttempt(ctx, finish); err != nil {
		return Result{State: recording.AttemptFinalizing, Reason: recording.ReasonFinalDatabaseFailed}, err
	}
	return Result{State: finish.State, Reason: finish.Reason}, nil
}

func (executor Executor) copy(parentCtx, ctx context.Context, lease providerstream.Lease, file PartialFile, attempt recording.Attempt,
	componentMode recording.ComponentMode, result streamCopyResult, oneSeg bool,
) (out streamCopyResult) {
	result.ReachedEnd = false
	result.Retryable = false
	buffer := make([]byte, provider.MaxStreamChunk)
	components := componentMode.Effective()
	filter := newTSComponentFilter(file, components.Captions, components.Data, result.Quality)
	defer func() {
		if parentCtx.Err() != nil {
			if reason, err := executor.cancelReason(parentCtx, attempt.ID); err == nil {
				out.Reason = reason
			} else {
				out.Reason = recording.ReasonProcessInterrupted
			}
			out.ReachedEnd, out.Retryable = false, false
		}
		allow := out.ReachedEnd || out.Reason == recording.ReasonUserRequestedStop ||
			parentCtx.Err() == nil && (out.Reason == recording.ReasonStreamEndedEarly ||
				out.Reason == recording.ReasonStreamTimeout || out.Reason == recording.ReasonStreamUnavailable)
		written, err := filter.Finish(allow)
		out.Quality = filter.Quality()
		if recording.IsCommunicationPartialReason(out.Reason) || out.Reason == recording.ReasonStreamUnavailable {
			out.Quality.Status = recording.QualityDegraded
		}
		if out.ByteCount > math.MaxInt64-written {
			err = errors.New("recording: TS byte count overflow")
		} else {
			out.ByteCount += written
		}
		if err != nil {
			out.Reason = recording.ReasonFileWriteFailed
			out.Retryable, out.ReachedEnd = false, false
		}
	}()
	for {
		if parentCtx.Err() != nil {
			if reason, err := executor.cancelReason(parentCtx, attempt.ID); err == nil {
				result.Reason = reason
			} else {
				result.Reason = recording.ReasonProcessInterrupted
			}
			return result
		}
		if !executor.now().Before(result.PlannedEnd) {
			result = executor.endCopy(parentCtx, attempt, result)
			return result
		}
		if ctx.Err() != nil {
			result.Reason = recording.ReasonStreamCancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.Reason = recording.ReasonStreamTimeout
			}
			return result
		}
		read, terminal, err := lease.Read(ctx, buffer)
		if read < 0 || read > len(buffer) {
			result.Reason = recording.ReasonStreamUnavailable
			return result
		}
		if read > 0 {
			previousSelection := result.Quality.SelectionUnverified
			written, writeErr := filter.Write(buffer[:read])
			result.Quality = filter.Quality()
			phase := "continue"
			if !previousSelection && result.Quality.SelectionUnverified {
				phase = "fallback"
			}
			result.observation.emit(phase, result.Quality, executor.now())
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
				plannedEnd, err = executor.Store.UpdateOneSegProgress(context.WithoutCancel(ctx), attempt.ID, result.ByteCount, now, result.Quality)
			} else {
				plannedEnd, err = executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attempt.ID, result.ByteCount, now, result.Quality)
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
		if parentCtx.Err() != nil {
			if reason, err := executor.cancelReason(parentCtx, attempt.ID); err == nil {
				result.Reason = reason
			} else {
				result.Reason = recording.ReasonProcessInterrupted
			}
			return result
		}
		if !now.Before(result.PlannedEnd) {
			result = executor.endCopy(parentCtx, attempt, result)
			return result
		}
		if err != nil || terminal.Done {
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

func (executor Executor) endCopy(ctx context.Context, attempt recording.Attempt, result streamCopyResult) streamCopyResult {
	stop, err := executor.Store.AttemptStopRequested(context.WithoutCancel(ctx), attempt.ID)
	if err != nil {
		result.Reason = recording.ReasonProcessInterrupted
	} else if stop {
		result.Reason = recording.ReasonUserRequestedStop
	} else {
		result.Reason, result.ReachedEnd = recording.ReasonCompleted, true
	}
	return result
}

func (executor Executor) finishCopyFailure(ctx context.Context, file PartialFile, reservation recording.Reservation,
	attempt recording.Attempt, result streamCopyResult, started bool,
) (Result, error) {
	if started && result.Reason == recording.ReasonUserRequestedStop {
		return executor.finishUserStop(ctx, file, reservation, attempt, result.ByteCount, result.Quality)
	}
	if started && recording.IsCommunicationPartialReason(result.Reason) {
		return executor.finishCommunicationPartial(ctx, file, attempt, result)
	}
	return executor.finishStreamFailure(ctx, file, attempt.ID, result.ByteCount, result.Reason, started, result.Quality)
}

func (executor Executor) finishCommunicationPartial(ctx context.Context, file PartialFile, attempt recording.Attempt,
	result streamCopyResult,
) (Result, error) {
	result.Quality.Status = recording.QualityDegraded
	if result.ByteCount < minimumUsefulTS || result.ByteCount%188 != 0 || result.ActualStart.IsZero() ||
		executor.now().Sub(result.ActualStart) < time.Second {
		return executor.finishPartial(ctx, file, attempt.ID, result.ByteCount, result.Reason, result.Quality)
	}
	if _, err := executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attempt.ID, result.ByteCount, executor.now(), result.Quality); err != nil {
		_ = file.Close()
		return Result{}, errors.New("recording: persist communication final progress")
	}
	if err := file.Sync(); err != nil {
		return executor.finishPartialAfterClose(ctx, file, attempt.ID, result.ByteCount, recording.ReasonFileSyncFailed, result.Quality)
	}
	if err := file.Close(); err != nil {
		return executor.finishByCount(context.WithoutCancel(ctx), attempt.ID, result.ByteCount, recording.ReasonFileSyncFailed, true, result.Quality)
	}
	return executor.publishFinal(ctx, attempt, result.ByteCount, recording.AttemptPartial, result.Reason, result.Quality)
}

func (executor Executor) finishStreamFailure(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason, started bool, quality recording.QualitySummary) (Result, error) {
	if !started {
		return executor.finishBeforeRecording(ctx, file, attemptID, reason, quality)
	}
	return executor.finishPartial(ctx, file, attemptID, byteCount, reason, quality)
}

func (executor Executor) finishBeforeRecording(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, reason recording.TerminalReason, quality recording.QualitySummary) (Result, error) {
	if err := file.Sync(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	if err := file.Close(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	return executor.finishByCount(context.WithoutCancel(ctx), attemptID, 0, reason, true, quality)
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

func (executor Executor) finishPartial(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason, quality recording.QualitySummary) (Result, error) {
	if _, err := executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attemptID, byteCount, executor.now(), quality); err != nil {
		_ = file.Sync()
		_ = file.Close()
		return Result{}, errors.New("recording: persist final progress")
	}
	if err := file.Sync(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	return executor.finishPartialAfterClose(ctx, file, attemptID, byteCount, reason, quality)
}

func (executor Executor) finishUserStop(ctx context.Context, file PartialFile, reservation recording.Reservation,
	attempt recording.Attempt, byteCount int64, quality recording.QualitySummary,
) (Result, error) {
	if _, err := executor.Store.UpdateRecordingProgress(context.WithoutCancel(ctx), attempt.ID, byteCount, executor.now(), quality); err != nil {
		_ = file.Sync()
		_ = file.Close()
		return Result{}, errors.New("recording: persist stopped progress")
	}
	if err := file.Sync(); err != nil {
		return executor.finishPartialAfterClose(ctx, file, attempt.ID, byteCount, recording.ReasonFileSyncFailed, quality)
	}
	if err := file.Close(); err != nil {
		return executor.finishByCount(context.WithoutCancel(ctx), attempt.ID, byteCount, recording.ReasonFileSyncFailed, true, quality)
	}
	if byteCount < minimumUsefulTS {
		return executor.finishByCount(context.WithoutCancel(ctx), attempt.ID, byteCount, recording.ReasonUserRequestedStop, true, quality)
	}
	return executor.publishAndPostProcess(context.WithoutCancel(ctx), reservation, attempt, byteCount,
		recording.AttemptPartial, recording.ReasonUserRequestedStop, quality)
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

func (executor Executor) finishPartialAfterClose(ctx context.Context, file PartialFile, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason, quality recording.QualitySummary) (Result, error) {
	if err := file.Close(); err != nil {
		reason = recording.ReasonFileSyncFailed
	}
	return executor.finishByCount(context.WithoutCancel(ctx), attemptID, byteCount, reason, true, quality)
}

func (executor Executor) finishByCount(ctx context.Context, attemptID catalogmodel.ID, byteCount int64, reason recording.TerminalReason, fileExists bool, quality recording.QualitySummary) (Result, error) {
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
		Availability: availability, Now: executor.now(), Quality: quality,
	}
	if err := executor.Store.FinishAttempt(ctx, finish); err != nil {
		return Result{}, errors.New("recording: persist unsuccessful finish")
	}
	return Result{State: state, Reason: reason}, nil
}

func (executor Executor) finishWithoutFile(ctx context.Context, attemptID catalogmodel.ID, reason recording.TerminalReason) (Result, error) {
	return executor.finishByCount(context.WithoutCancel(ctx), attemptID, 0, reason, false, recording.QualitySummary{})
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
