package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrations(t *testing.T) {
	s := newTestStore(t)
	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != 26 {
		t.Errorf("schema version = %d, want 26", v)
	}
}

func TestTaskControlStatePersistence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-control-state", "", "", "Persist control state", "", "feature", "", "default", 0)
	state := &model.TaskControlState{
		TaskID:           "task-control-state",
		DesiredStep:      "implement",
		ObservedStep:     "implement",
		RuntimeHealth:    "healthy",
		Progress:         model.ProgressPresent,
		ErrorCategory:    "agent_stalling",
		LastActuation:    "nudge",
		EscalationLevel:  1,
		OscillationScore: 2,
		UpdatedAt:        time.Now().UTC().Truncate(time.Second),
		FailureSignature: &model.FailureSignature{Source: "acceptance_controller", Class: "permission"},
	}
	if err := s.TaskControlStatePut(ctx, state); err != nil {
		t.Fatalf("TaskControlStatePut: %v", err)
	}
	got, err := s.TaskControlStateGet(ctx, state.TaskID)
	if err != nil {
		t.Fatalf("TaskControlStateGet: %v", err)
	}
	if got == nil || got.TaskID != state.TaskID || got.DesiredStep != state.DesiredStep || got.RuntimeHealth != state.RuntimeHealth {
		t.Fatalf("got = %+v, want task %q", got, state.TaskID)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatalf("got.UpdatedAt is zero")
	}

	updated := *state
	updated.RuntimeHealth = "degraded"
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Minute)
	if err := s.TaskControlStatePut(ctx, &updated); err != nil {
		t.Fatalf("TaskControlStatePut update: %v", err)
	}
	got, err = s.TaskControlStateGet(ctx, state.TaskID)
	if err != nil {
		t.Fatalf("TaskControlStateGet second read: %v", err)
	}
	if got.RuntimeHealth != "degraded" {
		t.Fatalf("updated runtime_health = %q, want degraded", got.RuntimeHealth)
	}
	if got.FailureSignature == nil || got.FailureSignature.Source != "acceptance_controller" {
		t.Errorf("failure_signature = %#v, want source acceptance_controller", got.FailureSignature)
	}
}

func TestPlanCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	plan, err := s.PlanCreate(ctx, "plan01", "Test Plan", "/tmp/plan01.md")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Title != "Test Plan" {
		t.Errorf("title = %q, want %q", plan.Title, "Test Plan")
	}
	if plan.Status != "active" {
		t.Errorf("status = %q, want active", plan.Status)
	}

	got, err := s.PlanGet(ctx, "plan01")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != plan.ID {
		t.Errorf("id mismatch")
	}

	plans, err := s.PlanList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Errorf("PlanList len = %d, want 1", len(plans))
	}
}

func TestTaskCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	task, err := s.TaskCreate(ctx, "task01", "", "", "Do the thing", "", "", "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Do the thing" {
		t.Errorf("title = %q", task.Title)
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if task.Priority != 5 {
		t.Errorf("priority = %d, want 5", task.Priority)
	}

	if err := s.TaskSetStatus(ctx, "task01", "running"); err != nil {
		t.Fatal(err)
	}
	updated, err := s.TaskGet(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "running" {
		t.Errorf("status = %q, want running", updated.Status)
	}
}

func TestTaskDeps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "t1", "", "", "Task 1", "", "", "", "", 0)
	s.TaskCreate(ctx, "t2", "", "", "Task 2", "", "", "", "", 0)

	if err := s.TaskAddDep(ctx, "t2", "t1"); err != nil {
		t.Fatal(err)
	}
	// Idempotent
	if err := s.TaskAddDep(ctx, "t2", "t1"); err != nil {
		t.Fatal(err)
	}
}

func TestTraceAppend(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "", 0)
	if err := s.TraceAppend(ctx, "task01", "", "signal.started", `{}`); err != nil {
		t.Fatal(err)
	}
	if err := s.TraceAppend(ctx, "task01", "", "signal.done", `{"message":"ok"}`); err != nil {
		t.Fatal(err)
	}

	traces, err := s.TraceList(ctx, "task01", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 2 {
		t.Errorf("trace count = %d, want 2", len(traces))
	}
}

func TestAgentEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "", 0)
	if _, err := s.AgentCreate(ctx, "agent01", "task01", 0, "session01", "", "", "claude-acp", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := s.AgentEventAppend(ctx, "agent01", "task01", 1, "acp.recv", `{"ok":true}`); err != nil {
		t.Fatal(err)
	}
	if err := s.AgentEventAppend(ctx, "agent01", "task01", 2, "acp.stderr", "warn"); err != nil {
		t.Fatal(err)
	}

	events, err := s.AgentEventsList(ctx, "agent01", "", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].Seq != 2 || events[0].Stream != "acp.stderr" {
		t.Fatalf("event = %+v, want seq 2 stderr", events[0])
	}

	agent, err := s.AgentGetBySession(ctx, "session01")
	if err != nil {
		t.Fatal(err)
	}
	if agent.ID != "agent01" {
		t.Fatalf("agent by session = %q, want agent01", agent.ID)
	}
}

