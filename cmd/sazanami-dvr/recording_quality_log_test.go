package main

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	app "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

func TestQualityLogBounds(t *testing.T) {
	var output bytes.Buffer
	observe := observeRecordingQuality(&output)
	var wait sync.WaitGroup
	for i := 0; i < 100; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			observe(app.QualityObservation{Ordinal: 1, Phase: "final", Quality: core.QualitySummary{
				Status: core.QualityDegraded, CCGapEvents: 2147483647,
			}})
		}()
	}
	wait.Wait()
	for _, bad := range []app.QualityObservation{
		{Ordinal: 2, Phase: "final"}, {Phase: "private/path\n"},
		{Phase: strings.Repeat("x", 3000)}, {Phase: "final", Quality: core.QualitySummary{CCGapEvents: -1}},
	} {
		observe(bad)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 100 {
		t.Fatalf("lines=%d", len(lines))
	}
	for _, line := range lines {
		if len(line) > 2048 || !strings.HasPrefix(line, "recording_quality ordinal=1 phase=final status=DEGRADED ") ||
			!strings.Contains(line, "cc_gap_events=2147483647") || strings.Contains(line, "private") {
			t.Fatalf("line=%q", line)
		}
	}
}
