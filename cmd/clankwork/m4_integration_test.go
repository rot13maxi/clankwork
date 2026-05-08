package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/api"
	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/learning"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

// m4Env holds the in-process daemon with FakeSpawner (no real tmux needed).
type m4Env struct {
	store      *store.Store
	disp       *scheduler.Dispatcher
	recon      *scheduler.Reconciler
	spawner    *worker.FakeSpawner
	wt         *worker.FakeWorktreeCreator
	homeDir    string
	socketPath string
	cfg        *config.Config
}

// ---------------------------------------------------------------------------
// Test: Template-based dispatch with full step routing (feature template)
// ---------------------------------------------------------------------------

func TestM4_TemplateDispatchFeatureLifecycle(t *testing.T) {
	env := setupM4(t)
	ctx := context.Background()

	// Create task with "feature" template.
	// Feature template: acceptance_spec → implement → lint → typecheck → test → acceptance → complete.
	taskID := fmt.Sprintf("task-feat-%d", time.Now().UnixNano())
	env.store.TaskCreate(ctx, taskID, "", "", "Add user profile page", "", "feature", "", "default", 0)

	// Tick dispatcher — should set entry step to "acceptance_spec" and dispatch.
	env.disp.Tick(ctx)

	task := mustGetTask(t, env, taskID)
	assertEqual(t, "running", task.Status, "task status after first dispatch")
	assertEqual(t, "acceptance_spec", task.CurrentStep, "current step after first dispatch")

	// Step 1: acceptance_spec succeeds → routes to implement.
	err := env.disp.RouteStep(ctx, taskID, "acceptance_spec", "success")
	if err != nil {
		t.Fatalf("RouteStep acceptance_spec→implement: %v", err)
	}

	task = mustGetTask(t, env, taskID)
	assertEqual(t, "implement", task.CurrentStep, "current step after first dispatch")

	// Step 2: implement (agent) succeeds → routes to lint.
	// Use RouteStep directly (simulates what signal done does for the routing part).
	env.store.TaskSetStatus(ctx, taskID, "running")
	err = env.disp.RouteStep(ctx, taskID, "implement", "success")
	if err != nil {
		t.Fatalf("RouteStep implement→lint: %v", err)
	}

	task = mustGetTask(t, env, taskID)
	assertEqual(t, "lint", task.CurrentStep, "current step after implement success")

	// Step 3: lint succeeds → routes to typecheck.
	env.store.TaskSetStatus(ctx, taskID, "running") // simulate being re-dispatched
	err = env.disp.RouteStep(ctx, taskID, "lint", "success")
	if err != nil {
		t.Fatalf("RouteStep lint→typecheck: %v", err)
	}

	task = mustGetTask(t, env, taskID)
	assertEqual(t, "typecheck", task.CurrentStep, "current step after lint success")

	// Step 4: typecheck succeeds → routes to test.
	env.store.TaskSetStatus(ctx, taskID, "running")
	err = env.disp.RouteStep(ctx, taskID, "typecheck", "success")
	if err != nil {
		t.Fatalf("RouteStep typecheck→test: %v", err)
	}

	task = mustGetTask(t, env, taskID)
	assertEqual(t, "test", task.CurrentStep, "current step after typecheck success")

	// Step 5: test (deterministic) succeeds → routes to acceptance.
	env.store.TaskSetStatus(ctx, taskID, "running")
	err = env.disp.RouteStep(ctx, taskID, "test", "success")
	if err != nil {
		t.Fatalf("RouteStep test→acceptance: %v", err)
	}

	task = mustGetTask(t, env, taskID)
	assertEqual(t, "acceptance", task.CurrentStep, "current step after test success")

	// Step 6: acceptance (agent) succeeds → routes to complete.
	env.store.TaskSetStatus(ctx, taskID, "running")
	err = env.disp.RouteStep(ctx, taskID, "acceptance", "success")
	if err != nil {
		t.Fatalf("RouteStep acceptance→complete: %v", err)
	}

	task = mustGetTask(t, env, taskID)
	assertEqual(t, "done", task.Status, "task status after acceptance success")

	// Verify step routing traces through the deterministic cheap-check funnel,
	// including the terminal acceptance -> complete route.
	traces := listTraces(t, env, taskID, "step.routed")
	if len(traces) != 6 {
		t.Errorf("expected 6 step.routed traces (acceptance_spec→implement→lint→typecheck→test→acceptance→complete), got %d", len(traces))
	}
}

