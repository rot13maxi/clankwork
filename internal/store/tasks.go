package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/names"
)

func (s *Store) TaskCreate(ctx context.Context, id, planID, repoID, title, body, template, role, runtime string, priority int) (*model.Task, error) {
	now := time.Now().UTC()
	name := names.Generate(id)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tasks (id, name, plan_id, repo_id, title, body, template, role, runtime, priority, status, retry_count, created_at, updated_at)
		 VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, 'pending', 0, ?, ?)`,
		id, name, planID, repoID, title, body, template, role, runtime, priority,
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}
	return s.TaskGet(ctx, id)
}

func (s *Store) TaskGet(ctx context.Context, id string) (*model.Task, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(name,''), COALESCE(plan_id,''), COALESCE(repo_id,''), title, COALESCE(body,''), COALESCE(template,''),
		        COALESCE(role,''), COALESCE(runtime,''),
		        priority, status, retry_count,
		        COALESCE(current_step,''), COALESCE(step_attempts,'{}'),
		        created_at, updated_at,
		        COALESCE(started_at,''), COALESCE(completed_at,'')
		 FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("task %q not found", id)
	}
	return t, err
}

// TaskGetByName returns a task matching the given name prefix (LIKE prefix%).
func (s *Store) TaskGetByName(ctx context.Context, namePrefix string) (*model.Task, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(name,''), COALESCE(plan_id,''), COALESCE(repo_id,''), title, COALESCE(body,''), COALESCE(template,''),
		        COALESCE(role,''), COALESCE(runtime,''),
		        priority, status, retry_count,
		        COALESCE(current_step,''), COALESCE(step_retry_count,0),
		        created_at, updated_at,
		        COALESCE(started_at,''), COALESCE(completed_at,'')
		 FROM tasks WHERE name LIKE ?||'%' LIMIT 1`, namePrefix)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no task found matching name %q", namePrefix)
	}
	return t, err
}

func (s *Store) TaskList(ctx context.Context, planID, repoID string, statuses []string) ([]*model.Task, error) {
	q := `SELECT id, COALESCE(name,''), COALESCE(plan_id,''), COALESCE(repo_id,''), title, COALESCE(body,''), COALESCE(template,''),
		         COALESCE(role,''), COALESCE(runtime,''),
		         priority, status, retry_count,
		         COALESCE(current_step,''), COALESCE(step_attempts,'{}'),
		         created_at, updated_at,
		         COALESCE(started_at,''), COALESCE(completed_at,'')
		  FROM tasks WHERE 1=1`
	var args []any
	if planID != "" {
		q += ` AND plan_id = ?`
		args = append(args, planID)
	}
	if repoID != "" {
		q += ` AND repo_id = ?`
		args = append(args, repoID)
	}
	if len(statuses) > 0 {
		placeholders := make([]string, len(statuses))
		for i, s := range statuses {
			placeholders[i] = "?"
			args = append(args, s)
		}
		q += ` AND status IN (` + joinPlaceholders(placeholders) + `)`
	}
	q += ` ORDER BY priority DESC, created_at ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
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

func (s *Store) TaskSetStatus(ctx context.Context, id, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var extra string
	switch status {
	case "running":
		extra = `, started_at = ?`
	case "done", "failed", "blocked", "merged", "closed":
		extra = `, completed_at = ?`
	}
	if extra != "" {
		_, err := s.db.ExecContext(ctx,
			`UPDATE tasks SET status = ?, updated_at = ?`+extra+` WHERE id = ?`,
			status, now, now, id)
		if err == nil && shouldIndexTaskStatus(status) {
			_ = s.PriorArtIndexTask(ctx, id)
		}
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`, status, now, id)
	if err == nil && shouldIndexTaskStatus(status) {
		_ = s.PriorArtIndexTask(ctx, id)
	}
	return err
}

func (s *Store) TaskSetPriority(ctx context.Context, id string, priority int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET priority = ?, updated_at = ? WHERE id = ?`, priority, now, id)
	return err
}

func (s *Store) TaskAddDep(ctx context.Context, taskID, dependsOnID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO task_deps (task_id, depends_on_id) VALUES (?, ?)`, taskID, dependsOnID)
	return err
}

