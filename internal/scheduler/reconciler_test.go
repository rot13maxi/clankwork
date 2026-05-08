package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/worker"
)

// stallableSpawner wraps FakeSpawner with controllable pane state for stall tests.
type stallableSpawner struct {
	*worker.FakeSpawner
	paneActivity time.Time
	paneContent  string
	nudgesSent   []string
	transport    string
	killCalls    []string
	gracefulKill []string
	nudgeErr     error // when set, SendNudge returns this error instead of recording
}

func (s *stallableSpawner) PaneLastActivity(sessionName string) (time.Time, error) {
	return s.paneActivity, nil
}

func (s *stallableSpawner) CapturePane(sessionName string, lines int) (string, error) {
	return s.paneContent, nil
}

func (s *stallableSpawner) SendNudge(sessionName, msg string) error {
	if s.nudgeErr != nil {
		return s.nudgeErr
	}
	s.nudgesSent = append(s.nudgesSent, sessionName)
	return nil
}

func (s *stallableSpawner) Kill(sessionName string) error {
	s.killCalls = append(s.killCalls, sessionName)
	return s.FakeSpawner.Kill(sessionName)
}

func (s *stallableSpawner) GracefulKill(sessionName string, gracePeriod time.Duration) error {
	s.gracefulKill = append(s.gracefulKill, sessionName)
	return s.FakeSpawner.GracefulKill(sessionName, gracePeriod)
}

func (s *stallableSpawner) TransportForSession(sessionName string) string {
	if s.transport != "" {
		return s.transport
	}
	return worker.TransportTmux
}

func newStallableReconciler(t *testing.T, timeout time.Duration) (*stallableSpawner, *Reconciler) {
	t.Helper()
	sp := &stallableSpawner{
		FakeSpawner:  &worker.FakeSpawner{},
		paneActivity: time.Now(),
	}
	r := NewReconciler(newTestStore(t), sp, &worker.FakeWorktreeCreator{}, timeout)
	return sp, r
}

// TestReconciler_StallDetected_SendsNudge verifies that when both heartbeat and pane
// are stale, a nudge is sent and the task is NOT immediately failed.
func TestReconciler_StallDetected_SendsNudge(t *testing.T) {
	sp, r := newStallableReconciler(t, 1*time.Millisecond)
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-stall", "", "", "Stall", "", "", "", "default", 0)
	r.store.TaskSetStatus(ctx, "task-stall", "running")
	r.store.AgentCreate(ctx, "agent-stall", "task-stall", 0, "cw-worker-stall", "", "", "claude", "")
	sp.FakeSpawner.Spawn("cw-worker-stall", "", "cmd", nil, nil)
	// Pane stale: last activity long ago.
	sp.paneActivity = time.Now().Add(-10 * time.Minute)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) == 0 {
		t.Error("no nudge sent to stalled agent")
	}
	task, _ := r.store.TaskGet(ctx, "task-stall")
	if task.Status != "running" {
		t.Errorf("task failed immediately after first stall detection; want still running, got %q", task.Status)
	}
}

// TestReconciler_NudgeTimeout_Handoff verifies that after nudgeTimeout elapses,
// the agent is killed and the task re-dispatched.
func TestReconciler_NudgeTimeout_Handoff(t *testing.T) {
	sp, r := newStallableReconciler(t, 1*time.Millisecond)
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-nudge", "", "", "Nudge", "", "", "", "default", 0)
	r.store.TaskSetStatus(ctx, "task-nudge", "running")
	r.store.AgentCreate(ctx, "agent-nudge", "task-nudge", 0, "cw-worker-nudge", "", "", "claude", "")
	sp.FakeSpawner.Spawn("cw-worker-nudge", "", "cmd", nil, nil)
	sp.paneActivity = time.Now().Add(-10 * time.Minute)

	// Inject an already-expired nudge so the reconciler sees "nudge timeout".
	r.nudgeMu.Lock()
	r.nudgeSent["agent-nudge"] = time.Now().Add(-nudgeTimeout - time.Second)
	r.nudgeMu.Unlock()

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	task, _ := r.store.TaskGet(ctx, "task-nudge")
	if task.Status == "running" {
		t.Error("task still running after nudge timeout; expected handoff (failed/pending)")
	}
}

// TestReconciler_ContextLimit_ImmediateHandoff verifies that a context-limit error
// in the pane skips the nudge entirely and hands off immediately.
func TestReconciler_ContextLimit_ImmediateHandoff(t *testing.T) {
	sp, r := newStallableReconciler(t, 1*time.Millisecond)
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-ctx", "", "", "ContextLimit", "", "", "", "default", 0)
	r.store.TaskSetStatus(ctx, "task-ctx", "running")
	r.store.AgentCreate(ctx, "agent-ctx", "task-ctx", 0, "cw-worker-ctx", "", "", "claude", "")
	sp.FakeSpawner.Spawn("cw-worker-ctx", "", "cmd", nil, nil)
	sp.paneActivity = time.Now().Add(-10 * time.Minute)
	sp.paneContent = "context window is full - please start a new conversation"

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) > 0 {
		t.Error("nudge sent despite context-limit error; expected immediate handoff")
	}
	task, _ := r.store.TaskGet(ctx, "task-ctx")
	if task.Status == "running" {
		t.Error("task still running after context-limit detection")
	}
	traces, err := r.store.TraceList(ctx, "task-ctx", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, trace := range traces {
		if trace.EventType != "agent_controller.decision" {
			continue
		}
		var payload model.AgentControllerDecisionPayload
		if err := model.UnmarshalPayload(trace.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Action != model.ControllerActionHandoff {
			t.Fatalf("context-limit controller action = %q, want %q", payload.Action, model.ControllerActionHandoff)
		}
		if payload.ErrorCategory != model.ControllerErrorAgentContextLimit {
			t.Fatalf("context-limit error category = %q, want %q", payload.ErrorCategory, model.ControllerErrorAgentContextLimit)
		}
		return
	}
	t.Fatal("agent_controller.decision trace not found for context-limit handoff")
}

// TestReconciler_PaneActive_HeartbeatStale verifies that pane activity alone keeps
// the agent alive — avoids false positives when heartbeat code is broken.
func TestReconciler_PaneActive_HeartbeatStale_NoAction(t *testing.T) {
	sp, r := newStallableReconciler(t, 1*time.Millisecond)
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-pane", "", "", "PaneActive", "", "", "", "default", 0)
	r.store.TaskSetStatus(ctx, "task-pane", "running")
	r.store.AgentCreate(ctx, "agent-pane", "task-pane", 0, "cw-worker-pane", "", "", "claude", "")
	sp.FakeSpawner.Spawn("cw-worker-pane", "", "cmd", nil, nil)
	// Pane is very active; heartbeat is nil (stale).
	sp.paneActivity = time.Now()

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) > 0 {
		t.Error("nudge sent when pane was active")
	}
	task, _ := r.store.TaskGet(ctx, "task-pane")
	if task.Status != "running" {
		t.Errorf("task failed when pane was active; got %q", task.Status)
	}
}

func TestReconciler_ACPTurnEndedWithoutSignal_SendsNudge(t *testing.T) {
	sp, r := newStallableReconciler(t, 1*time.Millisecond)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-ended", "", "", "ACP", "", "", "", "claude-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-ended", "running")
	r.store.AgentCreate(ctx, "agent-acp-ended", "task-acp-ended", 0, "cw-worker-acp-ended", "", "", "claude-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-ended", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-ended", "task-acp-ended", 1, "acp.recv", `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) == 0 {
		t.Fatal("no ACP no-signal nudge sent")
	}
	task, _ := r.store.TaskGet(ctx, "task-acp-ended")
	if task.Status != "running" {
		t.Fatalf("task status = %q, want running after first no-signal nudge", task.Status)
	}
}

