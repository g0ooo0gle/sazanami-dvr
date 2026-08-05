package provider

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/provider/fake"
	coreprovider "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/health"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

type endlessStreamProvider struct{}

func (endlessStreamProvider) OpenStream(context.Context, stream.Request) (stream.Lease, error) {
	return &endlessStreamLease{}, nil
}

type endlessStreamLease struct{ closed bool }

func (l *endlessStreamLease) Read(context.Context, []byte) (int, stream.Terminal, error) {
	return 1, stream.Terminal{Reason: stream.TerminalActive}, nil
}

func (*endlessStreamLease) Cancel() error { return nil }
func (l *endlessStreamLease) Close() error {
	l.closed = true
	return nil
}

var _ io.Closer = (*endlessStreamLease)(nil)

func testHarness(t *testing.T, steps []fake.StreamStep) *fake.Harness {
	t.Helper()
	scenario, err := fake.NewScenario(fake.Config{
		ID: "walk", WallTime: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), InitialNanos: 1,
		ServicePages: []catalog.ServicePage{{Items: []catalog.ServiceObservation{{Locator: "s", DisplayName: "A"}}, End: true}},
		ProgramPages: []catalog.ProgramPage{{Items: []catalog.ProgramObservation{{ServiceLocator: "s", EventLocator: "e", Title: "P"}}, End: true}},
		StreamSteps:  steps,
		Health:       health.Observation{Reachability: health.Reachable, Version: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	harness, err := fake.NewHarness(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

func TestWalkerUsesAllSeparatePorts(t *testing.T) {
	harness := testHarness(t, []fake.StreamStep{{Kind: fake.StepChunk, Data: []byte{1, 2, 3}}, {Kind: fake.StepCleanEnd}})
	walker := Walker{Catalog: harness.Catalog, Stream: harness.Stream, Health: harness.Health}
	result, err := walker.Walk(context.Background(), WalkRequest{
		ServiceLimit: 16, ProgramLimit: 16, ReadStream: true,
		Stream: stream.Request{Target: coreprovider.TuningTarget{Opaque: "target"}, CorrelationID: "stream"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Services != 1 || result.Programs != 1 || result.StreamBytes != 3 || result.Health.Reachability != health.Reachable {
		t.Fatalf("result=%+v", result)
	}
	snapshot := harness.Snapshot()
	if snapshot.Active != 0 || snapshot.ServiceOpens != 1 || snapshot.ProgramOpens != 1 || snapshot.StreamOpens != 1 || snapshot.HealthCalls != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestWalkerStopsAfterBoundedNoProgress(t *testing.T) {
	harness := testHarness(t, []fake.StreamStep{{Kind: fake.StepStall, ReleaseAt: 1_000}})
	walker := Walker{Catalog: harness.Catalog, Stream: harness.Stream, Health: harness.Health}
	_, err := walker.Walk(context.Background(), WalkRequest{
		ServiceLimit: 16, ProgramLimit: 16, ReadStream: true,
		Stream: stream.Request{Target: coreprovider.TuningTarget{Opaque: "target"}, CorrelationID: "stream"},
	})
	if !coreprovider.IsReason(err, coreprovider.ReasonTimeout) {
		t.Fatalf("err=%v", err)
	}
	if harness.Snapshot().Active != 0 {
		t.Fatal("deferred leases were not closed")
	}
}

func TestWalkerRejectsStreamWithoutTerminal(t *testing.T) {
	harness := testHarness(t, nil)
	walker := Walker{Catalog: harness.Catalog, Stream: endlessStreamProvider{}, Health: harness.Health}
	_, err := walker.Walk(context.Background(), WalkRequest{
		ServiceLimit: 16, ProgramLimit: 16, ReadStream: true,
		Stream: stream.Request{Target: coreprovider.TuningTarget{Opaque: "target"}, CorrelationID: "stream"},
	})
	if !coreprovider.IsReason(err, coreprovider.ReasonOverLimit) {
		t.Fatalf("err=%v", err)
	}
}
