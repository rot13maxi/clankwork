package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

const nudgeTimeout = 3 * time.Minute

// nudgeErrorRetryBudget is the number of times we re-nudge an ACP agent whose
// turn ended with stopReason:"error" before falling back to a handoff.
// Each retry extends the window by nudgeTimeout (~3 min), giving ~12 min total.
const nudgeErrorRetryBudget = 3

// contextLimitPatterns are strings that appear in Claude's pane output when it
// hits its context window and cannot continue without a new session.
var contextLimitPatterns = []string{
	"context window is full",
	"context_window_exceeded",
	"Unable to compact",
	"compaction failed",
	"too long to continue",
}

type Reconciler struct {
	store      *store.Store
	spawner    worker.AgentRuntime
	worktree   worker.WorktreeCreator
	timeout    time.Duration
	dispatcher *Dispatcher // for template-aware failure routing

	nudgeMu           sync.Mutex
	nudgeSent         map[string]time.Time // agentID → when nudge was sent
	nudgeErrorRetries map[string]int       // agentID → count of error-turn re-nudges used
}

func NewReconciler(st *store.Store, spawner worker.AgentRuntime, wt worker.WorktreeCreator, timeout time.Duration) *Reconciler {
	return &Reconciler{
		store:             st,
		spawner:           spawner,
		worktree:          wt,
		timeout:           timeout,
		nudgeSent:         make(map[string]time.Time),
		nudgeErrorRetries: make(map[string]int),
	}
}

func (r *Reconciler) SetDispatcher(d *Dispatcher) {
	r.dispatcher = d
}

func (r *Reconciler) Tick(ctx context.Context) error {
	agents, err := r.store.AgentList(ctx, "")
	if err != nil {
		return err
	}

	for _, agent := range agents {
		if agent.Status != "running" {
			r.cleanupTerminalRuntime(ctx, agent)
			continue
		}

		task, err := r.store.TaskGet(ctx, agent.TaskID)
		if err != nil {
			slog.Error("reconciler: get task", "agent", agent.ID, "err", err)
			continue
		}

		// Guard: if task already terminal, just close out the agent.
		if task.Status != "running" {
			r.store.AgentSetEnded(ctx, agent.ID)
			r.cleanup(agent)
			continue
		}

		sessionName := agentRuntimeSession(agent)

		// Deterministic steps run as goroutines, not long-lived agent sessions.
		if sessionName == "" {
			continue
		}

		alive, _ := r.spawner.IsAlive(sessionName)
		heartbeatStale := agent.LastHeartbeat == nil || time.Since(*agent.LastHeartbeat) > r.timeout
		heartbeatFresh := !heartbeatStale

		if r.agentTransport(agent, sessionName) == worker.TransportACP {
			// ACP nudge state is intentionally NOT cleared on heartbeat
			// freshness: agents emit signal.progress mid-nudge-response,
			// which would re-arm the nudge every tick and produce an
			// unbounded prompt loop. Termination or nudge-timeout escalation
			// are the only paths that clear it.
			r.reconcileACP(ctx, agent, task, alive, heartbeatStale, heartbeatFresh)
			continue
		}

		// tmux: clear nudge state when the agent's heartbeat shows recovery.
		if heartbeatFresh {
			r.clearNudge(agent.ID)
		}

		switch {
		case !alive && heartbeatStale:
			// Session gone and no recent heartbeat: dead agent.
			slog.Warn("reconciler: session gone and heartbeat stale", "agent", agent.ID, "session", sessionName)
			r.trace(ctx, agent, "reconciler.killed", map[string]string{"reason": "tmux session gone, heartbeat stale"})
			r.clearNudge(agent.ID)
			r.failTask(ctx, agent, task)

		case !alive && heartbeatFresh:
			// Session gone but heartbeat was recent — agent may be transitioning.
			slog.Warn("reconciler: session gone but heartbeat fresh, giving grace", "agent", agent.ID)

		case alive && heartbeatFresh:
			// Healthy: active session with recent heartbeat.

		case alive && heartbeatStale:
			// Session alive but no heartbeat: check pane activity before acting.
			// Require BOTH signals to avoid false positives on broken heartbeat code.
			paneActive, err := r.paneRecentlyActive(sessionName)
			if err != nil {
				slog.Warn("reconciler: pane activity check failed", "agent", agent.ID, "err", err)
				continue
			}
			if paneActive {
				// Pane is producing output — agent is working, heartbeat is broken.
				slog.Info("reconciler: heartbeat stale but pane active, skipping", "agent", agent.ID)
				continue
			}
			// Both signals confirm stall. Check pane content for context-limit errors.
			r.handleStall(ctx, agent, task)
		}
	}
	return nil
}

