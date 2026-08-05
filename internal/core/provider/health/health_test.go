package health

import (
	"testing"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

func TestCloneObservationDoesNotShareCapabilities(t *testing.T) {
	original := Observation{Capabilities: []provider.Capability{provider.CapabilityCatalog, provider.CapabilityHealth}}
	cloned := CloneObservation(original)

	cloned.Capabilities[0] = provider.CapabilityStream
	if original.Capabilities[0] != provider.CapabilityCatalog {
		t.Fatalf("元のCapabilitiesが変更されました: %v", original.Capabilities)
	}
	if cloned.Capabilities[0] != provider.CapabilityStream {
		t.Fatalf("copy側の変更が反映されませんでした: %v", cloned.Capabilities)
	}
}
