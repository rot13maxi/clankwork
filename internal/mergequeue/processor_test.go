package mergequeue

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newTestProcessor(t *testing.T, st *store.Store) *Processor {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Scheduler.MergeQueueMaxAttempts = 3
	return NewProcessor(st, cfg, t.TempDir(), nil)
}

// TestEnqueueIfAutoMerge_NotDone: task not yet done → not enqueued.
func TestEnqueueIfAutoMerge_NotDone(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "feature", "", "", 0)
	// status stays "pending"

	p := newTestProcessor(t, st)
	p.EnqueueIfAutoMerge(ctx, "task01")

	item, _ := st.MergeQueueGetByTask(ctx, "task01")
	if item != nil {
		t.Errorf("expected no queue item for pending task, got %+v", item)
	}
}

// TestEnqueueIfAutoMerge_NoTemplate: done task with no template → not enqueued.
func TestEnqueueIfAutoMerge_NoTemplate(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "plain task", "", "", "", "", 0)
	st.TaskSetStatus(ctx, "task01", "done")

	p := newTestProcessor(t, st)
	p.EnqueueIfAutoMerge(ctx, "task01")

	item, _ := st.MergeQueueGetByTask(ctx, "task01")
	if item != nil {
		t.Errorf("expected no queue item for no-template task, got %+v", item)
	}
}

// TestEnqueueIfAutoMerge_AutoMerge: done task with auto_merge template → enqueued.
func TestEnqueueIfAutoMerge_AutoMerge(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	// "feature" built-in has auto_merge=true
	st.TaskCreate(ctx, "task01", "", "repo01", "my feature", "", "feature", "", "", 5)
	st.TaskSetStatus(ctx, "task01", "done")

	p := newTestProcessor(t, st)
	p.EnqueueIfAutoMerge(ctx, "task01")

	item, err := st.MergeQueueGetByTask(ctx, "task01")
	if err != nil || item == nil {
		t.Fatalf("expected queue item, got nil (err=%v)", err)
	}
	if item.Status != "queued" {
		t.Errorf("status = %q, want queued", item.Status)
	}
	if item.Branch != "clankwork/task01" {
		t.Errorf("branch = %q, want clankwork/task01", item.Branch)
	}
	if item.Target != "main" {
		t.Errorf("target = %q, want main", item.Target)
	}
	if item.Priority != 5 {
		t.Errorf("priority = %d, want 5", item.Priority)
	}
}

// TestEnqueueIfAutoMerge_Idempotent: second call is a no-op (UNIQUE constraint).
func TestEnqueueIfAutoMerge_Idempotent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "my feature", "", "feature", "", "", 0)
	st.TaskSetStatus(ctx, "task01", "done")

	p := newTestProcessor(t, st)
	p.EnqueueIfAutoMerge(ctx, "task01")
	p.EnqueueIfAutoMerge(ctx, "task01") // should be silent no-op

	items, err := st.MergeQueueList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("queue depth = %d, want 1", len(items))
	}
}

// TestEnqueueIfAutoMerge_ConflictResolved: when a conflict-resolver task finishes,
// the parent merge item should be re-queued.
func TestEnqueueIfAutoMerge_ConflictResolved(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feature", "", "feature", "", "", 0)
	st.TaskCreate(ctx, "conflict01", "", "", "resolve conflict", "", "", "", "", 0)

	// Set up a merge item in conflicted state pointing to conflict01.
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)
	st.MergeQueueSetConflictTask(ctx, "mq01", "conflict01")
	st.MergeQueueSetStatus(ctx, "mq01", "conflicted")

	// Mark conflict task done.
	st.TaskSetStatus(ctx, "conflict01", "done")

	p := newTestProcessor(t, st)
	p.EnqueueIfAutoMerge(ctx, "conflict01")

	item, _ := st.MergeQueueGet(ctx, "mq01")
	if item.Status != "queued" {
		t.Errorf("parent item status = %q after conflict resolved, want queued", item.Status)
	}
}

