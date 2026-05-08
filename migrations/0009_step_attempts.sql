-- Replace single StepRetryCount with per-step attempt tracking (JSON map).
-- Old column kept for backward compat during migration; drop it in a future migration.
ALTER TABLE tasks ADD COLUMN step_attempts TEXT NOT NULL DEFAULT '{}';