// TestAgentEventsList_ReturnsMostRecent verifies that AgentEventsList returns the
// most recent N events (highest seq values) when an agent has more events than
// the limit, and that they are returned in ascending seq order.
func TestAgentEventsList_ReturnsMostRecent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-events", "", "", "Test", "", "", "", "", 0)
	if _, err := s.AgentCreate(ctx, "agent-events", "task-events", 0, "session-events", "", "", "claude-acp", "claude"); err != nil {
		t.Fatal(err)
	}

	// Insert 600 events (seq 1..600)
	for seq := int64(1); seq <= 600; seq++ {
		if err := s.AgentEventAppend(ctx, "agent-events", "task-events", seq, "acp.recv", `{"ok":true}`); err != nil {
			t.Fatalf("failed to append event %d: %v", seq, err)
		}
	}

	// Fetch with limit 500 — should return the LAST 500 events (seq 101..600)
	events, err := s.AgentEventsList(ctx, "agent-events", "", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 500 {
		t.Fatalf("event count = %d, want 500", len(events))
	}

	// First event should be seq 101 (ascending order, most recent 500)
	if events[0].Seq != 101 {
		t.Errorf("first event seq = %d, want 101", events[0].Seq)
	}
	// Last event should be seq 600
	if events[len(events)-1].Seq != 600 {
		t.Errorf("last event seq = %d, want 600", events[len(events)-1].Seq)
	}

	// Verify ascending order throughout
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Errorf("events not in ascending order at index %d: seq %d <= %d", i, events[i].Seq, events[i-1].Seq)
			break
		}
	}
}

func TestAgentUpdateRuntimeEventPreservesStopReason(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-runtime", "", "", "Runtime", "", "", "", "", 0)
	if _, err := s.AgentCreate(ctx, "agent-runtime", "task-runtime", 0, "session-runtime", "", "", "pi-acp", "pi"); err != nil {
		t.Fatal(err)
	}

	first := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	if err := s.AgentUpdateRuntimeEvent(ctx, "agent-runtime", first, "end_turn"); err != nil {
		t.Fatal(err)
	}
	second := first.Add(30 * time.Second)
	if err := s.AgentUpdateRuntimeEvent(ctx, "agent-runtime", second, ""); err != nil {
		t.Fatal(err)
	}

	agent, err := s.AgentGet(ctx, "agent-runtime")
	if err != nil {
		t.Fatal(err)
	}
	if agent.LastStopReason != "end_turn" {
		t.Fatalf("LastStopReason = %q, want end_turn", agent.LastStopReason)
	}
	if agent.LastEventAt == nil || !agent.LastEventAt.Equal(second) {
		t.Fatalf("LastEventAt = %v, want %v", agent.LastEventAt, second)
	}
}

func TestRepoCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	repo, err := s.RepoCreate(ctx, "repo01", "myrepo", "/home/user/myrepo", "main", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Name != "myrepo" {
		t.Errorf("name = %q", repo.Name)
	}

	repos, err := s.RepoList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Errorf("repo count = %d, want 1", len(repos))
	}
}

func TestRepoLintTypecheckFields(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	repo, err := s.RepoCreate(ctx, "repo02", "lintrepo", "/home/user/lintrepo", "main", "go test ./...", "golangci-lint run ./...", "go build ./...", false)
	if err != nil {
		t.Fatal(err)
	}
	if repo.LintCommand != "golangci-lint run ./..." {
		t.Errorf("lint_command = %q, want %q", repo.LintCommand, "golangci-lint run ./...")
	}
	if repo.TypecheckCommand != "go build ./..." {
		t.Errorf("typecheck_command = %q, want %q", repo.TypecheckCommand, "go build ./...")
	}

	// Verify empty lint/typecheck commands are stored as empty strings.
	repo2, err := s.RepoCreate(ctx, "repo03", "barerepo", "/home/user/barerepo", "main", "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if repo2.LintCommand != "" {
		t.Errorf("lint_command should be empty, got %q", repo2.LintCommand)
	}
	if repo2.TypecheckCommand != "" {
		t.Errorf("typecheck_command should be empty, got %q", repo2.TypecheckCommand)
	}
}

func TestLearningCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	l, err := s.LearningCreate(ctx, "learn01", "testing", "Don't mock the DB", "Production migrations failed when mocks passed.")
	if err != nil {
		t.Fatal(err)
	}
	if l.Category != "testing" {
		t.Errorf("category = %q", l.Category)
	}

	ls, err := s.LearningList(ctx, "testing", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 1 {
		t.Errorf("learning count = %d, want 1", len(ls))
	}
}

func TestTaskStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "t1", "", "", "A", "", "", "", "", 0)
	s.TaskCreate(ctx, "t2", "", "", "B", "", "", "", "", 0)
	s.TaskSetStatus(ctx, "t1", "done")

	stats, err := s.TaskStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 2 {
		t.Errorf("total = %d, want 2", stats.Total)
	}
	if stats.Done != 1 {
		t.Errorf("done = %d, want 1", stats.Done)
	}
	if stats.Pending != 1 {
		t.Errorf("pending = %d, want 1", stats.Pending)
	}
}

func TestTaskRetry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create task and mark it failed.
	s.TaskCreate(ctx, "t1", "", "", "Retry me", "body", "feature", "", "", 0)
	s.TaskSetStatus(ctx, "t1", "running")
	s.TaskSetStatus(ctx, "t1", "failed")

	// Retry should succeed.
	err := s.TaskRetry(ctx, "t1")
	if err != nil {
		t.Fatalf("TaskRetry: %v", err)
	}

	// Task should be back to pending.
	task, err := s.TaskGet(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if task.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", task.RetryCount)
	}
	if task.CurrentStep != "" {
		t.Errorf("current_step should be empty, got %q", task.CurrentStep)
	}

	// Retry on non-failed task should fail.
	err = s.TaskRetry(ctx, "t1")
	if err == nil {
		t.Error("expected error retrying non-failed task")
	}
}

