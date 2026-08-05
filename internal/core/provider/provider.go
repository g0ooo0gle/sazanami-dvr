// Package providerはinfrastructureに依存しないProviderの値とerrorを定義する。
package provider

import (
	"context"
	"errors"
	"fmt"
)

const (
	MaxCatalogPage       = 256
	MaxServiceOperation  = 4_096
	MaxProgramOperation  = 262_144
	MaxEncodedFieldBytes = 256 * 1024
	MaxStreamChunk       = 188 * 1024
	MaxHealthEnvelope    = 64 * 1024
	MaxDiagnosticBytes   = 256
	MaxScenarioPages     = 1_024
	MaxScenarioChunks    = 4_096
	MaxScenarioFaults    = 64
	MaxHistoryEvents     = 4_096
	MaxConcurrentOpen    = 32
)

// Reasonは安定した失敗分類であり、payload dataを含めない。
type Reason string

const (
	ReasonUnavailable Reason = "UNAVAILABLE"
	ReasonRejected    Reason = "REJECTED"
	ReasonNotFound    Reason = "NOT_FOUND"
	ReasonNoTuner     Reason = "NO_TUNER"
	ReasonTimeout     Reason = "TIMEOUT"
	ReasonEarlyEOF    Reason = "EARLY_EOF"
	ReasonMalformed   Reason = "MALFORMED"
	ReasonOverLimit   Reason = "OVER_LIMIT"
	ReasonCancelled   Reason = "CANCELLED"
	ReasonInternal    Reason = "INTERNAL"
)

// Failureはcore境界を越えて公開する唯一のProvider errorである。
type Failure struct {
	Reason     Reason
	Diagnostic string
}

// Errorは分類とbounded diagnosticから安定した文字列を返す。
func (e *Failure) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Diagnostic == "" {
		return "provider: " + string(e.Reason)
	}
	return "provider: " + string(e.Reason) + ": " + e.Diagnostic
}

// NewFailureは上限のないdiagnosticが境界を越える前に拒否する。
func NewFailure(reason Reason, diagnostic string) *Failure {
	if len(diagnostic) > MaxDiagnosticBytes {
		diagnostic = "diagnostic-over-limit"
		reason = ReasonOverLimit
	}
	return &Failure{Reason: reason, Diagnostic: diagnostic}
}

// IsReasonはerror chainに指定したProvider失敗分類が含まれるかを返す。
func IsReason(err error, reason Reason) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.Reason == reason
}

// ContextFailureはcontextのcancelとdeadlineを安定したProvider失敗分類へ変換する。
func ContextFailure(ctx context.Context) error {
	if ctx == nil {
		return NewFailure(ReasonInternal, "nil-context")
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return NewFailure(ReasonTimeout, "context-deadline")
	case errors.Is(ctx.Err(), context.Canceled):
		return NewFailure(ReasonCancelled, "context-cancelled")
	default:
		return nil
	}
}

// Provenanceは観測値を取得したbackendとrevisionを識別する。
type Provenance struct {
	Backend  string
	Revision string
}

// NewProvenanceは空値と過大な識別子を拒否してProvenanceを生成する。
func NewProvenance(backend, revision string) (Provenance, error) {
	if backend == "" || revision == "" {
		return Provenance{}, NewFailure(ReasonMalformed, "missing-provenance")
	}
	if len(backend) > MaxDiagnosticBytes || len(revision) > MaxDiagnosticBytes {
		return Provenance{}, NewFailure(ReasonOverLimit, "provenance-over-limit")
	}
	return Provenance{Backend: backend, Revision: revision}, nil
}

// CapabilityはProviderが申告する独立機能を表す。
type Capability string

const (
	CapabilityCatalog Capability = "CATALOG"
	CapabilityStream  Capability = "STREAM"
	CapabilityHealth  Capability = "HEALTH"
)

// ValidationStateはProvider由来の値を採用できるかを表す。
type ValidationState string

const (
	ValidationValid   ValidationState = "VALID"
	ValidationInvalid ValidationState = "INVALID"
	ValidationUnknown ValidationState = "UNKNOWN"
)

// TuningTargetはdomain/applicationから意図的にopaqueとして扱う。
type TuningTarget struct{ Opaque string }

// NewTuningTargetは空値と上限超過を拒否し、opaqueなtuning targetを生成する。
func NewTuningTarget(value string) (TuningTarget, error) {
	if value == "" {
		return TuningTarget{}, NewFailure(ReasonMalformed, "empty-tuning-target")
	}
	if len(value) > MaxDiagnosticBytes {
		return TuningTarget{}, NewFailure(ReasonOverLimit, "tuning-target-over-limit")
	}
	return TuningTarget{Opaque: value}, nil
}

// MonotonicDeadlineは再現可能なadapterで使う、注入された単調時刻の期限である。
type MonotonicDeadline struct {
	Enabled bool
	Nanos   int64
}

// EffectiveLimitはrequest値が正のhard limit内に収まることを確認する。
func EffectiveLimit(requested, hard int) (int, error) {
	if hard <= 0 {
		return 0, fmt.Errorf("invalid hard limit: %d", hard)
	}
	if requested <= 0 || requested > hard {
		return 0, NewFailure(ReasonOverLimit, "effective-limit-out-of-range")
	}
	return requested, nil
}
