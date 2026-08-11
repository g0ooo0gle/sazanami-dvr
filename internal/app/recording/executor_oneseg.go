package recording

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

// executeClaimedWithOneSegはメインの既存処理を保ち、開始後の補助録画と終了前の合流だけを加える。
func (executor Executor) executeClaimedWithOneSeg(ctx context.Context, reservation core.Reservation,
	attempt core.Attempt,
) (Result, error) {
	coordinator := &oneSegCoordinator{
		executor: executor, reservation: reservation, attempt: attempt, done: make(chan core.OneSegResult, 1),
	}
	store := &oneSegAttemptStore{AttemptStore: executor.Store, coordinator: coordinator}
	mainExecutor := executor
	mainExecutor.Store = store
	defer coordinator.close()
	return mainExecutor.executeClaimed(ctx, reservation, attempt)
}

// oneSegAttemptStoreはメインの状態遷移へ、補助録画の開始・合流・公開を一箇所だけ差し込む。
type oneSegAttemptStore struct {
	AttemptStore
	coordinator *oneSegCoordinator
}

// RecordingStartedはメインの録画開始を保存した後だけ、ワンセグの補助処理を始める。
func (store *oneSegAttemptStore) RecordingStarted(ctx context.Context, attemptID catalogmodel.ID,
	now time.Time,
) (time.Time, error) {
	plannedEnd, err := store.AttemptStore.RecordingStarted(ctx, attemptID, now)
	if err == nil {
		store.coordinator.start(ctx, plannedEnd)
	}
	return plannedEnd, err
}

// BeginFinalizationはワンセグの終了を待ち、二つの同期結果を一緒に保存する。
func (store *oneSegAttemptStore) BeginFinalization(ctx context.Context, request core.FinalizeRequest) error {
	oneSeg := store.coordinator.join(request.Reason, true)
	request.OneSeg = &oneSeg
	return store.AttemptStore.BeginFinalization(ctx, request)
}

// MarkDirectorySyncedはメインの完成を確定してから、公開可能なワンセグを処理する。
func (store *oneSegAttemptStore) MarkDirectorySynced(ctx context.Context, attemptID catalogmodel.ID,
	now time.Time,
) error {
	if err := store.AttemptStore.MarkDirectorySynced(ctx, attemptID, now); err != nil {
		return err
	}
	return store.coordinator.publish(ctx)
}

// FinishAttemptは失敗したメインより先に補助処理を止め、ワンセグの部分状態も同時に確定する。
func (store *oneSegAttemptStore) FinishAttempt(ctx context.Context, request core.FinishRequest) error {
	successful := request.State == core.AttemptSucceeded ||
		(request.State == core.AttemptPartial && request.Reason == core.ReasonUserRequestedStop)
	if !successful {
		oneSeg := store.coordinator.join(request.Reason, false)
		request.OneSeg = &oneSeg
	}
	return store.AttemptStore.FinishAttempt(ctx, request)
}

type oneSegCoordinator struct {
	executor    Executor
	reservation core.Reservation
	attempt     core.Attempt
	done        chan core.OneSegResult

	mu        sync.Mutex
	started   bool
	joined    bool
	published bool
	cancel    context.CancelFunc
	result    core.OneSegResult
}

func (coordinator *oneSegCoordinator) start(ctx context.Context, plannedEnd time.Time) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.started || coordinator.attempt.OneSegPlan == nil || coordinator.reservation.OneSegOutput == nil {
		return
	}
	maximumEnd := coordinator.attempt.PlannedStart.Add(core.MaxEffectiveDuration)
	if coordinator.attempt.PlannedEnd.After(maximumEnd) {
		maximumEnd = coordinator.attempt.PlannedEnd
	}
	streamContext, cancel := coordinator.executor.deadline(ctx, maximumEnd)
	coordinator.cancel = cancel
	coordinator.started = true
	go func() {
		coordinator.done <- coordinator.executor.runOneSeg(streamContext, coordinator.reservation,
			coordinator.attempt, plannedEnd, maximumEnd)
	}()
}

