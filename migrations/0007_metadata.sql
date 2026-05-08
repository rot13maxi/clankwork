-- Generic key-value metadata table for daemon state (e.g. last synthesis time).
CREATE TABLE IF NOT EXISTS metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
