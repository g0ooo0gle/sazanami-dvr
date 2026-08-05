package fake

import (
	"context"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
	providerhealth "github.com/g0ooo0gle/sazanami-dvr/internal/core/provider/health"
)

// HealthProviderはtunerを開かずScenarioのhealth observationを返すFake Portである。
type HealthProvider struct{ runtime *runtimeState }

// Probeはrequest、fault、同時open上限を検証し、defensive copyを返す。
func (p *HealthProvider) Probe(ctx context.Context, request providerhealth.Request) (providerhealth.Observation, error) {
	if err := provider.ContextFailure(ctx); err != nil {
		return providerhealth.Observation{}, err
	}
	if err := validateText(request.CorrelationID, provider.MaxDiagnosticBytes, "health-correlation"); err != nil || request.CorrelationID == "" {
		if err != nil {
			return providerhealth.Observation{}, err
		}
		return providerhealth.Observation{}, malformed("empty-health-correlation")
	}
	if expected := p.runtime.scenario.expected.HealthCorrelation; expected != "" && expected != request.CorrelationID {
		return providerhealth.Observation{}, provider.NewFailure(provider.ReasonRejected, "health-request-mismatch")
	}
	if failure := p.runtime.scenario.fault(FaultHealth, 0); failure != nil {
		return providerhealth.Observation{}, failure
	}
	p.runtime.mu.Lock()
	defer p.runtime.mu.Unlock()
	if err := p.runtime.recordLocked("health-probe", p.runtime.counters.HealthCalls); err != nil {
		return providerhealth.Observation{}, err
	}
	p.runtime.counters.HealthCalls++
	return providerhealth.CloneObservation(p.runtime.scenario.health), nil
}
