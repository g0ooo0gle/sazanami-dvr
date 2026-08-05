CREATE TABLE reservations (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    version INTEGER NOT NULL CHECK (version > 0),
    state TEXT NOT NULL CHECK (state IN ('ACTIVE', 'FINISHED')),
    program_instance_id BLOB NOT NULL REFERENCES program_instances(id) ON DELETE RESTRICT,
    program_revision_id BLOB NOT NULL REFERENCES program_revisions(id) ON DELETE RESTRICT,
    backend_instance_id BLOB NOT NULL REFERENCES backend_instances(id) ON DELETE RESTRICT,
    provider_service_locator TEXT NOT NULL CHECK (length(provider_service_locator) BETWEEN 1 AND 256),
    tuning_target TEXT NOT NULL CHECK (length(tuning_target) BETWEEN 1 AND 256),
    network_id INTEGER NOT NULL CHECK (network_id BETWEEN 0 AND 65535),
    transport_stream_id INTEGER NOT NULL CHECK (transport_stream_id BETWEEN 0 AND 65535),
    service_id INTEGER NOT NULL CHECK (service_id BETWEEN 0 AND 65535),
    event_id INTEGER NOT NULL CHECK (event_id BETWEEN 0 AND 65535),
    title TEXT NOT NULL CHECK (length(CAST(title AS BLOB)) <= 4096),
    station_name TEXT NOT NULL CHECK (length(CAST(station_name AS BLOB)) <= 4096),
    start_at_utc_ms INTEGER NOT NULL CHECK (start_at_utc_ms >= 0),
    duration_seconds INTEGER NOT NULL CHECK (duration_seconds BETWEEN 1 AND 86400),
    start_margin_seconds INTEGER NOT NULL DEFAULT 0 CHECK (start_margin_seconds = 0),
    end_margin_seconds INTEGER NOT NULL DEFAULT 0 CHECK (end_margin_seconds = 0),
    requested_priority INTEGER NOT NULL CHECK (requested_priority BETWEEN 1 AND 5),
    requested_follow INTEGER NOT NULL CHECK (requested_follow IN (0, 1)),
    effective_follow INTEGER NOT NULL DEFAULT 0 CHECK (effective_follow = 0),
    created_at_utc_ms INTEGER NOT NULL CHECK (created_at_utc_ms >= 0),
    updated_at_utc_ms INTEGER NOT NULL CHECK (updated_at_utc_ms >= created_at_utc_ms),
    finished_at_utc_ms INTEGER,
    CHECK ((state = 'ACTIVE' AND finished_at_utc_ms IS NULL) OR
           (state = 'FINISHED' AND finished_at_utc_ms IS NOT NULL AND finished_at_utc_ms >= created_at_utc_ms))
) STRICT;

CREATE TABLE ctrlcmd_reservation_ids (
    reserve_id INTEGER PRIMARY KEY AUTOINCREMENT CHECK (reserve_id BETWEEN 1 AND 2147483647),
    reservation_id BLOB NOT NULL UNIQUE REFERENCES reservations(id) ON DELETE RESTRICT
) STRICT;

CREATE TABLE recording_attempts (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    reservation_id BLOB NOT NULL UNIQUE REFERENCES reservations(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN (
        'CLAIMED', 'STARTING', 'RECORDING', 'FINALIZING',
        'SUCCEEDED', 'PARTIAL', 'FAILED', 'CANCELLED', 'MISSED'
    )),
    state_version INTEGER NOT NULL CHECK (state_version > 0),
    planned_start_utc_ms INTEGER NOT NULL CHECK (planned_start_utc_ms >= 0),
    planned_end_utc_ms INTEGER NOT NULL CHECK (planned_end_utc_ms > planned_start_utc_ms),
    actual_start_utc_ms INTEGER,
    actual_end_utc_ms INTEGER,
    byte_count INTEGER NOT NULL DEFAULT 0 CHECK (byte_count >= 0),
    owner_instance_id BLOB NOT NULL CHECK (length(owner_instance_id) = 16),
    owner_generation INTEGER NOT NULL CHECK (owner_generation > 0),
    heartbeat_utc_ms INTEGER NOT NULL CHECK (heartbeat_utc_ms >= 0),
    finalization_token BLOB CHECK (finalization_token IS NULL OR length(finalization_token) = 16),
    terminal_reason TEXT CHECK (terminal_reason IS NULL OR length(terminal_reason) BETWEEN 1 AND 96),
    recovered INTEGER NOT NULL DEFAULT 0 CHECK (recovered IN (0, 1)),
    created_at_utc_ms INTEGER NOT NULL CHECK (created_at_utc_ms >= 0),
    updated_at_utc_ms INTEGER NOT NULL CHECK (updated_at_utc_ms >= created_at_utc_ms),
    CHECK (actual_end_utc_ms IS NULL OR
           (actual_start_utc_ms IS NOT NULL AND actual_end_utc_ms >= actual_start_utc_ms)),
    CHECK ((state IN ('SUCCEEDED', 'PARTIAL', 'FAILED', 'CANCELLED', 'MISSED')) =
           (terminal_reason IS NOT NULL))
) STRICT;

CREATE TABLE recording_segments (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    attempt_id BLOB NOT NULL REFERENCES recording_attempts(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    state TEXT NOT NULL CHECK (state IN ('PLANNED', 'WRITING', 'PARTIAL', 'FINALIZED')),
    relative_partial_path TEXT NOT NULL UNIQUE CHECK (length(relative_partial_path) BETWEEN 1 AND 512),
    relative_final_path TEXT NOT NULL UNIQUE CHECK (length(relative_final_path) BETWEEN 1 AND 512),
    byte_count INTEGER NOT NULL DEFAULT 0 CHECK (byte_count >= 0),
    file_synced INTEGER NOT NULL DEFAULT 0 CHECK (file_synced IN (0, 1)),
    final_published INTEGER NOT NULL DEFAULT 0 CHECK (final_published IN (0, 1)),
    directory_synced INTEGER NOT NULL DEFAULT 0 CHECK (directory_synced IN (0, 1)),
    availability TEXT NOT NULL CHECK (availability IN ('PLANNED', 'PARTIAL', 'FINAL', 'MISSING', 'MISMATCHED')),
    integrity_reason TEXT CHECK (integrity_reason IS NULL OR length(integrity_reason) BETWEEN 1 AND 96),
    created_at_utc_ms INTEGER NOT NULL CHECK (created_at_utc_ms >= 0),
    updated_at_utc_ms INTEGER NOT NULL CHECK (updated_at_utc_ms >= created_at_utc_ms),
    UNIQUE (attempt_id, ordinal)
) STRICT;

CREATE UNIQUE INDEX reservations_one_active_program_idx
    ON reservations (backend_instance_id, network_id, transport_stream_id, service_id, event_id)
    WHERE state = 'ACTIVE';
CREATE INDEX reservations_due_idx
    ON reservations (start_at_utc_ms, id)
    WHERE state = 'ACTIVE';
CREATE INDEX recording_attempts_recovery_idx
    ON recording_attempts (state, heartbeat_utc_ms, id)
    WHERE state IN ('CLAIMED', 'STARTING', 'RECORDING', 'FINALIZING');
CREATE INDEX recording_segments_attempt_idx
    ON recording_segments (attempt_id, ordinal);
