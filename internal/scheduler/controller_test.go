package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

// TestEvaluateAgentHealth_Healthy verifies that a healthy agent produces a
// healthy decision with no error.
func TestEvaluateAgentHealth_Healthy(t *testing.T) {
	t.Parallel()
	state := model.AgentObservedState{
		AgentID:        "agent-1",
		TaskID:         "task-1",
		SessionAlive:   true,
		HeartbeatStale: false,
		PaneActive:     true,
	}
	decision := model.EvaluateAgentHealth(state, 10*time.Minute)

	if decision.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", decision.AgentID)
	}
	if decision.Health != model.AgentHealthHealthy {
		t.Errorf("Health = %q, want %q", decision.Health, model.AgentHealthHealthy)
	}
	if decision.Action != model.ControllerActionNone {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionNone)
	}
	if decision.Error != nil {
		t.Errorf("Error = %v, want nil for healthy agent", decision.Error)
	}
}

// TestEvaluateAgentHealth_Dead verifies that a dead agent session produces a
// dead health decision with a kill action.
func TestEvaluateAgentHealth_Dead(t *testing.T) {
	t.Parallel()
	state := model.AgentObservedState{
		AgentID:        "agent-dead",
		TaskID:         "task-1",
		SessionAlive:   false,
		HeartbeatStale: true,
		PaneActive:     false,
	}
	decision := model.EvaluateAgentHealth(state, 10*time.Minute)

	if decision.Health != model.AgentHealthDead {
		t.Errorf("Health = %q, want %q", decision.Health, model.AgentHealthDead)
	}
	if decision.Action != model.ControllerActionKill {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionKill)
	}
	if decision.Error == nil {
		t.Fatal("Error = nil, want non-nil for dead agent")
	}
	if decision.Error.Category != model.ControllerErrorAgentDead {
		t.Errorf("Error.Category = %q, want %q", decision.Error.Category, model.ControllerErrorAgentDead)
	}
	if decision.Error.AffectedAgentID != "agent-dead" {
		t.Errorf("Error.AffectedAgentID = %q, want agent-dead", decision.Error.AffectedAgentID)
	}
}

// TestEvaluateAgentHealth_Stalled verifies that both heartbeat and pane being
// stale produces a stalled health decision with a nudge action.
func TestEvaluateAgentHealth_Stalled(t *testing.T) {
	t.Parallel()
	state := model.AgentObservedState{
		AgentID:          "agent-stalled",
		TaskID:           "task-1",
		SessionAlive:     true,
		HeartbeatStale:   true,
		PaneActive:       false, // both stale = stall
		LastHeartbeatAge: 15 * time.Minute,
	}
	decision := model.EvaluateAgentHealth(state, 10*time.Minute)

	if decision.Health != model.AgentHealthStalled {
		t.Errorf("Health = %q, want %q", decision.Health, model.AgentHealthStalled)
	}
	if decision.Action != model.ControllerActionNudge {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionNudge)
	}
	if decision.Error == nil {
		t.Fatal("Error = nil, want non-nil for stalled agent")
	}
	if decision.Error.Category != model.ControllerErrorAgentStalling {
		t.Errorf("Error.Category = %q, want %q", decision.Error.Category, model.ControllerErrorAgentStalling)
	}
}

// TestEvaluateAgentHealth_StaleWithPaneActive verifies that a stale heartbeat
// with active pane produces a warning, not a stall (reconciler should not act).
func TestEvaluateAgentHealth_StaleWithPaneActive(t *testing.T) {
	t.Parallel()
	state := model.AgentObservedState{
		AgentID:        "agent-working",
		TaskID:         "task-1",
		SessionAlive:   true,
		HeartbeatStale: true,
		PaneActive:     true, // pane active = working, not stalled
	}
	decision := model.EvaluateAgentHealth(state, 10*time.Minute)

	if decision.Health != model.AgentHealthWarning {
		t.Errorf("Health = %q, want %q", decision.Health, model.AgentHealthWarning)
	}
	if decision.Action != model.ControllerActionNoOp {
		t.Errorf("Action = %q, want %q (no-op when pane is active)", decision.Action, model.ControllerActionNoOp)
	}
	if decision.Error == nil {
		t.Fatal("Error = nil, want non-nil for stale heartbeat with active pane")
	}
	if decision.Error.Category != model.ControllerErrorAgentStale {
		t.Errorf("Error.Category = %q, want %q", decision.Error.Category, model.ControllerErrorAgentStale)
	}
}

