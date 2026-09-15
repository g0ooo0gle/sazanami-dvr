DROP TRIGGER program_revisions_no_delete;

CREATE INDEX catalog_gc_sync_terminal_idx
    ON catalog_syncs (state, finished_at_utc_ms, id);
CREATE INDEX catalog_gc_program_instance_cutoff_idx
    ON program_instances (last_seen_at_utc_ms, id);
CREATE INDEX catalog_gc_service_cutoff_idx
    ON services (last_seen_at_utc_ms, id);
CREATE INDEX catalog_gc_program_observation_revision_idx
    ON program_observations (program_revision_id, sequence);
CREATE INDEX catalog_gc_program_observation_instance_idx
    ON program_observations (program_instance_id, sequence);
CREATE INDEX catalog_gc_service_observation_service_idx
    ON service_observations (service_id, sequence);
CREATE INDEX catalog_gc_reservation_revision_idx
    ON reservations (program_revision_id, id);
CREATE INDEX catalog_gc_reservation_instance_idx
    ON reservations (program_instance_id, id);
