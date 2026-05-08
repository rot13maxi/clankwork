package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
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

func testDispatcherConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Runtimes["default"] = config.RuntimeConfig{Command: "true", Transport: config.TransportTmux}
	return cfg
}

func newTestDispatcher(t *testing.T, st *store.Store) *scheduler.Dispatcher {
	t.Helper()
	ctx := context.Background()
	cfg := testDispatcherConfig()
	return scheduler.New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), cfg)
}

// createTestAgentWithTask creates a running task and an associated ACP agent
// with the given PID. Returns the agent.
func createTestAgentWithTask(t *testing.T, st *store.Store, taskID, template, currentStep string, pid int) *model.Agent {
	t.Helper()
	ctx := context.Background()

	_, err := st.TaskCreate(ctx, taskID, "", "", "Test task", "", template, "", "default", 0)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}
	if currentStep != "" {
		if err := st.TaskSetStepFromPending(ctx, taskID, currentStep); err != nil {
			t.Fatalf("TaskSetStepFromPending: %v", err)
		}
	}
	if err := st.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatalf("TaskSetStatus: %v", err)
	}

	agent, err := st.AgentCreateWithRuntime(ctx, "agent01", taskID, 1, "", worker.TransportACP, "session01", pid, "", "", "acp", "claude")
	if err != nil {
		t.Fatalf("AgentCreateWithRuntime: %v", err)
	}

	return agent
}

// createTestAgentWithTaskUnique is like createTestAgentWithTask but accepts
// a custom agent ID to avoid UNIQUE constraint failures when creating multiple agents.
func createTestAgentWithTaskUnique(t *testing.T, st *store.Store, taskID, agentID, template, currentStep string, pid int) *model.Agent {
	t.Helper()
	ctx := context.Background()

	_, err := st.TaskCreate(ctx, taskID, "", "", "Test task", "", template, "", "default", 0)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}
	if currentStep != "" {
		if err := st.TaskSetStepFromPending(ctx, taskID, currentStep); err != nil {
			t.Fatalf("TaskSetStepFromPending: %v", err)
		}
	}
	if err := st.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatalf("TaskSetStatus: %v", err)
	}

	agent, err := st.AgentCreateWithRuntime(ctx, agentID, taskID, 1, "", worker.TransportACP, "session01", pid, "", "", "acp", "claude")
	if err != nil {
		t.Fatalf("AgentCreateWithRuntime: %v", err)
	}

	return agent
}

