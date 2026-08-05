PRAGMA defer_foreign_keys = ON;

CREATE TABLE backend_instances_v2 (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    provider_kind TEXT NOT NULL CHECK (provider_kind IN ('FAKE', 'MIRAKURUN')),
    identity_hash BLOB NOT NULL UNIQUE CHECK (length(identity_hash) = 32),
    reported_version TEXT CHECK (reported_version IS NULL OR length(reported_version) <= 128),
    source_ref TEXT CHECK (source_ref IS NULL OR length(source_ref) <= 256),
    created_at_utc_ms INTEGER NOT NULL,
    last_seen_at_utc_ms INTEGER NOT NULL CHECK (last_seen_at_utc_ms >= created_at_utc_ms)
) STRICT;

INSERT INTO backend_instances_v2
    (id, provider_kind, identity_hash, reported_version, source_ref, created_at_utc_ms, last_seen_at_utc_ms)
SELECT id, provider_kind, identity_hash, reported_version, source_ref, created_at_utc_ms, last_seen_at_utc_ms
FROM backend_instances;

CREATE TABLE catalog_syncs_v2 (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    backend_instance_id BLOB NOT NULL REFERENCES backend_instances_v2(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('RUNNING', 'COMPLETED', 'FAILED')),
    started_at_utc_ms INTEGER NOT NULL,
    finished_at_utc_ms INTEGER,
    service_count INTEGER NOT NULL DEFAULT 0 CHECK (service_count BETWEEN 0 AND 10000),
    program_count INTEGER NOT NULL DEFAULT 0 CHECK (program_count BETWEEN 0 AND 262144),
    failure_reason TEXT CHECK (failure_reason IS NULL OR length(failure_reason) <= 96),
    correlation_id TEXT NOT NULL CHECK (length(correlation_id) BETWEEN 1 AND 128),
    CHECK (finished_at_utc_ms IS NULL OR finished_at_utc_ms >= started_at_utc_ms),
    CHECK ((state = 'RUNNING' AND finished_at_utc_ms IS NULL AND failure_reason IS NULL) OR
           (state = 'COMPLETED' AND finished_at_utc_ms IS NOT NULL AND failure_reason IS NULL) OR
           (state = 'FAILED' AND finished_at_utc_ms IS NOT NULL AND failure_reason IS NOT NULL))
) STRICT;

INSERT INTO catalog_syncs_v2
    (id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms,
     service_count, program_count, failure_reason, correlation_id)
SELECT id, backend_instance_id, state, started_at_utc_ms, finished_at_utc_ms,
       service_count, program_count, failure_reason, correlation_id
FROM catalog_syncs;

DROP TABLE catalog_syncs;
DROP TABLE backend_instances;
ALTER TABLE backend_instances_v2 RENAME TO backend_instances;
ALTER TABLE catalog_syncs_v2 RENAME TO catalog_syncs;

CREATE INDEX catalog_syncs_completed_backend_idx
    ON catalog_syncs (backend_instance_id, finished_at_utc_ms DESC, id)
    WHERE state = 'COMPLETED';
CREATE UNIQUE INDEX catalog_syncs_one_running_backend_idx
    ON catalog_syncs (backend_instance_id)
    WHERE state = 'RUNNING';
