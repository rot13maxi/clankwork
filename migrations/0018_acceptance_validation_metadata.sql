ALTER TABLE acceptance_specs ADD COLUMN strength_score INTEGER DEFAULT 0;
ALTER TABLE acceptance_specs ADD COLUMN risk_level TEXT DEFAULT 'normal';
ALTER TABLE acceptance_specs ADD COLUMN validation_status TEXT DEFAULT 'unknown';
ALTER TABLE acceptance_specs ADD COLUMN validation_errors TEXT DEFAULT '';

ALTER TABLE verification_reports ADD COLUMN validation_status TEXT DEFAULT 'unknown';
ALTER TABLE verification_reports ADD COLUMN validation_errors TEXT DEFAULT '';
