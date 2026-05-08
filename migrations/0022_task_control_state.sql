CREATE TABLE IF NOT EXISTS task_control_states (
    task_id           TEXT PRIMARY KEY,
    desired_step      TEXT NOT NULL,
    observed_step     TEXT NOT NULL,
    runtime_health    TEXT NOT NULL,
    progress          TEXT NOT NULL,
    error_category    TEXT NOT NULL,
    last_actuation    TEXT NOT NULL,
    escalation_level  INTEGER NOT NULL DEFAULT 0,
    oscillation_score INTEGER NOT NULL DEFAULT 0,
    failure_signature TEXT,
    updated_at        TEXT NOT NULL
);