// TaskAddDepWithCycleCheck inserts a dependency after verifying no cycle is introduced.
func (s *Store) TaskAddDepWithCycleCheck(ctx context.Context, taskID, dependsOnID string) error {
	// DFS from dependsOnID: if we can reach taskID, adding this edge creates a cycle.
	if err := s.checkNoCycle(ctx, taskID, dependsOnID); err != nil {
		return err
	}
	return s.TaskAddDep(ctx, taskID, dependsOnID)
}

func (s *Store) checkNoCycle(ctx context.Context, target, from string) error {
	visited := map[string]bool{}
	var dfs func(id string) error
	dfs = func(id string) error {
		if id == target {
			return fmt.Errorf("adding dep would create a cycle")
		}
		if visited[id] {
			return nil
		}
		visited[id] = true
		// Collect deps before recursing to avoid holding open rows across queries.
		rows, err := s.db.QueryContext(ctx, `SELECT depends_on_id FROM task_deps WHERE task_id = ?`, id)
		if err != nil {
			return err
		}
		var nexts []string
		for rows.Next() {
			var next string
			if err := rows.Scan(&next); err != nil {
				rows.Close()
				return err
			}
			nexts = append(nexts, next)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, next := range nexts {
			if err := dfs(next); err != nil {
				return err
			}
		}
		return nil
	}
	return dfs(from)
}

// TasksReady returns up to limit pending tasks whose dependencies are all done.
func (s *Store) TasksReady(ctx context.Context, limit int) ([]*model.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(name,''), COALESCE(plan_id,''), COALESCE(repo_id,''), title, COALESCE(body,''), COALESCE(template,''),
		       COALESCE(role,''), COALESCE(runtime,''),
		       priority, status, retry_count,
		       COALESCE(current_step,''), COALESCE(step_attempts,'{}'),
		       created_at, updated_at,
		       COALESCE(started_at,''), COALESCE(completed_at,'')
		FROM tasks
		WHERE status = 'pending'
		  AND NOT EXISTS (
		    SELECT 1 FROM task_deps d
		    JOIN tasks dep ON dep.id = d.depends_on_id
		    WHERE d.task_id = tasks.id AND dep.status NOT IN ('done', 'merged')
		  )
		ORDER BY priority DESC, created_at ASC
		LIMIT ?`, limit)
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

// TaskSetStatusIfRunning updates status only when the task is currently running.
func (s *Store) TaskSetStatusIfRunning(ctx context.Context, id, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var extra string
	if status == "done" || status == "failed" || status == "blocked" || status == "merged" {
		extra = `, completed_at = ?`
	}
	var res sql.Result
	var err error
	if extra != "" {
		res, err = s.db.ExecContext(ctx,
			`UPDATE tasks SET status = ?, updated_at = ?`+extra+` WHERE id = ? AND status = 'running'`,
			status, now, now, id)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ? AND status = 'running'`,
			status, now, id)
	}
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows > 0 && shouldIndexTaskStatus(status) {
		_ = s.PriorArtIndexTask(ctx, id)
	}
	return nil
}

func (s *Store) TaskStats(ctx context.Context) (model.TaskStats, error) {
	var ts model.TaskStats
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='done'    THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='failed'  THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='blocked' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='merged'  THEN 1 ELSE 0 END), 0)
		FROM tasks`)
	return ts, row.Scan(&ts.Total, &ts.Pending, &ts.Running, &ts.Done, &ts.Failed, &ts.Blocked, &ts.Merged)
}

// TasksCompletedSince returns tasks that reached done, failed, or merged status after the given time.
func (s *Store) TasksCompletedSince(ctx context.Context, since time.Time, limit int) ([]*model.Task, error) {
	q := `SELECT id, COALESCE(name,''), COALESCE(plan_id,''), COALESCE(repo_id,''), title, COALESCE(body,''), COALESCE(template,''),
	             COALESCE(role,''), COALESCE(runtime,''),
	             priority, status, retry_count,
	             COALESCE(current_step,''), COALESCE(step_attempts,'{}'),
	             created_at, updated_at,
	             COALESCE(started_at,''), COALESCE(completed_at,'')
	      FROM tasks
	      WHERE status IN ('done', 'failed', 'merged') AND completed_at > ?
	      ORDER BY completed_at ASC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, since.UTC().Format(time.RFC3339))
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

