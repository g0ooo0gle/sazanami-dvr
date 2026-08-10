ALTER TABLE reservations ADD COLUMN post_power_mode INTEGER NOT NULL DEFAULT 0
    CHECK (typeof(post_power_mode)='integer' AND post_power_mode BETWEEN 0 AND 5);

CREATE TRIGGER reservations_post_power_insert
BEFORE INSERT ON reservations
WHEN NEW.post_power_mode > 0 AND NEW.post_action_mode <> 0
BEGIN
    SELECT RAISE(ABORT, 'invalid post recording modes');
END;

CREATE TRIGGER reservations_post_power_update
BEFORE UPDATE OF post_action_mode, post_power_mode ON reservations
WHEN NEW.post_power_mode > 0 AND NEW.post_action_mode <> 0
BEGIN
    SELECT RAISE(ABORT, 'invalid post recording modes');
END;