func (r *Reconciler) cleanupTerminalRuntime(ctx context.Context, agent *model.Agent) {
	sessionName := agentRuntimeSession(agent)
	if sessionName == "" {
		return
	}
	r.ensureRuntimeTransport(agent)
	if agent.Transport == worker.TransportACP && agent.PID == 0 {
		return
	}
	alive, err := r.spawner.IsAlive(sessionName)
	if err != nil {
		slog.Warn("reconciler: terminal session liveness check failed", "agent", agent.ID, "session", sessionName, "err", err)
		return
	}
	if !alive {
		if agent.PID != 0 {
			r.store.AgentSetRuntimePID(ctx, agent.ID, 0)
		}
		return
	}
	slog.Info("reconciler: cleaning terminal agent runtime", "agent", agent.ID, "session", sessionName, "status", agent.Status)
	r.traceAgentControllerDecision(ctx, agent, model.AgentDecision{
		AgentID: agent.ID,
		Health:  model.AgentHealthDead,
		Action:  model.ControllerActionKill,
		Reason:  "agent terminal, runtime cleaned",
	})
	r.trace(ctx, agent, "reconciler.cleaned_runtime", map[string]string{"reason": "agent terminal", "status": agent.Status})
	if err := r.spawner.GracefulKill(sessionName, 5*time.Second); err != nil {
		slog.Warn("reconciler: clean terminal runtime failed", "agent", agent.ID, "session", sessionName, "err", err)
		r.traceActuation(ctx, agent, model.ControllerActionKill, model.ActuationOutcomeFailure, err.Error(), "runtime cleanup failed")
		return
	}
	r.traceActuation(ctx, agent, model.ControllerActionKill, model.ActuationOutcomeSuccess, "", "runtime cleaned")
	r.store.AgentSetRuntimePID(ctx, agent.ID, 0)
	r.clearNudge(agent.ID)
}

// acpReconcileState summarizes an ACP agent's state by walking events in order.
//
// Turn tracking is sequence-aware, not latched: InTurn flips with each
// turn_started / turn_ended pair, and HadTurn records whether at least one turn
// has ever ended. EndedWithoutSignal — the condition that triggers a no-signal
// nudge — is "we've ended at least one turn AND the agent is not currently in a
// new one." Latching the booleans (the old shape) made every agent look stalled
// once its first turn ended, because the first ACP prompt always returns with a
// stopReason.
type acpReconcileState struct {
	LastEventAt       time.Time
	LastStopReason    string
	InTurn            bool
	HadTurn           bool
	PermissionPending bool
	ToolActivity      bool
	ContextLimitError bool
	ErrorTurnCount    int // turns that ended with stopReason:"error"
}

func (s acpReconcileState) eventFresh(threshold time.Duration) bool {
	return !s.LastEventAt.IsZero() && time.Since(s.LastEventAt) < threshold
}

// EndedWithoutSignal reports whether the most recent turn has ended and the
// agent is now idle without having signaled a terminal lifecycle outcome.
func (s acpReconcileState) EndedWithoutSignal() bool {
	return s.HadTurn && !s.InTurn
}

func (s acpReconcileState) progressEvidence() string {
	switch {
	case s.ToolActivity:
		return model.ProgressPresent
	case s.EndedWithoutSignal():
		return model.ProgressAbsent
	case s.PermissionPending:
		return model.ProgressUnknown
	default:
		return model.ProgressUnknown
	}
}

func (r *Reconciler) reconcileACP(ctx context.Context, agent *model.Agent, task *model.Task, alive, heartbeatStale, heartbeatFresh bool) {
	state := r.acpState(ctx, agent)
	eventFresh := state.eventFresh(r.timeout)
	sessionName := agentRuntimeSession(agent)
	r.ensureRuntimeTransport(agent)

	switch {
	case state.LastStopReason == "cancelled" && state.EndedWithoutSignal():
		slog.Warn("reconciler: acp turn cancelled without lifecycle signal", "agent", agent.ID, "session", sessionName)
		r.traceAgentControllerDecision(ctx, agent, model.AgentDecision{
			AgentID: agent.ID,
			Health:  model.AgentHealthDead,
			Action:  model.ControllerActionKill,
			Reason:  "acp turn cancelled without lifecycle signal",
			Error: &model.ControllerError{
				Category:        model.ControllerErrorAgentStalling,
				Message:         "acp turn cancelled without lifecycle signal",
				AffectedAgentID: agent.ID,
			},
		})
		r.trace(ctx, agent, "reconciler.killed", map[string]string{"reason": "acp_turn_cancelled"})
		r.clearNudge(agent.ID)
		r.failTask(ctx, agent, task)
	case !alive && heartbeatStale && !eventFresh:
		slog.Warn("reconciler: acp process gone and heartbeat/events stale", "agent", agent.ID, "session", sessionName)
		r.trace(ctx, agent, "reconciler.killed", map[string]string{"reason": "acp process gone, heartbeat/events stale"})
		r.clearNudge(agent.ID)
		r.failTask(ctx, agent, task)
	case !alive:
		slog.Warn("reconciler: acp process gone but recent activity exists", "agent", agent.ID)
	case state.ContextLimitError:
		slog.Warn("reconciler: acp context limit detected, handing off", "agent", agent.ID)
		r.traceAgentControllerDecision(ctx, agent, model.AgentDecision{
			AgentID: agent.ID,
			Health:  model.AgentHealthStalled,
			Action:  model.ControllerActionHandoff,
			Reason:  "acp context limit detected",
			Error: &model.ControllerError{
				Category:        model.ControllerErrorAgentContextLimit,
				Message:         "agent hit ACP context limit",
				AffectedAgentID: agent.ID,
			},
		})
		r.trace(ctx, agent, "reconciler.stall", map[string]string{"reason": "context_limit"})
		r.clearNudge(agent.ID)
		if err := r.spawner.Kill(sessionName); err != nil {
			slog.Warn("reconciler: acp context-limit kill failed", "agent", agent.ID, "session", sessionName, "err", err)
			r.traceActuation(ctx, agent, model.ControllerActionHandoff, model.ActuationOutcomeFailure, err.Error(), "kill failed")
		} else {
			r.traceActuation(ctx, agent, model.ControllerActionHandoff, model.ActuationOutcomeSuccess, "", "process killed")
		}
		r.failTask(ctx, agent, task)
	case state.EndedWithoutSignal():
		r.handleACPEndedWithoutSignal(ctx, agent, task, state)
	case heartbeatFresh || eventFresh:
		// Agent is mid-turn or just-active. Don't clear nudge state here —
		// for ACP the agent emits signal.progress mid-response, which would
		// otherwise re-arm the nudge every tick. nudgeSent is cleared only
		// on terminal status transitions or when the timeout escalates.
	default:
		r.handleACPStall(ctx, agent, task)
	}
}

