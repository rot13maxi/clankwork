package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Store) AgentCreate(ctx context.Context, id, taskID string, slot int, tmuxSession, logfilePath, worktreePath, runtime, agentModel string) (*model.Agent, error) {
	transport := inferAgentTransport(runtime)
	return s.AgentCreateWithRuntime(ctx, id, taskID, slot, tmuxSession, transport, tmuxSession, 0, logfilePath, worktreePath, runtime, agentModel)
}

func (s *Store) AgentCreateWithRuntime(ctx context.Context, id, taskID string, slot int, tmuxSession, transport, runtimeSessionID string, pid int, logfilePath, worktreePath, runtime, agentModel string) (*model.Agent, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if transport == "" {
		transport = inferAgentTransport(runtime)
	}
	if runtimeSessionID == "" {
		runtimeSessionID = tmuxSession
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, task_id, slot, status, tmux_session, transport, runtime_session_id, pid, logfile_path, worktree_path, runtime, model, started_at)
		 VALUES (?, ?, ?, 'running', NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, 0), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?)`,
		id, taskID, slot, tmuxSession, transport, runtimeSessionID, pid, logfilePath, worktreePath, runtime, agentModel, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert agent: %w", err)
	}
	return s.AgentGet(ctx, id)
}

func inferAgentTransport(runtime string) string {
	switch {
	case runtime == "deterministic":
		return "deterministic"
	case strings.Contains(runtime, "acp"):
		return "acp"
	default:
		return "tmux"
	}
}

func (s *Store) AgentGet(ctx context.Context, id string) (*model.Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, slot, status,
		        COALESCE(tmux_session,''), COALESCE(transport,''), COALESCE(runtime_session_id,''), COALESCE(pid,0),
		        COALESCE(logfile_path,''), COALESCE(worktree_path,''),
		        COALESCE(runtime,''), COALESCE(model,''),
		        started_at, COALESCE(last_heartbeat,''), COALESCE(last_event_at,''), COALESCE(last_stop_reason,''), COALESCE(ended_at,'')
		 FROM agents WHERE id = ?`, id)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("agent %q not found", id)
	}
	return a, err
}

// AgentGetByTask returns the most relevant agent for a task:
// prefers running agents with a tmux session, then any agent with a session, then newest overall.
func (s *Store) AgentGetByTask(ctx context.Context, taskID string) (*model.Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, slot, status,
		        COALESCE(tmux_session,''), COALESCE(transport,''), COALESCE(runtime_session_id,''), COALESCE(pid,0),
		        COALESCE(logfile_path,''), COALESCE(worktree_path,''),
		        COALESCE(runtime,''), COALESCE(model,''),
		        started_at, COALESCE(last_heartbeat,''), COALESCE(last_event_at,''), COALESCE(last_stop_reason,''), COALESCE(ended_at,'')
		 FROM agents WHERE task_id = ?
		 ORDER BY
		   CASE status WHEN 'running' THEN 0 ELSE 1 END,
		   CASE WHEN COALESCE(runtime_session_id, tmux_session, '') != '' THEN 0 ELSE 1 END,
		   started_at DESC
		 LIMIT 1`, taskID)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no agent found for task %q", taskID)
	}
	return a, err
}

func (s *Store) AgentGetBySession(ctx context.Context, sessionName string) (*model.Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, slot, status,
		        COALESCE(tmux_session,''), COALESCE(transport,''), COALESCE(runtime_session_id,''), COALESCE(pid,0),
		        COALESCE(logfile_path,''), COALESCE(worktree_path,''),
		        COALESCE(runtime,''), COALESCE(model,''),
		        started_at, COALESCE(last_heartbeat,''), COALESCE(last_event_at,''), COALESCE(last_stop_reason,''), COALESCE(ended_at,'')
		 FROM agents WHERE tmux_session = ? OR runtime_session_id = ?
		 ORDER BY started_at DESC
		 LIMIT 1`, sessionName, sessionName)
	a, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no agent found for session %q", sessionName)
	}
	return a, err
}