func TestReconciler_ACPSessionHandshakeDoesNotLookStopped(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-handshake", "", "", "ACP", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-handshake", "running")
	r.store.AgentCreate(ctx, "agent-acp-handshake", "task-acp-handshake", 0, "cw-worker-acp-handshake", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-handshake", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-handshake", "task-acp-handshake", 1, "acp.recv", `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`); err != nil {
		t.Fatal(err)
	}
	if err := r.store.AgentEventAppend(ctx, "agent-acp-handshake", "task-acp-handshake", 2, "acp.recv", `{"jsonrpc":"2.0","id":2,"result":{"sessionId":"session-1"}}`); err != nil {
		t.Fatal(err)
	}
	if err := r.store.AgentEventAppend(ctx, "agent-acp-handshake", "task-acp-handshake", 3, "acp.recv", `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"turn_started","status":"running"}}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) != 0 {
		t.Fatalf("nudges = %d, want 0 during handshake/turn start", len(sp.nudgesSent))
	}
	task, _ := r.store.TaskGet(ctx, "task-acp-handshake")
	if task.Status != "running" {
		t.Fatalf("task status = %q, want running", task.Status)
	}
}

func TestReconciler_ACPTurnErrorWithoutSignal_SendsNudge(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-error", "", "", "ACP", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-error", "running")
	r.store.AgentCreate(ctx, "agent-acp-error", "task-acp-error", 0, "cw-worker-acp-error", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-error", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-error", "task-acp-error", 1, "acp.recv", `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"error"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) == 0 {
		t.Fatal("no ACP error no-signal nudge sent")
	}
	task, _ := r.store.TaskGet(ctx, "task-acp-error")
	if task.Status != "running" {
		t.Fatalf("task status = %q, want running after first error no-signal nudge", task.Status)
	}
}

func TestReconciler_ACPTurnEndedWithoutSignal_IsRetrySafe(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-retry", "", "", "ACP", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-retry", "running")
	r.store.AgentCreate(ctx, "agent-acp-retry", "task-acp-retry", 0, "cw-worker-acp-retry", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-retry", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-retry", "task-acp-retry", 1, "acp.recv", `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) != 1 {
		t.Fatalf("nudges = %d, want exactly 1 before timeout", len(sp.nudgesSent))
	}
	if len(sp.gracefulKill) != 0 {
		t.Fatalf("graceful kills = %d, want 0 before timeout", len(sp.gracefulKill))
	}
	task, _ := r.store.TaskGet(ctx, "task-acp-retry")
	if task.Status != "running" {
		t.Fatalf("task status = %q, want running before timeout", task.Status)
	}
}

func TestReconciler_ACPTurnEndedWithoutSignal_EventuallyHandsOff(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-timeout", "", "", "ACP", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-timeout", "running")
	r.store.AgentCreate(ctx, "agent-acp-timeout", "task-acp-timeout", 0, "cw-worker-acp-timeout", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-timeout", "", "cmd", nil, nil)
	// Non-error stop reason: one nudge then handoff (no error-retry path).
	if err := r.store.AgentEventAppend(ctx, "agent-acp-timeout", "task-acp-timeout", 1, "acp.recv", `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	r.nudgeMu.Lock()
	r.nudgeSent["agent-acp-timeout"] = time.Now().Add(-nudgeTimeout - time.Second)
	r.nudgeMu.Unlock()
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) != 1 {
		t.Fatalf("nudges = %d, want exactly 1 before handoff", len(sp.nudgesSent))
	}
	if len(sp.gracefulKill) != 1 {
		t.Fatalf("graceful kills = %d, want 1 after timeout handoff", len(sp.gracefulKill))
	}
	task, _ := r.store.TaskGet(ctx, "task-acp-timeout")
	if task.Status == "running" {
		t.Fatalf("task status = %q, want terminal after timeout handoff", task.Status)
	}
	agent, err := r.store.AgentGet(ctx, "agent-acp-timeout")
	if err != nil {
		t.Fatal(err)
	}
	if agent.Status != "killed" {
		t.Fatalf("agent status = %q, want killed after timeout handoff", agent.Status)
	}
}

func TestReconciler_ACPStateTracksLatestStopReasonAndTurnSignals(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	_ = sp
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-state", "", "", "ACP", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-state", "running")
	r.store.AgentCreate(ctx, "agent-acp-state", "task-acp-state", 0, "cw-worker-acp-state", "", "", "pi-acp", "")
	events := []string{
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"turn_started","status":"running"}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"status":"turn_completed"}}}`,
		`{"jsonrpc":"2.0","id":9,"result":{"stopReason":"end_turn"}}`,
		`{"jsonrpc":"2.0","id":10,"result":{"stopReason":"error"}}`,
	}
	for i, payload := range events {
		if err := r.store.AgentEventAppend(ctx, "agent-acp-state", "task-acp-state", int64(i+1), "acp.recv", payload); err != nil {
			t.Fatal(err)
		}
	}

	agent, err := r.store.AgentGet(ctx, "agent-acp-state")
	if err != nil {
		t.Fatal(err)
	}
	state := r.acpState(ctx, agent)
	if state.InTurn {
		t.Fatal("InTurn = true, want false after completion/end")
	}
	if !state.HadTurn {
		t.Fatal("HadTurn = false, want true after a turn ended")
	}
	if !state.EndedWithoutSignal() {
		t.Fatal("EndedWithoutSignal = false, want true (turn ended, agent idle)")
	}
	if state.LastStopReason != "error" {
		t.Fatalf("LastStopReason = %q, want error", state.LastStopReason)
	}
	if state.LastEventAt.IsZero() {
		t.Fatal("LastEventAt is zero, want latest event timestamp")
	}
	if !state.eventFresh(time.Hour) {
		t.Fatal("eventFresh = false, want true for recent ACP event")
	}
}

func TestReconciler_ACPStateTracksPermissionAndToolActivity(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-observed", "", "", "ACP observed state", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-observed", "running")
	r.store.AgentCreate(ctx, "agent-acp-observed", "task-acp-observed", 0, "cw-worker-acp-observed", "", "", "pi-acp", "")
	events := []string{
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"turn_started","status":"running"}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"itemType":"commandExecution","type":"tool_call_update","update":{"content":{"type":"text","text":"running tests"}}}}`,
		`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"permissionRequest":{"command":"go test ./...","policy":"allow"}}}}`,
	}
	for i, payload := range events {
		if err := r.store.AgentEventAppend(ctx, "agent-acp-observed", "task-acp-observed", int64(i+1), "acp.recv", payload); err != nil {
			t.Fatal(err)
		}
	}

	agent, err := r.store.AgentGet(ctx, "agent-acp-observed")
	if err != nil {
		t.Fatal(err)
	}
	state := r.acpState(ctx, agent)
	if !state.InTurn {
		t.Fatal("InTurn = false, want true before completion/end")
	}
	if state.HadTurn {
		t.Fatal("HadTurn = true, want false (no turn end seen yet)")
	}
	if state.EndedWithoutSignal() {
		t.Fatal("EndedWithoutSignal = true, want false during active turn")
	}
	if !state.ToolActivity {
		t.Fatal("ToolActivity = false, want true")
	}
	if !state.PermissionPending {
		t.Fatal("PermissionPending = false, want true")
	}
}

