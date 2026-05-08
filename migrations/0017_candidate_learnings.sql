CREATE TABLE IF NOT EXISTS candidate_learnings (
    id                TEXT PRIMARY KEY,
    source_trace_id   TEXT NOT NULL,
    proposed_learning TEXT NOT NULL,
    reason            TEXT NOT NULL,
    status            TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    reviewed_at       TEXT
);

CREATE INDEX IF NOT EXISTS idx_candidate_learnings_status ON candidate_learnings(status);
