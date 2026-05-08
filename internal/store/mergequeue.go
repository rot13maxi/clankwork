package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Store) MergeQueueEnqueue(ctx context.Context, id, taskID, repoID, branch, target string, priority int) (*model.MergeQueueItem, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO merge_queue (id, task_id, repo_id, branch, target, status, attempt_count, priority, queued_at)
		 VALUES (?, ?, ?, ?, ?, 'queued', 0, ?, ?)`,
		id, taskID, repoID, branch, target, priority, now,
	)
	if err != nil {
		existing, getErr := s.MergeQueueGetByTask(ctx, taskID)
		if getErr == nil && existing != nil && (existing.Status == "failed" || existing.Status == "rejected") {
			_, resetErr := s.db.ExecContext(ctx, `
				UPDATE merge_queue
				   SET id=?, repo_id=?, branch=?, target=?, status='queued', attempt_count=0,
				       priority=?, queued_at=?, started_at=NULL, completed_at=NULL,
				       merge_sha=NULL, failure_log=NULL, worktree_path=NULL, conflict_task_id=NULL
				 WHERE task_id=? AND status IN ('failed','rejected')`,
				id, repoID, branch, target, priority, now, taskID)
			if resetErr != nil {
				return nil, fmt.Errorf("requeue terminal item: %w", resetErr)
			}
			return s.MergeQueueGet(ctx, id)
		}
		return nil, fmt.Errorf("enqueue: %w", err)
	}
	return s.MergeQueueGet(ctx, id)
}

func (s *Store) MergeQueueGet(ctx context.Context, id string) (*model.MergeQueueItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, repo_id, branch, target, status, attempt_count, priority,
		        queued_at, COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(merge_sha,''), COALESCE(failure_log,''),
		        COALESCE(worktree_path,''), COALESCE(conflict_task_id,'')
		 FROM merge_queue WHERE id = ?`, id)
	item, err := scanMergeQueueItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("merge queue item %q not found", id)
	}
	return item, err
}

func (s *Store) MergeQueueGetByTask(ctx context.Context, taskID string) (*model.MergeQueueItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, repo_id, branch, target, status, attempt_count, priority,
		        queued_at, COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(merge_sha,''), COALESCE(failure_log,''),
		        COALESCE(worktree_path,''), COALESCE(conflict_task_id,'')
		 FROM merge_queue WHERE task_id = ?`, taskID)
	item, err := scanMergeQueueItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

// MergeQueueGetByConflictTask finds the queue item that owns a conflict-resolution task.
func (s *Store) MergeQueueGetByConflictTask(ctx context.Context, conflictTaskID string) (*model.MergeQueueItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, repo_id, branch, target, status, attempt_count, priority,
		        queued_at, COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(merge_sha,''), COALESCE(failure_log,''),
		        COALESCE(worktree_path,''), COALESCE(conflict_task_id,'')
		 FROM merge_queue WHERE conflict_task_id = ?`, conflictTaskID)
	item, err := scanMergeQueueItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

// MergeQueueNext returns the next queued item for a repo (highest priority, oldest first).
func (s *Store) MergeQueueNext(ctx context.Context, repoID string) (*model.MergeQueueItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, repo_id, branch, target, status, attempt_count, priority,
		        queued_at, COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(merge_sha,''), COALESCE(failure_log,''),
		        COALESCE(worktree_path,''), COALESCE(conflict_task_id,'')
		 FROM merge_queue
		 WHERE repo_id = ? AND status = 'queued'
		 ORDER BY priority DESC, queued_at ASC
		 LIMIT 1`, repoID)
	item, err := scanMergeQueueItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (s *Store) MergeQueueList(ctx context.Context) ([]*model.MergeQueueItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, repo_id, branch, target, status, attempt_count, priority,
		        queued_at, COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(merge_sha,''), COALESCE(failure_log,''),
		        COALESCE(worktree_path,''), COALESCE(conflict_task_id,'')
		 FROM merge_queue
		 ORDER BY queued_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.MergeQueueItem
	for rows.Next() {
		item, err := scanMergeQueueItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) MergeQueueDepth(ctx context.Context) (int, error) {
	var n int
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM merge_queue WHERE status IN ('queued','rebasing','verifying','merging')`)
	return n, row.Scan(&n)
}

func (s *Store) MergeQueuePressureSnapshot(ctx context.Context, since time.Time) (model.QueuePressureSnapshot, error) {
	var snap model.QueuePressureSnapshot
	var oldest string
	row := s.db.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN status IN ('queued','rebasing','verifying','merging') THEN 1 ELSE 0 END), 0),
		  COALESCE(MIN(CASE WHEN status IN ('queued','rebasing','verifying','merging') THEN queued_at ELSE NULL END), ''),
		  COALESCE(SUM(CASE WHEN status IN ('failed','rejected') AND completed_at >= ? THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN status = 'conflicted' THEN 1 ELSE 0 END), 0)
		FROM merge_queue`, since.UTC().Format(time.RFC3339))
	if err := row.Scan(&snap.Depth, &oldest, &snap.RecentFailures, &snap.ConflictCount); err != nil {
		return snap, err
	}
	if oldest != "" {
		if ts, err := time.Parse(time.RFC3339, oldest); err == nil {
			snap.OldestAge = time.Since(ts)
		}
	}
	return snap, nil
}

func (s *Store) MergeQueueStats(ctx context.Context) (model.MergeQueueStat, error) {
	var stat model.MergeQueueStat
	row := s.db.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN status='queued' THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN status IN ('rebasing','verifying','merging') THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN status='merged' THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN status IN ('failed','rejected','conflicted') THEN 1 ELSE 0 END), 0)
		FROM merge_queue`)
	return stat, row.Scan(&stat.Queued, &stat.InProgress, &stat.Merged, &stat.Failed)
}

