DROP TRIGGER recording_attempts_stop_plan_insert_check;
DROP TRIGGER recording_attempts_stop_plan_update_check;
ALTER TABLE recording_attempts RENAME COLUMN planned_terminal_reason TO legacy_planned_terminal_reason;
ALTER TABLE recording_attempts ADD COLUMN planned_terminal_reason TEXT
    CHECK (planned_terminal_reason IS NULL OR planned_terminal_reason IN (
        'COMPLETED', 'COMPLETED_AFTER_RECONNECT', 'USER_REQUESTED_STOP',
        'STREAM_ENDED_EARLY', 'STREAM_TIMEOUT', 'STREAM_RECONNECT_EXHAUSTED'
    ));
UPDATE recording_attempts SET planned_terminal_reason=legacy_planned_terminal_reason;

ALTER TABLE recording_segments ADD COLUMN quality_status TEXT NOT NULL DEFAULT 'UNKNOWN'
    CHECK (quality_status IN ('UNKNOWN', 'NO_ISSUES_OBSERVED', 'DEGRADED'));
ALTER TABLE recording_segments ADD COLUMN selection_unverified INTEGER NOT NULL DEFAULT 0
    CHECK (selection_unverified BETWEEN 0 AND 1);
ALTER TABLE recording_segments ADD COLUMN observation_limited INTEGER NOT NULL DEFAULT 0
    CHECK (observation_limited BETWEEN 0 AND 1);
ALTER TABLE recording_segments ADD COLUMN quality_counters_saturated INTEGER NOT NULL DEFAULT 0
    CHECK (quality_counters_saturated BETWEEN 0 AND 1);
ALTER TABLE recording_segments ADD COLUMN cc_gap_events INTEGER NOT NULL DEFAULT 0
    CHECK (cc_gap_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN cc_duplicate_events INTEGER NOT NULL DEFAULT 0
    CHECK (cc_duplicate_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN tei_packets INTEGER NOT NULL DEFAULT 0
    CHECK (tei_packets BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN malformed_packet_events INTEGER NOT NULL DEFAULT 0
    CHECK (malformed_packet_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN psi_continuity_events INTEGER NOT NULL DEFAULT 0
    CHECK (psi_continuity_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN psi_crc_events INTEGER NOT NULL DEFAULT 0
    CHECK (psi_crc_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN psi_structure_events INTEGER NOT NULL DEFAULT 0
    CHECK (psi_structure_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN psi_limit_events INTEGER NOT NULL DEFAULT 0
    CHECK (psi_limit_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN sync_loss_events INTEGER NOT NULL DEFAULT 0
    CHECK (sync_loss_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN sync_recovered_events INTEGER NOT NULL DEFAULT 0
    CHECK (sync_recovered_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN sync_discarded_bytes INTEGER NOT NULL DEFAULT 0
    CHECK (sync_discarded_bytes BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN trailing_incomplete_bytes INTEGER NOT NULL DEFAULT 0
    CHECK (trailing_incomplete_bytes BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN unfinished_psi_events INTEGER NOT NULL DEFAULT 0
    CHECK (unfinished_psi_events BETWEEN 0 AND 2147483647);
ALTER TABLE recording_segments ADD COLUMN fallback_events INTEGER NOT NULL DEFAULT 0
    CHECK (fallback_events BETWEEN 0 AND 1);
ALTER TABLE recording_segments ADD COLUMN reconnect_count INTEGER NOT NULL DEFAULT 0
    CHECK (reconnect_count BETWEEN 0 AND 3);

CREATE TRIGGER recording_attempts_stop_plan_insert_check
BEFORE INSERT ON recording_attempts
WHEN
    (NEW.stop_requested_at_utc_ms IS NOT NULL AND (
        NEW.stop_requested_at_utc_ms < NEW.created_at_utc_ms OR
        NEW.state NOT IN ('CLAIMED', 'STARTING', 'RECORDING', 'FINALIZING', 'PARTIAL', 'FAILED', 'CANCELLED')
    )) OR
    ((NEW.planned_final_state IS NULL) <> (NEW.planned_terminal_reason IS NULL)) OR
    (NEW.planned_final_state = 'SUCCEEDED' AND NEW.planned_terminal_reason NOT IN ('COMPLETED', 'COMPLETED_AFTER_RECONNECT')) OR
    (NEW.planned_final_state = 'PARTIAL' AND NEW.planned_terminal_reason NOT IN (
        'USER_REQUESTED_STOP', 'STREAM_ENDED_EARLY', 'STREAM_TIMEOUT', 'STREAM_RECONNECT_EXHAUSTED'
    ))
BEGIN
    SELECT RAISE(ABORT, 'invalid recording stop or finalization plan');
END;

CREATE TRIGGER recording_attempts_stop_plan_update_check
BEFORE UPDATE ON recording_attempts
WHEN
    (OLD.stop_requested_at_utc_ms IS NOT NULL AND NEW.stop_requested_at_utc_ms IS NOT OLD.stop_requested_at_utc_ms) OR
    (NEW.stop_requested_at_utc_ms IS NOT NULL AND (
        NEW.stop_requested_at_utc_ms < NEW.created_at_utc_ms OR
        NEW.state NOT IN ('CLAIMED', 'STARTING', 'RECORDING', 'FINALIZING', 'PARTIAL', 'FAILED', 'CANCELLED')
    )) OR
    ((NEW.planned_final_state IS NULL) <> (NEW.planned_terminal_reason IS NULL)) OR
    (NEW.planned_final_state = 'SUCCEEDED' AND NEW.planned_terminal_reason NOT IN ('COMPLETED', 'COMPLETED_AFTER_RECONNECT')) OR
    (NEW.planned_final_state = 'PARTIAL' AND NEW.planned_terminal_reason NOT IN (
        'USER_REQUESTED_STOP', 'STREAM_ENDED_EARLY', 'STREAM_TIMEOUT', 'STREAM_RECONNECT_EXHAUSTED'
    )) OR
    (NEW.planned_final_state = 'PARTIAL' AND NEW.planned_terminal_reason = 'USER_REQUESTED_STOP'
        AND NEW.stop_requested_at_utc_ms IS NULL) OR
    (NEW.state = 'FINALIZING' AND NEW.planned_final_state IS NULL) OR
    (NEW.state = 'SUCCEEDED' AND NEW.planned_final_state <> 'SUCCEEDED') OR
    (NEW.state = 'PARTIAL' AND NEW.planned_final_state IS NOT NULL AND NEW.planned_final_state <> 'PARTIAL') OR
    (NEW.state = 'CANCELLED' AND NEW.stop_requested_at_utc_ms IS NOT NULL AND NEW.terminal_reason <> 'USER_REQUESTED_STOP')
BEGIN
    SELECT RAISE(ABORT, 'invalid recording stop or finalization plan');
END;

