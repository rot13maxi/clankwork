ALTER TABLE agents ADD COLUMN transport TEXT;
ALTER TABLE agents ADD COLUMN runtime_session_id TEXT;
ALTER TABLE agents ADD COLUMN pid INTEGER;
ALTER TABLE agents ADD COLUMN last_event_at TEXT;
ALTER TABLE agents ADD COLUMN last_stop_reason TEXT;

UPDATE agents
SET transport = CASE
	WHEN runtime = 'deterministic' THEN 'deterministic'
	WHEN runtime LIKE '%acp%' THEN 'acp'
	ELSE 'tmux'
END
WHERE transport IS NULL OR transport = '';

UPDATE agents
SET runtime_session_id = tmux_session
WHERE (runtime_session_id IS NULL OR runtime_session_id = '')
  AND tmux_session IS NOT NULL
  AND tmux_session != '';