// TestComputeState_DispatchAvailable verifies that the computed state correctly
// reflects when dispatch is available.
func TestComputeState_DispatchAvailable(t *testing.T) {
	t.Parallel()
	desired := model.DesiredState{
		MaxSlots:               3,
		QueuePressureThreshold: 10,
		HeartbeatTimeout:       10 * time.Minute,
	}
	observed := model.ObservedState{
		RunningAgentCount: 2,
		ReadyTaskCount:    1,
		MergeQueueDepth:   0,
	}
	cs := model.ComputeState(desired, observed)

	if cs.SlotsDelta != -1 {
		t.Errorf("SlotsDelta = %d, want -1 (2 running - 3 max)", cs.SlotsDelta)
	}
	if cs.Observed.AvailableSlots != 1 {
		t.Errorf("Observed.AvailableSlots = %d, want 1", cs.Observed.AvailableSlots)
	}
	if cs.QueuePressured {
		t.Error("QueuePressured = true, want false")
	}
	if !cs.CanDispatch {
		t.Error("CanDispatch = false, want true (available slots > 0, tasks ready, not pressured)")
	}
}

// TestComputeState_QueuePressured verifies that queue pressure makes dispatch unavailable.
func TestComputeState_QueuePressured(t *testing.T) {
	t.Parallel()
	desired := model.DesiredState{
		MaxSlots:               3,
		QueuePressureThreshold: 5,
		HeartbeatTimeout:       10 * time.Minute,
	}
	observed := model.ObservedState{
		RunningAgentCount: 1,
		ReadyTaskCount:    2,
		MergeQueueDepth:   5, // exactly at threshold
	}
	cs := model.ComputeState(desired, observed)

	if !cs.QueuePressured {
		t.Error("QueuePressured = false, want true (depth = threshold)")
	}
	if cs.CanDispatch {
		t.Error("CanDispatch = true, want false (queue pressured)")
	}
}

// TestComputeError_Underutilized verifies that underutilization produces the
// correct controller error with the right magnitude.
func TestComputeError_Underutilized(t *testing.T) {
	t.Parallel()
	cs := model.ComputedState{
		Desired: model.DesiredState{
			MaxSlots: 3,
		},
		Observed: model.ObservedState{
			RunningAgentCount: 1,
			ReadyTaskCount:    2,
			MergeQueueDepth:   0,
			AvailableSlots:    2,
		},
		SlotsDelta:     -2,
		QueuePressured: false,
		CanDispatch:    true,
	}
	err := model.ComputeError(cs)

	if err == nil {
		t.Fatal("ComputeError returned nil, want non-nil error for underutilization")
	}
	if err.Category != model.ControllerErrorSlotsUnderutilized {
		t.Errorf("Category = %q, want %q", err.Category, model.ControllerErrorSlotsUnderutilized)
	}
	if err.Magnitude != 2 {
		t.Errorf("Magnitude = %d, want 2 (available slots)", err.Magnitude)
	}
}

// TestComputeError_Overcommitted verifies that overcommitment produces the
// correct controller error.
func TestComputeError_Overcommitted(t *testing.T) {
	t.Parallel()
	cs := model.ComputedState{
		Desired: model.DesiredState{
			MaxSlots: 2,
		},
		Observed: model.ObservedState{
			RunningAgentCount: 4,
			MergeQueueDepth:   0,
			AvailableSlots:    -2,
		},
		SlotsDelta:     2,
		QueuePressured: false,
		CanDispatch:    false,
	}
	err := model.ComputeError(cs)

	if err == nil {
		t.Fatal("ComputeError returned nil, want non-nil error for overcommitment")
	}
	if err.Category != model.ControllerErrorSlotsOvercommitted {
		t.Errorf("Category = %q, want %q", err.Category, model.ControllerErrorSlotsOvercommitted)
	}
	if err.Magnitude != 2 {
		t.Errorf("Magnitude = %d, want 2 (overcommitted by 2 slots)", err.Magnitude)
	}
}

