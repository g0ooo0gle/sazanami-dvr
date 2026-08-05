package fake

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/catalog"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/health"
	providerstream "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/stream"
)

func testService(name string) catalog.ServiceObservation {
	return catalog.ServiceObservation{
		Provenance: provider.Provenance{Backend: "synthetic", Revision: "1"},
		Locator:    "service:" + name, Broadcast: "GR", NetworkID: 1, ServiceID: 2,
		DisplayName: name, TuningTarget: provider.TuningTarget{Opaque: "target:" + name},
		Validation: provider.ValidationValid,
	}
}

func testProgram(title string) catalog.ProgramObservation {
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	duration := 30 * time.Minute
	return catalog.ProgramObservation{
		Provenance:     provider.Provenance{Backend: "synthetic", Revision: "1"},
		ServiceLocator: "service:a", EventLocator: "event:1", Start: &start, Duration: &duration,
		Title: title, Description: "合成データ", RevisionFingerprint: "rev:1", Validation: provider.ValidationValid,
	}
}

func baseConfig() Config {
	return Config{
		ID: "test", Seed: 42, WallTime: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC), InitialNanos: 100,
		ServicePages: []catalog.ServicePage{{Items: []catalog.ServiceObservation{testService("A")}}, {End: true}},
		ProgramPages: []catalog.ProgramPage{{Items: []catalog.ProgramObservation{testProgram("番組")}}, {End: true}},
		StreamSteps:  []StreamStep{{Kind: StepChunk, Data: []byte{1, 2, 3, 4}}, {Kind: StepCleanEnd}},
		Health: health.Observation{
			Reachability: health.Reachable, Provenance: provider.Provenance{Backend: "synthetic", Revision: "1"},
			Version: "1", Capabilities: []provider.Capability{provider.CapabilityCatalog, provider.CapabilityHealth},
			TunerSummary: "unused", ObservedAt: time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		},
		Expected: ExpectedRequests{ServiceLimit: 16, ProgramLimit: 16, StreamTarget: "target:A", HealthCorrelation: "health"},
	}
}

