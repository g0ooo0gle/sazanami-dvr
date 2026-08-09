ALTER TABLE recording_attempts ADD COLUMN stop_requested_at_utc_ms INTEGER
    CHECK (stop_requested_at_utc_ms IS NULL OR stop_requested_at_utc_ms >= 0);
ALTER TABLE recording_attempts ADD COLUMN planned_final_state TEXT
    CHECK (planned_final_state IS NULL OR planned_final_state IN ('SUCCEEDED', 'PARTIAL'));
ALTER TABLE recording_attempts ADD COLUMN planned_terminal_reason TEXT
    CHECK (planned_terminal_reason IS NULL OR planned_terminal_reason IN (
        'COMPLETED', 'COMPLETED_AFTER_RECONNECT', 'USER_REQUESTED_STOP'
    ));

UPDATE recording_attempts SET
    planned_final_state = 'SUCCEEDED',
    planned_terminal_reason = CASE
        WHEN terminal_reason = 'COMPLETED_AFTER_RECONNECT' THEN 'COMPLETED_AFTER_RECONNECT'
        ELSE 'COMPLETED'
    END
WHERE state IN ('FINALIZING', 'SUCCEEDED');

CREATE TRIGGER recording_attempts_stop_plan_insert_check
BEFORE INSERT ON recording_attempts
WHEN
    (NEW.stop_requested_at_utc_ms IS NOT NULL AND (
        NEW.stop_requested_at_utc_ms < NEW.created_at_utc_ms OR
        NEW.state NOT IN ('CLAIMED', 'STARTING', 'RECORDING', 'FINALIZING', 'PARTIAL', 'FAILED', 'CANCELLED')
    )) OR
    ((NEW.planned_final_state IS NULL) <> (NEW.planned_terminal_reason IS NULL)) OR
    (NEW.planned_final_state = 'SUCCEEDED' AND NEW.planned_terminal_reason NOT IN ('COMPLETED', 'COMPLETED_AFTER_RECONNECT')) OR
    (NEW.planned_final_state = 'PARTIAL' AND NEW.planned_terminal_reason <> 'USER_REQUESTED_STOP')
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
    (NEW.planned_final_state = 'PARTIAL' AND (
        NEW.planned_terminal_reason <> 'USER_REQUESTED_STOP' OR NEW.stop_requested_at_utc_ms IS NULL
    )) OR
    (NEW.state = 'FINALIZING' AND NEW.planned_final_state IS NULL) OR
    (NEW.state = 'SUCCEEDED' AND NEW.planned_final_state <> 'SUCCEEDED') OR
    (NEW.state = 'PARTIAL' AND NEW.planned_final_state IS NOT NULL AND NEW.planned_final_state <> 'PARTIAL') OR
    (NEW.state = 'CANCELLED' AND NEW.stop_requested_at_utc_ms IS NOT NULL AND NEW.terminal_reason <> 'USER_REQUESTED_STOP')
BEGIN
    SELECT RAISE(ABORT, 'invalid recording stop or finalization plan');
END;
