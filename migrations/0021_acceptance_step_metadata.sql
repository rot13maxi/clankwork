ALTER TABLE acceptance_specs ADD COLUMN step_id TEXT NOT NULL DEFAULT 'acceptance_spec';
ALTER TABLE verification_reports ADD COLUMN step_id TEXT NOT NULL DEFAULT 'acceptance';
