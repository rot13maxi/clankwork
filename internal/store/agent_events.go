package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Store) AgentEventAppend(ctx context.Context, agentID, taskID string, seq int64, stream, payload string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_events (agent_id, task_id, seq, stream, payload, created_at)
		 VALUES (NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?)`,
		agentID, taskID, seq, stream, payload, now,
	)
	if err != nil {
		return fmt.Errorf("append agent event: %w", err)
	}
	return nil
}

func (s *Store) AgentEventsList(ctx context.Context, agentID, taskID string, afterSeq int64, limit int) ([]*model.AgentEvent, error) {
	q := `SELECT id, COALESCE(agent_id,''), COALESCE(task_id,''), seq, stream, payload, created_at
		  FROM agent_events
		  WHERE 1=1`
	var args []any
	if agentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	if taskID != "" {
		q += ` AND task_id = ?`
		args = append(args, taskID)
	}
	if afterSeq > 0 {
		q += ` AND seq > ?`
		args = append(args, afterSeq)
	}
	q += ` ORDER BY seq DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*model.AgentEvent
	for rows.Next() {
		var ev model.AgentEvent
		var createdAt string
		if err := rows.Scan(&ev.ID, &ev.AgentID, &ev.TaskID, &ev.Seq, &ev.Stream, &ev.Payload, &createdAt); err != nil {
			return nil, err
		}
		ev.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		events = append(events, &ev)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	// Reverse to return events in ascending seq order so callers don't break.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events, nil
}