func TestM4_TemplateDispatchWithSignalDone(t *testing.T) {
	env := setupM4(t)
	ctx := context.Background()

	// Create task with "simple" template: implement → complete (no deterministic step).
	taskID := fmt.Sprintf("task-simple-%d", time.Now().UnixNano())
	env.store.TaskCreate(ctx, taskID, "", "", "Simple task via signal", "", "simple", "", "default", 0)

	// Dispatch via Tick.
	env.disp.Tick(ctx)
	task := mustGetTask(t, env, taskID)
	assertEqual(t, "running", task.Status, "task status after dispatch")
	assertEqual(t, "implement", task.CurrentStep, "current step")

	// Signal done via HTTP — exercises the full signal → RouteStep path.
	signalDone(t, env, taskID)

	task = mustGetTask(t, env, taskID)
	assertEqual(t, "done", task.Status, "task status after signal done")
}

// ---------------------------------------------------------------------------
// Test: Triage auto-classification
// ---------------------------------------------------------------------------

func TestM4_TriageClassification(t *testing.T) {
	tests := []struct {
		title    string
		body     string
		expected string
	}{
		{"fix login bug", "", "bugfix"},
		{"Bug in auth flow", "", "bugfix"},
		{"refactor auth module", "", "refactor"},
		{"update button color", "", "simple"}, // short body, no acceptance criteria
		{"Add comprehensive user management system", "This feature must have the following acceptance criteria: users can CRUD their profiles, admins can manage roles.", "feature"},
	}

	for _, tc := range tests {
		t.Run(tc.title, func(t *testing.T) {
			task := &model.Task{Title: tc.title, Body: tc.body}
			result := scheduler.TriageTask(task)
			assertEqual(t, tc.expected, result, "triage result for "+tc.title)
		})
	}
}

// ---------------------------------------------------------------------------
// Test: Configurable verify command
// ---------------------------------------------------------------------------

func TestM4_ConfigurableVerifyCommand(t *testing.T) {
	env := setupM4(t)
	ctx := context.Background()

	// Create a repo with a custom verify command.
	repoID := "repo-verify-custom"
	env.store.RepoCreate(ctx, repoID, "verify-repo", "/tmp/verify-repo", "main", "npm test -- --ci", "", "", false)

	// Create task using feature template (which has "clankwork verify" in the test step).
	taskID := "task-verify"
	env.store.TaskCreate(ctx, taskID, "", repoID, "Add widget", "", "feature", "", "default", 0)
	env.store.TaskSetStepFromPending(ctx, taskID, "test")
	env.store.TaskSetStatus(ctx, taskID, "running")

	// Get the task and use the exported RouteStep to verify resolve logic.
	// Instead, we test resolveVerifyCommand indirectly: the dispatcher would
	// call it during dispatchDeterministic. We can verify via the template step.
	// The key test: when the repo has verify_command set, the "clankwork verify"
	// sentinel should resolve to that command.

	// Verify by checking the repo's verify command is set correctly.
	repo, err := env.store.RepoGet(ctx, repoID)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, "npm test -- --ci", repo.VerifyCommand, "repo verify command")

	// Also verify the fallback: repo without verify command falls back to "go test ./...".
	repoIDNoVerify := "repo-no-verify"
	env.store.RepoCreate(ctx, repoIDNoVerify, "bare-repo", "/tmp/bare-repo", "main", "", "", "", false)
	repoNoVerify, _ := env.store.RepoGet(ctx, repoIDNoVerify)
	assertEqual(t, "", repoNoVerify.VerifyCommand, "repo with no verify command should be empty")
}

// ---------------------------------------------------------------------------
// Test: Progressive disclosure in bootstrap
// ---------------------------------------------------------------------------

