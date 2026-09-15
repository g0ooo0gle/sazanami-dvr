// Package cataloggcは番組表保持処理の期限、再開、結果境界を所有する。
package cataloggc

import (
	"context"
	"errors"
	"time"
)

const (
	// RetentionPeriodはcatalogの観測と世代summaryを保持する期間である。
	RetentionPeriod = 30 * 24 * time.Hour
	// OperationTimeoutは一回の自動GCへ許す処理上限である。
	OperationTimeout = 30 * time.Second
	maxBatchRows     = 1000
)

var (
	// ErrFailedはDBまたはbatch contractの詳細を隠したGC失敗である。
	ErrFailed = errors.New("catalog-gc-failed")
)

// ClockはGC開始時刻をUTCで注入する境界である。
type Clock interface {
	Now() time.Time
}

// BatchResultは永続化adapterから受け取る一段階分の削除結果である。
// SQLiteやSQLの型をapplicationへ持ち込まない。
type BatchResult struct {
	ProgramObservationsDeleted int
	ServiceObservationsDeleted int
	CatalogSyncsDeleted        int
	ProgramRevisionsDeleted    int
	ProgramInstancesDeleted    int
	ServicesDeleted            int
	AllEmpty                   bool
}

// BatchOperationは固定cutoffで一つの削除batchを実行するfunctional portである。
type BatchOperation func(context.Context, int64) (BatchResult, error)

// Resultは一回のGCで得た削除件数と処理状態である。
type Result struct {
	ProgramObservationsDeleted int
	ServiceObservationsDeleted int
	CatalogSyncsDeleted        int
	ProgramRevisionsDeleted    int
	ProgramInstancesDeleted    int
	ServicesDeleted            int
	More                       bool
	Completed                  bool
	DurationMS                 int64
	Reason                     string
}

// Serviceはclockから一度だけcutoffを作り、削除batchを直列に繰り返す。
type Service struct {
	Clock Clock
	Prune BatchOperation
}

// Runは自動GCを最大30秒実行する。自分の期限到達は部分成功として返す。
func (service Service) Run(ctx context.Context) (Result, error) {
	return service.run(ctx, OperationTimeout)
}

func (service Service) run(ctx context.Context, timeout time.Duration) (result Result, returnErr error) {
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()

	if ctx == nil || service.Clock == nil || service.Prune == nil || timeout < 0 {
		result.Reason = ErrFailed.Error()
		return result, ErrFailed
	}
	now := service.Clock.Now().UTC()
	if now.IsZero() || now.UnixMilli() < 0 {
		result.Reason = ErrFailed.Error()
		return result, ErrFailed
	}
	cutoffUTCMS := now.Add(-RetentionPeriod).UnixMilli()

	operationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if parentErr := ctx.Err(); parentErr != nil {
			result.Reason = "canceled"
			return result, parentErr
		}
		if errors.Is(operationContext.Err(), context.DeadlineExceeded) {
			result.More = true
			result.Reason = "deadline"
			return result, nil
		}

		batch, err := service.Prune(operationContext, cutoffUTCMS)
		if err != nil {
			if parentErr := ctx.Err(); parentErr != nil {
				result.Reason = "canceled"
				return result, parentErr
			}
			if errors.Is(operationContext.Err(), context.DeadlineExceeded) {
				result.More = true
				result.Reason = "deadline"
				return result, nil
			}
			result.Reason = ErrFailed.Error()
			return result, ErrFailed
		}
		if !validBatch(batch) {
			if parentErr := ctx.Err(); parentErr != nil {
				result.Reason = "canceled"
				return result, parentErr
			}
			result.Reason = ErrFailed.Error()
			return result, ErrFailed
		}
		result.ProgramObservationsDeleted += batch.ProgramObservationsDeleted
		result.ServiceObservationsDeleted += batch.ServiceObservationsDeleted
		result.CatalogSyncsDeleted += batch.CatalogSyncsDeleted
		result.ProgramRevisionsDeleted += batch.ProgramRevisionsDeleted
		result.ProgramInstancesDeleted += batch.ProgramInstancesDeleted
		result.ServicesDeleted += batch.ServicesDeleted
		if parentErr := ctx.Err(); parentErr != nil {
			result.Reason = "canceled"
			return result, parentErr
		}
		if errors.Is(operationContext.Err(), context.DeadlineExceeded) {
			result.More = true
			result.Reason = "deadline"
			return result, nil
		}
		if batch.AllEmpty {
			result.Completed = true
			result.Reason = "completed"
			return result, nil
		}
	}
}

func validBatch(batch BatchResult) bool {
	counts := [...]int{
		batch.ProgramObservationsDeleted,
		batch.ServiceObservationsDeleted,
		batch.CatalogSyncsDeleted,
		batch.ProgramRevisionsDeleted,
		batch.ProgramInstancesDeleted,
		batch.ServicesDeleted,
	}
	total := 0
	for _, count := range counts {
		if count < 0 || count > maxBatchRows || total > maxBatchRows-count {
			return false
		}
		total += count
	}
	if total > maxBatchRows {
		return false
	}
	if batch.AllEmpty {
		return total == 0
	}
	return total > 0
}
