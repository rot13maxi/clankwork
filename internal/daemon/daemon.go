package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/mergequeue"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

// Run starts the daemon and blocks until SIGTERM or SIGINT.
func Run(homeDir string) error {
	if err := os.MkdirAll(homeDir, 0700); err != nil {
		return fmt.Errorf("create home dir: %w", err)
	}

	startedAt := time.Now()

	logPath := filepath.Join(homeDir, "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	setupLogging(logFile)

	// Use a dedicated lock file so the flock doesn't interfere with SQLite.
	lockPath := filepath.Join(homeDir, "clankwork.lock")
	lockFD, err := acquireLock(lockPath)
	if err != nil {
		return err
	}
	defer syscall.Close(lockFD)

	pidPath := filepath.Join(homeDir, "clankwork.pid")
	if err := writePID(pidPath); err != nil {
		return err
	}
	defer os.Remove(pidPath)

	cfg, err := config.Load(homeDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	dbPath := filepath.Join(homeDir, "clankwork.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	logDir := filepath.Join(homeDir, "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return fmt.Errorf("create logs dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(homeDir, "worktrees"), 0700); err != nil {
		return fmt.Errorf("create worktrees dir: %w", err)
	}

	tmuxSpawner := &worker.TmuxSpawner{LogDir: logDir}
	acpRuntime := worker.NewACPRuntime(logDir)
	var acpSeqMu sync.Mutex
	acpSeq := make(map[string]int64)
	acpRuntime.SetEventSink(func(agentID, taskID, sessionName, stream, payload string) {
		acpSeqMu.Lock()
		acpSeq[agentID]++
		seq := acpSeq[agentID]
		acpSeqMu.Unlock()
		if err := st.AgentEventAppend(context.Background(), agentID, taskID, seq, stream, payload); err != nil {
			slog.Warn("append agent event", "session", sessionName, "err", err)
		}
		if err := st.AgentUpdateRuntimeEvent(context.Background(), agentID, time.Now().UTC(), acpStopReason(payload)); err != nil {
			slog.Warn("update agent runtime event", "session", sessionName, "err", err)
		}
	})
	agentRuntime := worker.NewRuntimeMux(tmuxSpawner, acpRuntime)
	wtCreator := &worker.GitWorktreeCreator{HomeDir: homeDir}

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	disp := scheduler.New(runCtx, st, agentRuntime, wtCreator, homeDir, cfg)
	recon := scheduler.NewReconciler(st, agentRuntime, wtCreator,
		time.Duration(cfg.Scheduler.HeartbeatTimeoutSec)*time.Second)
	recon.SetDispatcher(disp)

	mqProcessor := mergequeue.NewProcessor(st, cfg, homeDir, disp)
	if err := mqProcessor.StartupRecovery(context.Background()); err != nil {
		slog.Warn("merge queue startup recovery error", "err", err)
	}
	disp.SetTaskCompletedHook(mqProcessor.EnqueueIfAutoMerge)

	// Kill orphaned acp-adapter processes from the previous daemon instance.
	// When the daemon is rebuilt or restarted, the new daemon starts with an
	// empty in-memory session map — it has no handles to still-running acp-adapter
	// processes. If we don't kill them, the reconciler sees those tasks as !alive
	// (session not in map) but with recent activity (DB events still fresh), and
	// loops for up to HeartbeatTimeoutSec before declaring them stale.
	// Tmux agents survive restarts (tmux is independent of the daemon), so we
	// only target pi-acp runtime agents here.
	//
	// For templated tasks, route through the normal failure/retry path (via
	// dispatcher) instead of immediately marking the task as terminal failed.
	// This preserves step-level retry budget and acceptance artifacts.
	killOrphanedACPAdapters(context.Background(), st, disp)

	socketPath := filepath.Join(homeDir, "clankwork.sock")
	srv := NewWithDispatcher(homeDir, socketPath, st, disp, wtCreator)
	srv.SetMergeProcessor(mqProcessor)
	if err := srv.Start(); err != nil {
		return err
	}
	defer os.Remove(socketPath)

	tickInterval := time.Duration(cfg.Scheduler.TickSec) * time.Second
	mqTickInterval := time.Duration(cfg.Scheduler.MergeQueueTickSec) * time.Second
	go runLoop(runCtx, tickInterval, disp.Tick)
	go runLoop(runCtx, 10*time.Second, recon.Tick)
	go runLoop(runCtx, mqTickInterval, mqProcessor.Tick)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	receivedSig := <-quit

	// Log diagnostic info about who sent the signal to help diagnose
	// unexpected shutdowns (e.g., external process managers, hooks, etc.).
	slog.Info("shutting down",
		"signal", receivedSig.String(),
		"pid", os.Getpid(),
		"ppid", os.Getppid(),
		"uptime", time.Since(startedAt).Round(time.Second),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

func acpStopReason(payload string) string {
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

func runLoop(ctx context.Context, interval time.Duration, fn func(context.Context) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := fn(ctx); err != nil {
				slog.Error("loop tick error", "err", err)
			}
		}
	}
}

func setupLogging(logFile io.Writer) {
	fileHandler := slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})
	if isTerminal(os.Stderr) {
		stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
		slog.SetDefault(slog.New(multiHandler{fileHandler, stderrHandler}))
	} else {
		slog.SetDefault(slog.New(fileHandler))
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func acquireLock(lockPath string) (int, error) {
	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return 0, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		return 0, fmt.Errorf("daemon already running (could not acquire lock)")
	}
	return fd, nil
}

func writePID(path string) error {
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600)
}

// multiHandler fans out to multiple slog.Handler instances.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make(multiHandler, len(m))
	for i, h := range m {
		handlers[i] = h.WithAttrs(attrs)
	}
	return handlers
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	handlers := make(multiHandler, len(m))
	for i, h := range m {
		handlers[i] = h.WithGroup(name)
	}
	return handlers
}

// isACPAdapterProcess checks whether the process with the given PID is actually
// an acp-adapter process by inspecting its command line via ps.
// This prevents accidental SIGTERM to unrelated processes that happen to share
// a recycled PID with a previously-recorded acp-adapter.
//
// We check the full command line (args=) rather than just the command name (comm=)
// because comm= can be truncated or misleading on some systems. The args= output
// includes the full path and arguments, giving us a more reliable match.
//
// We check for the following patterns, ordered from most specific to least:
//   - "acp-adapter" — the standard binary name after `make install-acp-adapter`
//   - "acp_adapter" — alternate naming convention (underscore)
//   - "acp " — the raw Go binary name (cmd/acp) before Makefile rename,
//     matched with trailing space to require it be a separate word
//     (e.g., "acp --adapter pi" matches; "feat/acp-whatever" does not)
//   - "--adapter" — the CLI flag always present when the adapter is invoked,
//     catching both renamed and raw binary names
func isACPAdapterProcess(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return false
	}
	args := strings.TrimSpace(string(out))
	return strings.Contains(args, "acp-adapter") ||
		strings.Contains(args, "acp_adapter") ||
		strings.Contains(args, "acp ") ||
		strings.Contains(args, "--adapter")
}