func (r *Reconciler) handleACPEndedWithoutSignal(ctx context.Context, agent *model.Agent, task *model.Task, state acpReconcileState) {
	decision := model.EvaluateAgentHealth(model.AgentObservedState{
		AgentID:        agent.ID,
		TaskID:         agent.TaskID,
		SessionAlive:   true,
		HeartbeatStale: false,
		PaneActive:     true,
		Progress:       state.progressEvidence(),
		NoSignalTurns:  state.ErrorTurnCount,
	}, r.timeout)
	if decision.Action == model.ControllerActionBlock {
		r.traceAgentControllerDecision(ctx, agent, decision)
		sigHash := ""
		if decision.FailureSignature != nil {
			sigHash = decision.FailureSignature.NormalizedHash
		}
		r.blockTask(ctx, agent, task, decision.Reason, sigHash)
		return
	}

	r.nudgeMu.Lock()
	sentAt, alreadyNudged := r.nudgeSent[agent.ID]
	r.nudgeMu.Unlock()
	if !alreadyNudged {
		r.ensureRuntimeTransport(agent)
		decision.Health = model.AgentHealthStalled
		decision.Action = model.ControllerActionNudge
		decision.Reason = "acp turn ended without lifecycle signal"
		decision.Error = &model.ControllerError{
			Category:        model.ControllerErrorAgentStalling,
			Message:         "acp turn ended without lifecycle signal",
			AffectedAgentID: agent.ID,
		}
		r.traceAgentControllerDecision(ctx, agent, decision)
		r.trace(ctx, agent, "reconciler.stall", map[string]string{"reason": "acp_turn_ended_without_signal"})
		msg := "Your ACP turn ended but the task is still running. If your work is complete, run: clankwork signal done. If blocked, run: clankwork signal blocked \"<reason>\". If you cannot continue, run: clankwork signal failed \"<reason>\"."
		if err := r.spawner.SendNudge(agentRuntimeSession(agent), msg); err != nil {
			if isACPTurnActiveError(err) {
				slog.Info("reconciler: acp no-signal nudge rejected — turn is active",
					"agent", agent.ID)
				r.traceActuation(ctx, agent, model.ControllerActionNudge, model.ActuationOutcomeFailure, err.Error(), "turn active, nudge deferred")
				return
			}
			slog.Warn("reconciler: send acp no-signal nudge failed", "agent", agent.ID, "err", err)
			r.traceActuation(ctx, agent, model.ControllerActionNudge, model.ActuationOutcomeFailure, err.Error(), "nudge send failed")
		} else {
			r.traceActuation(ctx, agent, model.ControllerActionNudge, model.ActuationOutcomeSuccess, "", "nudge sent")
		}
		r.nudgeMu.Lock()
		r.nudgeSent[agent.ID] = time.Now()
		r.nudgeMu.Unlock()
		return
	}
	if time.Since(sentAt) < nudgeTimeout {
		return
	}

	// For error-ended turns, re-nudge up to nudgeErrorRetryBudget times before
	// handing off. Each retry extends the window by nudgeTimeout (~3 min).
	// Non-error stop reasons (end_turn, turn_completed, etc.) always hand off.
	if state.LastStopReason == "error" {
		r.nudgeMu.Lock()
		retries := r.nudgeErrorRetries[agent.ID]
		r.nudgeMu.Unlock()
		if retries < nudgeErrorRetryBudget {
			r.nudgeMu.Lock()
			r.nudgeErrorRetries[agent.ID] = retries + 1
			delete(r.nudgeSent, agent.ID) // clear so nudge is re-sent below
			r.nudgeMu.Unlock()
			slog.Warn("reconciler: acp error turn re-nudging", "agent", agent.ID, "retry", retries+1, "budget", nudgeErrorRetryBudget)
			r.ensureRuntimeTransport(agent)
			msg := "Your ACP turn ended with an error. If your work is complete, run: clankwork signal done. If blocked, run: clankwork signal blocked \"<reason>\". If you cannot continue, run: clankwork signal failed \"<reason>\"."
			if err := r.spawner.SendNudge(agentRuntimeSession(agent), msg); err != nil {
				slog.Warn("reconciler: send acp error re-nudge failed", "agent", agent.ID, "err", err)
				r.traceActuation(ctx, agent, model.ControllerActionNudge, model.ActuationOutcomeFailure, err.Error(), "error re-nudge failed")
			} else {
				r.traceActuation(ctx, agent, model.ControllerActionNudge, model.ActuationOutcomeSuccess, "", "error re-nudge sent")
			}
			r.nudgeMu.Lock()
			r.nudgeSent[agent.ID] = time.Now()
			r.nudgeMu.Unlock()
			return
		}
		slog.Warn("reconciler: acp error turn retry budget exhausted, handing off", "agent", agent.ID, "retries", retries)
	} else {
		slog.Warn("reconciler: acp no-signal nudge timeout, handing off", "agent", agent.ID, "stop_reason", state.LastStopReason)
	}

	r.traceAgentControllerDecision(ctx, agent, model.AgentDecision{
		AgentID: agent.ID,
		Health:  model.AgentHealthStalled,
		Action:  model.ControllerActionHandoff,
		Reason:  "acp turn ended without signal, nudge timeout exceeded",
		Error: &model.ControllerError{
			Category:        model.ControllerErrorAgentStalling,
			Message:         "acp turn ended without signal, nudge timeout exceeded",
			AffectedAgentID: agent.ID,
		},
	})
	r.trace(ctx, agent, "reconciler.stall", map[string]string{"reason": "acp_no_signal_nudge_timeout"})
	r.clearNudge(agent.ID)
	r.ensureRuntimeTransport(agent)
	if err := r.spawner.GracefulKill(agentRuntimeSession(agent), 5*time.Second); err != nil {
		slog.Warn("reconciler: acp no-signal graceful kill failed", "agent", agent.ID, "err", err)
		r.traceActuation(ctx, agent, model.ControllerActionHandoff, model.ActuationOutcomeFailure, err.Error(), "graceful kill failed")
	} else {
		r.traceActuation(ctx, agent, model.ControllerActionHandoff, model.ActuationOutcomeSuccess, "", "graceful kill sent")
	}
	r.failTask(ctx, agent, task)
}

