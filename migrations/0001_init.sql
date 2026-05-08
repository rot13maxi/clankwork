-- rigs: repositories managed by the control plane
CREATE TABLE rigs (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    path          TEXT NOT NULL UNIQUE,
    target_branch TEXT NOT NULL DEFAULT 'main',
    created_at    TEXT NOT NULL
);

-- plans: durable artifacts; markdown stored at $CLANKWORK_HOME/plans/<id>.md
CREATE TABLE plans (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL,
    status     TEXT NOT NULL,
    path       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- tasks: units of work dispatched to agents
CREATE TABLE tasks (
    id           TEXT PRIMARY KEY,
    plan_id      TEXT REFERENCES plans(id),
    rig_id       TEXT REFERENCES rigs(id),
    title        TEXT NOT NULL,
    body         TEXT,
    template     TEXT,
    priority     INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL,
    retry_count  INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    started_at   TEXT,
    completed_at TEXT
);
CREATE INDEX tasks_status_idx ON tasks(status);
CREATE INDEX tasks_plan_idx   ON tasks(plan_id);

-- task_deps: edges of the work DAG
CREATE TABLE task_deps (
    task_id       TEXT NOT NULL REFERENCES tasks(id),
    depends_on_id TEXT NOT NULL REFERENCES tasks(id),
    PRIMARY KEY (task_id, depends_on_id)
);

-- agents: runtime instances (populated by M2)
CREATE TABLE agents (
    id             TEXT PRIMARY KEY,
    task_id        TEXT REFERENCES tasks(id),
    slot           INTEGER,
    status         TEXT NOT NULL,
    tmux_session   TEXT,
    logfile_path   TEXT,
    worktree_path  TEXT,
    runtime        TEXT,
    model          TEXT,
    started_at     TEXT NOT NULL,
    last_heartbeat TEXT,
    ended_at       TEXT
);

-- traces: append-only event log
CREATE TABLE traces (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT REFERENCES tasks(id),
    agent_id    TEXT REFERENCES agents(id),
    event_type  TEXT NOT NULL,
    step_name   TEXT,
    retry_num   INTEGER,
    runtime     TEXT,
    model       TEXT,
    payload     TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX traces_task_idx ON traces(task_id, created_at);

-- learnings: populated by M6 batch synthesis
CREATE TABLE learnings (
    id            TEXT PRIMARY KEY,
    category      TEXT NOT NULL,
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    tier          TEXT NOT NULL DEFAULT 'source',
    created_at    TEXT NOT NULL,
    last_accessed TEXT,
    access_count  INTEGER NOT NULL DEFAULT 0
);