// killOrphanedACPAdapters finds acp-adapter processes that were spawned by a
// previous daemon instance and sends them SIGTERM.
//
// ROOT CAUSE INVESTIGATION: daemon SIGTERM every ~4 minutes
// =============================================================================
// The daemon was receiving clean SIGTERM and shutting down (~4 min intervals).
// After thorough investigation of all internal code paths:
//
//  1. Signal handler (daemon.go): Only listens for SIGTERM/SIGINT from the OS.
//     No internal timer or condition triggers self-shutdown.
//  2. PID file / lock file: Lock is acquired BEFORE PID file is written.
//     Lock acquisition failure returns an error (no SIGTERM to incumbent).
//  3. Orphan killer (this function): Runs ONCE at startup with PID collision guard.
//  4. Reconciler (scheduler/reconciler.go): Only kills agent sessions, not daemon.
//  5. stopDaemon (cmd/clankwork/daemon.go): CLI command, not called internally.
//     Includes process verification before sending SIGTERM.
//
// Conclusion: The daemon's internal code has NO path that sends SIGTERM to
// itself. The ~4-minute SIGTERM pattern is caused by an external source.
//
// DIAGNOSTIC IMPROVEMENTS:
// - Shutdown logging now includes pid, ppid, and uptime for correlation
// - isACPAdapterProcess is more precise (--adapter flag instead of broad "acp ")
// - Enhanced logging helps identify the signal sender via process hierarchy
//
// To diagnose the external trigger:
// 1. Check daemon.log for "shutting down" entries with pid/ppid/uptime
// 2. Run `ps -o pid,ppid,comm,args -p <ppid>` on the recorded ppid
// 3. Check for hooks calling `clankwork daemon stop` (e.g., .claude/hooks/)
// 4. Check coding agent subprocess management for tool-use timeouts
// =============================================================================
//
// When the daemon restarts (rebuild, crash recovery, etc.), the new daemon has
// an empty in-memory session map. Running acp-adapter processes become orphans
// with no one managing them. Without this cleanup, the reconciler sees those
// tasks as !alive (session not in the map) but with recent DB activity, and
// loops every 10s for up to HeartbeatTimeoutSec (600s) before declaring them
// stale and re-dispatching.
//
// We only target pi-acp runtime agents here. Tmux agents survive daemon
// restarts because tmux itself is independent of the daemon process.
//
// PID collision guard: before sending SIGTERM, we verify the process is actually
// an acp-adapter by checking its command line via ps. PID reuse after the old
// acp-adapter exits and the new daemon starts can otherwise cause the daemon to
// SIGTERM itself, resulting in immediate shutdown after restart.
//
// Template-aware routing: for tasks with a template and current_step, route
// through the dispatcher's RouteStep (with "failure" outcome) instead of
// immediately marking the task as terminal failed. This preserves step-level
// retry budget and allows the normal retry mechanism to recover.
func killOrphanedACPAdapters(ctx context.Context, st *store.Store, dispatcher *scheduler.Dispatcher) {
	agents, err := st.AgentRunningACPPIDs(ctx)
	if err != nil {
		slog.Warn("query orphaned ACP agents", "err", err)
		return
	}
	if len(agents) == 0 {
		return
	}

	slog.Info("found orphaned acp-adapter processes", "count", len(agents))

	for _, agent := range agents {
		pid := agent.PID
		if pid <= 0 {
			continue
		}

		// PID collision guard: verify the process is actually an acp-adapter
		// before sending SIGTERM. PID reuse after the old adapter exits can
		// cause us to SIGTERM the new daemon itself (or any other process).
		if !isACPAdapterProcess(pid) {
			slog.Info("orphaned PID no longer an acp-adapter, skipping SIGTERM", "pid", pid, "agent", agent.ID, "task", agent.TaskID)
			routeOrphanedAgent(ctx, st, dispatcher, agent, "pid no longer an acp-adapter")
			continue
		}

		err := syscall.Kill(pid, syscall.SIGTERM)
		switch {
		case err == nil:
			slog.Info("sent SIGTERM to orphaned acp-adapter", "pid", pid, "agent", agent.ID, "task", agent.TaskID)
		case err == syscall.ESRCH:
			slog.Debug("orphaned acp-adapter already dead", "pid", pid, "agent", agent.ID, "task", agent.TaskID)
		default:
			slog.Warn("failed to SIGTERM orphaned acp-adapter", "pid", pid, "agent", agent.ID, "task", agent.TaskID, "err", err)
		}

		// Route the task failure through the normal retry path.
		routeOrphanedAgent(ctx, st, dispatcher, agent, "orphaned on daemon restart")
	}
}

