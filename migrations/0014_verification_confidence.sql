ALTER TABLE verification_reports ADD COLUMN computed_confidence REAL DEFAULT 0;
ALTER TABLE verification_reports ADD COLUMN confidence_label TEXT DEFAULT '';