func (coordinator *oneSegCoordinator) join(mainReason core.TerminalReason, allowPublish bool) core.OneSegResult {
	coordinator.mu.Lock()
	if coordinator.joined {
		result := coordinator.result
		coordinator.mu.Unlock()
		return result
	}
	if !coordinator.started {
		coordinator.result = core.OneSegResult{Availability: core.AvailabilityMissing, Reason: mainReason}
		coordinator.joined = true
		result := coordinator.result
		coordinator.mu.Unlock()
		return result
	}
	cancel := coordinator.cancel
	coordinator.mu.Unlock()
	var result core.OneSegResult
	stoppedAtMainEnd := false
	select {
	case result = <-coordinator.done:
		cancel()
	default:
		stoppedAtMainEnd = true
		cancel()
		result = <-coordinator.done
	}
	usefulStoppedFile := result.ByteCount >= minimumUsefulTS && result.FileSynced
	mainStoppedAuxiliary := stoppedAtMainEnd && result.Reason == core.ReasonStreamCancelled
	userStoppedAuxiliary := mainReason == core.ReasonUserRequestedStop &&
		(result.Reason == core.ReasonUserRequestedStop || result.Reason == core.ReasonStreamCancelled)
	if allowPublish && usefulStoppedFile && (mainStoppedAuxiliary || userStoppedAuxiliary) &&
		(mainReason == core.ReasonCompleted || mainReason == core.ReasonCompletedAfterReconnect ||
			mainReason == core.ReasonUserRequestedStop) {
		result.Publish = true
		result.Reason = core.ReasonCompleted
		if mainReason == core.ReasonUserRequestedStop {
			result.Reason = core.ReasonUserRequestedStop
		}
		result.Availability = core.AvailabilityPartial
	}
	if !allowPublish && result.Publish {
		result.Publish = false
		result.Reason = mainReason
		result.Availability = core.AvailabilityPartial
	}
	coordinator.mu.Lock()
	coordinator.result = result
	coordinator.joined = true
	coordinator.mu.Unlock()
	return result
}

func (coordinator *oneSegCoordinator) publish(ctx context.Context) error {
	coordinator.mu.Lock()
	if coordinator.published || !coordinator.joined || !coordinator.result.Publish {
		coordinator.mu.Unlock()
		return nil
	}
	coordinator.published = true
	result := coordinator.result
	coordinator.mu.Unlock()
	plan := *coordinator.attempt.OneSegPlan
	if err := coordinator.executor.Files.LinkFinal(plan); err != nil {
		reason, availability := core.ReasonFinalPublicationFailed, core.AvailabilityPartial
		if errors.Is(err, core.ErrFinalExists) {
			reason, availability = core.ReasonFinalNameConflict, core.AvailabilityMismatched
		}
		return coordinator.savePublishFailure(ctx, result, reason, availability)
	}
	if err := coordinator.executor.Store.MarkOneSegFinalPublished(ctx, coordinator.attempt.ID,
		coordinator.executor.now()); err != nil {
		return errors.New("recording: persist one-seg publication")
	}
	if err := coordinator.executor.Files.SyncDirectory(plan); err != nil {
		return coordinator.savePublishFailure(ctx, result, core.ReasonFileSyncFailed, core.AvailabilityPartial)
	}
	if err := coordinator.executor.Files.RemovePartial(plan); err != nil {
		return coordinator.savePublishFailure(ctx, result, core.ReasonFinalPublicationFailed, core.AvailabilityPartial)
	}
	if err := coordinator.executor.Files.SyncDirectory(plan); err != nil {
		return coordinator.savePublishFailure(ctx, result, core.ReasonFileSyncFailed, core.AvailabilityPartial)
	}
	if err := coordinator.executor.Store.MarkOneSegDirectorySynced(ctx, coordinator.attempt.ID,
		coordinator.executor.now()); err != nil {
		return errors.New("recording: persist one-seg directory sync")
	}
	return nil
}

func (coordinator *oneSegCoordinator) savePublishFailure(ctx context.Context, previous core.OneSegResult,
	reason core.TerminalReason, availability core.Availability,
) error {
	previous.Publish = false
	previous.Reason = reason
	previous.Availability = availability
	if err := coordinator.executor.Store.SetOneSegOutcome(ctx, coordinator.attempt.ID, previous,
		coordinator.executor.now()); err != nil {
		return errors.New("recording: persist one-seg publication failure")
	}
	coordinator.mu.Lock()
	coordinator.result = previous
	coordinator.mu.Unlock()
	return nil
}

func (coordinator *oneSegCoordinator) close() {
	coordinator.mu.Lock()
	if !coordinator.started || coordinator.joined {
		coordinator.mu.Unlock()
		return
	}
	cancel := coordinator.cancel
	coordinator.mu.Unlock()
	cancel()
	<-coordinator.done
}

