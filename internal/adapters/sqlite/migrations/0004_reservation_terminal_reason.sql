ALTER TABLE reservations ADD COLUMN terminal_reason TEXT
    CHECK (terminal_reason IS NULL OR length(CAST(terminal_reason AS BLOB)) BETWEEN 1 AND 64);

UPDATE reservations SET terminal_reason='ATTEMPT_FINISHED' WHERE state='FINISHED';
