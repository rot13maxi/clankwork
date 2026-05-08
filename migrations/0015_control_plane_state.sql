CREATE TABLE IF NOT EXISTS control_observations (
    id            TEXT PRIMARY KEY,
    target_type   TEXT NOT NULL,
    target_id     TEXT NOT NULL,
    task_id       TEXT,
    agent_id      TEXT,
    worktree_path TEXT,
    kind          TEXT NOT NULL,
    status        TEXT NOT NULL,
    reason        TEXT,
    evidence_refs TEXT NOT NULL DEFAULT '[]',
    payload       TEXT NOT NULL DEFAULT '{}',
    observed_at   TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_control_observations_target
    ON control_observations(target_type, target_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_control_observations_task
    ON control_observations(task_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS controller_decisions (
    id                 TEXT PRIMARY KEY,
    controller         TEXT NOT NULL,
    controller_version TEXT,
    task_id            TEXT,
    step_name          TEXT,
    agent_id           TEXT,
    target_type        TEXT NOT NULL,
    target_id          TEXT NOT NULL,
    decision_kind      TEXT NOT NULL,
    action             TEXT NOT NULL,
    reason             TEXT NOT NULL,
    retryable          INTEGER NOT NULL DEFAULT 0,
    evidence_refs      TEXT NOT NULL DEFAULT '[]',
    payload            TEXT NOT NULL DEFAULT '{}',
    decided_at         TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_controller_decisions_target
    ON controller_decisions(target_type, target_id, decided_at DESC);
CREATE INDEX IF NOT EXISTS idx_controller_decisions_task
    ON controller_decisions(task_id, decided_at DESC);

CREATE TABLE IF NOT EXISTS controller_actuations (
    id                  TEXT PRIMARY KEY,
    requested_operation TEXT NOT NULL,
    actor_type          TEXT NOT NULL,
    actor_id            TEXT NOT NULL,
    intent_id           TEXT NOT NULL,
    correlation_id      TEXT NOT NULL,
    target_type         TEXT NOT NULL,
    target_id           TEXT NOT NULL,
    task_id             TEXT,
    step_name           TEXT,
    agent_id            TEXT,
    previous_state      TEXT,
    new_state           TEXT,
    outcome             TEXT NOT NULL,
    error               TEXT,
    reason              TEXT,
    evidence_refs       TEXT NOT NULL DEFAULT '[]',
    payload             TEXT NOT NULL DEFAULT '{}',
    created_at          TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_controller_actuations_target
    ON controller_actuations(target_type, target_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_controller_actuations_task
    ON controller_actuations(task_id, created_at DESC);

CREATE TABLE IF NOT EXISTS escalations (
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
    resolved_at      TEXT
);

CREATE INDEX IF NOT EXISTS idx_escalations_task
    ON escalations(task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_escalations_status
    ON escalations(status, created_at DESC);
