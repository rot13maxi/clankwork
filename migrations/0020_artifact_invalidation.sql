ALTER TABLE artifacts ADD COLUMN status TEXT NOT NULL DEFAULT 'valid';
ALTER TABLE artifacts ADD COLUMN invalidated_at TEXT;
CREATE INDEX IF NOT EXISTS idx_artifacts_status ON artifacts(status);