func TestControlPlaneDiagnosisProjection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	task, err := s.TaskCreate(ctx, "task-control", "", "", "Control task", "", "feature", "implementer", "default", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TaskSetStatus(ctx, task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	agent, err := s.AgentCreate(ctx, "agent-control", task.ID, 0, "session-control", "", "/tmp/worktree-control", "default", "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ControlObservationPut(ctx, &model.ControlObservation{
		TargetType: "task",
		TargetID:   task.ID,
		TaskID:     task.ID,
		Kind:       "validation",
		Status:     "rejected",
		Reason:     "missing probe evidence",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "acceptance_controller",
		TaskID:       task.ID,
		AgentID:      agent.ID,
		TargetType:   "task",
		TargetID:     task.ID,
		DecisionKind: "validation_rejection",
		Action:       "reject_done_signal",
		Reason:       "missing probe evidence",
		Retryable:    true,
	}); err != nil {
		t.Fatal(err)
	}

	diag, err := s.TaskDiagnose(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diag.NextAction != "retry_or_reset_step" {
		t.Fatalf("next action = %q, want retry_or_reset_step", diag.NextAction)
	}
	if diag.Observed.LatestValidation == nil || diag.Observed.LatestValidation.Reason != "missing probe evidence" {
		t.Fatalf("latest validation not projected: %#v", diag.Observed.LatestValidation)
	}
	if diag.LatestDecision == nil || diag.LatestDecision.Action != "reject_done_signal" {
		t.Fatalf("latest decision not projected: %#v", diag.LatestDecision)
	}
}

func TestTaskDiagnosePendingDispatchFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	task, err := s.TaskCreate(ctx, "task-dispatch-fail", "", "", "Dispatch fail", "", "feature", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TaskSetStepFromPending(ctx, task.ID, "acceptance_spec"); err != nil {
		t.Fatal(err)
	}
	if err := s.ControlObservationPut(ctx, &model.ControlObservation{
		TargetType: "task",
		TargetID:   task.ID,
		TaskID:     task.ID,
		Kind:       "dispatch",
		Status:     "failed",
		Reason:     "spawn agent: thread/start failed",
	}); err != nil {
		t.Fatal(err)
	}

	diag, err := s.TaskDiagnose(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diag.NextAction != "inspect_runtime" {
		t.Fatalf("next action = %q, want inspect_runtime", diag.NextAction)
	}
	if !strings.Contains(diag.Reason, "thread/start failed") {
		t.Fatalf("reason = %q, want dispatch failure details", diag.Reason)
	}
}

func TestTaskDiagnosePersistsControlState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	task, err := s.TaskCreate(ctx, "task-control-diagnose", "", "", "Diagnose persists control state", "", "feature", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TaskSetStatus(ctx, task.ID, "running"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AgentCreate(ctx, "agent-control-diagnose", task.ID, 0, "session-control-diagnose", "", "/tmp/worktree-control-diagnose", "default", "test-model"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "acceptance_controller",
		TaskID:       task.ID,
		TargetType:   "task",
		TargetID:     task.ID,
		DecisionKind: "observe_progress",
		Action:       "continue",
		Reason:       "agent is active",
	}); err != nil {
		t.Fatal(err)
	}

	diag, err := s.TaskDiagnose(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diag.ControlState == nil {
		t.Fatal("diag.ControlState is nil")
	}

	persisted, err := s.TaskControlStateGet(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil {
		t.Fatalf("expected persisted control state for task %q", task.ID)
	}
	if persisted.Progress != diag.ControlState.Progress {
		t.Fatalf("persisted progress = %q, want %q", persisted.Progress, diag.ControlState.Progress)
	}
	if persisted.ErrorCategory != diag.ControlState.ErrorCategory {
		t.Fatalf("persisted error_category = %q, want %q", persisted.ErrorCategory, diag.ControlState.ErrorCategory)
	}
}

func TestEscalationLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.TaskCreate(ctx, "task-escalate", "", "", "Escalate task", "", "feature", "", "default", 0); err != nil {
		t.Fatal(err)
	}
	esc := &model.Escalation{
		TaskID:          "task-escalate",
		TargetType:      "runtime_operator",
		RequestedAction: "restart runtime",
		Reason:          "adapter is wedged",
		CreatedByType:   "controller",
		CreatedByID:     "agent_controller",
	}
	if err := s.EscalationCreate(ctx, esc); err != nil {
		t.Fatal(err)
	}
	open, err := s.EscalationList(ctx, "task-escalate", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].TargetType != "runtime_operator" {
		t.Fatalf("open escalations = %#v", open)
	}
	if err := s.EscalationResolve(ctx, esc.ID, "runtime restarted", "operator"); err != nil {
		t.Fatal(err)
	}
	open, err = s.EscalationList(ctx, "task-escalate", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("open escalations after resolve = %#v", open)
	}
	resolved, err := s.EscalationList(ctx, "task-escalate", "resolved")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Outcome != "runtime restarted" {
		t.Fatalf("resolved escalations = %#v", resolved)
	}
}

func TestTasksReady(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// t1 has no deps → ready immediately.
	s.TaskCreate(ctx, "t1", "", "", "A", "", "", "", "", 0)
	// t2 depends on t1 → not ready until t1 is done.
	s.TaskCreate(ctx, "t2", "", "", "B", "", "", "", "", 0)
	s.TaskAddDep(ctx, "t2", "t1")

	ready, err := s.TasksReady(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != "t1" {
		t.Errorf("ready = %v, want [t1]", ready)
	}

	// Mark t1 done → t2 becomes ready.
	s.TaskSetStatus(ctx, "t1", "done")
	ready, err = s.TasksReady(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].ID != "t2" {
		t.Errorf("ready after t1 done = %v, want [t2]", ready)
	}
}

func TestTaskListStatusFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "t1", "", "", "A", "", "", "", "", 0)
	s.TaskCreate(ctx, "t2", "", "", "B", "", "", "", "", 0)
	s.TaskCreate(ctx, "t3", "", "", "C", "", "", "", "", 0)
	s.TaskSetStatus(ctx, "t1", "running")
	s.TaskSetStatus(ctx, "t2", "failed")

	// Single status filter.
	tasks, err := s.TaskList(ctx, "", "", []string{"running"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != "t1" {
		t.Errorf("single status = %v, want [t1]", tasks)
	}

	// Multiple status filter.
	tasks, err = s.TaskList(ctx, "", "", []string{"pending", "failed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Errorf("multi status count = %d, want 2", len(tasks))
	}
	ids := map[string]bool{tasks[0].ID: true, tasks[1].ID: true}
	if !ids["t2"] || !ids["t3"] {
		t.Errorf("multi status ids = %v, want [t2, t3]", ids)
	}

	// No filter (nil) returns all.
	tasks, err = s.TaskList(ctx, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Errorf("no filter count = %d, want 3", len(tasks))
	}
}

func TestCycleDetection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "t1", "", "", "A", "", "", "", "", 0)
	s.TaskCreate(ctx, "t2", "", "", "B", "", "", "", "", 0)
	s.TaskCreate(ctx, "t3", "", "", "C", "", "", "", "", 0)

	s.TaskAddDepWithCycleCheck(ctx, "t2", "t1") // t2 → t1
	s.TaskAddDepWithCycleCheck(ctx, "t3", "t2") // t3 → t2 → t1

	if err := s.TaskAddDepWithCycleCheck(ctx, "t1", "t3"); err == nil {
		t.Error("expected cycle error adding t1 → t3")
	}
}

func TestMergeQueueLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task01", "", "repo01", "My feature", "", "", "", "", 5)
	s.RepoCreate(ctx, "repo01", "myrepo", "/tmp/repo", "main", "", "", "", false)

	item, err := s.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 5)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if item.Status != "queued" {
		t.Errorf("status = %q, want queued", item.Status)
	}
	if item.AttemptCount != 0 {
		t.Errorf("attempt_count = %d, want 0", item.AttemptCount)
	}

	// Idempotent enqueue should fail (UNIQUE on task_id).
	_, err2 := s.MergeQueueEnqueue(ctx, "mq02", "task01", "repo01", "clankwork/task01", "main", 5)
	if err2 == nil {
		t.Error("second enqueue for same task_id should fail")
	}

	// Next returns the queued item.
	next, err := s.MergeQueueNext(ctx, "repo01")
	if err != nil || next == nil {
		t.Fatalf("MergeQueueNext: %v / %v", next, err)
	}
	if next.ID != "mq01" {
		t.Errorf("next.ID = %q, want mq01", next.ID)
	}

	// Status transitions.
	s.MergeQueueSetStatus(ctx, "mq01", "rebasing")
	got, _ := s.MergeQueueGet(ctx, "mq01")
	if got.Status != "rebasing" || got.StartedAt == nil {
		t.Errorf("after rebasing: status=%q startedAt=%v", got.Status, got.StartedAt)
	}

	// IncrAttempt.
	s.MergeQueueIncrAttempt(ctx, "mq01")
	got, _ = s.MergeQueueGet(ctx, "mq01")
	if got.AttemptCount != 1 {
		t.Errorf("attempt_count = %d, want 1", got.AttemptCount)
	}

	// Depth counts in-progress items.
	depth, err := s.MergeQueueDepth(ctx)
	if err != nil || depth != 1 {
		t.Errorf("depth = %d want 1, err = %v", depth, err)
	}

	// SetMergeSHA + merged status.
	s.MergeQueueSetMergeSHA(ctx, "mq01", "abc123")
	s.MergeQueueSetStatus(ctx, "mq01", "merged")
	got, _ = s.MergeQueueGet(ctx, "mq01")
	if got.MergeSHA != "abc123" || got.Status != "merged" || got.CompletedAt == nil {
		t.Errorf("after merged: sha=%q status=%q completedAt=%v", got.MergeSHA, got.Status, got.CompletedAt)
	}
}

func TestMergeQueuePriorityOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.RepoCreate(ctx, "repo01", "myrepo", "/tmp/repo", "main", "", "", "", false)
	s.TaskCreate(ctx, "t1", "", "repo01", "Low priority", "", "", "", "", 0)
	s.TaskCreate(ctx, "t2", "", "repo01", "High priority", "", "", "", "", 10)

	s.MergeQueueEnqueue(ctx, "mq-lo", "t1", "repo01", "clankwork/t1", "main", 0)
	s.MergeQueueEnqueue(ctx, "mq-hi", "t2", "repo01", "clankwork/t2", "main", 10)

	next, err := s.MergeQueueNext(ctx, "repo01")
	if err != nil || next == nil {
		t.Fatalf("MergeQueueNext: %v", err)
	}
	if next.ID != "mq-hi" {
		t.Errorf("expected high-priority item first, got %q", next.ID)
	}
}

