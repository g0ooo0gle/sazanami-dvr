// Package healthはtunerを使用しないProvider health観測を定義する。
package health

import (
	"context"
	"time"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

// ReachabilityはProvider endpointへの到達状態を表す。
type Reachability string

const (
	Reachable   Reachability = "REACHABLE"
	Unavailable Reachability = "UNAVAILABLE"
	Unknown     Reachability = "UNKNOWN"
)

// Requestはhealth観測を追跡する相関IDを渡す。
type Request struct{ CorrelationID string }

// Observationはtunerを開かずに取得できるversion、capability、到達状態を表す。
type Observation struct {
	Reachability Reachability
	Provenance   provider.Provenance
	Version      string
	Capabilities []provider.Capability
	TunerSummary string
	Reason       provider.Reason
	ObservedAt   time.Time
}

// CloneObservationはCapabilitiesのbacking arrayを共有しないcopyを返す。
func CloneObservation(value Observation) Observation {
	capabilities := make([]provider.Capability, len(value.Capabilities))
	copy(capabilities, value.Capabilities)
	value.Capabilities = capabilities
	return value
}

// Providerはtuner-freeなhealth observationを返すPortである。
type Provider interface {
	Probe(context.Context, Request) (Observation, error)
}
