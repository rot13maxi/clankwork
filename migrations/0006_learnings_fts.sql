-- FTS5 virtual table backed by learnings content table
CREATE VIRTUAL TABLE learnings_fts USING fts5(
    title, body, category,
    content='learnings',
    content_rowid='rowid'
);

-- Backfill existing rows
INSERT INTO learnings_fts(rowid, title, body, category)
SELECT rowid, title, body, category FROM learnings;

-- Keep FTS in sync
CREATE TRIGGER learnings_ai AFTER INSERT ON learnings BEGIN
    INSERT INTO learnings_fts(rowid, title, body, category)
    VALUES (new.rowid, new.title, new.body, new.category);
END;

CREATE TRIGGER learnings_ad AFTER DELETE ON learnings BEGIN
    INSERT INTO learnings_fts(learnings_fts, rowid, title, body, category)
    VALUES ('delete', old.rowid, old.title, old.body, old.category);
END;

CREATE TRIGGER learnings_au AFTER UPDATE ON learnings BEGIN
    INSERT INTO learnings_fts(learnings_fts, rowid, title, body, category)
    VALUES ('delete', old.rowid, old.title, old.body, old.category);
    INSERT INTO learnings_fts(rowid, title, body, category)
    VALUES (new.rowid, new.title, new.body, new.category);
END;