// TestHandleConflictFailed: when a conflict-resolver task fails, the parent merge item
// should be re-queued so it retries conflict resolution.
func TestHandleConflictFailed(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feature", "", "feature", "", "", 0)
	st.TaskCreate(ctx, "conflict01", "", "", "Resolve conflicts: clankwork/task01", "", "", "conflict-resolver", "", 0)

	// Set up a merge item in conflicted state pointing to conflict01.
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)
	st.MergeQueueSetConflictTask(ctx, "mq01", "conflict01")
	st.MergeQueueSetStatus(ctx, "mq01", "conflicted")

	// Mark conflict task as failed.
	st.TaskSetStatus(ctx, "conflict01", "failed")

	p := newTestProcessor(t, st)
	p.HandleConflictFailed(ctx, "conflict01")

	item, _ := st.MergeQueueGet(ctx, "mq01")
	if item.Status != "queued" {
		t.Errorf("parent item status = %q after conflict resolver failed, want queued", item.Status)
	}

	events, err := st.ControlPlaneEvents(ctx, "mq01", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var hasDecision, hasActuation bool
	for _, ev := range events {
		if ev.Source == "decision" && ev.Type == "merge_conflict_resolver_failed" {
			hasDecision = true
		}
		if ev.Source == "actuation" && ev.Type == "merge.conflict_resolver" {
			hasActuation = true
		}
	}
	if !hasDecision {
		t.Fatal("expected merge_conflict_resolver_failed decision audit event")
	}
	if !hasActuation {
		t.Fatal("expected merge.conflict_resolver actuation audit event")
	}
}

// TestHandleConflictFailed_NotConflicted: only acts when parent is in conflicted state.
func TestHandleConflictFailed_NotConflicted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feature", "", "feature", "", "", 0)
	st.TaskCreate(ctx, "conflict01", "", "", "Resolve conflicts: clankwork/task01", "", "", "conflict-resolver", "", 0)

	// Set up a merge item in 'queued' state (not conflicted).
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)
	st.MergeQueueSetConflictTask(ctx, "mq01", "conflict01")

	st.TaskSetStatus(ctx, "conflict01", "failed")

	p := newTestProcessor(t, st)
	p.HandleConflictFailed(ctx, "conflict01")

	item, _ := st.MergeQueueGet(ctx, "mq01")
	if item.Status != "queued" {
		t.Errorf("parent item status = %q, expected unchanged (queued)", item.Status)
	}
}

// TestRequeueOrReject_BelowMax: below max attempts -> task reset to pending, item to failed.
func TestRequeueOrReject_BelowMax(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.TaskSetStatus(ctx, "task01", "running")
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)
	// attempt_count starts at 0; maxAttempts=3, so 1 attempt after incr → still below
	if err := p.requeueOrReject(ctx, item, nil, "verify failed"); err != nil {
		t.Fatal(err)
	}

	updatedItem, _ := st.MergeQueueGet(ctx, "mq01")
	if updatedItem.Status != "failed" {
		t.Errorf("item status = %q, want failed", updatedItem.Status)
	}

	updatedTask, _ := st.TaskGet(ctx, "task01")
	if updatedTask.Status != "pending" {
		t.Errorf("task status = %q, want pending", updatedTask.Status)
	}
}

func TestEnqueueIfAutoMerge_RequeuesRejectedTaskItem(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "feature", "", "", 0)
	st.TaskSetStatus(ctx, "task01", "done")
	st.MergeQueueEnqueue(ctx, "mq-old", "task01", "repo01", "clankwork/task01", "main", 0)
	st.MergeQueueSetFailureLog(ctx, "mq-old", "semantic conflict")
	st.MergeQueueSetStatus(ctx, "mq-old", "rejected")

	p := newTestProcessor(t, st)
	p.EnqueueIfAutoMerge(ctx, "task01")

	item, err := st.MergeQueueGetByTask(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "queued" {
		t.Errorf("item status = %q, want queued", item.Status)
	}
	if item.ID == "mq-old" {
		t.Error("item id should be refreshed when terminal item is requeued")
	}
	if item.AttemptCount != 0 || item.FailureLog != "" || item.CompletedAt != nil {
		t.Errorf("requeued item not reset: attempts=%d failure=%q completed=%v", item.AttemptCount, item.FailureLog, item.CompletedAt)
	}
}