func TestMergeQueueStats(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.RepoCreate(ctx, "repo01", "myrepo", "/tmp/repo", "main", "", "", "", false)
	s.TaskCreate(ctx, "t1", "", "", "A", "", "", "", "", 0)
	s.TaskCreate(ctx, "t2", "", "", "B", "", "", "", "", 0)
	s.TaskCreate(ctx, "t3", "", "", "C", "", "", "", "", 0)

	s.MergeQueueEnqueue(ctx, "mq1", "t1", "repo01", "clankwork/t1", "main", 0)
	s.MergeQueueEnqueue(ctx, "mq2", "t2", "repo01", "clankwork/t2", "main", 0)
	s.MergeQueueEnqueue(ctx, "mq3", "t3", "repo01", "clankwork/t3", "main", 0)
	s.MergeQueueSetStatus(ctx, "mq2", "rebasing")
	s.MergeQueueSetStatus(ctx, "mq3", "merged")

	stats, err := s.MergeQueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Queued != 1 {
		t.Errorf("queued = %d, want 1", stats.Queued)
	}
	if stats.InProgress != 1 {
		t.Errorf("in_progress = %d, want 1", stats.InProgress)
	}
	if stats.Merged != 1 {
		t.Errorf("merged = %d, want 1", stats.Merged)
	}
}

func TestMergeQueueGetByConflictTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.RepoCreate(ctx, "repo01", "myrepo", "/tmp/repo", "main", "", "", "", false)
	s.TaskCreate(ctx, "task01", "", "", "feature", "", "", "", "", 0)
	s.TaskCreate(ctx, "conflict-task-01", "", "", "resolve conflict", "", "", "", "", 0)

	s.MergeQueueEnqueue(ctx, "mq01", "task01", "repo01", "clankwork/task01", "main", 0)
	s.MergeQueueSetConflictTask(ctx, "mq01", "conflict-task-01")
	s.MergeQueueSetStatus(ctx, "mq01", "conflicted")

	item, err := s.MergeQueueGetByConflictTask(ctx, "conflict-task-01")
	if err != nil || item == nil {
		t.Fatalf("GetByConflictTask: %v / %v", item, err)
	}
	if item.ID != "mq01" {
		t.Errorf("item.ID = %q, want mq01", item.ID)
	}

	// Missing conflict task returns nil, nil.
	none, err := s.MergeQueueGetByConflictTask(ctx, "nonexistent")
	if err != nil || none != nil {
		t.Errorf("expected nil, nil; got %v, %v", none, err)
	}
}

