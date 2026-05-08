package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

// CompiledWorkflowCreate persists a compiled workflow graph for the given task.
// A task's compiled graph is immutable: replaying the same graph is idempotent,
// but attempting to replace it with different graph content returns an error.
func (s *Store) CompiledWorkflowCreate(ctx context.Context, wf *model.CompiledWorkflow) error {
	now := time.Now().UTC()
	if wf.CreatedAt.IsZero() {
		wf.CreatedAt = now
	}
	if wf.UpdatedAt.IsZero() {
		wf.UpdatedAt = now
	}
	if wf.Status == "" {
		wf.Status = "active"
	}
	if wf.TaskID == "" {
		return fmt.Errorf("task_id is required")
	}
	if wf.GraphJSON == "" {
		return fmt.Errorf("graph_json is required")
	}
	if wf.GraphHash == "" {
		sum := sha256.Sum256([]byte(wf.GraphJSON))
		wf.GraphHash = "sha256:" + hex.EncodeToString(sum[:])
	}

	existing, err := s.compiledWorkflowGetByTask(ctx, wf.TaskID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if existing != nil {
		if existing.GraphHash == "" {
			sum := sha256.Sum256([]byte(existing.GraphJSON))
			existing.GraphHash = "sha256:" + hex.EncodeToString(sum[:])
		}
		if existing.GraphHash != wf.GraphHash {
			return fmt.Errorf("compiled workflow for task %q is immutable: existing graph_hash %s differs from %s", wf.TaskID, existing.GraphHash, wf.GraphHash)
		}
		return nil
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO compiled_workflows
		 (id, task_id, source_type, source_name, source_ref, policy_version, graph_hash, graph_json, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wf.ID, wf.TaskID, wf.SourceType, wf.SourceName, wf.SourceRef,
		wf.PolicyVersion, wf.GraphHash, wf.GraphJSON, wf.Status,
		wf.CreatedAt.Format(time.RFC3339), wf.UpdatedAt.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("create compiled_workflow: %w", err)
	}
	return nil
}

// CompiledWorkflowGetByTask returns the compiled workflow for a task.
// Returns sql.ErrNoRows wrapped with a useful message if not found.
func (s *Store) CompiledWorkflowGetByTask(ctx context.Context, taskID string) (*model.CompiledWorkflow, error) {
	wf, err := s.compiledWorkflowGetByTask(ctx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no compiled workflow for task %q", taskID)
	}
	return wf, err
}

func (s *Store) compiledWorkflowGetByTask(ctx context.Context, taskID string) (*model.CompiledWorkflow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, source_type, source_name, source_ref, policy_version, COALESCE(graph_hash,''), graph_json, status, created_at, updated_at
		   FROM compiled_workflows WHERE task_id = ?`, taskID)
	return scanCompiledWorkflow(row)
}

func scanCompiledWorkflow(row interface{ Scan(dest ...any) error }) (*model.CompiledWorkflow, error) {
	var wf model.CompiledWorkflow
	var createdAt, updatedAt string
	if err := row.Scan(&wf.ID, &wf.TaskID, &wf.SourceType, &wf.SourceName, &wf.SourceRef, &wf.PolicyVersion, &wf.GraphHash, &wf.GraphJSON, &wf.Status, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if wf.GraphHash == "" && wf.GraphJSON != "" {
		sum := sha256.Sum256([]byte(wf.GraphJSON))
		wf.GraphHash = "sha256:" + hex.EncodeToString(sum[:])
	}
	wf.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	wf.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &wf, nil
}
