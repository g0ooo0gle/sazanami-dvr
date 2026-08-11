CREATE TABLE reservation_oneseg_outputs (
    reservation_id BLOB PRIMARY KEY REFERENCES reservations(id) ON DELETE RESTRICT
        CHECK (length(reservation_id) = 16),
    provider_service_locator TEXT NOT NULL
        CHECK (typeof(provider_service_locator)='text' AND length(CAST(provider_service_locator AS BLOB)) BETWEEN 1 AND 256
               AND instr(provider_service_locator, char(0))=0
               AND provider_service_locator NOT GLOB '*[^0-9]*'
               AND substr(provider_service_locator, 1, 1) BETWEEN '1' AND '9'
               AND (length(provider_service_locator)<19 OR
                    (length(provider_service_locator)=19 AND provider_service_locator<='9223372036854775807'))),
    output_folder TEXT NOT NULL
        CHECK (typeof(output_folder)='text' AND length(CAST(output_folder AS BLOB))<=256
               AND instr(output_folder, char(0))=0),
    output_template TEXT NOT NULL
        CHECK (typeof(output_template)='text' AND length(CAST(output_template AS BLOB))<=512
               AND instr(output_template, char(0))=0)
) STRICT;

CREATE TRIGGER recording_segments_oneseg_ordinal_insert
BEFORE INSERT ON recording_segments
WHEN NEW.ordinal IS NULL OR typeof(NEW.ordinal)<>'integer' OR NEW.ordinal NOT IN (0, 1)
BEGIN
    SELECT RAISE(ABORT, 'recording segment ordinal outside supported range');
END;

CREATE TRIGGER recording_segments_oneseg_ordinal_update
BEFORE UPDATE OF ordinal ON recording_segments
WHEN NEW.ordinal IS NULL OR typeof(NEW.ordinal)<>'integer' OR NEW.ordinal NOT IN (0, 1)
BEGIN
    SELECT RAISE(ABORT, 'recording segment ordinal outside supported range');
END;
