CREATE TABLE IF NOT EXISTS compiled_workflows (
    id              TEXT PRIMARY KEY,
    task_id         TEXT NOT NULL UNIQUE REFERENCES tasks(id),
    source_type     TEXT NOT NULL,
    source_name     TEXT NOT NULL,
    source_ref      TEXT NOT NULL DEFAULT '',
    policy_version  TEXT NOT NULL DEFAULT '',
    graph_json      TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
