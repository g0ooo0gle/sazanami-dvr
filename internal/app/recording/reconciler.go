package recording

import (
	"context"
	"errors"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

const (
	// MaximumCompletedReconcileItemsは一回にDBから読む録画処理の最大件数である。
	MaximumCompletedReconcileItems = 1000
)

// CompletedReconcileStoreは完了録画の現在の利用状態だけを読み書きする。
type CompletedReconcileStore interface {
	RecoveryAttempts(context.Context, int, catalogmodel.ID) ([]core.RecoveryItem, error)
	SetRecordingAvailability(context.Context, catalogmodel.ID, core.Availability, core.TerminalReason, time.Time) error
	SetOneSegAvailability(context.Context, catalogmodel.ID, core.Availability, core.TerminalReason, time.Time) error
}

// CompletedReconcileResultは一回の完了録画照合で観測したboundedな件数である。
type CompletedReconcileResult struct {
	Checked    int
	Changed    int
	Missing    int
	Mismatched int
}

// CompletedReconcilerはDBに保存した完了録画だけを、前回cursorから上限付きで照合する。
// Runを同時に呼ばず、process再起動時は新しい値を作ってcursorを0へ戻す。
type CompletedReconciler struct {
	Store   CompletedReconcileStore
	Inspect func(core.FilePlan) (core.FileObservation, error)
	Clock   TimeSource
	after   catalogmodel.ID
}

// Runは最大1,000件を読み、完了録画のavailabilityだけを更新する。
func (reconciler *CompletedReconciler) Run(ctx context.Context) (CompletedReconcileResult, string, error) {
	if reconciler == nil || ctx == nil || reconciler.Store == nil || reconciler.Inspect == nil || reconciler.Clock == nil {
		return CompletedReconcileResult{}, "recording-reconcile-internal", errors.New("recording: invalid completed reconciler")
	}
	if err := ctx.Err(); err != nil {
		return CompletedReconcileResult{}, "recording-reconcile-cancelled", err
	}
	var result CompletedReconcileResult
	after := reconciler.after
	read := 0
	for read < MaximumCompletedReconcileItems {
		limit := min(core.MaxRecoveryPage, MaximumCompletedReconcileItems-read)
		items, err := reconciler.Store.RecoveryAttempts(ctx, limit, after)
		if err != nil {
			if ctx.Err() != nil {
				return CompletedReconcileResult{}, "recording-reconcile-cancelled", ctx.Err()
			}
			return CompletedReconcileResult{}, "recording-reconcile-read-failed", errors.New("recording: read completed reconciliation page")
		}
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return CompletedReconcileResult{}, "recording-reconcile-cancelled", err
			}
			read++
			after = item.ID
			if !completedReconcileTarget(item) {
				continue
			}
			if reason, err := reconciler.reconcileItem(ctx, item, &result); err != nil {
				return CompletedReconcileResult{}, reason, err
			}
		}
		if len(items) < limit {
			reconciler.after = catalogmodel.ID{}
			return result, "", nil
		}
	}
	reconciler.after = after
	return result, "", nil
}

func (reconciler *CompletedReconciler) reconcileItem(ctx context.Context, item core.RecoveryItem,
	result *CompletedReconcileResult,
) (string, error) {
	observation, err := reconciler.Inspect(item.Plan)
	if err != nil {
		return "recording-reconcile-inspect-failed", errors.New("recording: inspect completed file")
	}
	availability, reason := completedAvailability(item.ByteCount, observation)
	result.observe(availability)
	if item.Availability != availability || item.IntegrityReason != reason {
		if err := reconciler.Store.SetRecordingAvailability(ctx, item.ID, availability, reason, reconciler.now()); err != nil {
			return "recording-reconcile-update-failed", errors.New("recording: update completed file availability")
		}
		result.Changed++
	}
	if item.OneSeg == nil {
		return "", nil
	}
	oneSegObservation, err := reconciler.Inspect(item.OneSeg.Plan)
	if err != nil {
		return "recording-reconcile-inspect-failed", errors.New("recording: inspect completed one-seg file")
	}
	oneSegAvailability, oneSegReason := settledOneSegAvailability(*item.OneSeg, oneSegObservation)
	result.observe(oneSegAvailability)
	if item.OneSeg.Availability == oneSegAvailability && item.OneSeg.IntegrityReason == oneSegReason {
		return "", nil
	}
	if err := reconciler.Store.SetOneSegAvailability(ctx, item.ID, oneSegAvailability, oneSegReason, reconciler.now()); err != nil {
		return "recording-reconcile-update-failed", errors.New("recording: update completed one-seg availability")
	}
	result.Changed++
	return "", nil
}

func (result *CompletedReconcileResult) observe(availability core.Availability) {
	result.Checked++
	switch availability {
	case core.AvailabilityMissing:
		result.Missing++
	case core.AvailabilityMismatched:
		result.Mismatched++
	}
}

func completedReconcileTarget(item core.RecoveryItem) bool {
	return item.State == core.AttemptSucceeded ||
		item.State == core.AttemptPartial && item.PlannedReason == core.ReasonUserRequestedStop
}

func (reconciler *CompletedReconciler) now() time.Time { return reconciler.Clock.Now().UTC() }