// routeOrphanedAgent handles the task/agent state transition for an orphaned
// ACP agent. For templated tasks with a current step, it routes through the
// dispatcher's RouteStep to preserve retry budget. For non-templated tasks,
// it marks the task as failed directly.
func routeOrphanedAgent(ctx context.Context, st *store.Store, dispatcher *scheduler.Dispatcher, agent *model.Agent, reason string) {
	// Always clean up the agent state.
	_ = st.AgentSetStatus(ctx, agent.ID, "killed")
	_ = st.AgentSetRuntimePID(ctx, agent.ID, 0)

	// Look up the task to check for template-aware routing.
	task, err := st.TaskGet(ctx, agent.TaskID)
	if err != nil {
		slog.Warn("orphan cleanup: get task failed, marking as failed", "task", agent.TaskID, "err", err)
		_ = st.TaskSetStatus(ctx, agent.TaskID, "failed")
		recordOrphanedACPAdapterCleanup(ctx, st, agent, reason, false)
		return
	}

	hasTemplate := task.Template != "" && task.CurrentStep != ""

	if hasTemplate && dispatcher != nil {
		// Route through the normal failure path to preserve retry budget.
		fcPayload, _ := json.Marshal(map[string]string{
			"step":    task.CurrentStep,
			"message": reason,
		})
		_ = st.TraceAppend(ctx, agent.TaskID, agent.ID, "step.failure_context", string(fcPayload))

		err := dispatcher.RouteStep(ctx, task.ID, task.CurrentStep, "failure")
		if err != nil {
			slog.Warn("orphan cleanup: route step failed, falling back to direct fail", "task", task.ID, "err", err)
			_ = st.TaskSetStatusIfRunning(ctx, task.ID, "failed")
		}

		recordOrphanedACPAdapterCleanup(ctx, st, agent, reason, true)
	} else {
		// Non-templated task or no dispatcher: mark as failed directly.
		_ = st.TaskSetStatus(ctx, agent.TaskID, "failed")
		recordOrphanedACPAdapterCleanup(ctx, st, agent, reason, false)
	}

	payload, _ := json.Marshal(map[string]string{
		"reason":          reason,
		"pid":             fmt.Sprintf("%d", agent.PID),
		"template_routed": fmt.Sprintf("%v", hasTemplate),
		"current_step":    task.CurrentStep,
	})
	_ = st.TraceAppend(ctx, agent.TaskID, agent.ID, "signal.orphaned_kill", string(payload))
}

