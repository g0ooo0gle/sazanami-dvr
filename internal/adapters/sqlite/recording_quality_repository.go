package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/recording"
)

var qualityColumnNames = [...]string{
	"quality_status", "selection_unverified", "observation_limited", "quality_counters_saturated",
	"cc_gap_events", "cc_duplicate_events", "tei_packets", "malformed_packet_events",
	"psi_continuity_events", "psi_crc_events", "psi_structure_events", "psi_limit_events",
	"sync_loss_events", "sync_recovered_events", "sync_discarded_bytes", "trailing_incomplete_bytes",
	"unfinished_psi_events", "fallback_events", "reconnect_count",
}

const communicationPartialPredicate = `(a.state='PARTIAL'
	AND a.terminal_reason IN ('STREAM_ENDED_EARLY','STREAM_TIMEOUT','STREAM_RECONNECT_EXHAUSTED')
	AND a.planned_final_state='PARTIAL' AND a.planned_terminal_reason=a.terminal_reason
	AND a.finalization_token IS NOT NULL AND a.byte_count>=188 AND a.byte_count%188=0
	AND a.actual_end_utc_ms-a.actual_start_utc_ms>=1000)`

func qualityColumns(alias string, nullable bool) string {
	columns := make([]string, len(qualityColumnNames))
	for i, name := range qualityColumnNames {
		columns[i] = alias + "." + name
		if nullable {
			fallback := "0"
			if i == 0 {
				fallback = "'UNKNOWN'"
			}
			columns[i] = "COALESCE(" + columns[i] + "," + fallback + ")"
		}
	}
	return strings.Join(columns, ",")
}

func qualityValues(q core.QualitySummary) []any {
	return []any{q.Status.String(), q.SelectionUnverified, q.ObservationLimited, q.CountersSaturated,
		q.CCGapEvents, q.CCDuplicateEvents, q.TEIPackets, q.MalformedPacketEvents,
		q.PSIContinuityEvents, q.PSICRCEvents, q.PSIStructureEvents, q.PSILimitEvents,
		q.SyncLossEvents, q.SyncRecoveredEvents, q.SyncDiscardedBytes, q.TrailingIncompleteBytes,
		q.UnfinishedPSIEvents, q.FallbackEvents, q.ReconnectCount}
}

func qualityAssignments() string {
	columns := make([]string, len(qualityColumnNames))
	for i, name := range qualityColumnNames {
		columns[i] = name + "=?"
	}
	return strings.Join(columns, ",")
}

func saveQuality(ctx context.Context, tx *sql.Tx, attemptID catalogmodel.ID, ordinal int, q core.QualitySummary) error {
	if q.Validate() != nil {
		return errors.New("sqlite: invalid recording quality")
	}
	args := append(qualityValues(q), attemptID.Bytes(), ordinal)
	result, err := tx.ExecContext(ctx, `UPDATE recording_segments SET `+qualityAssignments()+` WHERE attempt_id=? AND ordinal=?`, args...)
	if err != nil {
		return sanitize("save-recording-quality", err)
	}
	if affected(result) != 1 {
		return ErrAttemptState
	}
	return nil
}

type qualityScanner struct {
	status                        string
	selection, limited, saturated int64
	summary                       core.QualitySummary
}

func (scan *qualityScanner) destinations() []any {
	q := &scan.summary
	return []any{&scan.status, &scan.selection, &scan.limited, &scan.saturated,
		&q.CCGapEvents, &q.CCDuplicateEvents, &q.TEIPackets, &q.MalformedPacketEvents,
		&q.PSIContinuityEvents, &q.PSICRCEvents, &q.PSIStructureEvents, &q.PSILimitEvents,
		&q.SyncLossEvents, &q.SyncRecoveredEvents, &q.SyncDiscardedBytes, &q.TrailingIncompleteBytes,
		&q.UnfinishedPSIEvents, &q.FallbackEvents, &q.ReconnectCount}
}

func (scan qualityScanner) value() (core.QualitySummary, error) {
	q := scan.summary
	status, err := core.ParseQualityStatus(scan.status)
	if err != nil || scan.selection < 0 || scan.selection > 1 || scan.limited < 0 || scan.limited > 1 || scan.saturated < 0 || scan.saturated > 1 {
		return core.QualitySummary{}, errors.New("sqlite: corrupt recording quality")
	}
	q.Status, q.SelectionUnverified, q.ObservationLimited, q.CountersSaturated = status, scan.selection == 1, scan.limited == 1, scan.saturated == 1
	if err := q.Validate(); err != nil {
		return core.QualitySummary{}, errors.New("sqlite: corrupt recording quality")
	}
	return q, nil
}
