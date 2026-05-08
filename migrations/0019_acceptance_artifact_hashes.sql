ALTER TABLE acceptance_specs ADD COLUMN path TEXT DEFAULT '';
ALTER TABLE acceptance_specs ADD COLUMN sha256 TEXT DEFAULT '';

ALTER TABLE verification_reports ADD COLUMN path TEXT DEFAULT '';
ALTER TABLE verification_reports ADD COLUMN sha256 TEXT DEFAULT '';