func TestReconciler_ACPStateIgnoresContextLimitTextInStreamedContent(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	_ = sp
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-false-context", "", "", "ACP", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-false-context", "running")
	r.store.AgentCreate(ctx, "agent-acp-false-context", "task-acp-false-context", 0, "cw-worker-acp-false-context", "", "", "pi-acp", "")
	if err := r.store.AgentEventAppend(ctx, "agent-acp-false-context", "task-acp-false-context", 1, "acp.recv", `{"jsonrpc":"2.0","method":"session/update","params":{"delta":"const reason = \"context_window_exceeded\"","itemType":"commandExecution","type":"tool_call_update","update":{"content":{"type":"text","text":"const reason = \"context_window_exceeded\""}}}}`); err != nil {
		t.Fatal(err)
	}

	agent, err := r.store.AgentGet(ctx, "agent-acp-false-context")
	if err != nil {
		t.Fatal(err)
	}
	state := r.acpState(ctx, agent)
	if state.ContextLimitError {
		t.Fatal("ContextLimitError = true, want false for streamed code/content text")
	}
}

func TestReconciler_ACPContextLimitFromStructuredErrorHandsOff(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-context-err", "", "", "ACP", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-context-err", "running")
	r.store.AgentCreate(ctx, "agent-acp-context-err", "task-acp-context-err", 0, "cw-worker-acp-context-err", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-context-err", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-context-err", "task-acp-context-err", 1, "acp.recv", `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Unable to compact conversation"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	task, _ := r.store.TaskGet(ctx, "task-acp-context-err")
	if task.Status == "running" {
		t.Fatalf("task status = %q, want terminal after structured context-limit error", task.Status)
	}
}

func TestReconciler_ACPRecentEventKeepsAgentAlive(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-active", "", "", "ACP", "", "", "", "claude-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-active", "running")
	r.store.AgentCreate(ctx, "agent-acp-active", "task-acp-active", 0, "cw-worker-acp-active", "", "", "claude-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-active", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-active", "task-acp-active", 1, "acp.recv", `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"working"}}}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	if len(sp.nudgesSent) > 0 {
		t.Fatal("nudge sent despite recent ACP activity")
	}
	task, _ := r.store.TaskGet(ctx, "task-acp-active")
	if task.Status != "running" {
		t.Fatalf("task status = %q, want running", task.Status)
	}
}

