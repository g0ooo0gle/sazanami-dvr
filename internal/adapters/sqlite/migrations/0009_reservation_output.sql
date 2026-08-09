ALTER TABLE reservations ADD COLUMN output_folder TEXT NOT NULL DEFAULT ''
    CHECK (typeof(output_folder)='text' AND length(CAST(output_folder AS BLOB))<=256 AND instr(output_folder, char(0))=0);
ALTER TABLE reservations ADD COLUMN output_template TEXT NOT NULL DEFAULT ''
    CHECK (typeof(output_template)='text' AND length(CAST(output_template AS BLOB))<=512 AND instr(output_template, char(0))=0);
