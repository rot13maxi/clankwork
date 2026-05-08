ALTER TABLE compiled_workflows ADD COLUMN graph_hash TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_compiled_workflows_task_hash
ON compiled_workflows(task_id, graph_hash);
