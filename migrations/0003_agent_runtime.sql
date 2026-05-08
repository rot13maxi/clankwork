-- Add role and runtime to tasks
ALTER TABLE tasks ADD COLUMN role    TEXT;
ALTER TABLE tasks ADD COLUMN runtime TEXT;

-- Scheduler pause state (single row)
CREATE TABLE scheduler_state (
    id     INTEGER PRIMARY KEY CHECK (id = 1),
    paused INTEGER NOT NULL DEFAULT 0
);
INSERT INTO scheduler_state (id, paused) VALUES (1, 0);