func (r *Reconciler) handleACPStall(ctx context.Context, agent *model.Agent, task *model.Task) {
	r.nudgeMu.Lock()
	sentAt, alreadyNudged := r.nudgeSent[agent.ID]
	r.nudgeMu.Unlock()
	if !alreadyNudged {
		r.ensureRuntimeTransport(agent)
		r.traceAgentControllerDecision(ctx, agent, model.AgentDecision{
			AgentID:  agent.ID,
			Health:   model.AgentHealthStalled,
			Action:   model.ControllerActionNudge,
			Reason:   "acp events and heartbeat silent",
			Progress: model.ProgressAbsent,
			Error: &model.ControllerError{
				Category:        model.ControllerErrorAgentStalling,
				Message:         "acp events and heartbeat silent",
				AffectedAgentID: agent.ID,
			},
		})
		r.trace(ctx, agent, "reconciler.stall", map[string]string{"reason": "acp_events_and_heartbeat_silent"})
		msg := "No recent ACP activity or heartbeat was observed. If you are still working, send progress with: clankwork signal progress \"brief status\". If complete, run: clankwork signal done."
		if err := r.spawner.SendNudge(agentRuntimeSession(agent), msg); err != nil {
			if isACPTurnActiveError(err) {
				// Adapter rejected the nudge because a turn is already active.
				// The agent is mid-inference, not stalled — don't start the handoff clock.
				slog.Info("reconciler: acp stall nudge rejected — turn is active, agent mid-inference",
					"agent", agent.ID)
				r.traceActuation(ctx, agent, model.ControllerActionNudge, model.ActuationOutcomeFailure, err.Error(), "turn active, nudge deferred")
				return
			}
			slog.Warn("reconciler: send acp stall nudge failed", "agent", agent.ID, "err", err)
			r.traceActuation(ctx, agent, model.ControllerActionNudge, model.ActuationOutcomeFailure, err.Error(), "nudge send failed")
		} else {
			r.traceActuation(ctx, agent, model.ControllerActionNudge, model.ActuationOutcomeSuccess, "", "nudge sent")
		}
		r.nudgeMu.Lock()
		r.nudgeSent[agent.ID] = time.Now()
		r.nudgeMu.Unlock()
		return
	}
	if time.Since(sentAt) < nudgeTimeout {
		return
	}
	r.traceAgentControllerDecision(ctx, agent, model.AgentDecision{
		AgentID: agent.ID,
		Health:  model.AgentHealthStalled,
		Action:  model.ControllerActionHandoff,
		Reason:  "acp stall nudge timeout exceeded",
		Error: &model.ControllerError{
			Category:        model.ControllerErrorAgentStalling,
			Message:         "acp stall nudge timeout exceeded",
			AffectedAgentID: agent.ID,
		},
	})
	r.trace(ctx, agent, "reconciler.stall", map[string]string{"reason": "acp_stall_nudge_timeout"})
	r.clearNudge(agent.ID)
	r.ensureRuntimeTransport(agent)
	if err := r.spawner.GracefulKill(agentRuntimeSession(agent), 5*time.Second); err != nil {
		slog.Warn("reconciler: acp stall graceful kill failed", "agent", agent.ID, "err", err)
		r.traceActuation(ctx, agent, model.ControllerActionHandoff, model.ActuationOutcomeFailure, err.Error(), "graceful kill failed")
	} else {
		r.traceActuation(ctx, agent, model.ControllerActionHandoff, model.ActuationOutcomeSuccess, "", "graceful kill sent")
	}
	r.failTask(ctx, agent, task)
}

