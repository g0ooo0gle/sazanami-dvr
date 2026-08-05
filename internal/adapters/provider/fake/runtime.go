// Package fakeはnetwork、filesystem、deviceを使わずProvider Portを再現する。
package fake

import (
	"sync"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

// EventはFake Provider内で発生した操作名とindexを記録する。
type Event struct {
	Name  string
	Index int
}

// Countersは外部I/Oなしで検証できるoperation数とbounded historyを返す。
type Counters struct {
	ServiceOpens int
	ProgramOpens int
	StreamOpens  int
	HealthCalls  int
	Closes       int
	Cancels      int
	Releases     int
	Active       int
	TunerOpens   int
	History      []Event
}

type runtimeState struct {
	mu       sync.Mutex
	scenario *Scenario
	clock    *ManualClock
	counters Counters
	history  []Event
}

func newRuntime(scenario *Scenario) *runtimeState {
	return &runtimeState{scenario: scenario, clock: NewManualClock(scenario.initialNanos)}
}

func (r *runtimeState) recordLocked(name string, index int) error {
	if len(r.history) >= r.scenario.historyLimit {
		return overLimit("history-overflow")
	}
	r.history = append(r.history, Event{Name: name, Index: index})
	return nil
}

func (r *runtimeState) acquireLocked(name string) error {
	if r.counters.Active >= provider.MaxConcurrentOpen {
		return overLimit("concurrent-open")
	}
	if err := r.recordLocked(name, r.counters.Active); err != nil {
		return err
	}
	r.counters.Active++
	return nil
}

func (r *runtimeState) releaseLocked(name string) error {
	if r.counters.Active > 0 {
		r.counters.Active--
	}
	r.counters.Releases++
	return r.recordLocked(name, r.counters.Releases)
}

func (r *runtimeState) snapshot() Counters {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := r.counters
	result.Active = r.counters.Active
	result.History = append([]Event(nil), r.history...)
	return result
}

// Harnessは共有されたbounded counterを持つ3つの独立Fake Portを公開する。
type Harness struct {
	runtime *runtimeState
	Catalog *CatalogProvider
	Stream  *StreamProvider
	Health  *HealthProvider
}

// NewHarnessはimmutableなScenarioから、外部I/Oを行わないFake Provider一式を生成する。
func NewHarness(scenario *Scenario) (*Harness, error) {
	if scenario == nil {
		return nil, malformed("nil-scenario")
	}
	runtime := newRuntime(scenario)
	return &Harness{
		runtime: runtime,
		Catalog: &CatalogProvider{runtime: runtime},
		Stream:  &StreamProvider{runtime: runtime},
		Health:  &HealthProvider{runtime: runtime},
	}, nil
}

// ClockはScenarioが使用する手動の単調時計を返す。
func (h *Harness) Clock() *ManualClock { return h.runtime.clock }

// Snapshotはcounterとhistoryのdefensive copyを返す。
func (h *Harness) Snapshot() Counters { return h.runtime.snapshot() }

// VerifyExpectedCountsはScenarioで宣言した呼出回数と実測counterを照合する。
func (h *Harness) VerifyExpectedCounts() error {
	got := h.Snapshot()
	want := h.runtime.scenario.expectedCounts
	if want.ServiceOpens != got.ServiceOpens || want.ProgramOpens != got.ProgramOpens ||
		want.StreamOpens != got.StreamOpens || want.HealthCalls != got.HealthCalls {
		return provider.NewFailure(provider.ReasonInternal, "expected-count-mismatch")
	}
	return nil
}