type taskScanner interface {
	Scan(dest ...any) error
}

// TaskSetStepIfRunning atomically advances current_step and resets status to pending.
// stepAttempts is serialized to JSON and stored in the step_attempts column.
// Returns (true, nil) if the update succeeded (task was running at oldStep).
// Returns (false, nil) if the task's current_step or status no longer matches (idempotent).
func (s *Store) TaskSetStepIfRunning(ctx context.Context, id, oldStep, newStep string, stepAttempts map[string]int) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	attemptsJSON := "{}"
	if stepAttempts != nil {
		if b, err := json.Marshal(stepAttempts); err == nil {
			attemptsJSON = string(b)
		}
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET current_step=?, step_attempts=?, status='pending',
		                  completed_at=NULL, updated_at=?
		 WHERE id=? AND COALESCE(current_step,'')=? AND status='running'`,
		newStep, attemptsJSON, now, id, oldStep)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TaskSetTemplate sets the template field on a task.
func (s *Store) TaskSetTemplate(ctx context.Context, id, template string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET template=NULLIF(?, ''), updated_at=? WHERE id=?`, template, now, id)
	return err
}

// TaskSetStepFromPending sets current_step on a pending task (no status guard).
// Used during initial dispatch before the task is marked running.
func (s *Store) TaskSetStepFromPending(ctx context.Context, id, stepName string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET current_step=?, updated_at=? WHERE id=?`,
		stepName, now, id)
	return err
}

// TaskRetry resets a failed task to pending, incrementing retry_count.
// Returns an error if the task is not in failed state.
func (s *Store) TaskRetry(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status='pending', retry_count=retry_count+1, current_step='', step_attempts='{}', started_at=NULL, completed_at=NULL, updated_at=? WHERE id=? AND status='failed'`,
		now, id)
	if err != nil {
		return fmt.Errorf("retry task: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %q is not in failed state (cannot retry)", id)
	}
	return nil
}

// TaskIncrRetry increments the global retry_count.
func (s *Store) TaskIncrRetry(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET retry_count=retry_count+1, updated_at=? WHERE id=?`, now, id)
	return err
}

func scanTask(row taskScanner) (*model.Task, error) {
	var t model.Task
	var createdAt, updatedAt, startedAt, completedAt string
	var stepAttemptsJSON string
	if err := row.Scan(
		&t.ID, &t.Name, &t.PlanID, &t.RepoID, &t.Title, &t.Body, &t.Template,
		&t.Role, &t.Runtime,
		&t.Priority, &t.Status, &t.RetryCount,
		&t.CurrentStep, &stepAttemptsJSON,
		&createdAt, &updatedAt, &startedAt, &completedAt,
	); err != nil {
		return nil, err
	}
	t.StepAttempts = make(map[string]int)
	if stepAttemptsJSON != "" && stepAttemptsJSON != "{}" {
		if err := json.Unmarshal([]byte(stepAttemptsJSON), &t.StepAttempts); err != nil {
			t.StepAttempts = make(map[string]int)
		}
	}
	t.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	t.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if startedAt != "" {
		ts, _ := time.Parse(time.RFC3339, startedAt)
		t.StartedAt = &ts
	}
	if completedAt != "" {
		ts, _ := time.Parse(time.RFC3339, completedAt)
		t.CompletedAt = &ts
	}
	return &t, nil
}

// joinPlaceholders joins placeholder strings with commas.
func joinPlaceholders(ps []string) string {
	return strings.Join(ps, ",")
}