func (r *Reconciler) acpState(ctx context.Context, agent *model.Agent) acpReconcileState {
	events, err := r.store.AgentEventsList(ctx, agent.ID, "", 0, 500)
	if err != nil {
		slog.Warn("reconciler: list acp events failed", "agent", agent.ID, "err", err)
		return acpReconcileState{}
	}
	return computeACPState(events)
}

// computeACPState walks ACP events in order and returns the derived
// reconcile state. Exposed as a free function so the state machine can be
// tested without a Reconciler.
func computeACPState(events []*model.AgentEvent) acpReconcileState {
	var state acpReconcileState
	for _, ev := range events {
		if ev.CreatedAt.After(state.LastEventAt) {
			state.LastEventAt = ev.CreatedAt
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(ev.Payload), &msg); err != nil {
			continue
		}
		if acpMessageHasContextLimitError(msg) {
			state.ContextLimitError = true
		}

		turnEnded := false
		if result, _ := msg["result"].(map[string]any); result != nil {
			if stopReason, _ := result["stopReason"].(string); stopReason != "" {
				state.LastStopReason = stopReason
				turnEnded = true
				if stopReason == "error" {
					state.ErrorTurnCount++
				}
			}
		}

		params, _ := msg["params"].(map[string]any)
		update := acpUpdateObject(params)
		sessionUpdate, _ := update["sessionUpdate"].(string)
		status, _ := update["status"].(string)
		turnStarted := sessionUpdate == "turn_started" || status == "turn_started"
		if sessionUpdate == "turn_completed" || status == "turn_completed" {
			turnEnded = true
		}
		if content, _ := update["content"].(map[string]any); content != nil {
			if text, _ := content["text"].(string); text == "turn_completed" {
				turnEnded = true
			}
		}

		// State transitions are sequence-aware: turn_started flips us into
		// the in-turn state; turn_completed or a result.stopReason flips us
		// out and records that a turn has ended at least once. Both flags
		// are observed even within the same event because a single message
		// could in principle carry both (e.g., a server that emits
		// turn_completed alongside a final stopReason on the same wire).
		if turnStarted {
			state.InTurn = true
			state.PermissionPending = false
			state.ToolActivity = false
		}
		if turnEnded {
			state.InTurn = false
			state.HadTurn = true
		}

		if _, ok := update["permissionRequest"].(map[string]any); ok {
			state.PermissionPending = true
		}
		if acpUpdateHasToolActivity(params, update) {
			state.ToolActivity = true
		}
	}
	return state
}

func acpUpdateObject(params map[string]any) map[string]any {
	if params == nil {
		return nil
	}
	if update, _ := params["update"].(map[string]any); update != nil {
		return update
	}
	return params
}

func acpUpdateHasToolActivity(params, update map[string]any) bool {
	for _, fields := range []map[string]any{params, update} {
		if fields == nil {
			continue
		}
		for _, key := range []string{"type", "status", "itemType", "sessionUpdate"} {
			value, _ := fields[key].(string)
			switch value {
			case "tool_use", "tool_result", "tool_call", "tool_call_update", "commandExecution":
				return true
			}
		}
	}
	return false
}

func (r *Reconciler) agentTransport(agent *model.Agent, sessionName string) string {
	if agent.Transport != "" {
		return agent.Transport
	}
	if reporter, ok := r.spawner.(worker.TransportReporter); ok {
		return reporter.TransportForSession(sessionName)
	}
	return worker.TransportTmux
}