func (s *Store) MergeQueueSetStatus(ctx context.Context, id, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var err error
	switch status {
	case "rebasing", "verifying", "merging":
		_, err = s.db.ExecContext(ctx,
			`UPDATE merge_queue SET status=?, started_at=COALESCE(started_at,?) WHERE id=?`,
			status, now, id)
	case "merged", "failed", "rejected", "conflicted":
		_, err = s.db.ExecContext(ctx,
			`UPDATE merge_queue SET status=?, completed_at=? WHERE id=?`,
			status, now, id)
	default:
		_, err = s.db.ExecContext(ctx,
			`UPDATE merge_queue SET status=? WHERE id=?`, status, id)
	}
	if err == nil && shouldIndexMergeStatus(status) {
		if item, getErr := s.MergeQueueGet(ctx, id); getErr == nil && item != nil {
			_ = s.PriorArtIndexTask(ctx, item.TaskID)
		}
	}
	return err
}

func (s *Store) MergeQueueSetMergeSHA(ctx context.Context, id, sha string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE merge_queue SET merge_sha=? WHERE id=?`, sha, id)
	return err
}

func (s *Store) MergeQueueSetFailureLog(ctx context.Context, id, log string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE merge_queue SET failure_log=? WHERE id=?`, log, id)
	return err
}

func (s *Store) MergeQueueSetWorktreePath(ctx context.Context, id, path string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE merge_queue SET worktree_path=? WHERE id=?`, path, id)
	return err
}

func (s *Store) MergeQueueSetConflictTask(ctx context.Context, id, conflictTaskID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE merge_queue SET conflict_task_id=? WHERE id=?`, conflictTaskID, id)
	return err
}

func (s *Store) MergeQueueIncrAttempt(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE merge_queue SET attempt_count=attempt_count+1 WHERE id=?`, id)
	return err
}

// MergeQueueResetStuck resets in-progress items to 'queued' on daemon startup.
// For items in 'merging' state, checks if the branch is already an ancestor of
// target before resetting — if so, marks them 'merged' directly.
func (s *Store) MergeQueueResetStuck(ctx context.Context) ([]*model.MergeQueueItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, repo_id, branch, target, status, attempt_count, priority,
		        queued_at, COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(merge_sha,''), COALESCE(failure_log,''),
		        COALESCE(worktree_path,''), COALESCE(conflict_task_id,'')
		 FROM merge_queue WHERE status IN ('rebasing','verifying','merging')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stuck []*model.MergeQueueItem
	for rows.Next() {
		item, err := scanMergeQueueItem(rows)
		if err != nil {
			return nil, err
		}
		stuck = append(stuck, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stuck, nil
}

// MergeQueueFindStrandedDone returns tasks that are 'done' with auto_merge templates
// but have no merge queue entry. Used for startup reconciliation.
func (s *Store) MergeQueueFindStrandedDone(ctx context.Context) ([]*model.Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, COALESCE(name,''), COALESCE(plan_id,''), COALESCE(repo_id,''), title, COALESCE(body,''), COALESCE(template,''),
		        COALESCE(role,''), COALESCE(runtime,''),
		        priority, status, retry_count,
		        COALESCE(current_step,''), COALESCE(step_retry_count,0),
		        created_at, updated_at,
		        COALESCE(started_at,''), COALESCE(completed_at,'')
		 FROM tasks
		 WHERE status = 'done'
		   AND template != ''
		   AND NOT EXISTS (
		       SELECT 1 FROM merge_queue
		        WHERE task_id = tasks.id
		          AND status NOT IN ('failed','rejected')
		   )`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []*model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// MergeQueueFindStuckConflicted returns merge queue items stuck in 'conflicted' state.
// Used during startup recovery to reset items whose conflict-resolver task has
// reached a terminal state (failed, done, merged).
func (s *Store) MergeQueueFindStuckConflicted(ctx context.Context) ([]*model.MergeQueueItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, task_id, repo_id, branch, target, status, attempt_count, priority,
		        queued_at, COALESCE(started_at,''), COALESCE(completed_at,''),
		        COALESCE(merge_sha,''), COALESCE(failure_log,''),
		        COALESCE(worktree_path,''), COALESCE(conflict_task_id,'')
		 FROM merge_queue WHERE status = 'conflicted'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []*model.MergeQueueItem
	for rows.Next() {
		item, err := scanMergeQueueItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type mergeQueueScanner interface {
	Scan(dest ...any) error
}

func scanMergeQueueItem(row mergeQueueScanner) (*model.MergeQueueItem, error) {
	var item model.MergeQueueItem
	var queuedAt, startedAt, completedAt string
	if err := row.Scan(
		&item.ID, &item.TaskID, &item.RepoID, &item.Branch, &item.Target,
		&item.Status, &item.AttemptCount, &item.Priority,
		&queuedAt, &startedAt, &completedAt,
		&item.MergeSHA, &item.FailureLog, &item.WorktreePath, &item.ConflictTaskID,
	); err != nil {
		return nil, err
	}
	item.QueuedAt, _ = time.Parse(time.RFC3339, queuedAt)
	if startedAt != "" {
		t, _ := time.Parse(time.RFC3339, startedAt)
		item.StartedAt = &t
	}
	if completedAt != "" {
		t, _ := time.Parse(time.RFC3339, completedAt)
		item.CompletedAt = &t
	}
	return &item, nil
}