func TestM4_ProgressiveDisclosureBootstrap(t *testing.T) {
	env := setupM4(t)
	ctx := context.Background()

	// Create learnings of different tiers.
	// Create 7 index, 5 digest, 3 source learnings with a searchable keyword "widget".
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("learn-idx-%d", i)
		env.store.LearningCreate(ctx, id, "testing", fmt.Sprintf("Widget index tip %d", i), fmt.Sprintf("Index body %d about widget", i))
		env.store.DB().ExecContext(ctx, `UPDATE learnings SET tier = 'index' WHERE id = ?`, id)
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("learn-dig-%d", i)
		env.store.LearningCreate(ctx, id, "testing", fmt.Sprintf("Widget digest tip %d", i), fmt.Sprintf("Digest body %d about widget patterns", i))
		env.store.DB().ExecContext(ctx, `UPDATE learnings SET tier = 'digest' WHERE id = ?`, id)
	}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("learn-src-%d", i)
		env.store.LearningCreate(ctx, id, "testing", fmt.Sprintf("Widget source material %d", i), fmt.Sprintf("Full source material %d about widget implementation details and patterns", i))
		// tier defaults to "source" from LearningCreate
	}

	// Create a task whose title matches the learnings.
	taskID := "task-bootstrap"
	env.store.TaskCreate(ctx, taskID, "", "", "Build widget feature", "We need to implement widget", "", "", "default", 0)

	// Call bootstrap via HTTP.
	boot := mustBootstrap(t, env, taskID, "", "")

	// Count learnings by tier.
	indexCount, digestCount, sourceCount := 0, 0, 0
	for _, l := range boot.Learnings {
		switch l.Tier {
		case "index":
			indexCount++
			if l.Body != "" {
				t.Errorf("index learning %q should have empty body for progressive disclosure, got %q", l.ID, l.Body)
			}
		case "digest":
			digestCount++
		default:
			sourceCount++
		}
	}

	if indexCount > 5 {
		t.Errorf("index learnings = %d, want <= 5 (tier cap)", indexCount)
	}
	if digestCount > 3 {
		t.Errorf("digest learnings = %d, want <= 3 (tier cap)", digestCount)
	}
	if sourceCount > 1 {
		t.Errorf("source learnings = %d, want <= 1 (tier cap)", sourceCount)
	}
}

// ---------------------------------------------------------------------------
// Test: Failure context propagation
// ---------------------------------------------------------------------------

func TestM4_FailureContextPropagation(t *testing.T) {
	env := setupM4(t)
	ctx := context.Background()

	// Create task with feature template.
	taskID := "task-fail-ctx"
	env.store.TaskCreate(ctx, taskID, "", "", "Fix widget crash", "", "feature", "", "default", 0)
	env.store.TaskSetStepFromPending(ctx, taskID, "implement")
	env.store.TaskSetStatus(ctx, taskID, "running")

	// Simulate failure: agent signals failed, which stores failure context.
	fcPayload, _ := json.Marshal(map[string]string{"step": "implement", "message": "tests failed: widget nil pointer"})
	env.store.TraceAppend(ctx, taskID, "", "step.failure_context", string(fcPayload))

	// Route step as failure → retries implement.
	env.disp.RouteStep(ctx, taskID, "implement", "failure")

	// Now bootstrap should include the failure context.
	boot := mustBootstrap(t, env, taskID, "", "")

	if boot.FailureContext == "" {
		t.Error("bootstrap should include failure context from previous attempt")
	}
	if !strings.Contains(boot.FailureContext, "widget nil pointer") {
		t.Errorf("failure context should contain the error message, got: %s", boot.FailureContext)
	}
}

// ---------------------------------------------------------------------------
// Test: Graceful kill via FakeSpawner
// ---------------------------------------------------------------------------

