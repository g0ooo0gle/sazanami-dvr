CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64),
    checksum BLOB NOT NULL CHECK (length(checksum) = 32),
    applied_at_utc_ms INTEGER NOT NULL
) STRICT;

CREATE TRIGGER schema_migrations_no_update
    BEFORE UPDATE ON schema_migrations
    BEGIN SELECT RAISE(ABORT, 'schema_migrations is insert-only'); END;
CREATE TRIGGER schema_migrations_no_delete
    BEFORE DELETE ON schema_migrations
    BEGIN SELECT RAISE(ABORT, 'schema_migrations is insert-only'); END;

CREATE TABLE backend_instances (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    provider_kind TEXT NOT NULL CHECK (provider_kind IN ('FAKE')),
    identity_hash BLOB NOT NULL UNIQUE CHECK (length(identity_hash) = 32),
    reported_version TEXT CHECK (reported_version IS NULL OR length(reported_version) <= 128),
    source_ref TEXT CHECK (source_ref IS NULL OR length(source_ref) <= 256),
    created_at_utc_ms INTEGER NOT NULL,
    last_seen_at_utc_ms INTEGER NOT NULL CHECK (last_seen_at_utc_ms >= created_at_utc_ms)
) STRICT;

CREATE TABLE catalog_syncs (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    backend_instance_id BLOB NOT NULL REFERENCES backend_instances(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('RUNNING', 'COMPLETED', 'FAILED')),
    started_at_utc_ms INTEGER NOT NULL,
    finished_at_utc_ms INTEGER,
    service_count INTEGER NOT NULL DEFAULT 0 CHECK (service_count BETWEEN 0 AND 10000),
    program_count INTEGER NOT NULL DEFAULT 0 CHECK (program_count BETWEEN 0 AND 100000),
    failure_reason TEXT CHECK (failure_reason IS NULL OR length(failure_reason) <= 96),
    correlation_id TEXT NOT NULL CHECK (length(correlation_id) BETWEEN 1 AND 128),
    CHECK (finished_at_utc_ms IS NULL OR finished_at_utc_ms >= started_at_utc_ms),
    CHECK ((state = 'RUNNING' AND finished_at_utc_ms IS NULL AND failure_reason IS NULL) OR
           (state = 'COMPLETED' AND finished_at_utc_ms IS NOT NULL AND failure_reason IS NULL) OR
           (state = 'FAILED' AND finished_at_utc_ms IS NOT NULL AND failure_reason IS NOT NULL))
) STRICT;

CREATE TABLE services (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    backend_instance_id BLOB NOT NULL REFERENCES backend_instances(id) ON DELETE RESTRICT,
    provider_locator TEXT NOT NULL CHECK (length(provider_locator) BETWEEN 1 AND 256),
    identity_state TEXT NOT NULL CHECK (identity_state IN ('VERIFIED', 'PROVISIONAL')),
    created_at_utc_ms INTEGER NOT NULL,
    last_seen_at_utc_ms INTEGER NOT NULL CHECK (last_seen_at_utc_ms >= created_at_utc_ms),
    UNIQUE (backend_instance_id, provider_locator)
) STRICT;

CREATE TABLE service_observations (
    sequence INTEGER PRIMARY KEY,
    sync_id BLOB NOT NULL REFERENCES catalog_syncs(id) ON DELETE RESTRICT,
    service_id BLOB NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
    provider_locator TEXT NOT NULL CHECK (length(provider_locator) BETWEEN 1 AND 256),
    network_id INTEGER,
    transport_stream_id INTEGER,
    service_number INTEGER,
    broadcast_kind TEXT CHECK (broadcast_kind IS NULL OR length(broadcast_kind) <= 32),
    display_name TEXT NOT NULL CHECK (length(CAST(display_name AS BLOB)) <= 4096),
    tuning_target TEXT CHECK (tuning_target IS NULL OR length(tuning_target) <= 256),
    validation_state TEXT NOT NULL CHECK (validation_state IN ('VALID', 'PROVISIONAL', 'INVALID')),
    validation_reason TEXT CHECK (validation_reason IS NULL OR length(validation_reason) <= 96),
    observation_hash BLOB NOT NULL CHECK (length(observation_hash) = 32),
    UNIQUE (sync_id, provider_locator)
) STRICT;