func newTestHarness(t *testing.T, mutate func(*Config)) *Harness {
	t.Helper()
	config := baseConfig()
	if mutate != nil {
		mutate(&config)
	}
	scenario, err := NewScenario(config)
	if err != nil {
		t.Fatal(err)
	}
	harness, err := NewHarness(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

func TestScenarioIsImmutable(t *testing.T) {
	config := baseConfig()
	scenario, err := NewScenario(config)
	if err != nil {
		t.Fatal(err)
	}
	config.ServicePages[0].Items[0].DisplayName = "改ざん"
	config.StreamSteps[0].Data[0] = 99
	page, _ := scenario.servicePage(0)
	step, _ := scenario.streamStep(0)
	if page.Items[0].DisplayName != "A" || step.Data[0] != 1 {
		t.Fatalf("scenario retained caller storage: page=%q data=%v", page.Items[0].DisplayName, step.Data)
	}
	page.Items[0].DisplayName = "返却値を変更"
	step.Data[0] = 88
	again, _ := scenario.servicePage(0)
	againStep, _ := scenario.streamStep(0)
	if again.Items[0].DisplayName != "A" || againStep.Data[0] != 1 {
		t.Fatal("scenario accessor did not return a defensive copy")
	}
}

func TestScenarioLimits(t *testing.T) {
	cases := map[string]func(*Config){
		"empty id":           func(c *Config) { c.ID = "" },
		"zero wall clock":    func(c *Config) { c.WallTime = time.Time{} },
		"negative monotonic": func(c *Config) { c.InitialNanos = -1 },
		"history over limit": func(c *Config) { c.HistoryLimit = provider.MaxHistoryEvents + 1 },
		"chunk over limit": func(c *Config) {
			c.StreamSteps = []StreamStep{{Kind: StepChunk, Data: make([]byte, provider.MaxStreamChunk+1)}}
		},
		"service page over limit": func(c *Config) {
			c.ServicePages = []catalog.ServicePage{{Items: make([]catalog.ServiceObservation, provider.MaxCatalogPage+1)}}
		},
		"faults over limit": func(c *Config) { c.Faults = make([]Fault, provider.MaxScenarioFaults+1) },
		"diagnostic over limit": func(c *Config) {
			c.StreamSteps = []StreamStep{{Kind: StepFailure, Diagnostic: string(bytes.Repeat([]byte{'x'}, provider.MaxDiagnosticBytes+1))}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			config := baseConfig()
			mutate(&config)
			_, err := NewScenario(config)
			if err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestCatalogPullPagingAndClose(t *testing.T) {
	harness := newTestHarness(t, nil)
	cursor, err := harness.Catalog.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "service", Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := harness.Snapshot()
	if len(snapshot.History) != 1 || snapshot.History[0].Name != "service-open" {
		t.Fatalf("Openでpageを先読みしました: %+v", snapshot.History)
	}
	first, err := cursor.Next(context.Background())
	if err != nil || len(first.Items) != 1 || first.End {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	first.Items[0].DisplayName = "改ざん"
	last, err := cursor.Next(context.Background())
	if err != nil || !last.End || len(last.Items) != 0 {
		t.Fatalf("last=%+v err=%v", last, err)
	}
	if err := cursor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Close(); err != nil {
		t.Fatal("Close must be idempotent:", err)
	}
	_, err = cursor.Next(context.Background())
	if !provider.IsReason(err, provider.ReasonRejected) {
		t.Fatalf("read after close err=%v", err)
	}
	snapshot = harness.Snapshot()
	if snapshot.Active != 0 || snapshot.Closes != 1 || snapshot.Releases != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestCatalogFaultBeforeAndAfterPage(t *testing.T) {
	for _, test := range []struct {
		name          string
		point         FaultPoint
		firstSucceeds bool
	}{{"before", FaultServiceBefore, false}, {"after", FaultServiceAfter, true}} {
		t.Run(test.name, func(t *testing.T) {
			harness := newTestHarness(t, func(c *Config) {
				c.Faults = []Fault{{Point: test.point, Index: 0, Reason: provider.ReasonUnavailable, Diagnostic: "synthetic"}}
			})
			cursor, err := harness.Catalog.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "service", Limit: 16})
			if err != nil {
				t.Fatal(err)
			}
			defer cursor.Close()
			page, firstErr := cursor.Next(context.Background())
			if test.firstSucceeds {
				if firstErr != nil || len(page.Items) != 1 {
					t.Fatalf("page=%+v err=%v", page, firstErr)
				}
				_, firstErr = cursor.Next(context.Background())
			}
			if !provider.IsReason(firstErr, provider.ReasonUnavailable) {
				t.Fatalf("err=%v", firstErr)
			}
		})
	}
}

func TestConcurrentOpenLimit(t *testing.T) {
	harness := newTestHarness(t, nil)
	var cursors []catalog.ServiceCursor
	for i := 0; i < provider.MaxConcurrentOpen; i++ {
		cursor, err := harness.Catalog.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "service", Limit: 16})
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		cursors = append(cursors, cursor)
	}
	_, err := harness.Catalog.OpenServices(context.Background(), catalog.ServiceRequest{CorrelationID: "service", Limit: 16})
	if !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("33rd open err=%v", err)
	}
	for _, cursor := range cursors {
		if err := cursor.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if harness.Snapshot().Active != 0 {
		t.Fatal("resource leak")
	}
}

func streamRequest(deadline provider.MonotonicDeadline) providerstream.Request {
	return providerstream.Request{
		Target: provider.TuningTarget{Opaque: "target:A"}, Usage: providerstream.UsageRecording,
		CorrelationID: "stream", MonotonicDeadline: deadline,
	}
}

func TestStreamPartialReadsAndTerminal(t *testing.T) {
	harness := newTestHarness(t, func(c *Config) {
		c.StreamSteps = []StreamStep{{Kind: StepChunk, Data: []byte{1, 2, 3, 4, 5}}, {Kind: StepCleanEnd}}
	})
	lease, err := harness.Stream.OpenStream(context.Background(), streamRequest(provider.MonotonicDeadline{}))
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	buffer := make([]byte, 2)
	for {
		n, terminal, readErr := lease.Read(context.Background(), buffer)
		if readErr != nil {
			t.Fatal(readErr)
		}
		got = append(got, buffer[:n]...)
		if terminal.Done {
			if terminal.Reason != providerstream.TerminalCleanEnd {
				t.Fatalf("terminal=%+v", terminal)
			}
			break
		}
	}
	if !bytes.Equal(got, []byte{1, 2, 3, 4, 5}) {
		t.Fatalf("got=%v", got)
	}
	if err := lease.Close(); err != nil || harness.Snapshot().Active != 0 {
		t.Fatalf("close=%v snapshot=%+v", err, harness.Snapshot())
	}
}

func TestStreamStallDeadlineCancelAndEarlyEOF(t *testing.T) {
	t.Run("stall release", func(t *testing.T) {
		harness := newTestHarness(t, func(c *Config) {
			c.StreamSteps = []StreamStep{{Kind: StepStall, ReleaseAt: 110}, {Kind: StepChunk, Data: []byte{9}}, {Kind: StepCleanEnd}}
		})
		lease, _ := harness.Stream.OpenStream(context.Background(), streamRequest(provider.MonotonicDeadline{}))
		buffer := make([]byte, 1)
		n, terminal, err := lease.Read(context.Background(), buffer)
		if err != nil || n != 0 || terminal.Done {
			t.Fatalf("n=%d terminal=%+v err=%v", n, terminal, err)
		}
		if err := harness.Clock().Advance(10); err != nil {
			t.Fatal(err)
		}
		n, _, err = lease.Read(context.Background(), buffer)
		if err != nil || n != 1 || buffer[0] != 9 {
			t.Fatalf("n=%d byte=%d err=%v", n, buffer[0], err)
		}
		_ = lease.Close()
	})
	t.Run("deadline", func(t *testing.T) {
		harness := newTestHarness(t, func(c *Config) { c.StreamSteps = []StreamStep{{Kind: StepStall, ReleaseAt: 1_000}} })
		lease, _ := harness.Stream.OpenStream(context.Background(), streamRequest(provider.MonotonicDeadline{Enabled: true, Nanos: 105}))
		_ = harness.Clock().Advance(5)
		_, terminal, err := lease.Read(context.Background(), make([]byte, 1))
		if !provider.IsReason(err, provider.ReasonTimeout) || terminal.Reason != providerstream.TerminalTimeout || !terminal.Done {
			t.Fatalf("terminal=%+v err=%v", terminal, err)
		}
		_ = lease.Close()
	})
	t.Run("cancel idempotent", func(t *testing.T) {
		harness := newTestHarness(t, nil)
		lease, _ := harness.Stream.OpenStream(context.Background(), streamRequest(provider.MonotonicDeadline{}))
		if err := lease.Cancel(); err != nil {
			t.Fatal(err)
		}
		if err := lease.Cancel(); err != nil {
			t.Fatal(err)
		}
		_, terminal, err := lease.Read(context.Background(), make([]byte, 1))
		if err != nil || !terminal.Done || terminal.Reason != providerstream.TerminalCancelled {
			t.Fatalf("terminal=%+v err=%v", terminal, err)
		}
		_ = lease.Close()
		snapshot := harness.Snapshot()
		if snapshot.Cancels != 1 || snapshot.Releases != 1 || snapshot.Active != 0 {
			t.Fatalf("snapshot=%+v", snapshot)
		}
	})
	t.Run("early EOF", func(t *testing.T) {
		harness := newTestHarness(t, func(c *Config) { c.StreamSteps = []StreamStep{{Kind: StepEarlyEOF, Diagnostic: "synthetic"}} })
		lease, _ := harness.Stream.OpenStream(context.Background(), streamRequest(provider.MonotonicDeadline{}))
		_, terminal, err := lease.Read(context.Background(), make([]byte, 1))
		if !provider.IsReason(err, provider.ReasonEarlyEOF) || !terminal.Done || terminal.Reason != providerstream.TerminalEarlyEOF {
			t.Fatalf("terminal=%+v err=%v", terminal, err)
		}
		_ = lease.Close()
	})
}

func TestContextCancellationIsStableFailure(t *testing.T) {
	harness := newTestHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := harness.Catalog.OpenServices(ctx, catalog.ServiceRequest{CorrelationID: "service", Limit: 16})
	if !provider.IsReason(err, provider.ReasonCancelled) {
		t.Fatalf("catalog err=%v", err)
	}
	_, err = harness.Stream.OpenStream(ctx, streamRequest(provider.MonotonicDeadline{}))
	if !provider.IsReason(err, provider.ReasonCancelled) {
		t.Fatalf("stream err=%v", err)
	}
	_, err = harness.Health.Probe(ctx, health.Request{CorrelationID: "health"})
	if !provider.IsReason(err, provider.ReasonCancelled) {
		t.Fatalf("health err=%v", err)
	}
}

func TestHealthIsTunerFreeAndDefensive(t *testing.T) {
	harness := newTestHarness(t, nil)
	observation, err := harness.Health.Probe(context.Background(), health.Request{CorrelationID: "health"})
	if err != nil {
		t.Fatal(err)
	}
	observation.Capabilities[0] = provider.CapabilityStream
	again, err := harness.Health.Probe(context.Background(), health.Request{CorrelationID: "health"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := harness.Snapshot()
	if again.Capabilities[0] != provider.CapabilityCatalog || snapshot.TunerOpens != 0 || snapshot.StreamOpens != 0 || snapshot.Active != 0 {
		t.Fatalf("observation=%+v snapshot=%+v", again, snapshot)
	}
}

func TestExpectedCounts(t *testing.T) {
	harness := newTestHarness(t, func(c *Config) { c.ExpectedCounts = ExpectedCounts{HealthCalls: 1} })
	_, _ = harness.Health.Probe(context.Background(), health.Request{CorrelationID: "health"})
	if err := harness.VerifyExpectedCounts(); err != nil {
		t.Fatal(err)
	}
	_, _ = harness.Health.Probe(context.Background(), health.Request{CorrelationID: "health"})
	if err := harness.VerifyExpectedCounts(); !provider.IsReason(err, provider.ReasonInternal) {
		t.Fatalf("err=%v", err)
	}
}

func TestManualClockBounds(t *testing.T) {
	clock := NewManualClock(1)
	if err := clock.Advance(-1); !provider.IsReason(err, provider.ReasonInternal) {
		t.Fatalf("negative err=%v", err)
	}
	clock = NewManualClock(1<<63 - 1)
	if err := clock.Advance(1); !provider.IsReason(err, provider.ReasonInternal) {
		t.Fatalf("overflow err=%v", err)
	}
}

func TestStreamRequestMismatchAndBufferLimits(t *testing.T) {
	harness := newTestHarness(t, nil)
	request := streamRequest(provider.MonotonicDeadline{})
	request.Target.Opaque = "other"
	_, err := harness.Stream.OpenStream(context.Background(), request)
	if !provider.IsReason(err, provider.ReasonRejected) {
		t.Fatalf("mismatch err=%v", err)
	}
	lease, _ := harness.Stream.OpenStream(context.Background(), streamRequest(provider.MonotonicDeadline{}))
	_, _, err = lease.Read(context.Background(), nil)
	if !provider.IsReason(err, provider.ReasonMalformed) {
		t.Fatalf("empty buffer err=%v", err)
	}
	_, _, err = lease.Read(context.Background(), make([]byte, provider.MaxStreamChunk+1))
	if !provider.IsReason(err, provider.ReasonOverLimit) {
		t.Fatalf("large buffer err=%v", err)
	}
	_ = lease.Close()
}

func TestHarnessRejectsNilScenario(t *testing.T) {
	_, err := NewHarness(nil)
	if !provider.IsReason(err, provider.ReasonMalformed) {
		t.Fatalf("err=%v", err)
	}
}

func TestFaultErrorsAreNotRawPayloads(t *testing.T) {
	harness := newTestHarness(t, func(c *Config) {
		c.Faults = []Fault{{Point: FaultHealth, Reason: provider.ReasonUnavailable, Diagnostic: "bounded"}}
	})
	_, err := harness.Health.Probe(context.Background(), health.Request{CorrelationID: "health"})
	var failure *provider.Failure
	if !errors.As(err, &failure) || failure.Diagnostic != "bounded" {
		t.Fatalf("err=%v", err)
	}
}
