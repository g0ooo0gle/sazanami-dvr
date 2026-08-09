CREATE TABLE automatic_reservation_rules (
    number INTEGER PRIMARY KEY AUTOINCREMENT CHECK (number BETWEEN 1 AND 2147483647),
    id BLOB NOT NULL UNIQUE CHECK (length(id) = 16),
    version INTEGER NOT NULL CHECK (version > 0),
    search_json TEXT NOT NULL,
    recording_json TEXT NOT NULL,
    created_at_utc_ms INTEGER NOT NULL CHECK (created_at_utc_ms >= 0),
    updated_at_utc_ms INTEGER NOT NULL CHECK (updated_at_utc_ms >= created_at_utc_ms),
    CHECK (length(CAST(search_json AS BLOB)) + length(CAST(recording_json AS BLOB)) <= 262144)
) STRICT;

CREATE TABLE automatic_reservation_matches (
    rule_id BLOB NOT NULL REFERENCES automatic_reservation_rules(id) ON DELETE CASCADE,
    program_instance_id BLOB NOT NULL UNIQUE REFERENCES program_instances(id) ON DELETE RESTRICT,
    reservation_id BLOB NOT NULL UNIQUE REFERENCES reservations(id) ON DELETE RESTRICT,
    created_at_utc_ms INTEGER NOT NULL CHECK (created_at_utc_ms >= 0),
    PRIMARY KEY (rule_id, program_instance_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX automatic_reservation_matches_rule_idx
    ON automatic_reservation_matches (rule_id, created_at_utc_ms, program_instance_id);