func recordOrphanedACPAdapterCleanup(ctx context.Context, st *store.Store, agent *model.Agent, reason string, templateRouted bool) {
	action := "kill_and_fail_task"
	newState := "killed/failed"
	if templateRouted {
		action = "kill_and_route_step_failure"
		newState = "killed/routed"
	}
	_ = st.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "health_controller",
		TaskID:       agent.TaskID,
		AgentID:      agent.ID,
		TargetType:   "agent",
		TargetID:     agent.ID,
		DecisionKind: "orphaned_runtime",
		Action:       action,
		Reason:       reason,
		Retryable:    true,
		Payload:      fmt.Sprintf(`{"template_routed":%v,"reason":"daemon_restart_orphan_recovery"}`, templateRouted),
	})
	_ = st.ControllerActuationAppend(ctx, &model.ControllerActuation{
		RequestedOperation: "daemon.startup_orphan_cleanup",
		ActorType:          "controller",
		ActorID:            "health_controller",
		TargetType:         "agent",
		TargetID:           agent.ID,
		TaskID:             agent.TaskID,
		AgentID:            agent.ID,
		PreviousState:      "running",
		NewState:           newState,
		Outcome:            "success",
		Reason:             reason,
		Payload:            fmt.Sprintf(`{"template_routed":%v,"reason":"daemon_restart_orphan_recovery"}`, templateRouted),
	})
	_ = st.ControlObservationPut(ctx, &model.ControlObservation{
		TargetType: "agent",
		TargetID:   agent.ID,
		TaskID:     agent.TaskID,
		AgentID:    agent.ID,
		Kind:       "agent_health",
		Status:     "orphaned",
		Reason:     reason,
		Payload:    fmt.Sprintf(`{"template_routed":%v}`, templateRouted),
	})
}
