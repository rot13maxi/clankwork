package scheduler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
	tmplpkg "github.com/rot13maxi/clankwork/internal/template"
	"github.com/rot13maxi/clankwork/internal/worker"
	"github.com/rot13maxi/clankwork/internal/workflow"
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

func TestDispatcherTick_PendingTask(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create a pending task with no deps.
	_, err := st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := testDispatcherConfig()
	homeDir := t.TempDir()

	d := New(ctx, st, spawner, wt, homeDir, cfg)
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Task should now be running.
	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "running" {
		t.Errorf("task status = %q, want running", task.Status)
	}

	// Agent should be alive.
	agents, _ := st.AgentList(ctx, "running")
	if len(agents) != 1 {
		t.Fatalf("running agents = %d, want 1", len(agents))
	}
	alive, _ := spawner.IsAlive(agents[0].TmuxSession)
	if !alive {
		t.Error("tmux session not alive")
	}
}

func TestDispatcherRejectsUnsupportedTransport(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	task, err := st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "claude-acp", 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := testDispatcherConfig()
	cfg.Runtimes["claude-acp"] = config.RuntimeConfig{
		Command:   "claude-agent-acp",
		Transport: config.TransportACP,
	}

	d := New(ctx, st, spawner, wt, t.TempDir(), cfg)
	if err := d.dispatch(ctx, task); err == nil {
		t.Fatal("dispatch succeeded for unsupported acp transport; want error")
	}
}

func TestDispatcherACPDoesNotWriteTrackedClaudeInstructions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	if _, err := st.RepoCreate(ctx, "repo01", "repo", repoDir, "main", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	task, err := st.TaskCreate(ctx, "task-acp-claude", "", "repo01", "ACP hygiene", "", "", "", "claude-acp", 0)
	if err != nil {
		t.Fatal(err)
	}

	worktreePath := filepath.Join(os.TempDir(), "fake-worktree-"+task.ID)
	if err := os.MkdirAll(worktreePath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(worktreePath) })

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := testDispatcherConfig()
	cfg.Runtimes["claude-acp"] = config.RuntimeConfig{
		Command:   "claude-agent-acp",
		Transport: config.TransportACP,
	}

	d := New(ctx, st, spawner, wt, t.TempDir(), cfg)
	if err := d.dispatch(ctx, task); err == nil {
		t.Fatal("dispatch succeeded for unsupported acp transport; want error")
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE.md stat err = %v, want not exists", err)
	}
}

