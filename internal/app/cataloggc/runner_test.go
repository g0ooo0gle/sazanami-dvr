package cataloggc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunFixesOneCutoffForEveryBatchAndAccumulatesCounts(t *testing.T) {
	if OperationTimeout != 30*time.Second {
		t.Fatalf("operation timeout=%s", OperationTimeout)
	}
	clock := &countingClock{now: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
	pruner := &recordingPruner{results: []BatchResult{
		{ProgramObservationsDeleted: 1, CatalogSyncsDeleted: 2},
		{ServiceObservationsDeleted: 3, ProgramRevisionsDeleted: 4, ProgramInstancesDeleted: 5, ServicesDeleted: 6},
		{AllEmpty: true},
	}}

	result, err := (Service{Clock: clock, Prune: pruner.Call}).run(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.More || !result.Completed || result.ProgramObservationsDeleted != 1 || result.ServiceObservationsDeleted != 3 ||
		result.CatalogSyncsDeleted != 2 || result.ProgramRevisionsDeleted != 4 ||
		result.ProgramInstancesDeleted != 5 || result.ServicesDeleted != 6 {
		t.Fatalf("result=%+v", result)
	}
	if clock.calls != 1 {
		t.Fatalf("clock calls=%d", clock.calls)
	}
	wantCutoff := clock.now.Add(-30 * 24 * time.Hour).UnixMilli()
	if len(pruner.cutoffs) != 3 || pruner.cutoffs[0] != wantCutoff || pruner.cutoffs[1] != wantCutoff || pruner.cutoffs[2] != wantCutoff {
		t.Fatalf("cutoffs=%v want=%d", pruner.cutoffs, wantCutoff)
	}
}

func TestRunTreatsItsDeadlineAsPartialSuccessWithoutWaitingForThirtySeconds(t *testing.T) {
	clock := &countingClock{now: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
	started := make(chan struct{})
	pruner := func(ctx context.Context, _ int64) (BatchResult, error) {
		close(started)
		<-ctx.Done()
		return BatchResult{}, ctx.Err()
	}

	startedAt := time.Now()
	result, err := (Service{Clock: clock, Prune: pruner}).run(context.Background(), 5*time.Millisecond)
	if err != nil || !result.More || time.Since(startedAt) > time.Second {
		t.Fatalf("result=%+v err=%v elapsed=%s", result, err, time.Since(startedAt))
	}
	select {
	case <-started:
	default:
		t.Fatal("pruner was not called")
	}
}

func TestRunReturnsStableErrorForDatabaseFailureAndZeroProgress(t *testing.T) {
	for _, test := range []struct {
		name    string
		results []BatchResult
		err     error
	}{
		{name: "database", err: errors.New("raw sqlite path /private/catalog.sqlite3")},
		{name: "zero progress", results: []BatchResult{{}}},
		{name: "invalid completion", results: []BatchResult{{AllEmpty: true, ServicesDeleted: 1}}},
		{name: "batch limit", results: []BatchResult{{ProgramObservationsDeleted: 1001}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &countingClock{now: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
			pruner := &recordingPruner{results: test.results, err: test.err}
			result, err := (Service{Clock: clock, Prune: pruner.Call}).run(context.Background(), time.Second)
			if !errors.Is(err, ErrFailed) || err.Error() != "catalog-gc-failed" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if strings.Contains(err.Error(), "raw sqlite") {
				t.Fatalf("raw error leaked: %v", err)
			}
		})
	}
}

func TestRunPreservesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &countingClock{now: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
	pruner := func(ctx context.Context, _ int64) (BatchResult, error) {
		cancel()
		return BatchResult{ServicesDeleted: 1}, nil
	}

	result, err := (Service{Clock: clock, Prune: pruner}).run(parent, time.Second)
	if !errors.Is(err, context.Canceled) || result.More || result.ServicesDeleted != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type countingClock struct {
	now   time.Time
	calls int
}

func (clock *countingClock) Now() time.Time {
	clock.calls++
	return clock.now.Add(time.Duration(clock.calls-1) * time.Hour)
}

type recordingPruner struct {
	mu      sync.Mutex
	results []BatchResult
	err     error
	cutoffs []int64
}

func (pruner *recordingPruner) Call(_ context.Context, cutoff int64) (BatchResult, error) {
	pruner.mu.Lock()
	defer pruner.mu.Unlock()
	pruner.cutoffs = append(pruner.cutoffs, cutoff)
	if pruner.err != nil {
		return BatchResult{}, pruner.err
	}
	if len(pruner.results) == 0 {
		return BatchResult{AllEmpty: true}, nil
	}
	result := pruner.results[0]
	pruner.results = pruner.results[1:]
	return result, nil
}
