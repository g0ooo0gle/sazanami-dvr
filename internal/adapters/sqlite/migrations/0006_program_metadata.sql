ALTER TABLE program_revisions
    ADD COLUMN metadata BLOB
    CHECK (metadata IS NULL OR length(metadata) BETWEEN 1 AND 262144);
