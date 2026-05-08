package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/worker"
)

// State-machine tests exercise computeACPState in isolation: just events in,
// derived state out. The reconciler glue (Tick + nudge logic) is tested
// separately. The cases below match the wire shapes Pi and Claude adapters
// emit in practice.

func acpEvents(payloads ...string) []*model.AgentEvent {
	now := time.Now()
	out := make([]*model.AgentEvent, len(payloads))
	for i, p := range payloads {
		out[i] = &model.AgentEvent{
			Seq:       int64(i + 1),
			Stream:    "acp.recv",
			Payload:   p,
			CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
		}
	}
	return out
}

const (
	evInitResult     = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`
	evSessionResult  = `{"jsonrpc":"2.0","id":2,"result":{"sessionId":"s-1"}}`
	evTurnStarted    = `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"turn_started","status":"running"}}}`
	evTurnStartedAlt = `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"status":"turn_started"}}}`
	evMessageChunk   = `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"working"}}}}`
	evToolCall       = `{"jsonrpc":"2.0","method":"session/update","params":{"itemType":"commandExecution","type":"tool_call_update","update":{"content":{"type":"text","text":"running tests"}}}}`
	evPermission     = `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"permissionRequest":{"command":"go test ./...","policy":"allow"}}}}`
	evTurnCompleted  = `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"status":"turn_completed"}}}`
	evStopEndTurn    = `{"jsonrpc":"2.0","id":9,"result":{"stopReason":"end_turn"}}`
	evStopError      = `{"jsonrpc":"2.0","id":10,"result":{"stopReason":"error"}}`
)

func TestComputeACPState_HandshakeOnly_NoTurnYet(t *testing.T) {
	state := computeACPState(acpEvents(evInitResult, evSessionResult))
	if state.InTurn {
		t.Error("InTurn = true, want false (no turn_started yet)")
	}
	if state.HadTurn {
		t.Error("HadTurn = true, want false (handshake responses do not end a turn)")
	}
	if state.EndedWithoutSignal() {
		t.Error("EndedWithoutSignal = true, want false during handshake")
	}
}

func TestComputeACPState_TurnStarted_InTurn(t *testing.T) {
	state := computeACPState(acpEvents(evInitResult, evSessionResult, evTurnStarted))
	if !state.InTurn {
		t.Error("InTurn = false, want true after turn_started")
	}
	if state.HadTurn {
		t.Error("HadTurn = true, want false (no end yet)")
	}
	if state.EndedWithoutSignal() {
		t.Error("EndedWithoutSignal = true, want false mid-turn")
	}
}

func TestComputeACPState_TurnStartedAltStatus_InTurn(t *testing.T) {
	state := computeACPState(acpEvents(evTurnStartedAlt))
	if !state.InTurn {
		t.Error("InTurn = false, want true (status:turn_started should also flip InTurn)")
	}
}

func TestComputeACPState_TurnEnded_StopReason(t *testing.T) {
	state := computeACPState(acpEvents(evTurnStarted, evMessageChunk, evStopEndTurn))
	if state.InTurn {
		t.Error("InTurn = true, want false after stopReason")
	}
	if !state.HadTurn {
		t.Error("HadTurn = false, want true after a turn ended")
	}
	if !state.EndedWithoutSignal() {
		t.Error("EndedWithoutSignal = false, want true (turn ended, agent idle)")
	}
	if state.LastStopReason != "end_turn" {
		t.Errorf("LastStopReason = %q, want end_turn", state.LastStopReason)
	}
}

func TestComputeACPState_TurnEnded_TurnCompleted(t *testing.T) {
	state := computeACPState(acpEvents(evTurnStarted, evTurnCompleted))
	if state.InTurn {
		t.Error("InTurn = true, want false after turn_completed")
	}
	if !state.HadTurn {
		t.Error("HadTurn = false, want true after turn_completed")
	}
	if !state.EndedWithoutSignal() {
		t.Error("EndedWithoutSignal = false, want true after turn_completed")
	}
}

// Regression for the latching bug: a prior turn that ended must NOT bleed into
// the next turn. Once a new turn starts, EndedWithoutSignal should be false
// again until that new turn also ends.
func TestComputeACPState_NewTurnAfterPriorEnd_NotEndedWithoutSignal(t *testing.T) {
	state := computeACPState(acpEvents(
		evTurnStarted, evMessageChunk, evStopEndTurn, // first turn completes
		evTurnStarted, evMessageChunk, // second turn in progress
	))
	if !state.InTurn {
		t.Error("InTurn = false, want true (currently mid second turn)")
	}
	if !state.HadTurn {
		t.Error("HadTurn = false, want true (first turn ended)")
	}
	if state.EndedWithoutSignal() {
		t.Error("EndedWithoutSignal = true, want false (mid new turn after a prior end)")
	}
}

// Permission requests and tool calls from a prior turn should not persist into a
// later active turn. A new turn_started is the boundary that should clear both
// latches.
func TestComputeACPState_TurnStartedResetsLatches(t *testing.T) {
	state := computeACPState(acpEvents(
		evTurnStarted, evPermission, evToolCall,
		evTurnStarted,
	))
	if !state.InTurn {
		t.Error("InTurn = false, want true after the latest turn_started")
	}
	if state.PermissionPending {
		t.Error("PermissionPending = true, want false after new turn_started")
	}
	if state.ToolActivity {
		t.Error("ToolActivity = true, want false after new turn_started")
	}
}

// Two complete turns in sequence: state at the end is "ended without signal"
// because the second turn closed and we are again idle.
func TestComputeACPState_TwoTurnsBothEnded_EndedWithoutSignal(t *testing.T) {
	state := computeACPState(acpEvents(
		evTurnStarted, evStopEndTurn,
		evTurnStarted, evStopError,
	))
	if state.InTurn {
		t.Error("InTurn = true, want false after second turn ended")
	}
	if !state.HadTurn {
		t.Error("HadTurn = false, want true")
	}
	if !state.EndedWithoutSignal() {
		t.Error("EndedWithoutSignal = false, want true after both turns closed")
	}
	if state.LastStopReason != "error" {
		t.Errorf("LastStopReason = %q, want error (most recent stop)", state.LastStopReason)
	}
}

func TestComputeACPState_ToolActivityAndPermission(t *testing.T) {
	state := computeACPState(acpEvents(evTurnStarted, evToolCall, evPermission))
	if !state.InTurn {
		t.Error("InTurn = false, want true (mid turn)")
	}
	if !state.ToolActivity {
		t.Error("ToolActivity = false, want true after tool_call_update")
	}
	if !state.PermissionPending {
		t.Error("PermissionPending = false, want true after permissionRequest")
	}
}

func TestACPProgressEvidence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		evs  []string
		want string
	}{
		{name: "tool activity", evs: []string{evTurnStarted, evToolCall}, want: model.ProgressPresent},
		{name: "ended without signal", evs: []string{evTurnStarted, evStopEndTurn}, want: model.ProgressAbsent},
		{name: "permission pending", evs: []string{evTurnStarted, evPermission}, want: model.ProgressUnknown},
		{name: "handshake", evs: []string{evInitResult, evSessionResult}, want: model.ProgressUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := computeACPState(acpEvents(tc.evs...))
			if got := state.progressEvidence(); got != tc.want {
				t.Fatalf("progressEvidence = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestComputeACPState_ErrorTurnCount verifies that turns ending with
// stopReason:"error" increment ErrorTurnCount, while other turn endings do not.
func TestComputeACPState_ErrorTurnCount(t *testing.T) {
	state := computeACPState(acpEvents(
		evTurnStarted, evStopError,
		evTurnStarted, evStopError,
		evTurnStarted, evStopError,
	))
	if state.ErrorTurnCount != 3 {
		t.Errorf("ErrorTurnCount = %d, want 3", state.ErrorTurnCount)
	}
}

// TestComputeACPState_ErrorAndNormalMixed verifies that only error-ended turns
// count toward ErrorTurnCount — normal end_turn events don't.
func TestComputeACPState_ErrorAndNormalMixed(t *testing.T) {
	state := computeACPState(acpEvents(
		evTurnStarted, evStopEndTurn, // normal
		evTurnStarted, evStopError, // error
		evTurnStarted, evStopEndTurn, // normal
	))
	if state.ErrorTurnCount != 1 {
		t.Errorf("ErrorTurnCount = %d, want 1 (only error-ended turns)", state.ErrorTurnCount)
	}
	if state.LastStopReason != "end_turn" {
		t.Errorf("LastStopReason = %q, want end_turn (most recent)", state.LastStopReason)
	}
}

func TestComputeACPState_MalformedEventsIgnored(t *testing.T) {
	state := computeACPState(acpEvents(
		`not json at all`,
		evTurnStarted,
		`{"truncated":`,
		evStopEndTurn,
	))
	if state.InTurn {
		t.Error("InTurn = true, want false (turn ended despite garbage interleaving)")
	}
	if !state.HadTurn || !state.EndedWithoutSignal() {
		t.Error("expected HadTurn && EndedWithoutSignal after a stopReason event")
	}
}

func TestComputeACPState_LastEventAtIsLatest(t *testing.T) {
	events := acpEvents(evTurnStarted, evToolCall, evMessageChunk)
	state := computeACPState(events)
	want := events[len(events)-1].CreatedAt
	if !state.LastEventAt.Equal(want) {
		t.Errorf("LastEventAt = %v, want %v (latest event timestamp)", state.LastEventAt, want)
	}
}

func TestComputeACPState_ContextLimitInResult(t *testing.T) {
	state := computeACPState(acpEvents(
		`{"jsonrpc":"2.0","id":7,"result":{"stopReason":"context_limit"}}`,
	))
	if !state.ContextLimitError {
		t.Error("ContextLimitError = false, want true for stopReason=context_limit")
	}
}

// --- Reconciler integration: nudge persistence under heartbeat refresh ---

// Regression for the second part of the ACP stall loop: in production the
// agent emits clankwork signal progress mid-nudge-response, refreshing the
// heartbeat. Previously the reconciler cleared nudgeSent on heartbeat
// freshness, which re-armed the no-signal nudge every tick — an unbounded
// prompt loop. The fix: ACP keeps nudge state across ticks, regardless of
// heartbeat freshness.
func TestReconciler_ACPNudgeStateSurvivesHeartbeatRefresh(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-hb", "", "", "ACP heartbeat-refresh test", "", "", "", "claude-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-hb", "running")
	r.store.AgentCreate(ctx, "agent-acp-hb", "task-acp-hb", 0, "cw-worker-acp-hb", "", "", "claude-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-hb", "", "cmd", nil, nil)

	// Turn ended without a terminal signal — the no-signal condition.
	if err := r.store.AgentEventAppend(ctx, "agent-acp-hb", "task-acp-hb", 1, "acp.recv", evStopEndTurn); err != nil {
		t.Fatal(err)
	}

	// Tick 1: should send the first nudge.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sp.nudgesSent) != 1 {
		t.Fatalf("nudges after tick 1 = %d, want 1", len(sp.nudgesSent))
	}

	// Simulate the agent emitting progress mid-nudge-response: heartbeat
	// becomes fresh. Under the old code this would clear nudgeSent and let
	// the next tick send a second nudge.
	if err := r.store.AgentUpdateHeartbeat(ctx, "agent-acp-hb"); err != nil {
		t.Fatal(err)
	}

	// Tick 2: nudgeSent must persist; no second nudge sent within the timeout.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sp.nudgesSent) != 1 {
		t.Fatalf("nudges after tick 2 = %d, want 1 (heartbeat freshness must not re-arm ACP nudge)", len(sp.nudgesSent))
	}
}

// Regression for the latching bug at the reconciler boundary: an agent that
// ended a prior turn but is currently mid-new-turn must not be nudged.
func TestReconciler_ACPMidNewTurnAfterPriorEnd_NotNudged(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-midturn", "", "", "ACP mid new turn", "", "", "", "claude-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-midturn", "running")
	r.store.AgentCreate(ctx, "agent-acp-midturn", "task-acp-midturn", 0, "cw-worker-acp-midturn", "", "", "claude-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-midturn", "", "cmd", nil, nil)
	if err := r.store.AgentUpdateHeartbeat(ctx, "agent-acp-midturn"); err != nil {
		t.Fatal(err)
	}

	for i, payload := range []string{evStopEndTurn, evTurnStarted, evMessageChunk} {
		if err := r.store.AgentEventAppend(ctx, "agent-acp-midturn", "task-acp-midturn", int64(i+1), "acp.recv", payload); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sp.nudgesSent) != 0 {
		t.Fatalf("nudges = %d, want 0 (agent is mid-new-turn after a prior end)", len(sp.nudgesSent))
	}
}