func TestMergeQueueResetStuck(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.RepoCreate(ctx, "repo01", "myrepo", "/tmp/repo", "main", "", "", "", false)
	s.TaskCreate(ctx, "t1", "", "", "A", "", "", "", "", 0)
	s.TaskCreate(ctx, "t2", "", "", "B", "", "", "", "", 0)
	s.TaskCreate(ctx, "t3", "", "", "C", "", "", "", "", 0)

	s.MergeQueueEnqueue(ctx, "mq1", "t1", "repo01", "clankwork/t1", "main", 0)
	s.MergeQueueEnqueue(ctx, "mq2", "t2", "repo01", "clankwork/t2", "main", 0)
	s.MergeQueueEnqueue(ctx, "mq3", "t3", "repo01", "clankwork/t3", "main", 0)

	s.MergeQueueSetStatus(ctx, "mq1", "rebasing")
	s.MergeQueueSetStatus(ctx, "mq2", "verifying")
	// mq3 stays queued

	stuck, err := s.MergeQueueResetStuck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stuck) != 2 {
		t.Errorf("stuck count = %d, want 2", len(stuck))
	}
}

func TestMergeQueueFindStrandedDone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Task with template, done, no merge queue entry → stranded.
	s.TaskCreate(ctx, "t1", "", "", "feature task", "", "feature", "", "", 0)
	s.TaskSetStatus(ctx, "t1", "done")

	// Task without template → not stranded.
	s.TaskCreate(ctx, "t2", "", "", "plain task", "", "", "", "", 0)
	s.TaskSetStatus(ctx, "t2", "done")

	// Task with merge queue entry → not stranded.
	s.TaskCreate(ctx, "t3", "", "", "another feature", "", "feature", "", "", 0)
	s.TaskSetStatus(ctx, "t3", "done")
	s.RepoCreate(ctx, "repo01", "myrepo", "/tmp/repo", "main", "", "", "", false)
	s.MergeQueueEnqueue(ctx, "mq3", "t3", "repo01", "clankwork/t3", "main", 0)

	stranded, err := s.MergeQueueFindStrandedDone(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stranded) != 1 || stranded[0].ID != "t1" {
		t.Errorf("stranded = %v, want [t1]", stranded)
	}
}

func TestMergeQueueEnqueueReactivatesTerminalItem(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.RepoCreate(ctx, "repo01", "myrepo", "/tmp/repo", "main", "", "", "", false)
	s.TaskCreate(ctx, "task01", "", "repo01", "feature task", "", "feature", "", "", 0)
	s.MergeQueueEnqueue(ctx, "mq-old", "task01", "repo01", "clankwork/task01", "main", 1)
	s.MergeQueueSetFailureLog(ctx, "mq-old", "old failure")
	s.MergeQueueSetStatus(ctx, "mq-old", "rejected")

	item, err := s.MergeQueueEnqueue(ctx, "mq-new", "task01", "repo01", "clankwork/task01", "main", 7)
	if err != nil {
		t.Fatalf("requeue terminal item: %v", err)
	}
	if item.ID != "mq-new" || item.Status != "queued" || item.Priority != 7 {
		t.Errorf("item = %+v, want refreshed queued mq-new priority 7", item)
	}
	if item.AttemptCount != 0 || item.FailureLog != "" || item.CompletedAt != nil {
		t.Errorf("terminal item fields not reset: attempts=%d failure=%q completed=%v", item.AttemptCount, item.FailureLog, item.CompletedAt)
	}
}

func TestLearningSearch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.LearningCreate(ctx, "l1", "go", "Use defer for cleanup", "Always defer file.Close() immediately after opening.")
	s.LearningCreate(ctx, "l2", "git", "Rebase before merge", "Run git rebase on the feature branch before merging to main.")
	s.LearningCreate(ctx, "l3", "go", "Handle errors explicitly", "Never ignore error return values from Go functions.")

	// FTS search matching one term.
	results, err := s.LearningSearch(ctx, "rebase OR merge", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "l2" {
		t.Errorf("search('rebase OR merge') = %v, want [l2]", results)
	}

	// Search with no matches still returns empty (not error).
	none, err := s.LearningSearch(ctx, "xyzzy", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("search('xyzzy') = %d results, want 0", len(none))
	}

	// Empty query falls back to LearningList (all results).
	all, err := s.LearningSearch(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("search('') = %d results, want 3", len(all))
	}
}

