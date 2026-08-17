package main

import (
	"fmt"
	"io"

	recordingapp "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
)

func observeCompletedRecordingReconcile(stdout, stderr io.Writer) func(recordingapp.CompletedReconcileEvent) {
	return func(event recordingapp.CompletedReconcileEvent) {
		if event.Completed {
			fmt.Fprintf(stdout, "recording_reconcile result=completed checked=%d changed=%d missing=%d mismatched=%d duration_ms=%d\n",
				event.Checked, event.Changed, event.Missing, event.Mismatched, event.DurationMS)
			return
		}
		fmt.Fprintf(stderr, "recording_reconcile result=failed reason=%s duration_ms=%d\n", event.Reason, event.DurationMS)
	}
}