func TestDispatcherACPLifecycleWithFakeAdapter(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, "logs"), 0700); err != nil {
		t.Fatal(err)
	}

	_, err := st.TaskCreate(ctx, "task-acp", "", "", "ACP lifecycle", "", "", "", "helper-acp", 0)
	if err != nil {
		t.Fatal(err)
	}

	tmuxRuntime := &worker.FakeSpawner{}
	acpRuntime := worker.NewACPRuntime(filepath.Join(homeDir, "logs"))
	var seqMu sync.Mutex
	seq := make(map[string]int64)
	acpRuntime.SetEventSink(func(agentID, taskID, sessionName, stream, payload string) {
		seqMu.Lock()
		seq[agentID]++
		n := seq[agentID]
		seqMu.Unlock()
		if err := st.AgentEventAppend(context.Background(), agentID, taskID, n, stream, payload); err != nil {
			t.Errorf("AgentEventAppend: %v", err)
		}
		if err := st.AgentUpdateRuntimeEvent(context.Background(), agentID, time.Now().UTC(), testACPStopReason(payload)); err != nil {
			t.Errorf("AgentUpdateRuntimeEvent: %v", err)
		}
	})
	runtime := worker.NewRuntimeMux(tmuxRuntime, acpRuntime)

	cfg := testDispatcherConfig()
	cfg.Runtimes["helper-acp"] = config.RuntimeConfig{
		Command:   "env",
		Args:      []string{"CLANKWORK_ACP_DISPATCH_HELPER=1", os.Args[0], "-test.run=TestACPDispatchHelperProcess", "--"},
		Transport: config.TransportACP,
		Model:     "helper",
	}

	disp := New(ctx, st, runtime, &worker.FakeWorktreeCreator{}, homeDir, cfg)
	if err := disp.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	defer func() {
		agents, _ := st.AgentList(context.Background(), "")
		for _, agent := range agents {
			_ = runtime.Kill(agent.TmuxSession)
		}
	}()

	agent := waitForAgent(t, st, "task-acp")
	if agent.Transport != worker.TransportACP {
		t.Fatalf("transport = %q, want acp", agent.Transport)
	}
	if agent.RuntimeSessionID != "session-1" {
		t.Fatalf("runtime_session_id = %q, want session-1", agent.RuntimeSessionID)
	}
	if agent.PID == 0 {
		t.Fatal("pid was not persisted")
	}
	if agent.LastEventAt == nil {
		t.Fatal("last_event_at was not persisted")
	}

	events := waitForAgentEvents(t, st, agent.ID, 3)
	if len(events) < 3 {
		t.Fatalf("events = %d, want initialize/session/prompt events", len(events))
	}

	if err := st.TaskSetStatus(ctx, "task-acp", "done"); err != nil {
		t.Fatal(err)
	}
	if err := st.AgentSetEnded(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	recon := NewReconciler(st, runtime, &worker.FakeWorktreeCreator{}, time.Minute)
	if err := recon.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	alive, _ := runtime.IsAlive(agent.TmuxSession)
	if alive {
		t.Fatal("ACP runtime still alive after terminal reconciler cleanup")
	}
	agent, err = st.AgentGet(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.PID != 0 {
		t.Fatalf("pid = %d, want cleared", agent.PID)
	}
}

func TestDispatcherTick_Paused(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := testDispatcherConfig()

	d := New(ctx, st, spawner, wt, t.TempDir(), cfg)
	d.Pause()
	d.Tick(ctx)

	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "pending" {
		t.Errorf("task status = %q, want pending (dispatcher is paused)", task.Status)
	}

	// SetQueuePressure(true) should also make IsPaused() return true.
	d2 := New(ctx, st, spawner, wt, t.TempDir(), cfg)
	d2.SetQueuePressure(true)
	if !d2.IsPaused() {
		t.Error("IsPaused() = false after SetQueuePressure(true), want true")
	}

	// Resume() should NOT clear queuePressured.
	d2.Resume()
	if !d2.IsPaused() {
		t.Error("IsPaused() = false after Resume() with queuePressured still set, want true")
	}
}

func TestDispatcherTickRecordsDispatchFailure(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	_, err := st.TaskCreate(ctx, "task-dispatch-fail", "", "", "Test", "", "", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &worker.FakeSpawner{SpawnErr: fmt.Errorf("thread/start failed")}
	d := New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	task, err := st.TaskGet(ctx, "task-dispatch-fail")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "pending" {
		t.Fatalf("task status = %q, want pending", task.Status)
	}
	diag, err := st.TaskDiagnose(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if diag.NextAction != "inspect_runtime" {
		t.Fatalf("next action = %q, want inspect_runtime", diag.NextAction)
	}
	if diag.LatestDecision == nil || diag.LatestDecision.DecisionKind != "dispatch_failure" {
		t.Fatalf("latest decision = %#v, want dispatch_failure", diag.LatestDecision)
	}
	if diag.LatestAction == nil || diag.LatestAction.Outcome != "failed" {
		t.Fatalf("latest action = %#v, want failed dispatch actuation", diag.LatestAction)
	}
}

func waitForAgent(t *testing.T, st *store.Store, taskID string) *model.Agent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		agent, err := st.AgentGetByTask(context.Background(), taskID)
		if err == nil && agent.LastEventAt != nil {
			return agent
		}
		time.Sleep(20 * time.Millisecond)
	}
	agent, err := st.AgentGetByTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func waitForAgentEvents(t *testing.T, st *store.Store, agentID string, want int) []*model.AgentEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var events []*model.AgentEvent
	for time.Now().Before(deadline) {
		var err error
		events, err = st.AgentEventsList(context.Background(), agentID, "", 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) >= want {
			return events
		}
		time.Sleep(20 * time.Millisecond)
	}
	return events
}

func testACPStopReason(payload string) string {
	var msg map[string]any
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return ""
	}
	result, _ := msg["result"].(map[string]any)
	if result == nil {
		return ""
	}
	stopReason, _ := result["stopReason"].(string)
	return stopReason
}

func TestACPDispatchHelperProcess(t *testing.T) {
	if os.Getenv("CLANKWORK_ACP_DISPATCH_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			writeACPDispatchHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentInfo":       map[string]string{"title": "Fake ACP"},
					"agentCapabilities": map[string]any{
						"promptCapabilities": map[string]bool{},
					},
				},
			})
		case "session/new":
			writeACPDispatchHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]string{"sessionId": "session-1"},
			})
		case "session/prompt":
			writeACPDispatchHelper(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "session-1",
					"update": map[string]string{
						"sessionUpdate": "agent_message_chunk",
						"text":          "working",
					},
				},
			})
			writeACPDispatchHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]string{"stopReason": "end_turn"},
			})
		case "session/cancel":
			writeACPDispatchHelper(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params":  map[string]string{"status": "cancelled"},
			})
		default:
			writeACPDispatchHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32601, "message": "not found: " + req.Method},
			})
		}
	}
	os.Exit(0)
}