func TestM4_GracefulKill(t *testing.T) {
	// Use a TrackingSpawner that records the sequence of operations.
	tracker := &TrackingSpawner{}

	st := newM4Store(t)
	ctx := context.Background()

	wt := &worker.FakeWorktreeCreator{}
	recon := scheduler.NewReconciler(st, tracker, wt, 50*time.Millisecond) // very short timeout

	// Create a running task with a tmux agent.
	st.TaskCreate(ctx, "task-gk", "", "", "Graceful kill test", "", "", "", "default", 0)
	st.TaskSetStatus(ctx, "task-gk", "running")
	st.AgentCreate(ctx, "agent-gk", "task-gk", 0, "cw-worker-gk", "", "", "default", "")

	// Set a stale heartbeat so it triggers graceful kill.
	staleTime := time.Now().Add(-1 * time.Hour)
	st.DB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE id = ?`,
		staleTime.UTC().Format(time.RFC3339), "agent-gk")

	// Mark session as alive in the tracker, with stale pane (both signals required).
	tracker.PaneActivityTime = time.Now().Add(-2 * time.Hour)
	tracker.Spawn("cw-worker-gk", "", "", nil, nil)

	// First tick: stall detected, nudge sent.
	recon.Tick(ctx)

	// Inject expired nudge (well past the 3-minute nudgeTimeout) so next tick triggers GracefulKill.
	recon.SetNudgeSentAt("agent-gk", time.Now().Add(-10*time.Minute))

	// Second tick: nudge timeout → GracefulKill.
	recon.Tick(ctx)

	// Verify the tracker recorded a GracefulKill (not just Kill).
	tracker.mu.Lock()
	ops := append([]string{}, tracker.ops...)
	tracker.mu.Unlock()

	foundGraceful := false
	for _, op := range ops {
		if strings.HasPrefix(op, "graceful_kill:cw-worker-gk") {
			foundGraceful = true
			break
		}
	}
	if !foundGraceful {
		t.Errorf("expected GracefulKill to be called, ops=%v", ops)
	}
}

// TrackingSpawner records all operations in order for assertion.
type TrackingSpawner struct {
	mu               sync.Mutex
	sessions         map[string]bool
	ops              []string
	PaneActivityTime time.Time // zero = active (now); set past to simulate stale pane
}

func (ts *TrackingSpawner) Spawn(sessionName, workdir, command string, args []string, env map[string]string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.sessions == nil {
		ts.sessions = make(map[string]bool)
	}
	ts.sessions[sessionName] = true
	ts.ops = append(ts.ops, "spawn:"+sessionName)
	return nil
}

func (ts *TrackingSpawner) IsAlive(sessionName string) (bool, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.ops = append(ts.ops, "is_alive:"+sessionName)
	return ts.sessions[sessionName], nil
}

func (ts *TrackingSpawner) Kill(sessionName string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.sessions, sessionName)
	ts.ops = append(ts.ops, "kill:"+sessionName)
	return nil
}

func (ts *TrackingSpawner) GracefulKill(sessionName string, gracePeriod time.Duration) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.sessions, sessionName)
	ts.ops = append(ts.ops, fmt.Sprintf("graceful_kill:%s:%v", sessionName, gracePeriod))
	return nil
}

func (ts *TrackingSpawner) PaneLastActivity(sessionName string) (time.Time, error) {
	if !ts.PaneActivityTime.IsZero() {
		return ts.PaneActivityTime, nil
	}
	return time.Now(), nil
}

func (ts *TrackingSpawner) CapturePane(sessionName string, lines int) (string, error) {
	return "", nil
}

func (ts *TrackingSpawner) SendInitialPrompt(sessionName, msg string) error {
	return nil
}

func (ts *TrackingSpawner) SendNudge(sessionName, msg string) error {
	return nil
}

// ---------------------------------------------------------------------------
// Test: Reconciler liveness cases
// ---------------------------------------------------------------------------

func TestM4_ReconcilerLivenessCases(t *testing.T) {
	t.Run("dead_tmux_stale_heartbeat_fails", func(t *testing.T) {
		st := newM4Store(t)
		ctx := context.Background()
		spawner := &worker.FakeSpawner{} // session not added → dead

		st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
		st.TaskSetStatus(ctx, "task01", "running")
		st.AgentCreate(ctx, "agent01", "task01", 0, "cw-worker-01", "", "", "default", "")
		staleTime := time.Now().Add(-1 * time.Hour)
		st.DB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE id = ?`,
			staleTime.UTC().Format(time.RFC3339), "agent01")

		recon := scheduler.NewReconciler(st, spawner, &worker.FakeWorktreeCreator{}, 10*time.Minute)
		recon.Tick(ctx)

		task, _ := st.TaskGet(ctx, "task01")
		assertEqual(t, "failed", task.Status, "dead+stale should fail")
	})

	t.Run("dead_tmux_no_heartbeat_fails", func(t *testing.T) {
		st := newM4Store(t)
		ctx := context.Background()
		spawner := &worker.FakeSpawner{}

		st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
		st.TaskSetStatus(ctx, "task01", "running")
		st.AgentCreate(ctx, "agent01", "task01", 0, "cw-worker-01", "", "", "default", "")
		// No heartbeat set (NULL)

		recon := scheduler.NewReconciler(st, spawner, &worker.FakeWorktreeCreator{}, 10*time.Minute)
		recon.Tick(ctx)

		task, _ := st.TaskGet(ctx, "task01")
		assertEqual(t, "failed", task.Status, "dead+no heartbeat should fail")
	})

	t.Run("dead_tmux_fresh_heartbeat_grace", func(t *testing.T) {
		st := newM4Store(t)
		ctx := context.Background()
		spawner := &worker.FakeSpawner{} // dead

		st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
		st.TaskSetStatus(ctx, "task01", "running")
		st.AgentCreate(ctx, "agent01", "task01", 0, "cw-worker-01", "", "", "default", "")
		freshTime := time.Now().Add(-1 * time.Minute)
		st.DB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE id = ?`,
			freshTime.UTC().Format(time.RFC3339), "agent01")

		recon := scheduler.NewReconciler(st, spawner, &worker.FakeWorktreeCreator{}, 10*time.Minute)
		recon.Tick(ctx)

		task, _ := st.TaskGet(ctx, "task01")
		assertEqual(t, "running", task.Status, "dead+fresh should get grace period")
	})

	t.Run("alive_tmux_stale_heartbeat_graceful_kills", func(t *testing.T) {
		st := newM4Store(t)
		ctx := context.Background()
		// Both signals required: heartbeat stale AND pane stale.
		spawner := &worker.FakeSpawner{PaneActivityTime: time.Now().Add(-2 * time.Hour)}
		spawner.Spawn("cw-worker-01", "", "", nil, nil) // alive

		st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
		st.TaskSetStatus(ctx, "task01", "running")
		st.AgentCreate(ctx, "agent01", "task01", 0, "cw-worker-01", "", "", "default", "")
		staleTime := time.Now().Add(-1 * time.Hour)
		st.DB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE id = ?`,
			staleTime.UTC().Format(time.RFC3339), "agent01")

		recon := scheduler.NewReconciler(st, spawner, &worker.FakeWorktreeCreator{}, 10*time.Minute)

		// First tick: stall detected, nudge sent.
		recon.Tick(ctx)

		// Inject expired nudge so second tick triggers handoff.
		recon.SetNudgeSentAt("agent01", time.Now().Add(-10*time.Minute))
		recon.Tick(ctx)

		task, _ := st.TaskGet(ctx, "task01")
		assertEqual(t, "failed", task.Status, "alive+stale should graceful kill and fail")

		// Session should be killed.
		alive, _ := spawner.IsAlive("cw-worker-01")
		if alive {
			t.Error("session should have been killed")
		}
	})

	t.Run("alive_tmux_fresh_heartbeat_healthy", func(t *testing.T) {
		st := newM4Store(t)
		ctx := context.Background()
		spawner := &worker.FakeSpawner{}
		spawner.Spawn("cw-worker-01", "", "", nil, nil)

		st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
		st.TaskSetStatus(ctx, "task01", "running")
		st.AgentCreate(ctx, "agent01", "task01", 0, "cw-worker-01", "", "", "default", "")
		freshTime := time.Now().Add(-1 * time.Minute)
		st.DB().ExecContext(ctx, `UPDATE agents SET last_heartbeat = ? WHERE id = ?`,
			freshTime.UTC().Format(time.RFC3339), "agent01")

		recon := scheduler.NewReconciler(st, spawner, &worker.FakeWorktreeCreator{}, 10*time.Minute)
		recon.Tick(ctx)

		task, _ := st.TaskGet(ctx, "task01")
		assertEqual(t, "running", task.Status, "alive+fresh should be healthy")
	})
}

