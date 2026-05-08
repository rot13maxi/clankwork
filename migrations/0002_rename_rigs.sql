ALTER TABLE rigs RENAME TO repos;
ALTER TABLE tasks RENAME COLUMN rig_id TO repo_id;