// runOneSegは一つの補助streamを独立したleaseと再接続回数で部分fileへ保存する。
func (executor Executor) runOneSeg(ctx context.Context, reservation core.Reservation, attempt core.Attempt,
	plannedEnd, maximumEnd time.Time,
) core.OneSegResult {
	missing := core.OneSegResult{Availability: core.AvailabilityMissing, Reason: core.ReasonFileCreateFailed}
	if attempt.OneSegPlan == nil || reservation.OneSegOutput == nil {
		return missing
	}
	file, err := executor.Files.CreatePartial(*attempt.OneSegPlan)
	if err != nil {
		return missing
	}
	target, err := provider.NewTuningTarget(reservation.OneSegOutput.ProviderServiceLocator)
	if err != nil {
		return executor.closeOneSeg(file, attempt, streamCopyResult{Reason: core.ReasonStreamNotFound}, false, false)
	}
	result := streamCopyResult{LastProgress: executor.now(), PlannedEnd: plannedEnd, MaximumEnd: maximumEnd}
	started, reconnected := false, false
	for connection := 0; ; connection++ {
		lease, openErr := executor.Stream.OpenStream(ctx, providerstream.Request{
			Target: target, Usage: providerstream.UsageRecording, PriorityPolicy: "0", RequireDescrambled: true,
			CorrelationID: oneSegStreamCorrelationID(attempt.ID, connection),
		})
		if openErr != nil {
			result.Reason = streamFailureReason(openErr)
			result.Retryable = retryableStreamFailure(openErr, providerstream.Terminal{})
			if !executor.prepareReconnect(ctx, result.PlannedEnd, connection, result.Retryable) {
				if result.Retryable && connection == len(reconnectDelays) {
					result.Reason = core.ReasonStreamReconnectExhausted
				}
				return executor.closeOneSeg(file, attempt, result, started, false)
			}
			continue
		}
		if connection > 0 {
			reconnected = true
		}
		if !started {
			if err := executor.Store.OneSegRecordingStarted(context.WithoutCancel(ctx), attempt.ID,
				executor.now()); err != nil {
				_ = lease.Cancel()
				_ = lease.Close()
				result.Reason = core.ReasonProcessInterrupted
				return executor.closeOneSeg(file, attempt, result, false, false)
			}
			started = true
		}
		result = executor.copy(ctx, lease, file, attempt, reservation.Components, result, true)
		_ = lease.Cancel()
		_ = lease.Close()
		if result.ReachedEnd {
			reason := core.ReasonCompleted
			if reconnected {
				reason = core.ReasonCompletedAfterReconnect
			}
			result.Reason = reason
			return executor.closeOneSeg(file, attempt, result, true, true)
		}
		if ctx.Err() != nil {
			result.Reason = core.ReasonStreamCancelled
			return executor.closeOneSeg(file, attempt, result, true, false)
		}
		if !executor.prepareReconnect(ctx, result.PlannedEnd, connection, result.Retryable) {
			if result.Retryable && connection == len(reconnectDelays) {
				result.Reason = core.ReasonStreamReconnectExhausted
			}
			return executor.closeOneSeg(file, attempt, result, true, false)
		}
	}
}

func (executor Executor) closeOneSeg(file PartialFile, attempt core.Attempt, copyResult streamCopyResult,
	started, reachedEnd bool,
) core.OneSegResult {
	if started {
		if _, err := executor.Store.UpdateOneSegProgress(context.Background(), attempt.ID, copyResult.ByteCount,
			executor.now()); err != nil {
			copyResult.Reason = core.ReasonProcessInterrupted
			reachedEnd = false
		}
	}
	fileSynced := file.Sync() == nil
	if err := file.Close(); err != nil {
		fileSynced = false
	}
	if !fileSynced {
		copyResult.Reason = core.ReasonFileSyncFailed
		reachedEnd = false
	}
	if reachedEnd && copyResult.ByteCount < minimumUsefulTS {
		copyResult.Reason = core.ReasonStreamEndedEarly
		reachedEnd = false
	}
	return core.OneSegResult{
		ByteCount: copyResult.ByteCount, Availability: core.AvailabilityPartial, Reason: copyResult.Reason,
		FileSynced: fileSynced, Publish: reachedEnd,
	}
}

func oneSegStreamCorrelationID(attemptID catalogmodel.ID, connection int) string {
	if connection == 0 {
		return attemptID.String() + "-oneseg"
	}
	return fmt.Sprintf("%s-oneseg-reconnect-%d", attemptID.String(), connection)
}