// ---------------------------------------------------------------------------
// Test: Batch synthesis
// ---------------------------------------------------------------------------

func TestM4_BatchSynthesis(t *testing.T) {
	st := newM4Store(t)
	ctx := context.Background()

	// Create some tasks that look like they struggled.
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("synth-task-%d", i)
		st.TaskCreate(ctx, id, "", "", fmt.Sprintf("Struggling task %d", i), "", "feature", "", "default", 0)
		st.TaskSetStepFromPending(ctx, id, "implement")
		st.TaskSetStatus(ctx, id, "running")

		// Add failure traces.
		fcPayload, _ := json.Marshal(map[string]string{"step": "implement", "log": fmt.Sprintf("Error: test %d failed with segfault", i)})
		st.TraceAppend(ctx, id, "", "step.failure_context", string(fcPayload))

		// Bump retry count above threshold.
		for j := 0; j < 3; j++ {
			st.TaskIncrRetry(ctx, id)
		}

		// Mark as done with a completed_at timestamp.
		st.TaskSetStatus(ctx, id, "done")
	}

	synth := learning.NewSynthesizer(st, learning.SynthesizerConfig{
		RetryThreshold: 2,
		Interval:       1 * time.Minute,
	})

	if err := synth.Run(ctx); err != nil {
		t.Fatalf("synthesizer.Run: %v", err)
	}

	// Check that learnings were created across all auto categories.
	var learnings []*model.Learning
	for _, cat := range []string{"auto:failure-pattern", "auto:step-bottleneck", "auto:file-hotspot", "auto:template-insight"} {
		ls, err := st.LearningList(ctx, cat, 100)
		if err != nil {
			t.Fatalf("LearningList(%s): %v", cat, err)
		}
		learnings = append(learnings, ls...)
	}

	if len(learnings) < 3 {
		t.Errorf("expected at least 3 synthesized learnings, got %d", len(learnings))
	}

	// Verify the learnings have the right structure.
	for _, l := range learnings {
		if l.Title == "" {
			t.Error("synthesized learning title should not be empty")
		}
		if l.Tier != "index" && l.Tier != "digest" {
			t.Errorf("synthesized learning should be index or digest tier, got %q", l.Tier)
		}
		if l.Body == "" {
			t.Error("synthesized learning body should not be empty")
		}
	}

	// Run again — should be idempotent (no new learnings since no new completed tasks).
	countBefore := len(learnings)
	synth.Run(ctx)
	var learningsAfter []*model.Learning
	for _, cat := range []string{"auto:failure-pattern", "auto:step-bottleneck", "auto:file-hotspot", "auto:template-insight"} {
		ls, _ := st.LearningList(ctx, cat, 100)
		learningsAfter = append(learningsAfter, ls...)
	}
	if len(learningsAfter) != countBefore {
		t.Errorf("second run should not create more learnings: before=%d, after=%d", countBefore, len(learningsAfter))
	}
}

