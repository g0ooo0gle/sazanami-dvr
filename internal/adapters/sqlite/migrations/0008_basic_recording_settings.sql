ALTER TABLE reservations ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1));
ALTER TABLE reservations ADD COLUMN use_default_margins INTEGER NOT NULL DEFAULT 1 CHECK (use_default_margins IN (0, 1));
ALTER TABLE reservations ADD COLUMN effective_start_margin_seconds INTEGER NOT NULL DEFAULT 5
    CHECK (effective_start_margin_seconds BETWEEN -3600 AND 3600);
ALTER TABLE reservations ADD COLUMN effective_end_margin_seconds INTEGER NOT NULL DEFAULT 2
    CHECK (effective_end_margin_seconds BETWEEN -3600 AND 3600);

CREATE TRIGGER reservations_basic_settings_insert
BEFORE INSERT ON reservations
FOR EACH ROW
WHEN (NEW.use_default_margins = 1 AND
      (NEW.effective_start_margin_seconds != 5 OR NEW.effective_end_margin_seconds != 2))
  OR (NEW.start_at_utc_ms - NEW.effective_start_margin_seconds * 1000 < 0)
  OR (NEW.duration_seconds + NEW.effective_start_margin_seconds + NEW.effective_end_margin_seconds NOT BETWEEN 1 AND 86400)
BEGIN
    SELECT RAISE(ABORT, 'invalid basic recording settings');
END;

CREATE TRIGGER reservations_basic_settings_update
BEFORE UPDATE OF start_at_utc_ms, duration_seconds, use_default_margins, effective_start_margin_seconds, effective_end_margin_seconds
ON reservations
FOR EACH ROW
WHEN (NEW.use_default_margins = 1 AND
      (NEW.effective_start_margin_seconds != 5 OR NEW.effective_end_margin_seconds != 2))
  OR (NEW.start_at_utc_ms - NEW.effective_start_margin_seconds * 1000 < 0)
  OR (NEW.duration_seconds + NEW.effective_start_margin_seconds + NEW.effective_end_margin_seconds NOT BETWEEN 1 AND 86400)
BEGIN
    SELECT RAISE(ABORT, 'invalid basic recording settings');
END;

UPDATE reservations SET effective_start_margin_seconds=effective_start_margin_seconds;
