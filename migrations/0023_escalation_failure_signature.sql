-- Add failure_signature and suggested_commands to escalations for deduplication and operator guidance.
CREATE TABLE IF NOT EXISTS _escalations_new (
    id               TEXT PRIMARY KEY,
    task_id          TEXT,
    step_name        TEXT,
    target_type      TEXT NOT NULL,
    target_ref       TEXT,
    requested_action TEXT NOT NULL,
    reason           TEXT NOT NULL,
    evidence_refs    TEXT NOT NULL DEFAULT '[]',
    status           TEXT NOT NULL,
    outcome          TEXT,
    created_by_type  TEXT NOT NULL,
    created_by_id    TEXT NOT NULL,
    resolved_by_type TEXT,
    resolved_by_id   TEXT,
    created_at       TEXT NOT NULL,
    resolved_at      TEXT,
    failure_signature TEXT NOT NULL DEFAULT '',
    suggested_commands TEXT NOT NULL DEFAULT '[]'
);
INSERT INTO _escalations_new SELECT id, task_id, step_name, target_type, target_ref, requested_action, reason, evidence_refs, status, outcome, created_by_type, created_by_id, resolved_by_type, resolved_by_id, created_at, resolved_at, COALESCE('', ''), COALESCE('[]', '[]') FROM escalations;
DROP TABLE escalations;
ALTER TABLE _escalations_new RENAME TO escalations;
CREATE INDEX IF NOT EXISTS idx_escalations_task
    ON escalations(task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_escalations_status
    ON escalations(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_escalations_failure_sig
    ON escalations(task_id, failure_signature, status, created_at DESC);