func TestLearningBumpAccess(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.LearningCreate(ctx, "l1", "go", "defer cleanup", "body")
	s.LearningBumpAccess(ctx, []string{"l1"})
	s.LearningBumpAccess(ctx, []string{"l1"})

	l, err := s.LearningGet(ctx, "l1")
	if err != nil {
		t.Fatal(err)
	}
	if l.AccessCount != 2 {
		t.Errorf("access_count = %d, want 2", l.AccessCount)
	}
	if l.LastAccessed == nil {
		t.Error("last_accessed not set")
	}
}

func TestAgentCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "", 0)
	s.TaskSetStatus(ctx, "task01", "running")

	agent, err := s.AgentCreate(ctx, "agent01", "task01", 0, "cw-worker-task01", "/tmp/cw/logs/task01.log", "/tmp/cw/worktrees/task01", "default", "claude-sonnet-4-20250514")
	if err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	if agent.Status != "running" {
		t.Errorf("status = %q, want running", agent.Status)
	}
	if agent.Transport != "tmux" {
		t.Errorf("transport = %q, want tmux", agent.Transport)
	}
	if agent.RuntimeSessionID != "cw-worker-task01" {
		t.Errorf("runtime_session_id = %q, want cw-worker-task01", agent.RuntimeSessionID)
	}

	// GetByTask
	a2, err := s.AgentGetByTask(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if a2 == nil || a2.ID != agent.ID {
		t.Errorf("AgentGetByTask returned wrong agent")
	}

	// Heartbeat
	if err := s.AgentUpdateHeartbeat(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	a3, _ := s.AgentGet(ctx, agent.ID)
	if a3.LastHeartbeat == nil {
		t.Error("last_heartbeat not set")
	}

	// SetEnded
	if err := s.AgentSetEnded(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	a4, _ := s.AgentGet(ctx, agent.ID)
	if a4.Status != "done" {
		t.Errorf("status after SetEnded = %q, want done", a4.Status)
	}
	if a4.EndedAt == nil {
		t.Error("ended_at not set")
	}

	// RunningCount should now be 0.
	n, _ := s.AgentRunningCount(ctx)
	if n != 0 {
		t.Errorf("running count = %d, want 0", n)
	}

	// AgentStats should reflect: total=1, running=0, done=1, killed=0.
	stats, err := s.AgentStats(ctx)
	if err != nil {
		t.Fatalf("AgentStats: %v", err)
	}
	if stats.Total != 1 {
		t.Errorf("AgentStats.Total = %d, want 1", stats.Total)
	}
	if stats.Running != 0 {
		t.Errorf("AgentStats.Running = %d, want 0", stats.Running)
	}
	if stats.Done != 1 {
		t.Errorf("AgentStats.Done = %d, want 1", stats.Done)
	}
	if stats.Killed != 0 {
		t.Errorf("AgentStats.Killed = %d, want 0", stats.Killed)
	}
}

func TestAgentRuntimeMetadata(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "", 0)
	agent, err := s.AgentCreateWithRuntime(ctx, "agent01", "task01", 0, "cw-worker-task01-acp", "acp", "cw-worker-task01-acp", 1234, "", "", "claude-acp", "claude")
	if err != nil {
		t.Fatalf("AgentCreateWithRuntime: %v", err)
	}
	if agent.Transport != "acp" || agent.RuntimeSessionID != "cw-worker-task01-acp" || agent.PID != 1234 {
		t.Fatalf("runtime metadata = transport %q session %q pid %d, want acp/cw-worker-task01-acp/1234", agent.Transport, agent.RuntimeSessionID, agent.PID)
	}
	if err := s.AgentUpdateRuntimeEvent(ctx, agent.ID, agent.StartedAt, "end_turn"); err != nil {
		t.Fatal(err)
	}
	agent, err = s.AgentGet(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.LastEventAt == nil || agent.LastStopReason != "end_turn" {
		t.Fatalf("runtime event metadata = last_event_at %v stop %q, want set/end_turn", agent.LastEventAt, agent.LastStopReason)
	}
	bySession, err := s.AgentGetBySession(ctx, agent.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if bySession.ID != agent.ID {
		t.Fatalf("AgentGetBySession = %q, want %q", bySession.ID, agent.ID)
	}
}

func TestAgentRunningACPPIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create tasks for agents.
	s.TaskCreate(ctx, "task01", "", "", "Test 1", "", "", "", "", 0)
	s.TaskCreate(ctx, "task02", "", "", "Test 2", "", "", "", "", 0)
	s.TaskCreate(ctx, "task03", "", "", "Test 3", "", "", "", "", 0)
	s.TaskCreate(ctx, "task04", "", "", "Test 4", "", "", "", "", 0)

	// ACP agent with PID (running) — should be returned.
	s.AgentCreateWithRuntime(ctx, "acp01", "task01", 0, "acp-session-01", "acp", "acp-session-01", 9999, "", "", "pi-acp", "pi")
	// Tmux agent with PID (running) — should NOT be returned (tmux survives restarts).
	s.AgentCreateWithRuntime(ctx, "tmux01", "task02", 0, "tmux-session-01", "tmux", "tmux-session-01", 8888, "", "", "default", "claude")
	// ACP agent without PID (running) — should NOT be returned (no PID to kill).
	s.AgentCreateWithRuntime(ctx, "acp02", "task03", 0, "acp-session-02", "acp", "acp-session-02", 0, "", "", "pi-acp", "pi")
	// ACP agent with PID but not running (done) — should NOT be returned.
	acpDone, _ := s.AgentCreateWithRuntime(ctx, "acp03", "task04", 0, "acp-session-03", "acp", "acp-session-03", 7777, "", "", "pi-acp", "pi")
	s.AgentSetStatus(ctx, acpDone.ID, "done")

	agents, err := s.AgentRunningACPPIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("AgentRunningACPPIDs returned %d agents, want 1", len(agents))
	}
	if agents[0].ID != "acp01" {
		t.Errorf("agent ID = %q, want acp01", agents[0].ID)
	}
	if agents[0].PID != 9999 {
		t.Errorf("agent PID = %d, want 9999", agents[0].PID)
	}
	if agents[0].TaskID != "task01" {
		t.Errorf("agent taskID = %q, want task01", agents[0].TaskID)
	}
}

func TestPriorArtIndexAndSearch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.RepoCreate(ctx, "repo-auth", "auth-repo", t.TempDir(), "main", "go test ./...", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.TaskCreate(ctx, "task-auth", "", "repo-auth", "Add auth middleware negative cases", "Implement token validation", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	spec := &model.AcceptanceSpec{
		TaskID: "task-auth",
		Criteria: []model.AcceptanceCriterion{{
			ID:                        "expired_token_rejected",
			Description:               "Expired tokens are rejected",
			RequiresNegativeAssertion: true,
			RequiredArtifacts:         []string{"cli_transcript"},
			Probes: []model.AcceptanceProbe{{
				ID:                "expired_token",
				Description:       "request with expired token fails",
				Type:              "command",
				Command:           "go test ./...",
				RequiredEvidence:  []string{"cli_transcript"},
				NegativeAssertion: "expired token must not authenticate",
			}},
		}},
	}
	if err := s.AcceptanceSpecPut(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := s.DoneBundlePut(ctx, &model.DoneBundle{
		TaskID:       "task-auth",
		Summary:      "Added auth middleware and expired token coverage",
		FilesChanged: []string{"internal/auth/middleware.go", "internal/auth/middleware_test.go"},
		TestsRun:     []string{"go test ./..."},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.TraceAppend(ctx, "task-auth", "", "step.failure_context", `{"message":"acceptance failed because expired token negative case was missing"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.VerificationReportPut(ctx, &model.VerificationReport{
		TaskID: "task-auth",
		Results: []model.VerificationResult{{
			CriterionID: "expired_token_rejected",
			Status:      "pass",
			Reason:      "integration test passed",
			Evidence:    []model.Evidence{{Type: "cli_transcript", ProbeID: "expired_token", Command: "go test ./..."}},
		}},
		ComputedConfidence: 0.9,
		ConfidenceLabel:    "high",
	}, "pass"); err != nil {
		t.Fatal(err)
	}
	if err := s.TaskSetStatus(ctx, "task-auth", "merged"); err != nil {
		t.Fatal(err)
	}

	resp, err := s.PriorArtSearch(ctx, model.PriorArtSearchRequest{Query: "auth middleware expired token", RepoID: "repo-auth", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].ReworkScore == 0 || resp.Results[0].RiskScore == 0 {
		t.Fatalf("scores = rework %.0f risk %.0f, want non-zero", resp.Results[0].ReworkScore, resp.Results[0].RiskScore)
	}
}

func TestTaskSetStatusIfRunningIndexesOnlyWhenStatusChanges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.TaskCreate(ctx, "task-stale", "", "", "Stale task", "should not index", "", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.TaskSetStatusIfRunning(ctx, "task-stale", "done"); err != nil {
		t.Fatal(err)
	}
	h, err := s.PriorArtGetByTask(ctx, "task-stale")
	if err != nil {
		t.Fatal(err)
	}
	if h != nil {
		t.Fatalf("pending task was indexed without a running -> done transition: %+v", h)
	}

	if err := s.TaskSetStatus(ctx, "task-stale", "running"); err != nil {
		t.Fatal(err)
	}
	if err := s.TaskSetStatusIfRunning(ctx, "task-stale", "done"); err != nil {
		t.Fatal(err)
	}
	h, err = s.PriorArtGetByTask(ctx, "task-stale")
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		t.Fatal("running task was not indexed after done transition")
	}
}
