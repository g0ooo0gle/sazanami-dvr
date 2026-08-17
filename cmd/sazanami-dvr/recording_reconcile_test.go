package main

import (
	"bytes"
	"strings"
	"testing"

	recordingapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
)

func TestCompletedRecordingReconcileOutputIsBoundedAndRedacted(t *testing.T) {
	var output, diagnostic bytes.Buffer
	observe := observeCompletedRecordingReconcile(&output, &diagnostic)
	observe(recordingapp.CompletedReconcileEvent{
		Completed: true, Checked: 3, Changed: 2, Missing: 1, Mismatched: 1, DurationMS: 4,
	})
	observe(recordingapp.CompletedReconcileEvent{Reason: "recording-reconcile-read-failed", DurationMS: 5})
	wantOutput := "recording_reconcile result=completed checked=3 changed=2 missing=1 mismatched=1 duration_ms=4\n"
	wantDiagnostic := "recording_reconcile result=failed reason=recording-reconcile-read-failed duration_ms=5\n"
	if output.String() != wantOutput || diagnostic.String() != wantDiagnostic {
		t.Fatalf("output=%q diagnostic=%q", output.String(), diagnostic.String())
	}
	for _, private := range []string{"/home/", "recordings/", "番組", "private", "http://"} {
		if strings.Contains(output.String(), private) || strings.Contains(diagnostic.String(), private) {
			t.Fatalf("private value=%q", private)
		}
	}
}