// TestComputeError_NoError verifies that a well-balanced system produces no error.
func TestComputeError_NoError(t *testing.T) {
	t.Parallel()
	cs := model.ComputedState{
		Desired: model.DesiredState{
			MaxSlots: 3,
		},
		Observed: model.ObservedState{
			RunningAgentCount: 3,
			ReadyTaskCount:    0,
			MergeQueueDepth:   0,
			AvailableSlots:    0,
		},
		SlotsDelta:     0,
		QueuePressured: false,
		CanDispatch:    false,
	}
	err := model.ComputeError(cs)

	if err != nil {
		t.Errorf("ComputeError returned %v, want nil for balanced system", err)
	}
}

// TestDecisionFromError_Dispatch verifies that underutilization error produces
// a dispatch action with the correct task count.
func TestDecisionFromError_Dispatch(t *testing.T) {
	t.Parallel()
	err := &model.ControllerError{
		Category:  model.ControllerErrorSlotsUnderutilized,
		Message:   "2 slots available",
		Magnitude: 2,
	}
	decision := model.DecisionFromError(err, 5)

	if decision.Action != model.ControllerActionDispatch {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionDispatch)
	}
	if decision.TasksToDispatch != 2 {
		t.Errorf("TasksToDispatch = %d, want 2 (capped by error magnitude)", decision.TasksToDispatch)
	}
	if decision.Error != err {
		t.Error("Decision.Error != err")
	}
}

// TestDecisionFromError_NoOp verifies that overcommitted error produces no-op.
func TestDecisionFromError_NoOp(t *testing.T) {
	t.Parallel()
	err := &model.ControllerError{
		Category:  model.ControllerErrorSlotsOvercommitted,
		Message:   "overcommitted",
		Magnitude: 1,
	}
	decision := model.DecisionFromError(err, 5)

	if decision.Action != model.ControllerActionNoOp {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionNoOp)
	}
}

// TestDecisionFromError_NilError verifies that nil error produces no-op.
func TestDecisionFromError_NilError(t *testing.T) {
	t.Parallel()
	decision := model.DecisionFromError(nil, 5)

	if decision.Action != model.ControllerActionNoOp {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionNoOp)
	}
	if decision.Reason != "system in desired state" {
		t.Errorf("Reason = %q, want 'system in desired state'", decision.Reason)
	}
}

// TestDecisionFromError_QueuePressured verifies that queue pressure produces pause.
func TestDecisionFromError_QueuePressured(t *testing.T) {
	t.Parallel()
	err := &model.ControllerError{
		Category:  model.ControllerErrorQueuePressured,
		Message:   "queue depth 10 exceeds threshold 5",
		Magnitude: 10,
	}
	decision := model.DecisionFromError(err, 5)

	if decision.Action != model.ControllerActionPause {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionPause)
	}
}

func TestEvaluateAgentHealth_NoProgressNudges(t *testing.T) {
	t.Parallel()
	state := model.AgentObservedState{
		AgentID:        "agent-active-no-progress",
		TaskID:         "task-1",
		SessionAlive:   true,
		HeartbeatStale: false,
		PaneActive:     true,
		Progress:       model.ProgressAbsent,
	}
	decision := model.EvaluateAgentHealth(state, 10*time.Minute)
	if decision.Action != model.ControllerActionNudge {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionNudge)
	}
	if decision.Progress != model.ProgressAbsent {
		t.Errorf("Progress = %q, want %q", decision.Progress, model.ProgressAbsent)
	}
	if decision.Error == nil || decision.Error.Category != model.ControllerErrorAgentNoProgress {
		t.Fatalf("Error = %#v, want no-progress error", decision.Error)
	}
}

func TestEvaluateAgentHealth_OscillationBlocks(t *testing.T) {
	t.Parallel()
	state := model.AgentObservedState{
		AgentID:          "agent-looping",
		TaskID:           "task-1",
		SessionAlive:     true,
		HeartbeatStale:   false,
		PaneActive:       true,
		Progress:         model.ProgressAbsent,
		RepeatedFailures: model.DefaultOscillationThreshold,
		FailureSignature: model.NewFailureSignature("deterministic_test", "test", "go test ./...", "exit_1", "FAIL foo_test.go:42", nil),
	}
	decision := model.EvaluateAgentHealth(state, 10*time.Minute)
	if decision.Action != model.ControllerActionBlock {
		t.Errorf("Action = %q, want %q", decision.Action, model.ControllerActionBlock)
	}
	if decision.Error == nil || decision.Error.Category != model.ControllerErrorOscillation {
		t.Fatalf("Error = %#v, want oscillation error", decision.Error)
	}
	if decision.OscillationScore != model.DefaultOscillationThreshold {
		t.Errorf("OscillationScore = %d, want %d", decision.OscillationScore, model.DefaultOscillationThreshold)
	}
	if decision.FailureSignature == nil {
		t.Fatal("FailureSignature = nil, want signature")
	}
}