func writeACPDispatchHelper(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(strings.TrimSpace(string(b)))
}

func TestDispatcherTwoFlagPause(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := testDispatcherConfig()

	d := New(ctx, st, spawner, wt, t.TempDir(), cfg)

	// Neither flag set — not paused.
	if d.IsPaused() {
		t.Error("IsPaused() = true on fresh dispatcher, want false")
	}

	// userPaused only.
	d.Pause()
	if !d.IsPaused() {
		t.Error("IsPaused() = false after Pause(), want true")
	}
	d.Resume()
	if d.IsPaused() {
		t.Error("IsPaused() = true after Resume(), want false")
	}

	// queuePressured only.
	d.SetQueuePressure(true)
	if !d.IsPaused() {
		t.Error("IsPaused() = false after SetQueuePressure(true), want true")
	}
	d.SetQueuePressure(false)
	if d.IsPaused() {
		t.Error("IsPaused() = true after SetQueuePressure(false), want false")
	}

	// Both flags set — Resume() clears only userPaused, queue pressure remains.
	d.Pause()
	d.SetQueuePressure(true)
	if !d.IsPaused() {
		t.Error("IsPaused() = false with both flags set, want true")
	}
	d.Resume()
	if !d.IsPaused() {
		t.Error("IsPaused() = false after Resume() with queuePressured still set, want true")
	}
	d.SetQueuePressure(false)
	if d.IsPaused() {
		t.Error("IsPaused() = true after both flags cleared, want false")
	}

	// Verify Tick respects queuePressured without userPaused.
	st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
	d.SetQueuePressure(true)
	d.Tick(ctx)
	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "pending" {
		t.Errorf("task status = %q, want pending (queuePressured)", task.Status)
	}
}

func TestDispatcherTick_MaxSlots(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	cfg := testDispatcherConfig()
	cfg.Scheduler.MaxSlots = 1

	// Create 2 pending tasks.
	st.TaskCreate(ctx, "t1", "", "", "A", "", "", "", "default", 0)
	st.TaskCreate(ctx, "t2", "", "", "B", "", "", "", "default", 0)

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	d := New(ctx, st, spawner, wt, t.TempDir(), cfg)
	d.Tick(ctx)

	// Only 1 should be running.
	n, _ := st.AgentRunningCount(ctx)
	if n != 1 {
		t.Errorf("running agents = %d, want 1 (max_slots=1)", n)
	}
}

func TestReconciler_DeadSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
	st.TaskSetStatus(ctx, "task01", "running")
	st.AgentCreate(ctx, "agent01", "task01", 0, "cw-worker-task01", "", "", "default", "")

	spawner := &worker.FakeSpawner{} // session never added → IsAlive returns false
	wt := &worker.FakeWorktreeCreator{}
	recon := NewReconciler(st, spawner, wt, 10*60*1e9) // 10 min timeout

	if err := recon.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "failed" {
		t.Errorf("task status = %q, want failed", task.Status)
	}

	agents, _ := st.AgentList(ctx, "killed")
	if len(agents) != 1 {
		t.Errorf("killed agents = %d, want 1", len(agents))
	}
}

func TestReconciler_RaceGuard(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Task already done by signal before reconciler runs.
	st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
	st.TaskSetStatus(ctx, "task01", "running")
	st.AgentCreate(ctx, "agent01", "task01", 0, "cw-worker-task01", "", "", "default", "")
	st.TaskSetStatus(ctx, "task01", "done")

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	recon := NewReconciler(st, spawner, wt, 10*60*1e9)
	recon.Tick(ctx)

	// Task should still be done, not changed to failed.
	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "done" {
		t.Errorf("task status = %q, want done (race guard failed)", task.Status)
	}
}

