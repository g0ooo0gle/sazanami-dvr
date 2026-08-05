package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStableFailureReasonAndBoundedDiagnostic(t *testing.T) {
	err := NewFailure(ReasonUnavailable, "bounded")
	if !IsReason(err, ReasonUnavailable) || IsReason(err, ReasonTimeout) {
		t.Fatalf("err=%v", err)
	}
	long := NewFailure(ReasonInternal, strings.Repeat("x", MaxDiagnosticBytes+1))
	if long.Reason != ReasonOverLimit || long.Diagnostic != "diagnostic-over-limit" {
		t.Fatalf("long=%+v", long)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatal("errors.As failed")
	}
}

func TestContextFailure(t *testing.T) {
	if !IsReason(ContextFailure(nil), ReasonInternal) {
		t.Fatal("nil context")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if !IsReason(ContextFailure(cancelled), ReasonCancelled) {
		t.Fatal("cancelled context")
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if !IsReason(ContextFailure(deadline), ReasonTimeout) {
		t.Fatal("deadline context")
	}
}

func TestValueValidation(t *testing.T) {
	if _, err := NewProvenance("", "1"); !IsReason(err, ReasonMalformed) {
		t.Fatalf("provenance err=%v", err)
	}
	if _, err := NewTuningTarget(""); !IsReason(err, ReasonMalformed) {
		t.Fatalf("target err=%v", err)
	}
	if _, err := EffectiveLimit(0, MaxCatalogPage); !IsReason(err, ReasonOverLimit) {
		t.Fatalf("limit err=%v", err)
	}
	if got, err := EffectiveLimit(16, MaxCatalogPage); err != nil || got != 16 {
		t.Fatalf("got=%d err=%v", got, err)
	}
}