CREATE TABLE program_instances (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    service_id BLOB NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
    provider_event_locator TEXT NOT NULL CHECK (length(provider_event_locator) BETWEEN 1 AND 256),
    raw_event_id INTEGER,
    identity_state TEXT NOT NULL CHECK (identity_state IN ('VERIFIED', 'PROVISIONAL', 'AMBIGUOUS')),
    created_at_utc_ms INTEGER NOT NULL,
    last_seen_at_utc_ms INTEGER NOT NULL CHECK (last_seen_at_utc_ms >= created_at_utc_ms),
    UNIQUE (service_id, provider_event_locator)
) STRICT;

CREATE TABLE program_revisions (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    program_instance_id BLOB NOT NULL REFERENCES program_instances(id) ON DELETE RESTRICT,
    revision_number INTEGER NOT NULL CHECK (revision_number > 0),
    content_hash BLOB NOT NULL CHECK (length(content_hash) = 32),
    start_at_utc_ms INTEGER,
    duration_ms INTEGER CHECK (duration_ms IS NULL OR duration_ms > 0),
    title TEXT CHECK (title IS NULL OR length(CAST(title AS BLOB)) <= 4096),
    description TEXT CHECK (description IS NULL OR length(CAST(description AS BLOB)) <= 65536),
    free_access INTEGER CHECK (free_access IS NULL OR free_access IN (0, 1)),
    validation_state TEXT NOT NULL CHECK (validation_state IN ('VALID', 'PROVISIONAL', 'INVALID')),
    created_at_utc_ms INTEGER NOT NULL,
    UNIQUE (program_instance_id, revision_number),
    UNIQUE (program_instance_id, content_hash)
) STRICT;

CREATE TRIGGER program_revisions_no_update
    BEFORE UPDATE ON program_revisions
    BEGIN SELECT RAISE(ABORT, 'program_revisions is insert-only'); END;
CREATE TRIGGER program_revisions_no_delete
    BEFORE DELETE ON program_revisions
    BEGIN SELECT RAISE(ABORT, 'program_revisions is insert-only'); END;

CREATE TABLE program_observations (
    sequence INTEGER PRIMARY KEY,
    sync_id BLOB NOT NULL REFERENCES catalog_syncs(id) ON DELETE RESTRICT,
    provider_service_locator TEXT NOT NULL CHECK (length(provider_service_locator) BETWEEN 1 AND 256),
    provider_event_locator TEXT NOT NULL CHECK (length(provider_event_locator) BETWEEN 1 AND 256),
    raw_event_id INTEGER,
    content_hash BLOB CHECK (content_hash IS NULL OR length(content_hash) = 32),
    program_instance_id BLOB REFERENCES program_instances(id) ON DELETE RESTRICT,
    program_revision_id BLOB REFERENCES program_revisions(id) ON DELETE RESTRICT,
    classification TEXT NOT NULL CHECK (classification IN ('SAME_CONTENT', 'VERIFIED_SUCCESSOR', 'AMBIGUOUS', 'NEW_INSTANCE', 'INVALID')),
    validation_reason TEXT CHECK (validation_reason IS NULL OR length(validation_reason) <= 96),
    CHECK ((program_instance_id IS NULL) = (program_revision_id IS NULL)),
    UNIQUE (sync_id, provider_service_locator, provider_event_locator)
) STRICT;

CREATE INDEX catalog_syncs_completed_backend_idx
    ON catalog_syncs (backend_instance_id, finished_at_utc_ms DESC, id)
    WHERE state = 'COMPLETED';
CREATE UNIQUE INDEX catalog_syncs_one_running_backend_idx
    ON catalog_syncs (backend_instance_id)
    WHERE state = 'RUNNING';
CREATE INDEX service_observations_sync_service_idx
    ON service_observations (sync_id, service_id, sequence);
CREATE INDEX program_instances_service_event_idx
    ON program_instances (service_id, provider_event_locator, last_seen_at_utc_ms DESC, id);
CREATE INDEX program_revisions_instance_number_idx
    ON program_revisions (program_instance_id, revision_number DESC, id);
CREATE INDEX program_revisions_time_idx
    ON program_revisions (start_at_utc_ms, id)
    WHERE start_at_utc_ms IS NOT NULL;
CREATE INDEX program_observations_sync_instance_idx
    ON program_observations (sync_id, program_instance_id, sequence);