func (s *Store) AgentList(ctx context.Context, status string) ([]*model.Agent, error) {
	q := `SELECT id, task_id, slot, status,
		         COALESCE(tmux_session,''), COALESCE(transport,''), COALESCE(runtime_session_id,''), COALESCE(pid,0),
		         COALESCE(logfile_path,''), COALESCE(worktree_path,''),
		         COALESCE(runtime,''), COALESCE(model,''),
		         started_at, COALESCE(last_heartbeat,''), COALESCE(last_event_at,''), COALESCE(last_stop_reason,''), COALESCE(ended_at,'')
		  FROM agents`
	var args []any
	if status != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY started_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []*model.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *Store) AgentSetStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) AgentSetEnded(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET status = 'done', ended_at = ? WHERE id = ?`, now, id)
	return err
}

func (s *Store) AgentUpdateHeartbeat(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents SET last_heartbeat = ? WHERE id = ?`, now, id)
	return err
}

func (s *Store) AgentSetRuntimePID(ctx context.Context, id string, pid int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET pid = NULLIF(?, 0) WHERE id = ?`, pid, id)
	return err
}

func (s *Store) AgentUpdateRuntimeEvent(ctx context.Context, id string, eventAt time.Time, stopReason string) error {
	if eventAt.IsZero() {
		eventAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE agents
		 SET last_event_at = ?,
		     last_stop_reason = CASE WHEN ? != '' THEN ? ELSE last_stop_reason END
		 WHERE id = ?`,
		eventAt.UTC().Format(time.RFC3339), stopReason, stopReason, id)
	return err
}

func (s *Store) AgentRunningCount(ctx context.Context) (int, error) {
	var n int
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE status = 'running'`)
	return n, row.Scan(&n)
}

// AgentRunningACPPIDs returns all agents with status "running", an ACP-based runtime,
// and a non-zero PID. Used at daemon startup to find orphaned acp-adapter processes
// that need to be SIGTERM'd when the previous daemon instance died.
func (s *Store) AgentRunningACPPIDs(ctx context.Context) ([]*model.Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, slot, status,
		        COALESCE(tmux_session,''), COALESCE(transport,''), COALESCE(runtime_session_id,''), COALESCE(pid,0),
		        COALESCE(logfile_path,''), COALESCE(worktree_path,''),
		        COALESCE(runtime,''), COALESCE(model,''),
		        started_at, COALESCE(last_heartbeat,''), COALESCE(last_event_at,''), COALESCE(last_stop_reason,''), COALESCE(ended_at,'')
		 FROM agents
		 WHERE status = 'running'
		   AND pid > 0
		   AND (runtime LIKE '%acp%' OR transport = 'acp')
		 ORDER BY pid`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []*model.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

func (s *Store) AgentStats(ctx context.Context) (model.AgentStats, error) {
	var st model.AgentStats
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='done'    THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='killed'  THEN 1 ELSE 0 END), 0)
		FROM agents`)
	return st, row.Scan(&st.Total, &st.Running, &st.Done, &st.Killed)
}

type agentScanner interface {
	Scan(dest ...any) error
}

func scanAgent(row agentScanner) (*model.Agent, error) {
	var a model.Agent
	var startedAt, lastHeartbeat, lastEventAt, endedAt string
	if err := row.Scan(
		&a.ID, &a.TaskID, &a.Slot, &a.Status,
		&a.TmuxSession, &a.Transport, &a.RuntimeSessionID, &a.PID,
		&a.LogfilePath, &a.WorktreePath,
		&a.Runtime, &a.Model,
		&startedAt, &lastHeartbeat, &lastEventAt, &a.LastStopReason, &endedAt,
	); err != nil {
		return nil, err
	}
	a.StartedAt, _ = time.Parse(time.RFC3339, startedAt)
	if a.Transport == "" {
		a.Transport = inferAgentTransport(a.Runtime)
	}
	if a.RuntimeSessionID == "" {
		a.RuntimeSessionID = a.TmuxSession
	}
	if lastHeartbeat != "" {
		t, _ := time.Parse(time.RFC3339, lastHeartbeat)
		a.LastHeartbeat = &t
	}
	if lastEventAt != "" {
		t, _ := time.Parse(time.RFC3339, lastEventAt)
		a.LastEventAt = &t
	}
	if endedAt != "" {
		t, _ := time.Parse(time.RFC3339, endedAt)
		a.EndedAt = &t
	}
	return &a, nil
}