func (r *Reconciler) ensureRuntimeTransport(agent *model.Agent) {
	transport := agent.Transport
	if transport == "" {
		return
	}
	if transport != worker.TransportACP && transport != worker.TransportTmux {
		return
	}
	if selector, ok := r.spawner.(worker.TransportSelector); ok {
		if err := selector.UseTransport(agentRuntimeSession(agent), transport); err != nil {
			slog.Warn("reconciler: restore runtime transport failed", "agent", agent.ID, "transport", transport, "err", err)
		}
	}
}

// handleStall is called when both heartbeat and pane are stale. It checks for
// a context-limit error (immediate handoff) or sends a nudge and waits before
// handing off. This keeps the decision tree simple: detect → nudge → handoff.
func (r *Reconciler) handleStall(ctx context.Context, agent *model.Agent, task *model.Task) {
	sessionName := agentRuntimeSession(agent)
	paneContent, err := r.spawner.CapturePane(sessionName, 80)
	if err != nil {
		slog.Warn("reconciler: capture pane failed", "agent", agent.ID, "err", err)
	}

	if hasContextLimitError(paneContent) {
		slog.Warn("reconciler: context limit detected, handing off", "agent", agent.ID)
		r.traceAgentControllerDecision(ctx, agent, model.AgentDecision{
			AgentID: agent.ID,
			Health:  model.AgentHealthStalled,
			Action:  model.ControllerActionHandoff,
			Reason:  "context limit detected",
			Error: &model.ControllerError{
				Category:        model.ControllerErrorAgentContextLimit,
				Message:         "agent hit context limit",
				AffectedAgentID: agent.ID,
			},
		})
		r.trace(ctx, agent, "reconciler.stall", map[string]string{"reason": "context_limit"})
		r.clearNudge(agent.ID)
		r.spawner.Kill(sessionName)
		r.failTask(ctx, agent, task)
		return
	}

	decision := model.EvaluateAgentHealth(model.AgentObservedState{
		AgentID:          agent.ID,
		TaskID:           agent.TaskID,
		SessionAlive:     true,
		HeartbeatStale:   true,
		PaneActive:       false,
		LastHeartbeatAge: r.computeLastHeartbeatAge(agent),
		Progress:         model.ProgressAbsent,
	}, r.timeout)

	r.nudgeMu.Lock()
	sentAt, alreadyNudged := r.nudgeSent[agent.ID]
	r.nudgeMu.Unlock()

	if !alreadyNudged {
		slog.Warn("reconciler: stall detected, sending nudge", "agent", agent.ID)
		r.traceAgentControllerDecision(ctx, agent, decision)
		r.trace(ctx, agent, "reconciler.stall", map[string]string{"reason": "pane_and_heartbeat_silent"})
		nudgeMsg := "You appear to have stalled. If your work is complete, run: clankwork signal done. If blocked, run: clankwork signal blocked \"<reason>\". If you cannot continue, run: clankwork signal failed \"<reason>\"."
		if err := r.spawner.SendNudge(sessionName, nudgeMsg); err != nil {
			slog.Warn("reconciler: send nudge failed", "agent", agent.ID, "err", err)
		}
		r.nudgeMu.Lock()
		r.nudgeSent[agent.ID] = time.Now()
		r.nudgeMu.Unlock()
		return
	}

	if time.Since(sentAt) < nudgeTimeout {
		// Still within nudge window: wait.
		return
	}

	slog.Warn("reconciler: nudge timeout, handing off", "agent", agent.ID, "nudge_age", time.Since(sentAt).Round(time.Second))
	r.traceAgentControllerDecision(ctx, agent, model.AgentDecision{
		AgentID: agent.ID,
		Health:  model.AgentHealthStalled,
		Action:  model.ControllerActionHandoff,
		Reason:  "nudge timeout exceeded",
		Error:   decision.Error,
	})
	r.trace(ctx, agent, "reconciler.stall", map[string]string{"reason": "nudge_timeout"})
	r.clearNudge(agent.ID)
	r.spawner.GracefulKill(sessionName, 5*time.Second)
	r.failTask(ctx, agent, task)
}

func (r *Reconciler) paneRecentlyActive(sessionName string) (bool, error) {
	lastActivity, err := r.spawner.PaneLastActivity(sessionName)
	if err != nil {
		return false, err
	}
	return time.Since(lastActivity) < r.timeout, nil
}

func (r *Reconciler) clearNudge(agentID string) {
	r.nudgeMu.Lock()
	delete(r.nudgeSent, agentID)
	delete(r.nudgeErrorRetries, agentID)
	r.nudgeMu.Unlock()
}

// SetNudgeSentAt injects a nudge timestamp for a given agent, for use in tests only.
func (r *Reconciler) SetNudgeSentAt(agentID string, t time.Time) {
	r.nudgeMu.Lock()
	r.nudgeSent[agentID] = t
	r.nudgeMu.Unlock()
}

