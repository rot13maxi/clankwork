CREATE TABLE IF NOT EXISTS task_history_index (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL UNIQUE REFERENCES tasks(id) ON DELETE CASCADE,
    repo_id TEXT,
    plan_id TEXT,
    title TEXT NOT NULL,
    body TEXT,
    template TEXT,
    status TEXT,
    summary TEXT,
    search_text TEXT NOT NULL,
    risk_score REAL DEFAULT 0,
    rework_score REAL DEFAULT 0,
    tags TEXT NOT NULL DEFAULT '[]',
    metadata TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_task_history_repo ON task_history_index(repo_id);
CREATE INDEX IF NOT EXISTS idx_task_history_scores ON task_history_index(rework_score DESC, risk_score DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS task_history_fts USING fts5(
    title, body, summary, search_text, tags,
    content='task_history_index',
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS task_history_ai AFTER INSERT ON task_history_index BEGIN
    INSERT INTO task_history_fts(rowid, title, body, summary, search_text, tags)
    VALUES (new.rowid, new.title, new.body, new.summary, new.search_text, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS task_history_ad AFTER DELETE ON task_history_index BEGIN
    INSERT INTO task_history_fts(task_history_fts, rowid, title, body, summary, search_text, tags)
    VALUES ('delete', old.rowid, old.title, old.body, old.summary, old.search_text, old.tags);
END;

CREATE TRIGGER IF NOT EXISTS task_history_au AFTER UPDATE ON task_history_index BEGIN
    INSERT INTO task_history_fts(task_history_fts, rowid, title, body, summary, search_text, tags)
    VALUES ('delete', old.rowid, old.title, old.body, old.summary, old.search_text, old.tags);
    INSERT INTO task_history_fts(rowid, title, body, summary, search_text, tags)
    VALUES (new.rowid, new.title, new.body, new.summary, new.search_text, new.tags);
END;
