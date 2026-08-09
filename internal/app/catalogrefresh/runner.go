// Package catalogrefreshは一つの番組表更新を直列に繰り返し、失敗時も次の予定まで待つ。
package catalogrefresh

import (
	"context"
	"errors"
	"time"
)

const (
	// DefaultIntervalは録画プロセスが番組表更新を繰り返す既定間隔である。
	DefaultInterval = time.Hour
	// MinimumIntervalはoperatorが設定できる最短更新間隔である。
	MinimumInterval = 5 * time.Minute
	// MaximumIntervalはoperatorが設定できる最長更新間隔である。
	MaximumInterval = 24 * time.Hour
	// OperationTimeoutは一回のprovider取得、保存、事前検証、完了に許す時間である。
	OperationTimeout = 10 * time.Minute
)

// Resultは秘密情報を含まない一回の正常更新結果である。
type Result struct {
	Services int
	Programs int
}

// Eventは一回の更新後にoperatorへ出せるboundedな観測結果である。
type Event struct {
	Completed  bool
	Services   int
	Programs   int
	DurationMS int64
	Reason     string
}

// Runnerは起動直後と各完了後の一定間隔で、同時に一つだけ更新する。
type Runner struct {
	Interval time.Duration
	Sync     func(context.Context) (Result, string, error)
	Observe  func(Event)
}

// Runは親contextが終了するまで更新を直列実行する。個々の失敗では終了しない。
func (runner Runner) Run(ctx context.Context) error {
	if ctx == nil || runner.Sync == nil || runner.Observe == nil ||
		runner.Interval < MinimumInterval || runner.Interval > MaximumInterval {
		return errors.New("catalogrefresh: invalid runner")
	}
	return run(ctx, runner.Interval, OperationTimeout, runner.Sync, runner.Observe)
}

func run(ctx context.Context, interval, timeout time.Duration,
	sync func(context.Context) (Result, string, error), observe func(Event),
) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		started := time.Now()
		operationContext, cancel := context.WithTimeout(ctx, timeout)
		result, reason, err := sync(operationContext)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		event := Event{Completed: err == nil, Services: result.Services, Programs: result.Programs,
			DurationMS: time.Since(started).Milliseconds()}
		if err != nil {
			event.Services = 0
			event.Programs = 0
			event.Reason = stableReason(reason)
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

func stableReason(value string) string {
	if value == "" || len(value) > 64 {
		return "catalog-refresh-internal"
	}
	for _, character := range value {
		if character != '-' && (character < 'a' || character > 'z') {
			return "catalog-refresh-internal"
		}
	}
	return value
}
