-- Add mnemonic name (adjective-animal) to tasks for easier identification
ALTER TABLE tasks ADD COLUMN name TEXT NOT NULL DEFAULT '';
