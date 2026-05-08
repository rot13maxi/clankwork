CREATE TABLE agent_events (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id   TEXT REFERENCES agents(id),
    task_id    TEXT REFERENCES tasks(id),
    seq        INTEGER NOT NULL,
    stream     TEXT NOT NULL,
    payload    TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX agent_events_agent_seq_idx ON agent_events(agent_id, seq);
CREATE INDEX agent_events_task_seq_idx  ON agent_events(task_id, seq);
