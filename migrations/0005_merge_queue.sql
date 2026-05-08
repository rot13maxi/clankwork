CREATE TABLE merge_queue (
    id               TEXT PRIMARY KEY,
    task_id          TEXT NOT NULL UNIQUE REFERENCES tasks(id),
    repo_id          TEXT NOT NULL REFERENCES repos(id),
    branch           TEXT NOT NULL,
    target           TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'queued',
    attempt_count    INTEGER NOT NULL DEFAULT 0,
    priority         INTEGER NOT NULL DEFAULT 0,
    queued_at        TEXT NOT NULL,
    started_at       TEXT,
    completed_at     TEXT,
    merge_sha        TEXT,
    failure_log      TEXT,
    worktree_path    TEXT,
    conflict_task_id TEXT REFERENCES tasks(id)
);

CREATE INDEX idx_merge_queue_repo ON merge_queue(repo_id, status, priority DESC, queued_at ASC);

ALTER TABLE repos ADD COLUMN verify_command TEXT;
ALTER TABLE repos ADD COLUMN auto_push      INTEGER NOT NULL DEFAULT 0;
