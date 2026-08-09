ALTER TABLE reservations ADD COLUMN component_mode INTEGER NOT NULL DEFAULT 0
    CHECK (typeof(component_mode)='integer' AND component_mode BETWEEN 0 AND 4);