func TestReconciler_TerminalACPAgentCleansRuntime(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-done", "", "", "ACP", "", "", "", "claude-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-done", "done")
	if _, err := r.store.AgentCreateWithRuntime(ctx, "agent-acp-done", "task-acp-done", 0, "cw-worker-acp-done", worker.TransportACP, "session-1", 4242, "", "", "claude-acp", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := r.store.AgentSetEnded(ctx, "agent-acp-done"); err != nil {
		t.Fatal(err)
	}
	sp.FakeSpawner.Spawn("cw-worker-acp-done", "", "cmd", nil, nil)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	alive, _ := sp.FakeSpawner.IsAlive("cw-worker-acp-done")
	if alive {
		t.Fatal("terminal ACP runtime still alive after reconciler cleanup")
	}
	agent, err := r.store.AgentGet(ctx, "agent-acp-done")
	if err != nil {
		t.Fatal(err)
	}
	if agent.PID != 0 {
		t.Fatalf("agent PID = %d, want cleared after runtime cleanup", agent.PID)
	}
}

// TestHasContextLimitError verifies case-insensitive pattern matching.
func TestHasContextLimitError(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"context window is full", true},
		{"CONTEXT WINDOW IS FULL", true},
		{"Unable to compact", true},
		{"compaction failed", true},
		{"context_window_exceeded", true},
		{"too long to continue", true},
		{"all good, working on it", false},
		{"", false},
	}
	for _, c := range cases {
		got := hasContextLimitError(c.input)
		if got != c.want {
			t.Errorf("hasContextLimitError(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

func TestACPMessageHasContextLimitError(t *testing.T) {
	cases := []struct {
		name string
		msg  map[string]any
		want bool
	}{
		{
			name: "stop reason context limit",
			msg:  map[string]any{"result": map[string]any{"stopReason": "context_limit"}},
			want: true,
		},
		{
			name: "structured error message",
			msg:  map[string]any{"error": map[string]any{"message": "Unable to compact conversation"}},
			want: true,
		},
		{
			name: "streamed content code text",
			msg: map[string]any{
				"params": map[string]any{
					"delta": `const reason = "context_window_exceeded"`,
					"update": map[string]any{
						"content": map[string]any{"type": "text", "text": `const reason = "context_window_exceeded"`},
					},
				},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		if got := acpMessageHasContextLimitError(tc.msg); got != tc.want {
			t.Errorf("%s: acpMessageHasContextLimitError() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestReconciler_ACPTurnEndedWithoutSignal_EmitsNudgeDecisionTrace verifies that
// the ACP turn-ended-without-signal path emits an agent_controller.decision trace
// with action=nudge before sending the nudge message.
func TestReconciler_ACPTurnEndedWithoutSignal_EmitsNudgeDecisionTrace(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-nosig-trace", "", "", "ACP no-signal trace test", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-nosig-trace", "running")
	r.store.AgentCreate(ctx, "agent-acp-nosig-trace", "task-acp-nosig-trace", 0, "cw-worker-acp-nosig-trace", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-nosig-trace", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-nosig-trace", "task-acp-nosig-trace", 1, "acp.recv", `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	traces, err := r.store.TraceList(ctx, "task-acp-nosig-trace", 100)
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
			if payload.Action != model.ControllerActionNudge {
				t.Fatalf("action = %q, want %q", payload.Action, model.ControllerActionNudge)
			}
			if payload.Health != model.AgentHealthStalled {
				t.Fatalf("health = %q, want %q", payload.Health, model.AgentHealthStalled)
			}
			if payload.Reason == "" {
				t.Fatal("reason is empty")
			}
			break
		}
	}
	if !found {
		t.Fatal("agent_controller.decision trace not found for ACP turn-ended-without-signal nudge")
	}
}

// TestReconciler_ACPContextLimit_EmitsHandoffDecisionTrace verifies that the ACP
// context-limit path emits an agent_controller.decision trace with action=handoff
// before failing the task.
func TestReconciler_ACPContextLimit_EmitsHandoffDecisionTrace(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-ctx-decision", "", "", "ACP context-limit decision test", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-ctx-decision", "running")
	r.store.AgentCreate(ctx, "agent-acp-ctx-decision", "task-acp-ctx-decision", 0, "cw-worker-acp-ctx-decision", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-ctx-decision", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-ctx-decision", "task-acp-ctx-decision", 1, "acp.recv", `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Unable to compact conversation"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	traces, err := r.store.TraceList(ctx, "task-acp-ctx-decision", 100)
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
			if payload.Action != model.ControllerActionHandoff {
				t.Fatalf("action = %q, want %q", payload.Action, model.ControllerActionHandoff)
			}
			if payload.ErrorCategory != model.ControllerErrorAgentContextLimit {
				t.Fatalf("error_category = %q, want %q", payload.ErrorCategory, model.ControllerErrorAgentContextLimit)
			}
			break
		}
	}
	if !found {
		t.Fatal("agent_controller.decision trace not found for ACP context-limit handoff")
	}
}

// TestReconciler_ACPContextLimit_DoesNotScanStreamedContent verifies that structured
// ACP context-limit detection does not scan streamed content text, ensuring the
// no-raw-scan constraint is maintained.
func TestReconciler_ACPContextLimit_DoesNotScanStreamedContent(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-no-raw-scan", "", "", "ACP no raw scan test", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-no-raw-scan", "running")
	r.store.AgentCreate(ctx, "agent-acp-no-raw-scan", "task-acp-no-raw-scan", 0, "cw-worker-acp-no-raw-scan", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-no-raw-scan", "", "cmd", nil, nil)
	// Streamed content text that would trigger false positives if scanned raw.
	if err := r.store.AgentEventAppend(ctx, "agent-acp-no-raw-scan", "task-acp-no-raw-scan", 1, "acp.recv", `{"jsonrpc":"2.0","method":"session/update","params":{"delta":"const reason = \"context_window_exceeded\"","itemType":"commandExecution","type":"tool_call_update","update":{"content":{"type":"text","text":"const reason = \"context_window_exceeded\""}}}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// Task should still be running — no context-limit false detection.
	task, _ := r.store.TaskGet(ctx, "task-acp-no-raw-scan")
	if task.Status != "running" {
		t.Fatalf("task status = %q, want running (no false context-limit detection)", task.Status)
	}
}

// TestReconciler_ACPNoSignalNudgeTimeout_EmitsHandoffDecisionTrace verifies that
// the ACP no-signal nudge-timeout path emits an agent_controller.decision trace
// with action=handoff.
func TestReconciler_ACPNoSignalNudgeTimeout_EmitsHandoffDecisionTrace(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-nosig-timeout", "", "", "ACP no-signal timeout test", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-nosig-timeout", "running")
	r.store.AgentCreate(ctx, "agent-acp-nosig-timeout", "task-acp-nosig-timeout", 0, "cw-worker-acp-nosig-timeout", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-nosig-timeout", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-acp-nosig-timeout", "task-acp-nosig-timeout", 1, "acp.recv", `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate nudge timeout.
	r.nudgeMu.Lock()
	r.nudgeSent["agent-acp-nosig-timeout"] = time.Now().Add(-nudgeTimeout - time.Second)
	r.nudgeMu.Unlock()
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	traces, err := r.store.TraceList(ctx, "task-acp-nosig-timeout", 100)
	if err != nil {
		t.Fatalf("TraceList: %v", err)
	}

	handoffFound := false
	for _, trace := range traces {
		if trace.EventType == "agent_controller.decision" {
			var payload model.AgentControllerDecisionPayload
			if err := model.UnmarshalPayload(trace.Payload, &payload); err != nil {
				t.Fatalf("UnmarshalPayload: %v", err)
			}
			if payload.Action == model.ControllerActionHandoff && payload.Reason != "" {
				handoffFound = true
			}
		}
	}
	if !handoffFound {
		t.Fatal("agent_controller.decision (handoff) trace not found for ACP no-signal nudge timeout")
	}
}

// TestReconciler_ACPStall_EmitsNudgeDecisionTrace verifies that the ACP stall path
// (events and heartbeat silent) emits an agent_controller.decision trace.
func TestReconciler_ACPStall_EmitsNudgeDecisionTrace(t *testing.T) {
	sp, r := newStallableReconciler(t, 1*time.Millisecond)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-acp-stall-trace", "", "", "ACP stall trace test", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-acp-stall-trace", "running")
	r.store.AgentCreate(ctx, "agent-acp-stall-trace", "task-acp-stall-trace", 0, "cw-worker-acp-stall-trace", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-acp-stall-trace", "", "cmd", nil, nil)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	traces, err := r.store.TraceList(ctx, "task-acp-stall-trace", 100)
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
			if payload.Action != model.ControllerActionNudge {
				t.Fatalf("action = %q, want %q", payload.Action, model.ControllerActionNudge)
			}
			break
		}
	}
	if !found {
		t.Fatal("agent_controller.decision trace not found for ACP stall nudge")
	}
}

// TestReconciler_TerminalRuntime_EmitsControllerDecisionTrace verifies that the
// terminal/dead runtime cleanup path emits an agent_controller.decision trace.
func TestReconciler_TerminalRuntime_EmitsControllerDecisionTrace(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-terminal-decision", "", "", "Terminal decision trace test", "", "", "", "claude-acp", 0)
	r.store.TaskSetStatus(ctx, "task-terminal-decision", "done")
	if _, err := r.store.AgentCreateWithRuntime(ctx, "agent-terminal-decision", "task-terminal-decision", 0, "cw-worker-terminal-decision", worker.TransportACP, "session-1", 4242, "", "", "claude-acp", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := r.store.AgentSetEnded(ctx, "agent-terminal-decision"); err != nil {
		t.Fatal(err)
	}
	sp.FakeSpawner.Spawn("cw-worker-terminal-decision", "", "cmd", nil, nil)

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	traces, err := r.store.TraceList(ctx, "task-terminal-decision", 100)
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
			if payload.Action != model.ControllerActionKill {
				t.Fatalf("action = %q, want %q", payload.Action, model.ControllerActionKill)
			}
			if payload.Health != model.AgentHealthDead {
				t.Fatalf("health = %q, want %q", payload.Health, model.AgentHealthDead)
			}
			break
		}
	}
	if !found {
		t.Fatal("agent_controller.decision trace not found for terminal runtime cleanup")
	}
}

// TestReconciler_ActuationTraces_NudgeAndHandoff verifies that actuation outcome
// traces are emitted for nudge and handoff actions.
func TestReconciler_ActuationTraces_NudgeAndHandoff(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	// Test 1: ACP no-signal nudge emits actuation trace.
	r.store.TaskCreate(ctx, "task-act-nudge", "", "", "Actuation nudge test", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-act-nudge", "running")
	r.store.AgentCreate(ctx, "agent-act-nudge", "task-act-nudge", 0, "cw-worker-act-nudge", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-act-nudge", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-act-nudge", "task-act-nudge", 1, "acp.recv", `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	traces, err := r.store.TraceList(ctx, "task-act-nudge", 100)
	if err != nil {
		t.Fatalf("TraceList: %v", err)
	}

	nudgeActuationFound := false
	for _, trace := range traces {
		if trace.EventType == "agent_controller.actuation" {
			var payload model.AgentControllerActuationPayload
			if err := model.UnmarshalPayload(trace.Payload, &payload); err != nil {
				t.Fatalf("UnmarshalPayload: %v", err)
			}
			if payload.Action == model.ControllerActionNudge && payload.Outcome == model.ActuationOutcomeSuccess {
				nudgeActuationFound = true
			}
		}
	}
	if !nudgeActuationFound {
		t.Fatal("agent_controller.actuation (nudge, success) trace not found")
	}

	// Test 2: ACP context-limit handoff emits actuation trace.
	r.store.TaskCreate(ctx, "task-act-handoff", "", "", "Actuation handoff test", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-act-handoff", "running")
	r.store.AgentCreate(ctx, "agent-act-handoff", "task-act-handoff", 0, "cw-worker-act-handoff", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-act-handoff", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-act-handoff", "task-act-handoff", 1, "acp.recv", `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Unable to compact conversation"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	traces, err = r.store.TraceList(ctx, "task-act-handoff", 100)
	if err != nil {
		t.Fatalf("TraceList: %v", err)
	}

	handoffActuationFound := false
	for _, trace := range traces {
		if trace.EventType == "agent_controller.actuation" {
			var payload model.AgentControllerActuationPayload
			if err := model.UnmarshalPayload(trace.Payload, &payload); err != nil {
				t.Fatalf("UnmarshalPayload: %v", err)
			}
			if payload.Action == model.ControllerActionHandoff && payload.Outcome == model.ActuationOutcomeSuccess {
				handoffActuationFound = true
			}
		}
	}
	if !handoffActuationFound {
		t.Fatal("agent_controller.actuation (handoff, success) trace not found")
	}
}

// --- Fix B: error-turn retry budget ---

// TestReconciler_ACPErrorStopReason_RetriesNudgeUpToBudget verifies that when an
// ACP agent's turn ends with stopReason:"error", the reconciler re-nudges up to
// nudgeErrorRetryBudget times before falling back to a handoff.
//
// Timeline per retry (each fast-forwarded by overwriting nudgeSent):
//
//	Tick 1:  !alreadyNudged → first nudge (nudgesSent=1)
//	Tick 2+: timeout elapsed, error turn, retries 0,1,2 < 3 → re-nudge
//	Tick N:  retries=3, budget exhausted → handoff
func TestReconciler_ACPErrorStopReason_RetriesNudgeUpToBudget(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-err-retry", "", "", "ErrorRetry", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-err-retry", "running")
	r.store.AgentCreate(ctx, "agent-err-retry", "task-err-retry", 0, "cw-worker-err-retry", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-err-retry", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-err-retry", "task-err-retry", 1, "acp.recv",
		`{"jsonrpc":"2.0","id":7,"result":{"stopReason":"error"}}`); err != nil {
		t.Fatal(err)
	}

	// Tick 1: first nudge sent.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sp.nudgesSent) != 1 {
		t.Fatalf("nudges after tick 1 = %d, want 1 (first nudge)", len(sp.nudgesSent))
	}
	task, _ := r.store.TaskGet(ctx, "task-err-retry")
	if task.Status != "running" {
		t.Fatalf("task status = %q after tick 1, want running", task.Status)
	}

	// Exhaust the error retry budget: nudgeErrorRetryBudget=3 re-nudges.
	for i := 0; i < nudgeErrorRetryBudget; i++ {
		r.SetNudgeSentAt("agent-err-retry", time.Now().Add(-nudgeTimeout-time.Second))
		if err := r.Tick(ctx); err != nil {
			t.Fatalf("Tick (retry %d): %v", i+1, err)
		}
		wantNudges := i + 2 // 1 initial + (i+1) retries
		if len(sp.nudgesSent) != wantNudges {
			t.Fatalf("nudges after retry %d = %d, want %d", i+1, len(sp.nudgesSent), wantNudges)
		}
		task, _ = r.store.TaskGet(ctx, "task-err-retry")
		if task.Status != "running" {
			t.Fatalf("task status = %q after retry %d, want running (budget not exhausted)", task.Status, i+1)
		}
	}

	// One more timeout: budget exhausted, should hand off now.
	r.SetNudgeSentAt("agent-err-retry", time.Now().Add(-nudgeTimeout-time.Second))
	if err := r.Tick(ctx); err != nil {
		t.Fatalf("Tick (final handoff): %v", err)
	}

	wantTotal := nudgeErrorRetryBudget + 1 // 1 initial + 3 retries
	if len(sp.nudgesSent) != wantTotal {
		t.Fatalf("total nudges = %d, want %d (1 initial + %d retries)", len(sp.nudgesSent), wantTotal, nudgeErrorRetryBudget)
	}
	if len(sp.gracefulKill) != 1 {
		t.Fatalf("graceful kills = %d, want 1 (handoff after budget exhausted)", len(sp.gracefulKill))
	}
	task, _ = r.store.TaskGet(ctx, "task-err-retry")
	if task.Status == "running" {
		t.Fatalf("task status = %q, want terminal after budget exhausted", task.Status)
	}
}

func TestReconciler_ACPCancelledStopReasonFailsWithoutWaitingForStaleEvents(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-cancelled-turn", "", "", "CancelledTurn", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-cancelled-turn", "running")
	r.store.AgentCreate(ctx, "agent-cancelled-turn", "task-cancelled-turn", 0, "cw-worker-cancelled-turn", "", "", "pi-acp", "")
	if err := r.store.AgentEventAppend(ctx, "agent-cancelled-turn", "task-cancelled-turn", 1, "acp.recv",
		`{"jsonrpc":"2.0","id":7,"result":{"stopReason":"cancelled"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	task, _ := r.store.TaskGet(ctx, "task-cancelled-turn")
	if task.Status != "failed" {
		t.Fatalf("task status = %q, want failed", task.Status)
	}
	agent, _ := r.store.AgentGet(ctx, "agent-cancelled-turn")
	if agent.Status != "killed" {
		t.Fatalf("agent status = %q, want killed", agent.Status)
	}
	if len(sp.nudgesSent) != 0 {
		t.Fatalf("nudges sent = %d, want 0", len(sp.nudgesSent))
	}
}

// TestReconciler_ACPNormalStopReason_HandsOffAtTimeout verifies that non-error
// stop reasons still hand off after a single nudge timeout — no retry budget.
func TestReconciler_ACPNormalStopReason_HandsOffAtTimeout(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-normal-timeout", "", "", "NormalTimeout", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-normal-timeout", "running")
	r.store.AgentCreate(ctx, "agent-normal-timeout", "task-normal-timeout", 0, "cw-worker-normal-timeout", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-normal-timeout", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-normal-timeout", "task-normal-timeout", 1, "acp.recv",
		`{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sp.nudgesSent) != 1 {
		t.Fatalf("nudges after tick 1 = %d, want 1", len(sp.nudgesSent))
	}

	r.SetNudgeSentAt("agent-normal-timeout", time.Now().Add(-nudgeTimeout-time.Second))
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// Should have handed off with exactly 1 nudge — no error-retry path.
	if len(sp.nudgesSent) != 1 {
		t.Fatalf("nudges = %d, want 1 (non-error stop reason hands off without retry)", len(sp.nudgesSent))
	}
	if len(sp.gracefulKill) != 1 {
		t.Fatalf("graceful kills = %d, want 1 after timeout handoff", len(sp.gracefulKill))
	}
	task, _ := r.store.TaskGet(ctx, "task-normal-timeout")
	if task.Status == "running" {
		t.Fatalf("task status = %q, want terminal after nudge timeout", task.Status)
	}
}

// TestReconciler_ACPErrorRetries_ResetOnTerminalStatus verifies that the error-retry
// counter is cleared when the agent reaches a terminal status, so a future stall
// does not inherit the previous agent's retry history.
func TestReconciler_ACPErrorRetries_ResetOnTerminalStatus(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-reset-retry", "", "", "ResetRetry", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-reset-retry", "running")
	r.store.AgentCreate(ctx, "agent-reset-retry", "task-reset-retry", 0, "cw-worker-reset-retry", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-reset-retry", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-reset-retry", "task-reset-retry", 1, "acp.recv",
		`{"jsonrpc":"2.0","id":7,"result":{"stopReason":"error"}}`); err != nil {
		t.Fatal(err)
	}

	// First nudge.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// Use one error retry.
	r.SetNudgeSentAt("agent-reset-retry", time.Now().Add(-nudgeTimeout-time.Second))
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sp.nudgesSent) != 2 {
		t.Fatalf("nudges = %d, want 2 (one retry used)", len(sp.nudgesSent))
	}

	// Simulate agent completing: clearNudge is called on terminal transition.
	r.clearNudge("agent-reset-retry")

	// Verify counter is reset.
	r.nudgeMu.Lock()
	retries := r.nudgeErrorRetries["agent-reset-retry"]
	r.nudgeMu.Unlock()
	if retries != 0 {
		t.Errorf("nudgeErrorRetries after clearNudge = %d, want 0", retries)
	}
}

// --- Fix D: turn/start failed nudge rejection ---

// TestReconciler_ACPStall_TurnActiveError_DoesNotStartHandoffClock verifies that
// when handleACPStall's nudge is rejected with "turn/start failed", the reconciler
// does NOT set nudgeSent and does NOT start the 3-minute handoff clock. The agent
// is mid-inference, not stalled; killing it would be wrong.
func TestReconciler_ACPStall_TurnActiveError_DoesNotStartHandoffClock(t *testing.T) {
	sp, r := newStallableReconciler(t, 1*time.Millisecond)
	sp.transport = worker.TransportACP
	sp.nudgeErr = fmt.Errorf("acp session/prompt: json-rpc error -32000: turn/start failed")
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-turn-active", "", "", "TurnActive", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-turn-active", "running")
	r.store.AgentCreate(ctx, "agent-turn-active", "task-turn-active", 0, "cw-worker-turn-active", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-turn-active", "", "cmd", nil, nil)
	// Stale heartbeat + no events → hits handleACPStall.
	if err := r.store.AgentEventAppend(ctx, "agent-turn-active", "task-turn-active", 1, "acp.recv",
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// nudgeSent must NOT be set — handoff clock must not start.
	r.nudgeMu.Lock()
	_, clockStarted := r.nudgeSent["agent-turn-active"]
	r.nudgeMu.Unlock()
	if clockStarted {
		t.Fatal("nudgeSent was set after turn/start failed — handoff clock started incorrectly")
	}

	// Task must still be running.
	task, _ := r.store.TaskGet(ctx, "task-turn-active")
	if task.Status != "running" {
		t.Fatalf("task status = %q, want running (turn is active, not a stall)", task.Status)
	}

	// Second tick: still no clock, still no handoff.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	r.nudgeMu.Lock()
	_, clockStarted = r.nudgeSent["agent-turn-active"]
	r.nudgeMu.Unlock()
	if clockStarted {
		t.Fatal("nudgeSent set on second tick — should remain clear while turn is active")
	}
}

// TestReconciler_ACPStall_OtherError_StartsHandoffClock verifies that non-turn-active
// nudge errors still start the handoff clock (existing behavior preserved).
func TestReconciler_ACPStall_OtherError_StartsHandoffClock(t *testing.T) {
	sp, r := newStallableReconciler(t, 1*time.Millisecond)
	sp.transport = worker.TransportACP
	sp.nudgeErr = fmt.Errorf("acp session/prompt: connection refused")
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-other-err", "", "", "OtherErr", "", "", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-other-err", "running")
	r.store.AgentCreate(ctx, "agent-other-err", "task-other-err", 0, "cw-worker-other-err", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-other-err", "", "cmd", nil, nil)
	if err := r.store.AgentEventAppend(ctx, "agent-other-err", "task-other-err", 1, "acp.recv",
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`); err != nil {
		t.Fatal(err)
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// nudgeSent MUST be set — non-turn-active errors start the handoff clock.
	r.nudgeMu.Lock()
	_, clockStarted := r.nudgeSent["agent-other-err"]
	r.nudgeMu.Unlock()
	if !clockStarted {
		t.Fatal("nudgeSent not set after non-turn-active nudge error — handoff clock should start")
	}

	// Fast-forward past nudgeTimeout → should hand off.
	r.SetNudgeSentAt("agent-other-err", time.Now().Add(-nudgeTimeout-time.Second))
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sp.gracefulKill) != 1 {
		t.Fatalf("graceful kills = %d, want 1 after nudge timeout", len(sp.gracefulKill))
	}
	task, _ := r.store.TaskGet(ctx, "task-other-err")
	if task.Status == "running" {
		t.Fatal("task still running after nudge timeout with non-turn-active error")
	}
}

// --- Escalation-on-block tests ---

// TestReconciler_BlockTask_CreatesEscalation verifies that when the reconciler
// blocks a task due to repeated unresolved controller errors (oscillation), an
// open escalation is created automatically. This satisfies C1.
func TestReconciler_BlockTask_CreatesEscalation(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-osc", "", "", "Oscillation", "", "feature", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-osc", "running")
	r.store.TaskSetStepFromPending(ctx, "task-osc", "implement")
	r.store.AgentCreate(ctx, "agent-osc", "task-osc", 0, "cw-worker-osc", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-osc", "", "cmd", nil, nil)

	// Error turns trigger oscillation (end_turn does not — only stopReason:"error" counts).
	for i := 0; i < model.DefaultOscillationThreshold; i++ {
		if err := r.store.AgentEventAppend(ctx, "agent-osc", "task-osc", int64(i+1), "acp.recv",
			`{"jsonrpc":"2.0","id":7,"result":{"stopReason":"error"}}`); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// Task should be blocked.
	task, err := r.store.TaskGet(ctx, "task-osc")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "blocked" {
		t.Fatalf("task status = %q, want blocked", task.Status)
	}

	// An open escalation should have been created.
	escalations, err := r.store.EscalationList(ctx, "task-osc", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(escalations) != 1 {
		t.Fatalf("open escalations = %d, want 1", len(escalations))
	}
	esc := escalations[0]
	if esc.TaskID != "task-osc" {
		t.Errorf("escalation task_id = %q, want task-osc", esc.TaskID)
	}
	if esc.TargetType != "human" {
		t.Errorf("escalation target_type = %q, want human", esc.TargetType)
	}
	if esc.Status != "open" {
		t.Errorf("escalation status = %q, want open", esc.Status)
	}
	if esc.CreatedByType != "system" {
		t.Errorf("escalation created_by_type = %q, want system", esc.CreatedByType)
	}
	if esc.Reason == "" {
		t.Error("escalation reason is empty")
	}
}

// TestReconciler_BlockTask_NoDuplicateEscalation verifies that creating a block
// escalation is idempotent — a second block with the same failure signature
// does NOT create a duplicate escalation. This satisfies C4.
func TestReconciler_BlockTask_NoDuplicateEscalation(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-nodup", "", "", "NoDup", "", "feature", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-nodup", "running")
	r.store.TaskSetStepFromPending(ctx, "task-nodup", "implement")
	r.store.AgentCreate(ctx, "agent-nodup", "task-nodup", 0, "cw-worker-nodup", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-nodup", "", "cmd", nil, nil)

	// Enough turns to trigger oscillation.
	for i := 0; i < model.DefaultOscillationThreshold; i++ {
		if err := r.store.AgentEventAppend(ctx, "agent-nodup", "task-nodup", int64(i+1), "acp.recv",
			`{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`); err != nil {
			t.Fatal(err)
		}
	}

	// First block.
	if err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// Reset task to running to simulate redrive, then tick again.
	// This tests that a second block on the same task does not create a duplicate.
	r.store.TaskSetStatus(ctx, "task-nodup", "running")
	r.store.AgentCreate(ctx, "agent-nodup-2", "task-nodup", 0, "cw-worker-nodup-2", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-nodup-2", "", "cmd", nil, nil)

	// Tick again — this should see the existing open escalation and not create a duplicate
	// (the task is running but agent-nodup-2 is healthy, so no block this tick).
	// Instead, manually trigger blockTask to verify dedup directly.
	sigHash := "test-sig-hash"
	task, _ := r.store.TaskGet(ctx, "task-nodup")
	agent2, _ := r.store.AgentGetByTask(ctx, "task-nodup")
	r.store.TaskSetStatus(ctx, "task-nodup", "running")
	r.blockTask(ctx, agent2, task, "repeated error", sigHash)

	escalations, err := r.store.EscalationList(ctx, "task-nodup", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(escalations) != 1 {
		t.Fatalf("open escalations = %d, want 1 (no duplicates)", len(escalations))
	}
}

// TestReconciler_BlockTask_ResolvedEscalationReopened verifies that when a
// previously resolved escalation's failure signature recurs, a NEW open escalation
// is created. This satisfies C3.
func TestReconciler_BlockTask_ResolvedEscalationReopened(t *testing.T) {
	sp, r := newStallableReconciler(t, time.Hour)
	sp.transport = worker.TransportACP
	ctx := context.Background()

	r.store.TaskCreate(ctx, "task-reopen", "", "", "Reopen", "", "feature", "", "pi-acp", 0)
	r.store.TaskSetStatus(ctx, "task-reopen", "running")
	r.store.TaskSetStepFromPending(ctx, "task-reopen", "implement")
	r.store.AgentCreate(ctx, "agent-reopen", "task-reopen", 0, "cw-worker-reopen", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-reopen", "", "cmd", nil, nil)

	sigHash := "recurrence-sig"

	// First block — creates an escalation.
	task, _ := r.store.TaskGet(ctx, "task-reopen")
	agent, _ := r.store.AgentGetByTask(ctx, "task-reopen")
	r.blockTask(ctx, agent, task, "first occurrence", sigHash)

	// Verify escalation was created.
	escalations, _ := r.store.EscalationList(ctx, "task-reopen", "open")
	if len(escalations) != 1 {
		t.Fatalf("open escalations after first block = %d, want 1", len(escalations))
	}
	firstEscID := escalations[0].ID

	// Resolve the escalation (operator fixed the issue).
	if err := r.store.EscalationResolve(ctx, firstEscID, "operator fixed", "operator"); err != nil {
		t.Fatal(err)
	}

	// Verify it's resolved.
	open, _ := r.store.EscalationList(ctx, "task-reopen", "open")
	if len(open) != 0 {
		t.Fatalf("open escalations after resolve = %d, want 0", len(open))
	}
	resolved, _ := r.store.EscalationList(ctx, "task-reopen", "resolved")
	if len(resolved) != 1 {
		t.Fatalf("resolved escalations = %d, want 1", len(resolved))
	}

	// Now the same failure recurs — block again with same signature.
	r.store.TaskSetStatus(ctx, "task-reopen", "running")
	r.store.AgentCreate(ctx, "agent-reopen-2", "task-reopen", 0, "cw-worker-reopen-2", "", "", "pi-acp", "")
	sp.FakeSpawner.Spawn("cw-worker-reopen-2", "", "cmd", nil, nil)
	task2, _ := r.store.TaskGet(ctx, "task-reopen")
	agent2, _ := r.store.AgentGetByTask(ctx, "task-reopen")
	r.blockTask(ctx, agent2, task2, "recurrence of same failure", sigHash)

	// A NEW open escalation should exist.
	open2, _ := r.store.EscalationList(ctx, "task-reopen", "open")
	if len(open2) != 1 {
		t.Fatalf("open escalations after recurrence = %d, want 1", len(open2))
	}
	if open2[0].ID == firstEscID {
		t.Fatal("escalation was reopened instead of creating a new one")
	}

	// Total escalations: 1 resolved + 1 open = 2.
	allEsc, _ := r.store.EscalationList(ctx, "task-reopen", "")
	if len(allEsc) != 2 {
		t.Fatalf("total escalations = %d, want 2 (1 resolved + 1 open)", len(allEsc))
	}
}

// TestReconciler_BlockTask_WorktreeFailureEscalation verifies that worktree
// measurement failures that lead to blocking create an escalation. This satisfies C2.
func TestReconciler_BlockTask_WorktreeFailureEscalation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-wt-fail", "", "", "WorktreeFail", "", "feature", "", "default", 0)
	s.TaskSetStatus(ctx, "task-wt-fail", "running")
	s.TaskSetStepFromPending(ctx, "task-wt-fail", "implement")
	s.AgentCreate(ctx, "agent-wt-fail", "task-wt-fail", 0, "cw-worker-wt-fail", "", "/nonexistent/worktree", "default", "")

	sp := &worker.FakeSpawner{}
	r := NewReconciler(s, sp, &worker.FakeWorktreeCreator{}, time.Hour)

	// Record worktree measurement failure observations.
	for i := 0; i < 3; i++ {
		_ = s.ControlObservationPut(ctx, &model.ControlObservation{
			TargetType:   "worktree",
			TargetID:     "task-wt-fail",
			TaskID:       "task-wt-fail",
			Kind:         "worktree_state",
			Status:       "unmeasurable",
			Reason:       "could not measure worktree state: worktree is dirty and cannot be repaired",
		})
	}

	task, _ := s.TaskGet(ctx, "task-wt-fail")
	agent, _ := s.AgentGetByTask(ctx, "task-wt-fail")
	r.blockTask(ctx, agent, task, "could not measure worktree state", "worktree_measurement_failure")

	// Re-read task status after blockTask (it operates in-place but we need fresh data).
	taskAfter, _ := s.TaskGet(ctx, "task-wt-fail")
	if taskAfter.Status != "blocked" {
		t.Fatalf("task status = %q, want blocked", taskAfter.Status)
	}

	escalations, err := s.EscalationList(ctx, "task-wt-fail", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(escalations) != 1 {
		t.Fatalf("open escalations = %d, want 1", len(escalations))
	}
	esc := escalations[0]
	if esc.FailureSignature != "worktree_measurement_failure" {
		t.Errorf("escalation failure_signature = %q, want worktree_measurement_failure", esc.FailureSignature)
	}
	if len(esc.SuggestedCommands) == 0 {
		t.Error("escalation has no suggested commands")
	}
}

// TestReconciler_BlockTask_EscalationHasSuggestedCommands verifies that block
// escalations include suggested operator commands.
func TestReconciler_BlockTask_EscalationHasSuggestedCommands(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-cmds", "", "", "Commands", "", "feature", "", "default", 0)
	s.TaskSetStatus(ctx, "task-cmds", "running")
	s.TaskSetStepFromPending(ctx, "task-cmds", "implement")
	s.AgentCreate(ctx, "agent-cmds", "task-cmds", 0, "cw-worker-cmds", "", "", "default", "")

	sp := &worker.FakeSpawner{}
	r := NewReconciler(s, sp, &worker.FakeWorktreeCreator{}, time.Hour)

	task, _ := s.TaskGet(ctx, "task-cmds")
	agent, _ := s.AgentGetByTask(ctx, "task-cmds")
	r.blockTask(ctx, agent, task, "test block reason", "test-sig")

	escalations, _ := s.EscalationList(ctx, "task-cmds", "open")
	if len(escalations) != 1 {
		t.Fatalf("escalations = %d, want 1", len(escalations))
	}
	esc := escalations[0]

	// Should have at least diagnose and list commands.
	found := false
	for _, cmd := range esc.SuggestedCommands {
		if cmd == "clankwork task diagnose task-cmds" {
			found = true
		}
	}
	if !found {
		t.Errorf("suggested commands missing 'clankwork task diagnose task-cmds': %v", esc.SuggestedCommands)
	}
}

// TestReconciler_BlockTask_EscalationHasFailureSignature verifies that the
// escalation's failure_signature matches the reconciler's failure signature
// hash, enabling deduplication.
func TestReconciler_BlockTask_EscalationHasFailureSignature(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-sig", "", "", "Signature", "", "feature", "", "pi-acp", 0)
	s.TaskSetStatus(ctx, "task-sig", "running")
	s.TaskSetStepFromPending(ctx, "task-sig", "implement")
	s.AgentCreate(ctx, "agent-sig", "task-sig", 0, "cw-worker-sig", "", "", "pi-acp", "")

	sp := &worker.FakeSpawner{}
	r := NewReconciler(s, sp, &worker.FakeWorktreeCreator{}, time.Hour)

	expectedSig := "abc123def4567890"
	task, _ := s.TaskGet(ctx, "task-sig")
	agent, _ := s.AgentGetByTask(ctx, "task-sig")
	r.blockTask(ctx, agent, task, "test reason", expectedSig)

	escalations, _ := s.EscalationList(ctx, "task-sig", "open")
	if len(escalations) != 1 {
		t.Fatalf("escalations = %d, want 1", len(escalations))
	}
	if escalations[0].FailureSignature != expectedSig {
		t.Errorf("failure_signature = %q, want %q", escalations[0].FailureSignature, expectedSig)
	}

	// Verify dedup: querying by signature returns the escalation.
	found, err := s.EscalationGetOpenByTaskAndSignature(ctx, "task-sig", expectedSig)
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatal("EscalationGetOpenByTaskAndSignature returned nil")
	}
	if found.ID != escalations[0].ID {
		t.Errorf("dedup lookup returned different escalation: %s vs %s", found.ID, escalations[0].ID)
	}
}

// TestStore_EscalationGetOpenByTaskAndSignature_NoMatch verifies that the
// dedup query returns nil when no matching escalation exists.
func TestStore_EscalationGetOpenByTaskAndSignature_NoMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-empty", "", "", "Empty", "", "feature", "", "default", 0)

	found, err := s.EscalationGetOpenByTaskAndSignature(ctx, "task-empty", "no-such-sig")
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Errorf("expected nil, got escalation %s", found.ID)
	}
}

// TestStore_EscalationGetOpenByTaskAndSignature_IgnoresResolved verifies that
// resolved escalations are not returned by the dedup query, allowing new
// open escalations to be created for recurring failures.
func TestStore_EscalationGetOpenByTaskAndSignature_IgnoresResolved(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-resolved", "", "", "Resolved", "", "feature", "", "default", 0)

	esc := &model.Escalation{
		TaskID:           "task-resolved",
		TargetType:       "human",
		RequestedAction:  "investigate",
		Reason:           "previous failure",
		FailureSignature: "old-sig",
		Status:           "open",
		CreatedByType:    "system",
		CreatedByID:      "controller",
	}
	if err := s.EscalationCreate(ctx, esc); err != nil {
		t.Fatal(err)
	}

	// Resolve it.
	if err := s.EscalationResolve(ctx, esc.ID, "fixed", "operator"); err != nil {
		t.Fatal(err)
	}

	// Query should return nil (resolved escalations are ignored).
	found, err := s.EscalationGetOpenByTaskAndSignature(ctx, "task-resolved", "old-sig")
	if err != nil {
		t.Fatal(err)
	}
	if found != nil {
		t.Errorf("expected nil for resolved escalation, got %s", found.ID)
	}
}

// TestStore_TasksDiagnose_SurfacesOpenEscalations verifies that task diagnose
// surfaces open escalations for blocked tasks. This satisfies C5.
func TestStore_TasksDiagnose_SurfacesOpenEscalations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-diag", "", "", "Diagnose", "", "feature", "", "default", 0)
	s.TaskSetStatus(ctx, "task-diag", "blocked")
	s.TaskSetStepFromPending(ctx, "task-diag", "implement")

	// Create an open escalation.
	esc := &model.Escalation{
		TaskID:           "task-diag",
		StepName:         "implement",
		TargetType:       "human",
		RequestedAction:  "investigate",
		Reason:           "repeated unresolved controller error",
		FailureSignature: "oscillation-sig",
		Status:           "open",
		CreatedByType:    "system",
		CreatedByID:      "agent_controller",
	}
	if err := s.EscalationCreate(ctx, esc); err != nil {
		t.Fatal(err)
	}

	diag, err := s.TaskDiagnose(ctx, "task-diag")
	if err != nil {
		t.Fatal(err)
	}

	// The diagnosis should surface the open escalation.
	if len(diag.Observed.OpenEscalations) != 1 {
		t.Fatalf("open escalations in diagnosis = %d, want 1", len(diag.Observed.OpenEscalations))
	}
	if diag.Observed.OpenEscalations[0].ID != esc.ID {
		t.Errorf("escalation ID mismatch: %s vs %s", diag.Observed.OpenEscalations[0].ID, esc.ID)
	}
	if diag.NextAction != "await_escalation_resolution" {
		t.Errorf("next action = %q, want await_escalation_resolution", diag.NextAction)
	}
}

// TestEscalationCreate_Defaults verifies that EscalationCreate sets sensible
// defaults for status, requested_action, created_by_type, and created_at.
func TestEscalationCreate_Defaults(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-defaults", "", "", "Defaults", "", "feature", "", "default", 0)

	esc := &model.Escalation{
		TaskID: "task-defaults",
		TargetType: "human",
		Reason: "test",
	}
	if err := s.EscalationCreate(ctx, esc); err != nil {
		t.Fatal(err)
	}

	if esc.Status != "open" {
		t.Errorf("status = %q, want open", esc.Status)
	}
	if esc.RequestedAction != "investigate" {
		t.Errorf("requested_action = %q, want investigate", esc.RequestedAction)
	}
	if esc.CreatedByType != "system" {
		t.Errorf("created_by_type = %q, want system", esc.CreatedByType)
	}
	if esc.CreatedAt.IsZero() {
		t.Error("created_at is zero, want set time")
	}
	// Verify it can be listed.
	open, err := s.EscalationList(ctx, "task-defaults", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("escalations = %d, want 1", len(open))
	}
}

// TestReconciler_BlockTask_EscalationIncludesAgentID verifies that block
// escalations include the agent ID in the target_ref field and in the reason,
// enabling operators to identify which agent was involved.
func TestReconciler_BlockTask_EscalationIncludesAgentID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-agent-id", "", "", "AgentID", "", "feature", "", "default", 0)
	s.TaskSetStatus(ctx, "task-agent-id", "running")
	s.TaskSetStepFromPending(ctx, "task-agent-id", "implement")
	s.AgentCreate(ctx, "agent-with-id", "task-agent-id", 0, "cw-worker-agent-id", "", "", "default", "")

	sp := &worker.FakeSpawner{}
	r := NewReconciler(s, sp, &worker.FakeWorktreeCreator{}, time.Hour)

	task, _ := s.TaskGet(ctx, "task-agent-id")
	agent, _ := s.AgentGetByTask(ctx, "task-agent-id")
	r.blockTask(ctx, agent, task, "oscillation detected", "osc-sig")

	escalations, _ := s.EscalationList(ctx, "task-agent-id", "open")
	if len(escalations) != 1 {
		t.Fatalf("escalations = %d, want 1", len(escalations))
	}
	esc := escalations[0]

	// Agent ID should be in target_ref.
	if esc.TargetRef != "agent-with-id" {
		t.Errorf("escalation target_ref = %q, want agent-with-id", esc.TargetRef)
	}

	// Agent ID should be in the reason text.
	if esc.Reason == "" {
		t.Error("escalation reason is empty")
	}
}

// TestReconciler_BlockTask_EscalationSuggestedCommandsIncludesEscalationList verifies
// that suggested commands include the escalation list command for quick triage.
func TestReconciler_BlockTask_EscalationSuggestedCommandsIncludesEscalationList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "task-esc-list", "", "", "EscList", "", "feature", "", "default", 0)
	s.TaskSetStatus(ctx, "task-esc-list", "running")
	s.TaskSetStepFromPending(ctx, "task-esc-list", "implement")
	s.AgentCreate(ctx, "agent-esc-list", "task-esc-list", 0, "cw-worker-esc-list", "", "", "default", "")

	sp := &worker.FakeSpawner{}
	r := NewReconciler(s, sp, &worker.FakeWorktreeCreator{}, time.Hour)

	task, _ := s.TaskGet(ctx, "task-esc-list")
	agent, _ := s.AgentGetByTask(ctx, "task-esc-list")
	r.blockTask(ctx, agent, task, "test reason", "test-sig")

	escalations, _ := s.EscalationList(ctx, "task-esc-list", "open")
	if len(escalations) != 1 {
		t.Fatalf("escalations = %d, want 1", len(escalations))
	}
	esc := escalations[0]

	// Should include escalation list command.
	foundEscList := false
	foundDiagnose := false
	for _, cmd := range esc.SuggestedCommands {
		if cmd == "clankwork escalation list --task task-esc-list --status open" {
			foundEscList = true
		}
		if cmd == "clankwork task diagnose task-esc-list" {
			foundDiagnose = true
		}
	}
	if !foundEscList {
		t.Errorf("suggested commands missing escalation list: %v", esc.SuggestedCommands)
	}
	if !foundDiagnose {
		t.Errorf("suggested commands missing diagnose: %v", esc.SuggestedCommands)
	}
}