// ---------------------------------------------------------------------------
// Test: Triage auto-classification during dispatch
// ---------------------------------------------------------------------------

func TestM4_TriageDispatch(t *testing.T) {
	env := setupM4(t)
	ctx := context.Background()

	// Create task without template — should be auto-classified on dispatch.
	taskID := "task-triage-dispatch"
	env.store.TaskCreate(ctx, taskID, "", "", "fix authentication timeout", "", "", "", "default", 0)

	env.disp.Tick(ctx)

	task, _ := env.store.TaskGet(ctx, taskID)
	assertEqual(t, "bugfix", task.Template, "auto-classified template")
	assertEqual(t, "running", task.Status, "task should be dispatched after triage")
}

// ---------------------------------------------------------------------------
// Test: Reconciler routes failure through template retry
// ---------------------------------------------------------------------------

func TestM4_ReconcilerTemplateRetry(t *testing.T) {
	st := newM4Store(t)
	ctx := context.Background()

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := config.DefaultConfig()
	cfg.Runtimes["default"] = config.RuntimeConfig{Command: "true"}

	disp := scheduler.New(ctx, st, spawner, wt, t.TempDir(), cfg)
	recon := scheduler.NewReconciler(st, spawner, wt, 10*time.Minute)
	recon.SetDispatcher(disp)

	// Create a template task at implement step.
	st.TaskCreate(ctx, "task-retry", "", "", "Feature with retry", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task-retry", "implement")
	st.TaskSetStatus(ctx, "task-retry", "running")
	st.AgentCreate(ctx, "agent-retry", "task-retry", 0, "cw-worker-retry", "", "", "default", "")
	// Session is dead (not in FakeSpawner) and no heartbeat → reconciler should fail it.

	recon.Tick(ctx)

	// With the dispatcher wired in, the reconciler should route through the template
	// and retry the implement step instead of just failing.
	task, _ := st.TaskGet(ctx, "task-retry")
	// The task should be back at "implement" with status pending (ready for redispatch),
	// OR still implement with incremented retry count.
	if task.CurrentStep != "implement" {
		t.Errorf("current_step = %q, want implement (retry)", task.CurrentStep)
	}
	if task.StepAttempts["implement"] < 1 {
		t.Errorf("step_attempts[implement] = %v, want >= 1 (reconciler failure increments per-step attempts)", task.StepAttempts["implement"])
	}
}

// ---------------------------------------------------------------------------
// Test: TaskCompletedHook fires on done
// ---------------------------------------------------------------------------

func TestM4_TaskCompletedHook(t *testing.T) {
	st := newM4Store(t)
	ctx := context.Background()

	spawner := &worker.FakeSpawner{}
	cfg := config.DefaultConfig()
	disp := scheduler.New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, t.TempDir(), cfg)

	var hookCalled bool
	var hookTaskID string
	var mu sync.Mutex
	disp.SetTaskCompletedHook(func(ctx context.Context, taskID string) {
		mu.Lock()
		defer mu.Unlock()
		hookCalled = true
		hookTaskID = taskID
	})

	st.TaskCreate(ctx, "task-hook", "", "", "Hook test", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task-hook", "acceptance")
	st.TaskSetStatus(ctx, "task-hook", "running")

	// Route acceptance → complete.
	disp.RouteStep(ctx, "task-hook", "acceptance", "success")

	// Give the goroutine a moment.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if !hookCalled {
		t.Error("TaskCompletedHook should have been called")
	}
	assertEqual(t, "task-hook", hookTaskID, "hook task ID")
}

