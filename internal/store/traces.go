package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Store) TraceAppend(ctx context.Context, taskID, agentID, eventType, payload string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO traces (task_id, agent_id, event_type, payload, created_at)
		 VALUES (NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		taskID, agentID, eventType, payload, now,
	)
	if err != nil {
		return fmt.Errorf("append trace: %w", err)
	}
	return nil
}

func (s *Store) TraceList(ctx context.Context, taskID string, limit int) ([]*model.Trace, error) {
	q := `SELECT id, COALESCE(task_id,''), COALESCE(agent_id,''), event_type,
		         COALESCE(step_name,''), COALESCE(retry_num,0), COALESCE(runtime,''), COALESCE(model,''),
		         payload, created_at
		  FROM traces WHERE task_id = ?
		  ORDER BY id DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var traces []*model.Trace
	for rows.Next() {
		var tr model.Trace
		var createdAt string
		if err := rows.Scan(
			&tr.ID, &tr.TaskID, &tr.AgentID, &tr.EventType,
			&tr.StepName, &tr.RetryNum, &tr.Runtime, &tr.Model,
			&tr.Payload, &createdAt,
		); err != nil {
			return nil, err
		}
		tr.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		traces = append(traces, &tr)
	}
	return traces, rows.Err()
}

// TraceListFiltered returns traces matching optional filters, newest first.
// All filter parameters are optional — empty strings and zero values are ignored.
func (s *Store) TraceListFiltered(ctx context.Context, taskID, eventType string, since time.Time, limit int, template, retries, outcome, pathGlob string) ([]*model.Trace, error) {
	// Determine whether we need to JOIN to the tasks table.
	needJoin := template != "" || retries != "" || outcome != ""

	selectCols := `traces.id, COALESCE(traces.task_id,''), COALESCE(traces.agent_id,''), traces.event_type,
		         COALESCE(traces.step_name,''), COALESCE(traces.retry_num,0), COALESCE(traces.runtime,''), COALESCE(traces.model,''),
		         traces.payload, traces.created_at`

	var q string
	if needJoin {
		q = `SELECT ` + selectCols + `
		  FROM traces
		  INNER JOIN tasks ON tasks.id = traces.task_id
		  WHERE 1=1`
	} else {
		q = `SELECT ` + selectCols + ` FROM traces WHERE 1=1`
	}

	var args []any
	if taskID != "" {
		q += " AND traces.task_id = ?"
		args = append(args, taskID)
	}
	if eventType != "" {
		q += " AND traces.event_type = ?"
		args = append(args, eventType)
	}
	if !since.IsZero() {
		q += " AND traces.created_at >= ?"
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	if template != "" {
		q += " AND tasks.template = ?"
		args = append(args, template)
	}
	if retries != "" {
		op, val, err := parseRetryExpr(retries)
		if err != nil {
			return nil, fmt.Errorf("invalid retries expression %q: %w", retries, err)
		}
		q += fmt.Sprintf(" AND tasks.retry_count %s ?", op)
		args = append(args, val)
	}
	if outcome != "" {
		q += " AND tasks.status = ?"
		args = append(args, outcome)
	}
	if pathGlob != "" {
		// Convert glob pattern to SQL LIKE: replace * with %
		likePattern := "%" + strings.ReplaceAll(pathGlob, "*", "%") + "%"
		q += " AND traces.payload LIKE ?"
		args = append(args, likePattern)
	}
	q += " ORDER BY traces.id DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var traces []*model.Trace
	for rows.Next() {
		var tr model.Trace
		var createdAt string
		if err := rows.Scan(
			&tr.ID, &tr.TaskID, &tr.AgentID, &tr.EventType,
			&tr.StepName, &tr.RetryNum, &tr.Runtime, &tr.Model,
			&tr.Payload, &createdAt,
		); err != nil {
			return nil, err
		}
		tr.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		traces = append(traces, &tr)
	}
	return traces, rows.Err()
}

// parseRetryExpr parses expressions like ">2", ">=5", "3" into an SQL operator and integer value.
func parseRetryExpr(expr string) (string, int, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", 0, fmt.Errorf("empty expression")
	}

	var op string
	var numStr string
	if strings.HasPrefix(expr, ">=") {
		op = ">="
		numStr = strings.TrimSpace(expr[2:])
	} else if strings.HasPrefix(expr, "<=") {
		op = "<="
		numStr = strings.TrimSpace(expr[2:])
	} else if strings.HasPrefix(expr, ">") {
		op = ">"
		numStr = strings.TrimSpace(expr[1:])
	} else if strings.HasPrefix(expr, "<") {
		op = "<"
		numStr = strings.TrimSpace(expr[1:])
	} else {
		op = "="
		numStr = expr
	}

	val, err := strconv.Atoi(numStr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid number %q: %w", numStr, err)
	}
	return op, val, nil
}

// TraceListByType returns the last N traces of a given event_type for a task, newest first.
func (s *Store) TraceListByType(ctx context.Context, taskID, eventType string, limit int) ([]*model.Trace, error) {
	q := `SELECT id, COALESCE(task_id,''), COALESCE(agent_id,''), event_type,
		         COALESCE(step_name,''), COALESCE(retry_num,0), COALESCE(runtime,''), COALESCE(model,''),
		         payload, created_at
		  FROM traces WHERE task_id = ? AND event_type = ?
		  ORDER BY id DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, taskID, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var traces []*model.Trace
	for rows.Next() {
		var tr model.Trace
		var createdAt string
		if err := rows.Scan(
			&tr.ID, &tr.TaskID, &tr.AgentID, &tr.EventType,
			&tr.StepName, &tr.RetryNum, &tr.Runtime, &tr.Model,
			&tr.Payload, &createdAt,
		); err != nil {
			return nil, err
		}
		tr.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		traces = append(traces, &tr)
	}
	return traces, rows.Err()
}
