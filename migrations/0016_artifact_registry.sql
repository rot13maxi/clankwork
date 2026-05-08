CREATE TABLE IF NOT EXISTS artifacts (
    id                TEXT PRIMARY KEY,
    task_id           TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    step_id           TEXT NOT NULL,
    producer          TEXT NOT NULL,
    producer_type     TEXT NOT NULL,
    artifact_type     TEXT NOT NULL,
    path              TEXT NOT NULL,
    sha256            TEXT NOT NULL,
    command           TEXT,
    working_directory TEXT,
    exit_code         INTEGER,
    created_at        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_artifacts_task_id ON artifacts(task_id);