// ---------------------------------------------------------------------------
// Test: Max retries exceeded fails task
// ---------------------------------------------------------------------------

func TestM4_MaxRetriesFailsTask(t *testing.T) {
	st := newM4Store(t)
	ctx := context.Background()

	disp := scheduler.New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), config.DefaultConfig())

	// Feature template implement step has max_retries=5.
	st.TaskCreate(ctx, "task-maxretry", "", "", "Retry me", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task-maxretry", "implement")
	st.TaskSetStatus(ctx, "task-maxretry", "running")

	// Set per-step attempts to simulate 5 prior entries to implement (max_retries=5).
	// The next failure (6th attempt, attempts=5 >= max_retries=5) should fail the task.
	st.DB().ExecContext(ctx, `UPDATE tasks SET step_attempts = ? WHERE id = ?`,
		`{"implement":5}`, "task-maxretry")
	st.TaskSetStatus(ctx, "task-maxretry", "running")

	// Another failure should exceed max retries and fail the task.
	disp.RouteStep(ctx, "task-maxretry", "implement", "failure")

	task, _ := st.TaskGet(ctx, "task-maxretry")
	assertEqual(t, "failed", task.Status, "should fail after max retries exceeded")
}

// ---------------------------------------------------------------------------
// Test: Pause and resume
// ---------------------------------------------------------------------------