// TestRequeueOrReject_AtMax: at max attempts → item rejected, task stays.
func TestRequeueOrReject_AtMax(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)
	// Pre-load to maxAttempts-1 so next incr hits the limit.
	for i := 0; i < 2; i++ {
		st.MergeQueueIncrAttempt(ctx, "mq01")
	}

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)
	if err := p.requeueOrReject(ctx, item, nil, "verify failed"); err != nil {
		t.Fatal(err)
	}

	updatedItem, _ := st.MergeQueueGet(ctx, "mq01")
	if updatedItem.Status != "rejected" {
		t.Errorf("item status = %q, want rejected", updatedItem.Status)
	}
}

// TestHandleConflict_CreatesTask: first conflict → conflict task created, item = conflicted.
func TestHandleConflict_CreatesTask(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	repo, _ := st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)
	if err := p.handleConflict(ctx, item, repo, "conflict: foo.go"); err != nil {
		t.Fatal(err)
	}

	updatedItem, _ := st.MergeQueueGet(ctx, "mq01")
	if updatedItem.Status != "conflicted" {
		t.Errorf("item status = %q, want conflicted", updatedItem.Status)
	}
	if updatedItem.ConflictTaskID == "" {
		t.Error("conflict_task_id not set")
	}
	if updatedItem.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1", updatedItem.AttemptCount)
	}

	// Conflict task should exist in the store.
	conflictTask, err := st.TaskGet(ctx, updatedItem.ConflictTaskID)
	if err != nil || conflictTask == nil {
		t.Fatalf("conflict task not created: %v", err)
	}
}

// TestHandleConflict_MaxAttempts: at max attempts → item rejected, no conflict task.
func TestHandleConflict_MaxAttempts(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	repo, _ := st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)
	// Pre-load to maxAttempts-1 so next incr hits the limit.
	for i := 0; i < 2; i++ {
		st.MergeQueueIncrAttempt(ctx, "mq01")
	}

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)
	if err := p.handleConflict(ctx, item, repo, "still conflicting"); err != nil {
		t.Fatal(err)
	}

	updatedItem, _ := st.MergeQueueGet(ctx, "mq01")
	if updatedItem.Status != "rejected" {
		t.Errorf("item status = %q, want rejected", updatedItem.Status)
	}
}

// TestFailItem_RequeueBelowMax: below max attempts → item re-queued, not permanently failed.
func TestFailItem_RequeueBelowMax(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)
	p.failItem(ctx, item, "worktree add failed: exit 1")

	got, _ := st.MergeQueueGet(ctx, "mq01")
	if got.Status != "queued" {
		t.Errorf("status = %q, want queued (re-queued with backoff)", got.Status)
	}
	if got.FailureLog == "" {
		t.Error("failure_log not set")
	}
	if got.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1", got.AttemptCount)
	}
}

// TestFailItem_FailedAtMax: at max attempts → item permanently failed.
func TestFailItem_FailedAtMax(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "r", "/tmp/r", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "feat", "", "", "", "", 0)
	st.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)
	// Pre-load to maxAttempts-1 so next incr hits the limit.
	for i := 0; i < 2; i++ {
		st.MergeQueueIncrAttempt(ctx, "mq01")
	}

	item, _ := st.MergeQueueGet(ctx, "mq01")
	p := newTestProcessor(t, st)
	p.failItem(ctx, item, "worktree add failed: exit 1")

	got, _ := st.MergeQueueGet(ctx, "mq01")
	if got.Status != "failed" {
		t.Errorf("status = %q, want failed (max attempts exhausted)", got.Status)
	}
	if got.FailureLog == "" {
		t.Error("failure_log not set")
	}
}
