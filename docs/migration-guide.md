# Migration Guide

Clankwork stores durable state in SQLite under
`$CLANKWORK_HOME/clankwork.db`. Migrations are embedded from `migrations/` and
applied in filename order inside a single transaction.

## Naming

Use the next sequential filename:

```text
NNNN_short_description.sql
```

Example:

```text
0027_task_review_metadata.sql
```

## Rules

- Never edit a migration that may already have been applied.
- Append a new migration for every schema change.
- Keep migrations deterministic and SQLite-compatible.
- Prefer additive changes over destructive rewrites.
- Add indexes in the same migration when the new query path requires them.
- Keep compatibility columns until a later migration can safely remove them.

## Workflow

1. Inspect the current highest migration number.
2. Add `NNNN_description.sql`.
3. Update store/model/API code in the same change.
4. Add regression tests for the behavior that depends on the schema.
5. Run focused package tests, then `make test` before submitting.

## Common Patterns

Add a nullable or defaulted column:

```sql
ALTER TABLE tasks ADD COLUMN review_status TEXT DEFAULT '';
```

Add a table:

```sql
CREATE TABLE IF NOT EXISTS task_reviews (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_task_reviews_task_id ON task_reviews(task_id);
```

Add an FTS5 projection only when the feature needs search semantics, and include
insert/update/delete triggers to keep it in sync. See
`migrations/0025_prior_art_index.sql`.

## Testing

At minimum, add store tests that open a migrated database and exercise the new
read/write path. For API-visible schema, add API or CLI tests that prove the
field survives a round trip.