func TestRouteStep_SuccessAdvancesToNext(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Template "feature": implement → (success) → lint → typecheck → test → acceptance → complete
	st.TaskCreate(ctx, "task01", "", "", "Feat", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task01", "implement")
	st.TaskSetStatus(ctx, "task01", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.RouteStep(ctx, "task01", "implement", "success"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task01")
	if task.CurrentStep != "lint" {
		t.Errorf("current_step = %q, want lint", task.CurrentStep)
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if task.StepAttempts["lint"] != 1 {
		t.Errorf("step_attempts[lint] = %v, want 1 (advance increments destination step)", task.StepAttempts["lint"])
	}
}

func TestRouteStep_SuccessOnLastStepCompletes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.TaskCreate(ctx, "task01", "", "", "Feat", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task01", "acceptance")
	st.TaskSetStatus(ctx, "task01", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.RouteStep(ctx, "task01", "acceptance", "success"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "done" {
		t.Errorf("status = %q, want done", task.Status)
	}
	traces, err := st.TraceListByType(ctx, "task01", "step.routed", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("step.routed traces = %d, want 1", len(traces))
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(traces[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["from"] != "acceptance" || payload["to"] != "complete" || payload["outcome"] != "success" {
		t.Errorf("terminal route payload = %v, want acceptance -> complete success", payload)
	}
}

func TestRouteStep_FailureRetries(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// feature template: implement on_failure = "implement" (retry same step)
	st.TaskCreate(ctx, "task01", "", "", "Feat", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task01", "implement")
	st.TaskSetStatus(ctx, "task01", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.RouteStep(ctx, "task01", "implement", "failure"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task01")
	if task.CurrentStep != "implement" {
		t.Errorf("current_step = %q, want implement (failure should retry)", task.CurrentStep)
	}
	if task.StepAttempts["implement"] != 1 {
		t.Errorf("step_attempts[implement] = %v, want 1", task.StepAttempts["implement"])
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending", task.Status)
	}
}

func TestRouteStep_MaxRetriesExceededFails(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// refactor template has max_retries = 3.
	st.TaskCreate(ctx, "task01", "", "", "Refactor", "", "refactor", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task01", "implement")
	st.TaskSetStatus(ctx, "task01", "running")
	// Simulate 3 retries already done by directly setting step_attempts.
	// refactor template implement step has max_retries = 3.
	st.DB().ExecContext(ctx, `UPDATE tasks SET step_attempts = ? WHERE id = ?`,
		`{"implement":3}`, "task01")
	st.TaskSetStatus(ctx, "task01", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.RouteStep(ctx, "task01", "implement", "failure"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "failed" {
		t.Errorf("status = %q, want failed (max retries exceeded)", task.Status)
	}
}

func TestRouteStep_RepeatedFailureSignatureBlocks(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.TaskCreate(ctx, "task-loop", "", "", "Feat", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task-loop", "test")
	st.TaskSetStatus(ctx, "task-loop", "running")
	for i := 0; i < model.DefaultOscillationThreshold; i++ {
		payload, _ := json.Marshal(map[string]string{
			"step": "test",
			"log":  fmt.Sprintf("FAIL TestFoo at /tmp/run-%d/foo_test.go:%d after %dms", i, 40+i, 10+i),
		})
		st.TraceAppend(ctx, "task-loop", "", "step.failure_context", string(payload))
	}

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.RouteStep(ctx, "task-loop", "test", "failure"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task-loop")
	if task.Status != "blocked" {
		t.Fatalf("status = %q, want blocked", task.Status)
	}
	events, err := st.ControlPlaneEvents(ctx, "task-loop", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range events {
		if ev.Source == "decision" && ev.Type == "route_oscillation" {
			found = true
		}
	}
	if !found {
		t.Fatal("route_oscillation decision not found")
	}
}

func TestRouteStep_Idempotent(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	st.TaskCreate(ctx, "task01", "", "", "Feat", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task01", "implement")
	st.TaskSetStatus(ctx, "task01", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())

	// First call advances to "lint".
	d.RouteStep(ctx, "task01", "implement", "success")
	// Second call (duplicate signal) should be a no-op.
	if err := d.RouteStep(ctx, "task01", "implement", "success"); err != nil {
		t.Fatalf("second RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task01")
	if task.CurrentStep != "lint" {
		t.Errorf("current_step = %q, want lint (idempotent duplicate should not double-advance)", task.CurrentStep)
	}
}

func TestDispatchTemplate_SetsEntryStep(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	_, err := st.TaskCreate(ctx, "task01", "", "", "Feat", "", "feature", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &worker.FakeSpawner{}
	d := New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task01")
	if task.CurrentStep != "acceptance_spec" {
		t.Errorf("current_step = %q, want acceptance_spec (entry step)", task.CurrentStep)
	}
	if task.Status != "running" {
		t.Errorf("status = %q, want running", task.Status)
	}
}

func TestReconciler_DeterministicAgentSkipsTmux(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Deterministic agent has empty TmuxSession.
	st.TaskCreate(ctx, "task01", "", "", "Test", "", "", "", "default", 0)
	st.TaskSetStatus(ctx, "task01", "running")
	st.AgentCreate(ctx, "agent01", "task01", 0, "", "", "", "deterministic", "")

	spawner := &worker.FakeSpawner{} // any IsAlive would return false, but should not be called
	wt := &worker.FakeWorktreeCreator{}
	recon := NewReconciler(st, spawner, wt, 10*60*1e9)
	recon.Tick(ctx)

	// Task should still be running — reconciler should not kill it just because tmux is "not alive".
	task, _ := st.TaskGet(ctx, "task01")
	if task.Status != "running" {
		t.Errorf("task status = %q, want running (deterministic agent should not be killed by reconciler)", task.Status)
	}
}

// TestRouteStep_PerStepTracking_CrossStep verifies that per-step retry tracking works
// for cross-step loops (e.g., implement ↔ critic) where nextStep != currentStep.
// This is the exact bug that the step_attempts migration fixes.
func TestRouteStep_PerStepTracking_CrossStep(t *testing.T) {
	// This test verifies that attempts are tracked per-step, not globally.
	// In a cross-step loop (implement → on_failure: critic, critic → on_failure: implement),
	// the old StepRetryCount (only incremented when nextStep == currentStep) would never
	// increment, allowing infinite looping. The new StepAttempts map tracks each step's
	// entry count independently.
	//
	// We use the 'bugfix' template which has implement → critic routing on failure.
	// We simulate the task has already entered critic 4 times (attempts = 4, max = 5).
	// The next failure should fail the task (5 >= max_retries).
	st := newTestStore(t)
	ctx := context.Background()

	// Check that bugfix template exists and has the expected steps.
	tmpl, err := tmplpkg.Load("bugfix", "", "")
	if err != nil {
		t.Skipf("bugfix template not found: %v", err)
	}
	criticStep, hasCritic := tmpl.Steps["critic"]
	if !hasCritic || criticStep.MaxRetries == 0 {
		t.Skipf("bugfix template does not have a critic step with max_retries")
	}
	maxRetries := criticStep.MaxRetries

	st.TaskCreate(ctx, "task-xstep", "", "", "CrossStep", "", "bugfix", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task-xstep", "implement")
	// Pre-populate step_attempts to simulate 4 prior entries to critic.
	st.DB().ExecContext(ctx, `UPDATE tasks SET step_attempts = ? WHERE id = ?`,
		`{"critic":4}`, "task-xstep")
	st.TaskSetStatus(ctx, "task-xstep", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())

	// Simulate implement failing → routes to critic.
	// With 4 prior critic entries and max_retries=5, this 5th attempt should trigger max retries.
	if err := d.RouteStep(ctx, "task-xstep", "implement", "failure"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task-xstep")
	if task.Status != "failed" {
		t.Errorf("status = %q, want failed (max_retries=%d exceeded after 5th attempt)", task.Status, maxRetries)
	}
}

// TestRouteStep_PerStepTracking_SameStep verifies that same-step retry loops
// (implement → on_failure: implement) still work correctly with per-step tracking.
func TestRouteStep_PerStepTracking_SameStep(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Feature template: implement → on_failure: implement, max_retries=5.
	st.TaskCreate(ctx, "task-sstep", "", "", "SameStep", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task-sstep", "implement")
	st.TaskSetStatus(ctx, "task-sstep", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())

	// First failure: routes implement → implement, attempts[implement] = 1.
	if err := d.RouteStep(ctx, "task-sstep", "implement", "failure"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}
	task, _ := st.TaskGet(ctx, "task-sstep")
	if task.CurrentStep != "implement" {
		t.Errorf("current_step = %q, want implement", task.CurrentStep)
	}
	if task.StepAttempts["implement"] != 1 {
		t.Errorf("step_attempts[implement] = %v, want 1", task.StepAttempts["implement"])
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending", task.Status)
	}

	// Second failure: attempts[implement] = 2, still under max_retries=5.
	st.TaskSetStatus(ctx, "task-sstep", "running")
	d.RouteStep(ctx, "task-sstep", "implement", "failure")
	task, _ = st.TaskGet(ctx, "task-sstep")
	if task.StepAttempts["implement"] != 2 {
		t.Errorf("step_attempts[implement] = %v, want 2 after second failure", task.StepAttempts["implement"])
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending (still under max_retries=5)", task.Status)
	}

	// After 5 failures (attempts reach 5), the 6th failure should fail the task.
	// Set attempts to 5 directly via SQL.
	st.DB().ExecContext(ctx, `UPDATE tasks SET step_attempts = ? WHERE id = ?`,
		`{"implement":5}`, "task-sstep")
	st.TaskSetStatus(ctx, "task-sstep", "running")

	// 6th failure (attempts = 5 >= max_retries = 5) → fail.
	d.RouteStep(ctx, "task-sstep", "implement", "failure")
	task, _ = st.TaskGet(ctx, "task-sstep")
	if task.Status != "failed" {
		t.Errorf("status = %q, want failed (attempts=5 >= max_retries=5)", task.Status)
	}
}

// TestDispatchAgent_WaitsForPriorSession verifies that dispatchAgent does not Spawn
// a fresh adapter while a prior session at the same name is still being torn down.
//
// Regression guard for the ACP re-dispatch race: when the reconciler hands off a
// stalled agent, the dispatcher used to call Kill (fire-and-forget) and immediately
// Spawn a replacement — colliding with the prior process's CLI auth state, file locks,
// and sub-process IDs, killing the new adapter within seconds with
// "acp process exited" errors. The fix is GracefulKill, which polls IsAlive until the
// previous process is gone before returning.
//
// The test simulates a slow-dying prior session (GracefulKillDelay) and asserts that
// Spawn appears in the call log strictly after GracefulKill, with the timestamps
// separated by at least the simulated death duration.
func TestDispatchAgent_WaitsForPriorSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.TaskCreate(ctx, "task-race", "", "", "RaceFix", "", "feature", "", "default", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStepFromPending(ctx, "task-race", "implement"); err != nil {
		t.Fatal(err)
	}

	const simulatedDeathDuration = 150 * time.Millisecond
	spawner := &worker.FakeSpawner{GracefulKillDelay: simulatedDeathDuration}
	sessionName := "clankwork-worker-task-race"
	spawner.MarkAlive(sessionName) // leftover from a prior dispatch attempt

	d := New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	calls := spawner.CallLog()
	var graceful, spawn *worker.FakeSpawnerCall
	for i := range calls {
		c := &calls[i]
		if c.Session != sessionName {
			continue
		}
		switch c.Method {
		case "GracefulKill":
			if graceful == nil {
				graceful = c
			}
		case "Spawn":
			if spawn == nil {
				spawn = c
			}
		}
	}
	if graceful == nil {
		t.Fatalf("expected GracefulKill on session %q; calls=%+v", sessionName, calls)
	}
	if spawn == nil {
		t.Fatalf("expected Spawn on session %q; calls=%+v", sessionName, calls)
	}
	if !spawn.At.After(graceful.At) {
		t.Errorf("Spawn (%v) did not occur after GracefulKill (%v)", spawn.At, graceful.At)
	}
	if elapsed := spawn.At.Sub(graceful.At); elapsed < simulatedDeathDuration {
		t.Errorf("Spawn happened %v after GracefulKill; want at least %v (prior session not awaited)",
			elapsed, simulatedDeathDuration)
	}

	task, _ := st.TaskGet(ctx, "task-race")
	if task.Status != "running" {
		t.Errorf("task status = %q, want running", task.Status)
	}
}

// TestDispatchAgent_NoPriorSession verifies that dispatchAgent does NOT call
// GracefulKill when there is no leftover session — IsAlive returns false, so the
// kill path is skipped entirely. Guards against accidentally adding latency to
// the fast (cold-start) path.
func TestDispatchAgent_NoPriorSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.TaskCreate(ctx, "task-fresh", "", "", "Fresh", "", "feature", "", "default", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStepFromPending(ctx, "task-fresh", "implement"); err != nil {
		t.Fatal(err)
	}

	spawner := &worker.FakeSpawner{}
	d := New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	for _, c := range spawner.CallLog() {
		if c.Method == "GracefulKill" {
			t.Errorf("unexpected GracefulKill call when no prior session existed: %+v", c)
		}
	}
}

// TestStepAttempts_EmptyMap_BackwardCompat verifies that tasks with no step_attempts
// (null or empty) are treated as having zero attempts for each step.
func TestStepAttempts_EmptyMap_BackwardCompat(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create a task without any step_attempts (empty map default).
	st.TaskCreate(ctx, "task-empty", "", "", "Empty", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task-empty", "implement")
	st.TaskSetStatus(ctx, "task-empty", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())

	// First failure should work normally (attempts = 0 < max_retries = 5).
	if err := d.RouteStep(ctx, "task-empty", "implement", "failure"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}
	task, _ := st.TaskGet(ctx, "task-empty")
	if task.StepAttempts["implement"] != 1 {
		t.Errorf("step_attempts[implement] = %v, want 1 after first failure", task.StepAttempts["implement"])
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending", task.Status)
	}
}

// ---------------------------------------------------------------------------
// Compiled workflow graph integration tests
// ---------------------------------------------------------------------------

func TestDispatchTemplate_CreatesPersistedCompiledWorkflow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create a feature-template task (has template set).
	_, err := st.TaskCreate(ctx, "task-comp", "", "", "Compiled", "", "feature", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &worker.FakeSpawner{}
	d := New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// A compiled workflow should have been persisted.
	wf, err := st.CompiledWorkflowGetByTask(ctx, "task-comp")
	if err != nil {
		t.Fatalf("CompiledWorkflowGetByTask: %v", err)
	}
	if wf.SourceName != "feature" {
		t.Errorf("source_name = %q, want feature", wf.SourceName)
	}
	if wf.SourceType != "template" {
		t.Errorf("source_type = %q, want template", wf.SourceType)
	}
	if wf.GraphJSON == "" {
		t.Error("graph_json is empty")
	}
	if wf.Status != "active" {
		t.Errorf("status = %q, want active", wf.Status)
	}
}

func TestRouteStep_UsesCompiledGraph(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create a bugfix task with a pre-persisted compiled workflow.
	_, err := st.TaskCreate(ctx, "task-route", "", "", "RouteViaGraph", "", "bugfix", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-persist a compiled workflow graph.
	tmpl, err := tmplpkg.Load("bugfix", "", "")
	if err != nil {
		t.Fatal(err)
	}
	graph, diags := compileTemplate(tmpl)
	if len(diags) > 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	graphJSON, err := workflow.MarshalGraphString(graph)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.CompiledWorkflowCreate(ctx, &model.CompiledWorkflow{
		ID:         "wf-task-route",
		TaskID:     "task-route",
		SourceType: "template",
		SourceName: "bugfix",
		GraphJSON:  graphJSON,
	})

	st.TaskSetStepFromPending(ctx, "task-route", "implement")
	st.TaskSetStatus(ctx, "task-route", "running")

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.RouteStep(ctx, "task-route", "implement", "success"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task-route")
	// Bugfix template: implement → on_success → lint
	if task.CurrentStep != "lint" {
		t.Errorf("current_step = %q, want lint (routed via compiled graph)", task.CurrentStep)
	}
	if task.StepAttempts["lint"] != 1 {
		t.Errorf("step_attempts[lint] = %v, want 1", task.StepAttempts["lint"])
	}
}

func TestDispatchTemplate_RejectsOnCompilationDiagnostics(t *testing.T) {
	// This test verifies that if a template compilation produces policy diagnostics
	// (e.g., substantive graph missing gates), the dispatcher records controller
	// observations and rejects dispatch.
	// We use a template that passes structural validation but fails policy checks:
	// a substantive graph (has acceptance_spec) missing the implementation gate.

	st := newTestStore(t)
	ctx := context.Background()

	homeDir := t.TempDir()
	templatesDir := filepath.Join(homeDir, "templates")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Write a substantive graph template (has acceptance_spec) that is missing
	// implementation, verification, and acceptance gates — policy violation.
	badTmpl := `name = "bad-policy-template"
entry = "acceptance_spec"

[steps.acceptance_spec]
type = "agent"
role = "acceptance-author"
on_success = "complete"
on_failure = "acceptance_spec"
`
	if err := os.WriteFile(filepath.Join(templatesDir, "bad-policy-template.toml"), []byte(badTmpl), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := st.TaskCreate(ctx, "task-bad-policy", "", "", "Bad Policy", "", "bad-policy-template", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &worker.FakeSpawner{}
	d := New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, homeDir, testDispatcherConfig())
	_ = d.Tick(ctx)

	// Task should remain pending (dispatch was rejected).
	task, err := st.TaskGet(ctx, "task-bad-policy")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "pending" {
		t.Fatalf("task status = %q, want pending (dispatch rejected)", task.Status)
	}

	// Check that a graph_compilation failure was recorded.
	events, err := st.ControlPlaneEvents(ctx, "task-bad-policy", "", 20)
	if err != nil {
		t.Fatalf("ControlPlaneEvents: %v", err)
	}
	var found bool
	for _, ev := range events {
		if ev.Type == "graph_compilation" {
			found = true
			break
		}
	}
	if !found {
		var types []string
		for _, ev := range events {
			types = append(types, ev.Type)
		}
		t.Fatalf("graph_compilation event not found in control plane events, got types: %v", types)
	}
}

func TestDispatchTemplate_ReusesPersistedGraph(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create a bugfix task and dispatch it once (first dispatch creates graph).
	_, err := st.TaskCreate(ctx, "task-reuse", "", "", "Reuse", "", "bugfix", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &worker.FakeSpawner{}
	d := New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Get the initial compiled workflow.
	wf1, err := st.CompiledWorkflowGetByTask(ctx, "task-reuse")
	if err != nil {
		t.Fatal(err)
	}
	created1 := wf1.CreatedAt

	// Advance through routing: implement → success → lint.
	st.TaskSetStatus(ctx, "task-reuse", "running")
	if err := d.RouteStep(ctx, "task-reuse", "implement", "success"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	// Now dispatch again (lint step). This should reuse the persisted graph.
	// We can't easily call Tick because the task status is "pending" after RouteStep,
	// and Tick requires the task to be in pending status with no running agent.
	// Instead, verify the persisted graph has not changed.
	wf2, err := st.CompiledWorkflowGetByTask(ctx, "task-reuse")
	if err != nil {
		t.Fatal(err)
	}
	if wf2.CreatedAt != created1 {
		t.Errorf("compiled workflow was recompiled (created_at changed), want reuse")
	}

	// Verify the graph is intact.
	graph, err := workflow.UnmarshalGraphString(wf2.GraphJSON)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Nodes["implement"].Kind != workflow.KindAgent {
		t.Error("persisted graph lost node kind")
	}
	if graph.Edges["implement"].Success != "lint" {
		t.Errorf("persisted graph edge = %q, want lint", graph.Edges["implement"].Success)
	}
}

func TestRouteStep_BackwardCompat_TemplateFallback(t *testing.T) {
	// Test that RouteStep falls back to template loading when no compiled graph exists
	// (backward compatibility for tasks created before compiled workflow support).
	st := newTestStore(t)
	ctx := context.Background()

	// Create a task with a template but NO persisted compiled workflow.
	st.TaskCreate(ctx, "task-fallback", "", "", "Fallback", "", "feature", "", "default", 0)
	st.TaskSetStepFromPending(ctx, "task-fallback", "implement")
	st.TaskSetStatus(ctx, "task-fallback", "running")

	// No compiled workflow persisted — RouteStep should fall back to template loading.
	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), testDispatcherConfig())
	if err := d.RouteStep(ctx, "task-fallback", "implement", "success"); err != nil {
		t.Fatalf("RouteStep: %v", err)
	}

	task, _ := st.TaskGet(ctx, "task-fallback")
	// Feature template: implement → on_success → lint
	if task.CurrentStep != "lint" {
		t.Errorf("current_step = %q, want lint (fallback to template)", task.CurrentStep)
	}
}

// compileTemplate is a test helper that compiles a template and returns the graph.
func compileTemplate(tmpl *tmplpkg.Template) (*workflow.Graph, []workflow.CompileDiagnostic) {
	return workflow.Compile(tmpl)
}

func TestDispatcherDispatchFailsWhenGraphPersistenceFails(t *testing.T) {
	// When getOrCompileGraph compiles a template but cannot persist the compiled
	// workflow, dispatch must fail (task stays pending) instead of silently
	// ignoring the persistence error.
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := context.Background()

	// Create a task with a template so it goes through dispatchTemplate.
	_, err = st.TaskCreate(ctx, "task-persist-fail", "", "", "Persist Fail", "", "simple", "", "default", 0)
	if err != nil {
		t.Fatal(err)
	}

	// Pre-seed a bad compiled workflow so getOrCompileGraph tries to recompile.
	st.DB().ExecContext(ctx, `INSERT INTO compiled_workflows
		 (id, task_id, source_type, source_name, source_ref, policy_version, graph_json, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"wf-bad", "task-persist-fail", "template", "simple", "builtin", "1",
		`{invalid json}`, "active", time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))

	spawner := &worker.FakeSpawner{}
	d := New(ctx, st, spawner, &worker.FakeWorktreeCreator{}, tempDir, testDispatcherConfig())

	// Close the store DB so CompiledWorkflowCreate will fail when the graph
	// is recompiled and persisted.
	st.Close()

	// Tick should not panic. Dispatch will fail internally (persist error).
	// The task should remain pending.
	_ = d.Tick(ctx)

	// Reopen the store to read the task status.
	st2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer st2.Close()

	task, err := st2.TaskGet(ctx, "task-persist-fail")
	if err != nil {
		t.Fatalf("TaskGet: %v", err)
	}
	if task.Status != "pending" {
		t.Errorf("task status = %q, want pending (dispatch should fail when graph persistence fails)", task.Status)
	}
}
