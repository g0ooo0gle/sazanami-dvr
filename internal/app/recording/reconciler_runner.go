package recording

import (
	"context"
	"errors"
	"time"
)

const (
	// CompletedReconcileIntervalは前回完了後から次の録画照合までの待ち時間である。
	CompletedReconcileInterval = time.Minute
)

// CompletedReconcileEventは一回の完了録画照合後にoperatorへ出せるboundedな結果である。
type CompletedReconcileEvent struct {
	Completed  bool
	Checked    int
	Changed    int
	Missing    int
	Mismatched int
	DurationMS int64
	Reason     string
}

// CompletedReconcileRunnerは起動直後と各完了一分後に、同時に一つだけ完了録画を照合する。
type CompletedReconcileRunner struct {
	Reconcile func(context.Context) (CompletedReconcileResult, string, error)
	Observe   func(CompletedReconcileEvent)
}

// Runは親contextが終了するまで完了録画の照合を直列実行する。個々の失敗では終了しない。
func (runner CompletedReconcileRunner) Run(ctx context.Context) error {
	if ctx == nil || runner.Reconcile == nil || runner.Observe == nil {
		return errors.New("recording: invalid completed reconcile runner")
	}
	return runCompletedReconcile(ctx, CompletedReconcileInterval, runner.Reconcile, runner.Observe)
}

func runCompletedReconcile(ctx context.Context, interval time.Duration,
	reconcile func(context.Context) (CompletedReconcileResult, string, error),
	observe func(CompletedReconcileEvent),
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		started := time.Now()
		result, reason, err := reconcile(ctx)
		if ctx.Err() != nil {
			return nil
		}
		event := CompletedReconcileEvent{
			Completed: err == nil, DurationMS: time.Since(started).Milliseconds(),
		}
		if err == nil {
			event.Checked = result.Checked
			event.Changed = result.Changed
			event.Missing = result.Missing
			event.Mismatched = result.Mismatched
		} else {
			event.Reason = stableCompletedReconcileReason(reason)
		}
		observe(event)

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func stableCompletedReconcileReason(value string) string {
	if value == "" || len(value) > 64 {
		return "recording-reconcile-internal"
	}
	for _, character := range value {
		if character != '-' && (character < 'a' || character > 'z') {
			return "recording-reconcile-internal"
		}
	}
	return value
}
