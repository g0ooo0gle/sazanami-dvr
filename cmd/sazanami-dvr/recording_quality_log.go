package main

import (
	"fmt"
	"io"
	"sync"

	app "github.com/g0ooo0gle/sazanami-dvr/internal/app/recording"
)

func observeRecordingQuality(writer io.Writer) func(app.QualityObservation) {
	var mutex sync.Mutex
	return func(event app.QualityObservation) {
		if writer == nil || event.Ordinal < 0 || event.Ordinal > 1 || event.Quality.Validate() != nil {
			return
		}
		switch event.Phase {
		case "start", "continue", "fallback", "reconnect", "connection-end", "final":
		default:
			return
		}
		q := event.Quality
		mutex.Lock()
		defer mutex.Unlock()
		_, _ = fmt.Fprintf(writer, "recording_quality ordinal=%d phase=%s status=%s selection_unverified=%t observation_limited=%t counters_saturated=%t cc_gap_events=%d cc_duplicate_events=%d tei_packets=%d malformed_packet_events=%d psi_continuity_events=%d psi_crc_events=%d psi_structure_events=%d psi_limit_events=%d sync_loss_events=%d sync_recovered_events=%d sync_discarded_bytes=%d trailing_incomplete_bytes=%d unfinished_psi_events=%d fallback_events=%d reconnect_count=%d\n",
			event.Ordinal, event.Phase, q.Status.String(), q.SelectionUnverified, q.ObservationLimited, q.CountersSaturated,
			q.CCGapEvents, q.CCDuplicateEvents, q.TEIPackets, q.MalformedPacketEvents,
			q.PSIContinuityEvents, q.PSICRCEvents, q.PSIStructureEvents, q.PSILimitEvents,
			q.SyncLossEvents, q.SyncRecoveredEvents, q.SyncDiscardedBytes, q.TrailingIncompleteBytes,
			q.UnfinishedPSIEvents, q.FallbackEvents, q.ReconnectCount)
	}
}