func hasContextLimitError(paneContent string) bool {
	lower := strings.ToLower(paneContent)
	for _, pattern := range contextLimitPatterns {
		if strings.Contains(lower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

func acpMessageHasContextLimitError(msg map[string]any) bool {
	if result, _ := msg["result"].(map[string]any); result != nil {
		if stopReason, _ := result["stopReason"].(string); strings.EqualFold(stopReason, "context_limit") {
			return true
		}
		if structuredContextLimit(result) {
			return true
		}
	}
	if errObj, _ := msg["error"].(map[string]any); errObj != nil && structuredContextLimit(errObj) {
		return true
	}
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		return false
	}
	if errObj, _ := params["error"].(map[string]any); errObj != nil && structuredContextLimit(errObj) {
		return true
	}
	update, _ := params["update"].(map[string]any)
	if update != nil {
		if errObj, _ := update["error"].(map[string]any); errObj != nil && structuredContextLimit(errObj) {
			return true
		}
	}
	return false
}

func structuredContextLimit(v any) bool {
	switch x := v.(type) {
	case string:
		return hasContextLimitError(x)
	case map[string]any:
		for _, key := range []string{"message", "details", "data", "error", "stopReason"} {
			if child, ok := x[key]; ok && structuredContextLimit(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if structuredContextLimit(child) {
				return true
			}
		}
	}
	return false
}

// failTask routes the failure through the template (if any) for retry logic,
// or directly marks the task as failed for non-template tasks.
func (r *Reconciler) failTask(ctx context.Context, agent *model.Agent, task *model.Task) {
	r.store.AgentSetStatus(ctx, agent.ID, "killed")

	if r.dispatcher != nil && task.Template != "" && task.CurrentStep != "" {
		fcPayload, _ := json.Marshal(map[string]string{"step": task.CurrentStep, "message": "session killed by reconciler"})
		r.store.TraceAppend(ctx, task.ID, agent.ID, "step.failure_context", string(fcPayload))
		if err := r.dispatcher.RouteStep(ctx, task.ID, task.CurrentStep, "failure"); err != nil {
			slog.Error("reconciler: route step on kill", "task", task.ID, "err", err)
			r.store.TaskSetStatusIfRunning(ctx, task.ID, "failed")
		}
	} else {
		r.store.TaskSetStatusIfRunning(ctx, task.ID, "failed")
	}

	r.cleanup(agent)
}

func (r *Reconciler) blockTask(ctx context.Context, agent *model.Agent, task *model.Task, reason string, failureSignatureHash string) {
	r.store.AgentSetStatus(ctx, agent.ID, "killed")
	_ = r.store.TaskSetStatusIfRunning(ctx, task.ID, "blocked")
	r.trace(ctx, agent, "agent_controller.blocked", map[string]string{
		"reason":  reason,
		"step":    task.CurrentStep,
		"sig_hash": failureSignatureHash,
	})
	r.traceActuation(ctx, agent, model.ControllerActionBlock, model.ActuationOutcomeSuccess, "", reason)
	r.createBlockEscalation(ctx, agent, task, reason, failureSignatureHash)
	if sessionName := agentRuntimeSession(agent); sessionName != "" {
		r.ensureRuntimeTransport(agent)
		_ = r.spawner.GracefulKill(sessionName, 5*time.Second)
	}
	r.cleanup(agent)
}

func (r *Reconciler) cleanup(agent *model.Agent) {
	if agent.WorktreePath != "" {
		go func() {
			if err := r.worktree.Remove(agent.WorktreePath); err != nil {
				slog.Error("reconciler: remove worktree", "path", agent.WorktreePath, "err", err)
			}
		}()
	}
}

// createBlockEscalation creates an open escalation for a blocked task if one
// doesn't already exist for the same failure signature. Resolved escalations
// don't block new ones — recurring failures get fresh escalations.
func (r *Reconciler) createBlockEscalation(ctx context.Context, agent *model.Agent, task *model.Task, reason string, failureSigHash string) {
	if failureSigHash == "" {
		failureSigHash = reason
	}
	existing, err := r.store.EscalationGetOpenByTaskAndSignature(ctx, task.ID, failureSigHash)
	if err != nil {
		slog.Warn("reconciler: escalation dedup check failed", "task", task.ID, "err", err)
	} else if existing != nil {
		slog.Info("reconciler: open escalation already exists for block", "task", task.ID, "escalation", existing.ID, "sig", failureSigHash)
		return
	}

	// Build detailed reason with agent info and retry counts.
	detailReason := reason
	if agent != nil && agent.ID != "" {
		detailReason = fmt.Sprintf("agent=%s: %s", agent.ID, reason)
	}
	if task.RetryCount > 0 {
		detailReason = fmt.Sprintf("retry_count=%d: %s", task.RetryCount, detailReason)
	}

	// Build suggested commands for the operator.
	suggestedCommands := []string{
		fmt.Sprintf("clankwork task diagnose %s", task.ID),
		fmt.Sprintf("clankwork escalation list --task %s --status open", task.ID),
		fmt.Sprintf("clankwork task list --status blocked"),
	}
	if task.CurrentStep != "" {
		suggestedCommands = append(suggestedCommands,
			fmt.Sprintf("clankwork task retry-step %s %s", task.ID, task.CurrentStep),
			fmt.Sprintf("clankwork task retry %s", task.ID),
		)
	}

	esc := &model.Escalation{
		TaskID:            task.ID,
		StepName:          task.CurrentStep,
		TargetType:        "human",
		TargetRef:         agentIDOrEmpty(agent),
		RequestedAction:   "investigate",
		Reason:            fmt.Sprintf("task blocked: %s", detailReason),
		Status:            "open",
		CreatedByType:     "system",
		CreatedByID:       "agent_controller",
		FailureSignature:  failureSigHash,
		SuggestedCommands: suggestedCommands,
	}
	if err := r.store.EscalationCreate(ctx, esc); err != nil {
		slog.Warn("reconciler: create block escalation failed", "task", task.ID, "err", err)
		return
	}
	slog.Info("reconciler: block escalation created", "task", task.ID, "escalation", esc.ID, "sig", failureSigHash)
}

// agentIDOrEmpty returns the agent's ID or empty string if agent is nil.
func agentIDOrEmpty(agent *model.Agent) string {
	if agent == nil {
		return ""
	}
	return agent.ID
}

func (r *Reconciler) trace(ctx context.Context, agent *model.Agent, eventType string, payload map[string]string) {
	b, _ := json.Marshal(payload)
	if b == nil {
		b = []byte("{}")
	}
	r.store.TraceAppend(ctx, agent.TaskID, agent.ID, eventType, string(b))
}

func (r *Reconciler) traceAgentControllerDecision(ctx context.Context, agent *model.Agent, decision model.AgentDecision) {
	payload := model.AgentControllerDecisionPayload{
		AgentID:          decision.AgentID,
		Health:           decision.Health,
		Action:           decision.Action,
		Reason:           decision.Reason,
		Progress:         decision.Progress,
		OscillationScore: decision.OscillationScore,
		EscalationLevel:  decision.EscalationLevel,
	}
	if decision.Error != nil {
		payload.ErrorCategory = decision.Error.Category
		payload.ErrorMagnitude = decision.Error.Magnitude
	}
	if decision.FailureSignature != nil {
		payload.FailureSignature = decision.FailureSignature.NormalizedHash
		payload.FailureSigFields = decision.FailureSignature.StableFields
	}
	r.store.TraceAppend(ctx, agent.TaskID, agent.ID, "agent_controller.decision", model.MarshalPayload(payload))
	_ = r.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "agent_controller",
		TaskID:       agent.TaskID,
		AgentID:      agent.ID,
		TargetType:   "agent",
		TargetID:     agent.ID,
		DecisionKind: "health_reconcile",
		Action:       decision.Action,
		Reason:       decision.Reason,
		Retryable:    decision.Action != "fail_task",
		Payload:      model.MarshalPayload(payload),
	})
	_ = r.store.ControlObservationPut(ctx, &model.ControlObservation{
		TargetType: "agent",
		TargetID:   agent.ID,
		TaskID:     agent.TaskID,
		AgentID:    agent.ID,
		Kind:       "agent_health",
		Status:     decision.Health,
		Reason:     decision.Reason,
		Payload:    model.MarshalPayload(payload),
	})
}

func (r *Reconciler) traceActuation(ctx context.Context, agent *model.Agent, action, outcome, errMsg, detail string) {
	payload := model.AgentControllerActuationPayload{
		AgentID:  agent.ID,
		Action:   action,
		Outcome:  outcome,
		ErrorMsg: errMsg,
		Detail:   detail,
	}
	r.store.TraceAppend(ctx, agent.TaskID, agent.ID, "agent_controller.actuation", model.MarshalPayload(payload))
	_ = r.store.ControllerActuationAppend(ctx, &model.ControllerActuation{
		RequestedOperation: action,
		ActorType:          "controller",
		ActorID:            "agent_controller",
		TargetType:         "agent",
		TargetID:           agent.ID,
		TaskID:             agent.TaskID,
		AgentID:            agent.ID,
		Outcome:            outcome,
		Error:              errMsg,
		Reason:             detail,
		Payload:            model.MarshalPayload(payload),
	})
}

func (r *Reconciler) computeLastHeartbeatAge(agent *model.Agent) time.Duration {
	if agent.LastHeartbeat == nil {
		return 0
	}
	return time.Since(*agent.LastHeartbeat)
}

// isACPTurnActiveError returns true when a SendNudge error indicates the adapter
// already has an active turn and cannot accept a new session/prompt call.
// This is NOT a stall — it means the agent is alive and mid-inference.
func isACPTurnActiveError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "turn/start") || strings.Contains(s, "turn_started")
}