func TestNewFailureSignature_NormalizesVolatileFields(t *testing.T) {
	t.Parallel()
	a := model.NewFailureSignature("deterministic_test", "test", "go test ./...", "exit_1",
		"2026-05-05T10:00:00Z /tmp/run-a/foo_test.go:42 failed after 12.3s at 0xabc",
		map[string]string{"test": "TestFoo"})
	b := model.NewFailureSignature("deterministic_test", "test", "go test ./...", "exit_1",
		"2026-05-05T11:15:22Z /tmp/run-b/foo_test.go:77 failed after 95ms at 0xdef",
		map[string]string{"test": "TestFoo"})
	if a == nil || b == nil {
		t.Fatal("signature = nil, want signatures")
	}
	if a.NormalizedHash != b.NormalizedHash {
		t.Fatalf("hashes differ for same normalized failure: %s vs %s", a.NormalizedHash, b.NormalizedHash)
	}
}

func TestComputeQueuePressure_Graduated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		obs  model.QueuePressureSnapshot
		want string
	}{
		{name: "none", obs: model.QueuePressureSnapshot{Depth: 1}, want: model.QueuePressureNone},
		{name: "reduced", obs: model.QueuePressureSnapshot{Depth: 3}, want: model.QueuePressureReduced},
		{name: "drain", obs: model.QueuePressureSnapshot{Depth: 6}, want: model.QueuePressureDrain},
		{name: "hard", obs: model.QueuePressureSnapshot{Depth: 9}, want: model.QueuePressureHard},
		{name: "age", obs: model.QueuePressureSnapshot{Depth: 1, OldestAge: time.Hour}, want: model.QueuePressureDrain},
		{name: "failures", obs: model.QueuePressureSnapshot{Depth: 1, RecentFailures: 3}, want: model.QueuePressureHard},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := model.ComputeQueuePressure(tc.obs, 3, time.Hour, 4)
			if got.Level != tc.want {
				t.Fatalf("Level = %q, want %q", got.Level, tc.want)
			}
		})
	}
}

// TestTraceAgentControllerDecision verifies that the trace helper writes
// the correct payload to the trace store.
func TestTraceAgentControllerDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/trace_controller_test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	recon := NewReconciler(st, spawner, wt, 10*time.Minute)

	// Create a task and agent for tracing
	_, err = st.TaskCreate(ctx, "task-trace", "", "", "Trace test", "", "", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AgentCreate(ctx, "agent-trace", "task-trace", 0, "cw-worker-task-trace", "", "", "default", "")
	if err != nil {
		t.Fatal(err)
	}

	agent, _ := st.AgentGet(ctx, "agent-trace")

	// Trace a healthy decision
	decision := model.AgentDecision{
		AgentID: "agent-trace",
		Health:  model.AgentHealthHealthy,
		Action:  model.ControllerActionNone,
		Reason:  "agent is healthy",
	}
	recon.traceAgentControllerDecision(ctx, agent, decision)

	// Verify the trace was written
	traces, err := st.TraceList(ctx, "task-trace", 100)
	if err != nil {
		t.Fatalf("TraceList: %v", err)
	}
	if len(traces) == 0 {
		t.Fatal("no traces found after traceAgentControllerDecision")
	}

	// Find the agent_controller.decision trace
	var found bool
	for _, trace := range traces {
		if trace.EventType == "agent_controller.decision" {
			found = true
			// Verify the payload can be unmarshaled
			var payload model.AgentControllerDecisionPayload
			if err := model.UnmarshalPayload(trace.Payload, &payload); err != nil {
				t.Fatalf("UnmarshalPayload: %v", err)
			}
			if payload.AgentID != "agent-trace" {
				t.Errorf("payload.AgentID = %q, want agent-trace", payload.AgentID)
			}
			if payload.Health != model.AgentHealthHealthy {
				t.Errorf("payload.Health = %q, want %q", payload.Health, model.AgentHealthHealthy)
			}
			if payload.Action != model.ControllerActionNone {
				t.Errorf("payload.Action = %q, want %q", payload.Action, model.ControllerActionNone)
			}
			break
		}
	}
	if !found {
		t.Error("agent_controller.decision trace not found in trace store")
	}
}