func TestKillOrphanedACPAdapters_NoAgents(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	// Should return immediately with no error when no agents exist.
	killOrphanedACPAdapters(ctx, st, disp)

	// No tasks should have been affected.
	tasks, err := st.TaskList(ctx, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

// C1-P1: Daemon startup orphan cleanup does not mark a templated running task
// as terminal 'failed' when an ACP adapter's PID is no longer an acp-adapter process.
func TestC1_PIDCollision_TemplatedTask_NotFailed(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	// Create a templated task with a current step and an ACP agent with a PID
	// that is NOT actually an acp-adapter process (PID collision scenario).
	agent := createTestAgentWithTask(t, st, "task-templated", "feature", "implement", 999999)

	killOrphanedACPAdapters(ctx, st, disp)

	// Task should NOT be terminal failed.
	task, err := st.TaskGet(ctx, "task-templated")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.Status == "failed" {
		t.Errorf("templated task was marked 'failed' after PID collision; expected route to retry, got status=%q", task.Status)
	}

	// Agent should be killed.
	ag, err := st.AgentGet(ctx, agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if ag.Status != "killed" {
		t.Errorf("agent status = %q, want 'killed'", ag.Status)
	}
	if ag.PID != 0 {
		t.Errorf("agent PID = %d, want 0", ag.PID)
	}
}

// C1-P2: Task remains in a non-terminal state (pending) so it can be re-dispatched.
func TestC1_PIDCollision_TaskPendingForRedispatch(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	_ = createTestAgentWithTask(t, st, "task-redispatch", "feature", "implement", 999998)

	killOrphanedACPAdapters(ctx, st, disp)

	task, err := st.TaskGet(ctx, "task-redispatch")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	// RouteStep with "failure" on the feature template routes "implement" back to "implement"
	// (retry) and sets status to "pending".
	if task.Status != "pending" {
		t.Errorf("task status = %q, want 'pending' (routed for retry)", task.Status)
	}
	if task.CurrentStep != "implement" {
		t.Errorf("current_step = %q, want 'implement'", task.CurrentStep)
	}
}

// C1-P3: Step attempts are incremented when routing through failure path.
func TestC1_PIDCollision_StepAttemptsIncremented(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	_ = createTestAgentWithTask(t, st, "task-attempts", "feature", "implement", 999997)

	killOrphanedACPAdapters(ctx, st, disp)

	task, err := st.TaskGet(ctx, "task-attempts")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.StepAttempts == nil || task.StepAttempts["implement"] < 1 {
		t.Errorf("step_attempts[implement] = %v, want >= 1 (route increments destination step)", task.StepAttempts)
	}
}

// C3-P1: Non-template tasks still get marked 'failed' on orphan cleanup.
func TestC3_NonTemplatedTask_FailsDirectly(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	// No template, no current_step.
	agent := createTestAgentWithTask(t, st, "task-notemplate", "", "", 999996)

	killOrphanedACPAdapters(ctx, st, disp)

	task, err := st.TaskGet(ctx, "task-notemplate")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.Status != "failed" {
		t.Errorf("non-templated task status = %q, want 'failed'", task.Status)
	}

	ag, err := st.AgentGet(ctx, agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if ag.Status != "killed" {
		t.Errorf("agent status = %q, want 'killed'", ag.Status)
	}
}

// Template set but no current_step: should still fail directly.
func Test_TemplatedNoCurrentStep_FailsDirectly(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	_ = createTestAgentWithTask(t, st, "task-nostep", "feature", "", 999995)

	killOrphanedACPAdapters(ctx, st, disp)

	task, err := st.TaskGet(ctx, "task-nostep")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.Status != "failed" {
		t.Errorf("task status = %q, want 'failed' (template but no current_step)", task.Status)
	}
}

// No dispatcher: templated task falls back to direct fail.
func Test_NoDispatcher_FallbackToFail(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	agent := createTestAgentWithTask(t, st, "task-nodep", "feature", "implement", 999994)

	killOrphanedACPAdapters(ctx, st, nil)

	task, err := st.TaskGet(ctx, "task-nodep")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.Status != "failed" {
		t.Errorf("task status = %q, want 'failed' (no dispatcher fallback)", task.Status)
	}

	ag, err := st.AgentGet(ctx, agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if ag.Status != "killed" {
		t.Errorf("agent status = %q, want 'killed'", ag.Status)
	}
}

// C2-P1: Template-aware routing uses RouteStep with "failure" outcome.
func TestC2_RouteStepFailureOutcome(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	agent := createTestAgentWithTask(t, st, "task-route", "feature", "implement", 999993)

	routeOrphanedAgent(ctx, st, disp, agent, "test orphan recovery")

	// feature template: implement on_failure = "implement" (retry same step)
	// After route, status should be "pending" and current_step still "implement".
	task, err := st.TaskGet(ctx, "task-route")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.Status == "failed" {
		t.Errorf("templated task status = %q, want routed to retry (pending)", task.Status)
	}
}

// C2-P2: step.failure_context trace is created before RouteStep.
func TestC2_FailureContextTrace(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	agent := createTestAgentWithTask(t, st, "task-fc", "feature", "implement", 999992)

	routeOrphanedAgent(ctx, st, disp, agent, "test failure context")

	traces, err := st.TraceListByType(ctx, "task-fc", "step.failure_context", 10)
	if err != nil {
		t.Fatalf("TraceListByType: %v", err)
	}
	if len(traces) == 0 {
		t.Error("expected step.failure_context trace, found none")
	} else {
		var fc map[string]string
		if err := json.Unmarshal([]byte(traces[0].Payload), &fc); err == nil {
			if fc["step"] != "implement" {
				t.Errorf("failure_context step = %q, want 'implement'", fc["step"])
			}
			if fc["message"] != "test failure context" {
				t.Errorf("failure_context message = %q, want 'test failure context'", fc["message"])
			}
		}
	}
}

// C4-P1: Controller decision trace records the template-aware routing decision.
func TestC4_ReconcilerDecision_TemplateRouted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	agent := createTestAgentWithTask(t, st, "task-decision", "feature", "implement", 999991)

	routeOrphanedAgent(ctx, st, disp, agent, "test decision trace")

	// Query the decision table directly.
	row := st.DB().QueryRowContext(ctx,
		`SELECT action, decision_kind, payload FROM controller_decisions
		 WHERE task_id = ? ORDER BY decided_at DESC LIMIT 1`, "task-decision")
	var action, kind, payload string
	if err := row.Scan(&action, &kind, &payload); err != nil {
		t.Fatalf("scan decision: %v", err)
	}
	if action != "kill_and_route_step_failure" {
		t.Errorf("decision action = %q, want 'kill_and_route_step_failure'", action)
	}
	if kind != "orphaned_runtime" {
		t.Errorf("decision kind = %q, want 'orphaned_runtime'", kind)
	}
	var pl map[string]any
	if err := json.Unmarshal([]byte(payload), &pl); err == nil {
		if routed, ok := pl["template_routed"]; !ok || routed != true {
			t.Errorf("decision payload template_routed = %v, want true", routed)
		}
		if reason, ok := pl["reason"]; !ok || reason != "daemon_restart_orphan_recovery" {
			t.Errorf("decision payload reason = %v, want 'daemon_restart_orphan_recovery'", reason)
		}
	}
}

// C4-P2: Controller actuation trace records the template-aware state transition.
func TestC4_ControllerActuation_TemplateRouted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	agent := createTestAgentWithTask(t, st, "task-actuation", "feature", "implement", 999990)

	routeOrphanedAgent(ctx, st, disp, agent, "test actuation trace")

	row := st.DB().QueryRowContext(ctx,
		`SELECT requested_operation, new_state, payload FROM controller_actuations
		 WHERE task_id = ? ORDER BY created_at DESC LIMIT 1`, "task-actuation")
	var operation, newState, payload string
	if err := row.Scan(&operation, &newState, &payload); err != nil {
		t.Fatalf("scan actuation: %v", err)
	}
	if operation != "daemon.startup_orphan_cleanup" {
		t.Errorf("actuation operation = %q, want 'daemon.startup_orphan_cleanup'", operation)
	}
	if newState != "killed/routed" {
		t.Errorf("actuation new_state = %q, want 'killed/routed'", newState)
	}
	var pl map[string]any
	if err := json.Unmarshal([]byte(payload), &pl); err == nil {
		if routed, ok := pl["template_routed"]; !ok || routed != true {
			t.Errorf("actuation payload template_routed = %v, want true", routed)
		}
	}
}

// Non-template routed: decision action is kill_and_fail_task.
func TestC4_ControllerActuation_NotTemplateRouted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	agent := createTestAgentWithTask(t, st, "task-act-nonroute", "", "", 999989)

	routeOrphanedAgent(ctx, st, disp, agent, "test non-route actuation")

	row := st.DB().QueryRowContext(ctx,
		`SELECT action FROM controller_decisions
		 WHERE task_id = ? ORDER BY decided_at DESC LIMIT 1`, "task-act-nonroute")
	var action string
	if err := row.Scan(&action); err != nil {
		t.Fatalf("scan decision: %v", err)
	}
	if action != "kill_and_fail_task" {
		t.Errorf("decision action = %q, want 'kill_and_fail_task'", action)
	}

	row2 := st.DB().QueryRowContext(ctx,
		`SELECT new_state FROM controller_actuations
		 WHERE task_id = ? ORDER BY created_at DESC LIMIT 1`, "task-act-nonroute")
	var newState string
	if err := row2.Scan(&newState); err != nil {
		t.Fatalf("scan actuation: %v", err)
	}
	if newState != "killed/failed" {
		t.Errorf("actuation new_state = %q, want 'killed/failed'", newState)
	}
}

// Multiple agents: mixed templated and non-templated.
func Test_MultipleAgents_MixedRouting(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	createTestAgentWithTaskUnique(t, st, "task-multi-1", "agent-multi-1", "feature", "implement", 999988)
	createTestAgentWithTaskUnique(t, st, "task-multi-2", "agent-multi-2", "", "", 999987)

	killOrphanedACPAdapters(ctx, st, disp)

	task1, err := st.TaskGet(ctx, "task-multi-1")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task1.Status == "failed" {
		t.Errorf("templated task-multi-1 status = %q, want routed (not failed)", task1.Status)
	}

	task2, err := st.TaskGet(ctx, "task-multi-2")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task2.Status != "failed" {
		t.Errorf("non-templated task-multi-2 status = %q, want 'failed'", task2.Status)
	}
}

// Trace payload contains template routing info.
func Test_TracePayload_TemplateRoutingInfo(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	agent := createTestAgentWithTask(t, st, "task-trace", "feature", "implement", 999986)

	routeOrphanedAgent(ctx, st, disp, agent, "test trace payload")

	traces, err := st.TraceListByType(ctx, "task-trace", "signal.orphaned_kill", 10)
	if err != nil {
		t.Fatalf("TraceListByType: %v", err)
	}
	if len(traces) == 0 {
		t.Fatal("expected signal.orphaned_kill trace, found none")
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(traces[0].Payload), &payload); err != nil {
		t.Fatalf("unmarshal trace payload: %v", err)
	}
	if payload["template_routed"] != "true" {
		t.Errorf("trace template_routed = %q, want 'true'", payload["template_routed"])
	}
	if payload["current_step"] != "implement" {
		t.Errorf("trace current_step = %q, want 'implement'", payload["current_step"])
	}
	if payload["reason"] != "test trace payload" {
		t.Errorf("trace reason = %q, want 'test trace payload'", payload["reason"])
	}
}

// Control observation records template_routed.
func Test_ControlObservation_TemplateRouted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	agent := createTestAgentWithTask(t, st, "task-obs", "feature", "implement", 999985)

	routeOrphanedAgent(ctx, st, disp, agent, "test observation")

	row := st.DB().QueryRowContext(ctx,
		`SELECT status, reason, payload FROM control_observations
		 WHERE agent_id = ? ORDER BY observed_at DESC LIMIT 1`, "agent01")
	var status, reason, payload string
	if err := row.Scan(&status, &reason, &payload); err != nil {
		t.Fatalf("scan observation: %v", err)
	}
	if status != "orphaned" {
		t.Errorf("observation status = %q, want 'orphaned'", status)
	}
	if reason != "test observation" {
		t.Errorf("observation reason = %q, want 'test observation'", reason)
	}

	var pl map[string]any
	if err := json.Unmarshal([]byte(payload), &pl); err == nil {
		if routed, ok := pl["template_routed"]; !ok || routed != true {
			t.Errorf("observation payload template_routed = %v, want true", routed)
		}
	}
}

// Max retries exceeded: even templated tasks get marked failed after exhausting retries.
func Test_MaxRetriesExceeded_TemplatedTask_Fails(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	disp := newTestDispatcher(t, st)

	taskID := "task-maxretry"
	_, err := st.TaskCreate(ctx, taskID, "", "", "Test", "", "feature", "", "default", 0)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}
	if err := st.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatalf("TaskSetStepFromPending: %v", err)
	}

	// Exhaust retries by running RouteStep multiple times.
	// feature template: implement on_failure="implement" with max_retries=5.
	// Each failure route increments step_attempts[implement].
	// After 5 attempts, the 6th failure should hit max retries and fail the task.
	for i := 0; i < 5; i++ {
		if err := st.TaskSetStatus(ctx, taskID, "running"); err != nil {
			t.Fatalf("TaskSetStatus iteration %d: %v", i, err)
		}
		err := disp.RouteStep(ctx, taskID, "implement", "failure")
		if err != nil {
			t.Fatalf("RouteStep iteration %d: %v", i, err)
		}
	}

	// Task should now be in "pending" with step_attempts[implement] = 3.
	task, err := st.TaskGet(ctx, taskID)
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.StepAttempts["implement"] != 5 {
		t.Fatalf("step_attempts[implement] = %v, want 5", task.StepAttempts["implement"])
	}

	// Now mark running and simulate one more orphan failure — this should exceed max retries.
	if err := st.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatalf("TaskSetStatus final: %v", err)
	}
	agent, err := st.AgentCreateWithRuntime(ctx, "agent-maxretry", taskID, 1, "", worker.TransportACP, "session-maxretry", 999984, "", "", "acp", "claude")
	if err != nil {
		t.Fatalf("AgentCreateWithRuntime: %v", err)
	}

	routeOrphanedAgent(ctx, st, disp, agent, "orphaned on daemon restart")

	task, err = st.TaskGet(ctx, taskID)
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.Status != "failed" {
		t.Errorf("task status = %q, step_attempts = %v, want 'failed' (max retries exceeded)", task.Status, task.StepAttempts)
	}
}
