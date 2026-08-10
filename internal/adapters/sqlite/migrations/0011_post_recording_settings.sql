ALTER TABLE reservations ADD COLUMN post_action_mode INTEGER NOT NULL DEFAULT 0
    CHECK (typeof(post_action_mode)='integer' AND post_action_mode BETWEEN 0 AND 1);
ALTER TABLE reservations ADD COLUMN post_script_path TEXT NOT NULL DEFAULT ''
    CHECK (typeof(post_script_path)='text' AND length(CAST(post_script_path AS BLOB))<=1024 AND instr(post_script_path, char(0))=0);