// TestTraceAgentControllerDecision_WithError verifies that error fields are
// correctly carried in the payload.
func TestTraceAgentControllerDecision_WithError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/trace_controller_error_test.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	recon := NewReconciler(st, spawner, wt, 10*time.Minute)

	_, _ = st.TaskCreate(ctx, "task-trace-err", "", "", "Trace err test", "", "", "", "default", 0)
	_, _ = st.AgentCreate(ctx, "agent-trace-err", "task-trace-err", 0, "cw-worker-task-trace-err", "", "", "default", "")
	agent, _ := st.AgentGet(ctx, "agent-trace-err")

	decision := model.AgentDecision{
		AgentID: "agent-trace-err",
		Health:  model.AgentHealthStalled,
		Action:  model.ControllerActionNudge,
		Reason:  "agent heartbeat is stale and pane is silent",
		Error: &model.ControllerError{
			Category:        model.ControllerErrorAgentStalling,
			Message:         "agent heartbeat is stale and pane is silent",
			Magnitude:       600,
			AffectedAgentID: "agent-trace-err",
		},
	}
	recon.traceAgentControllerDecision(ctx, agent, decision)

	traces, err := st.TraceList(ctx, "task-trace-err", 100)
	if err != nil {
		t.Fatalf("TraceList: %v", err)
	}

	for _, trace := range traces {
		if trace.EventType == "agent_controller.decision" {
			var payload model.AgentControllerDecisionPayload
			if err := model.UnmarshalPayload(trace.Payload, &payload); err != nil {
				t.Fatalf("UnmarshalPayload: %v", err)
			}
			if payload.ErrorCategory != model.ControllerErrorAgentStalling {
				t.Errorf("ErrorCategory = %q, want %q", payload.ErrorCategory, model.ControllerErrorAgentStalling)
			}
			if payload.ErrorMagnitude != 600 {
				t.Errorf("ErrorMagnitude = %d, want 600", payload.ErrorMagnitude)
			}
			if payload.Health != model.AgentHealthStalled {
				t.Errorf("Health = %q, want %q", payload.Health, model.AgentHealthStalled)
			}
			if payload.Action != model.ControllerActionNudge {
				t.Errorf("Action = %q, want %q", payload.Action, model.ControllerActionNudge)
			}
			return
		}
	}
	t.Fatal("agent_controller.decision trace not found")
}

// TestHandleStall_TracesControllerDecision verifies that handleStall produces
// an agent_controller.decision trace with the correct decision shape.
func TestHandleStall_TracesControllerDecision(t *testing.T) {
	sp, recon := newStallableReconciler(t, 1*time.Millisecond)
	ctx := context.Background()

	recon.store.TaskCreate(ctx, "task-stall-trace", "", "", "Stall trace test", "", "", "", "default", 0)
	recon.store.TaskSetStatus(ctx, "task-stall-trace", "running")
	recon.store.AgentCreate(ctx, "agent-stall-trace", "task-stall-trace", 0, "cw-worker-task-stall-trace", "", "", "claude", "")
	sp.FakeSpawner.Spawn("cw-worker-task-stall-trace", "", "cmd", nil, nil)
	sp.paneActivity = time.Now().Add(-10 * time.Minute)

	if err := recon.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	traces, err := recon.store.TraceList(ctx, "task-stall-trace", 100)
	if err != nil {
		t.Fatalf("TraceList: %v", err)
	}

	var found bool
	for _, trace := range traces {
		if trace.EventType == "agent_controller.decision" {
			found = true
			var payload model.AgentControllerDecisionPayload
			if err := model.UnmarshalPayload(trace.Payload, &payload); err != nil {
				t.Fatalf("UnmarshalPayload: %v", err)
			}
			if payload.Health != model.AgentHealthStalled {
				t.Errorf("Health = %q, want %q", payload.Health, model.AgentHealthStalled)
			}
			if payload.Action != model.ControllerActionNudge {
				t.Errorf("Action = %q, want %q", payload.Action, model.ControllerActionNudge)
			}
			if payload.ErrorCategory != model.ControllerErrorAgentStalling {
				t.Errorf("ErrorCategory = %q, want %q", payload.ErrorCategory, model.ControllerErrorAgentStalling)
			}
			break
		}
	}
	if !found {
		t.Fatal("agent_controller.decision trace not found after handleStall")
	}
}