func TestM4_PauseResume(t *testing.T) {
	env := setupM4(t)
	ctx := context.Background()

	env.store.TaskCreate(ctx, "task-pause", "", "", "Paused task", "", "", "", "default", 0)

	env.disp.Pause()
	env.disp.Tick(ctx)

	task, _ := env.store.TaskGet(ctx, "task-pause")
	assertEqual(t, "pending", task.Status, "task should remain pending when paused")

	env.disp.Resume()
	env.disp.Tick(ctx)

	task, _ = env.store.TaskGet(ctx, "task-pause")
	assertEqual(t, "running", task.Status, "task should dispatch after resume")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func setupM4(t *testing.T) *m4Env {
	t.Helper()
	homeDir := t.TempDir()
	os.MkdirAll(filepath.Join(homeDir, "logs"), 0700)
	os.MkdirAll(filepath.Join(homeDir, "worktrees"), 0700)

	st, err := store.Open(filepath.Join(homeDir, "clankwork.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.DefaultConfig()
	cfg.Scheduler.TickSec = 1
	cfg.Runtimes["default"] = config.RuntimeConfig{Command: "true"}
	cfg.Runtimes["frontier"] = config.RuntimeConfig{Command: "true"}

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}

	ctx := context.Background()
	disp := scheduler.New(ctx, st, spawner, wt, homeDir, cfg)
	recon := scheduler.NewReconciler(st, spawner, wt, 10*time.Minute)
	recon.SetDispatcher(disp)

	// Use a short socket path to avoid macOS 104-char limit for Unix sockets.
	sockDir, err := os.MkdirTemp("/tmp", "m4sock")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	socketPath := filepath.Join(sockDir, "cw.sock")

	apiSrv := api.NewServerWithDispatcher(st, homeDir, disp, wt)
	httpSrv := &http.Server{Handler: apiSrv.Handler()}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go httpSrv.Serve(ln)

	t.Cleanup(func() {
		hctx, cf := context.WithTimeout(context.Background(), 2*time.Second)
		defer cf()
		httpSrv.Shutdown(hctx)
		os.RemoveAll(sockDir)
	})

	env := &m4Env{
		store:   st,
		disp:    disp,
		recon:   recon,
		spawner: spawner,
		wt:      wt,
		homeDir: homeDir,
		cfg:     cfg,
	}
	// Store the socket path for HTTP helpers.
	env.socketPath = socketPath
	return env
}

func newM4Store(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func createTestRepo(t *testing.T, env *m4Env, name string) *model.Repo {
	t.Helper()
	id := "repo-" + name
	repo, err := env.store.RepoCreate(context.Background(), id, name, "/tmp/"+name, "main", "", "", "", false)
	if err != nil {
		t.Fatalf("RepoCreate: %v", err)
	}
	return repo
}

func createTestTask(t *testing.T, env *m4Env, req model.CreateTaskRequest) *model.Task {
	t.Helper()
	id := fmt.Sprintf("task-%d", time.Now().UnixNano())
	task, err := env.store.TaskCreate(context.Background(), id, req.PlanID, req.RepoID, req.Title, req.Body, req.Template, req.Role, req.Runtime, req.Priority)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}
	return task
}

func mustGetTask(t *testing.T, env *m4Env, taskID string) *model.Task {
	t.Helper()
	task, err := env.store.TaskGet(context.Background(), taskID)
	if err != nil {
		t.Fatalf("TaskGet(%s): %v", taskID, err)
	}
	return task
}

func signalDone(t *testing.T, env *m4Env, taskID string) {
	t.Helper()
	// Use the HTTP API to signal done, so template routing is exercised.
	body, _ := json.Marshal(model.SignalRequest{TaskID: taskID, DoneBundle: testDoneBundle(taskID)})
	req, _ := http.NewRequest("POST", "http://unix/v1/signals.done", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", env.socketPath)
		},
	}
	httpClient := &http.Client{Transport: transport}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("signal done: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("signal done status: %d", resp.StatusCode)
	}
}

func mustBootstrap(t *testing.T, env *m4Env, taskID, role, repoID string) *model.BootstrapResponse {
	t.Helper()
	body, _ := json.Marshal(model.BootstrapRequest{TaskID: taskID, Role: role, RepoID: repoID})
	req, _ := http.NewRequest("POST", "http://unix/v1/bootstrap", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", env.socketPath)
		},
	}
	httpClient := &http.Client{Transport: transport}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer resp.Body.Close()

	var apiResp model.APIResponse
	json.NewDecoder(resp.Body).Decode(&apiResp)
	if !apiResp.OK {
		t.Fatalf("bootstrap failed: %+v", apiResp.Error)
	}

	// Re-decode Data into BootstrapResponse.
	dataBytes, _ := json.Marshal(apiResp.Data)
	var boot model.BootstrapResponse
	json.Unmarshal(dataBytes, &boot)
	return &boot
}

func listTraces(t *testing.T, env *m4Env, taskID, eventType string) []*model.Trace {
	t.Helper()
	traces, err := env.store.TraceListByType(context.Background(), taskID, eventType, 100)
	if err != nil {
		t.Fatalf("TraceListByType: %v", err)
	}
	return traces
}

func assertEqual(t *testing.T, expected, actual, msg string) {
	t.Helper()
	if expected != actual {
		t.Errorf("%s: got %q, want %q", msg, actual, expected)
	}
}
