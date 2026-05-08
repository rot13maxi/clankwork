package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handleTasksDiagnose(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		Fail(w, http.StatusBadRequest, "missing_id", "id query param required")
		return
	}
	diag, err := s.store.TaskDiagnose(r.Context(), id)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	OK(w, diag)
}

func (s *Server) handleReconcileTask(w http.ResponseWriter, r *http.Request) {
	var req model.ReconcileRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	actor := defaultActor(req.ActorID)
	_ = s.store.ReconcilerDecisionAppend(r.Context(), &model.ReconcilerDecision{
		Controller:   "task_controller",
		TaskID:       task.ID,
		StepName:     task.CurrentStep,
		TargetType:   "task",
		TargetID:     task.ID,
		DecisionKind: "manual_reconcile",
		Action:       "measure_then_tick",
		Reason:       "operator requested task reconciliation",
		Retryable:    true,
	})
	if err := s.refreshTaskState(r, task, actor); err != nil {
		Fail(w, http.StatusInternalServerError, "refresh_failed", err.Error())
		return
	}
	if s.dispatcher != nil {
		if err := s.dispatcher.Tick(r.Context()); err != nil {
			s.recordActuation(r, "reconcile.task", actor, "task", task.ID, task.ID, task.CurrentStep, "", task.Status, task.Status, "failed", err.Error(), "dispatcher tick failed")
			Fail(w, http.StatusInternalServerError, "reconcile_failed", err.Error())
			return
		}
	}
	s.recordActuation(r, "reconcile.task", actor, "task", task.ID, task.ID, task.CurrentStep, "", task.Status, task.Status, "success", "", "operator requested task reconciliation")
	diag, _ := s.store.TaskDiagnose(r.Context(), req.TaskID)
	OK(w, diag)
}

func (s *Server) handleReconcileAll(w http.ResponseWriter, r *http.Request) {
	var req model.ReconcileRequest
	if r.Body != nil {
		_ = Decode(r, &req)
	}
	actor := defaultActor(req.ActorID)
	if s.dispatcher != nil {
		if err := s.dispatcher.Tick(r.Context()); err != nil {
			s.recordActuation(r, "reconcile.all", actor, "system", "scheduler", "", "", "", "", "", "failed", err.Error(), "dispatcher tick failed")
			Fail(w, http.StatusInternalServerError, "reconcile_failed", err.Error())
			return
		}
	}
	s.recordActuation(r, "reconcile.all", actor, "system", "scheduler", "", "", "", "", "", "success", "", "operator requested global reconciliation")
	OK(w, map[string]bool{"reconciled": true})
}

func (s *Server) handleRefreshTask(w http.ResponseWriter, r *http.Request) {
	var req model.RefreshRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err := s.refreshTaskState(r, task, defaultActor(req.ActorID)); err != nil {
		Fail(w, http.StatusInternalServerError, "refresh_failed", err.Error())
		return
	}
	diag, _ := s.store.TaskDiagnose(r.Context(), req.TaskID)
	OK(w, diag)
}

func (s *Server) handleRefreshAgent(w http.ResponseWriter, r *http.Request) {
	var req model.RefreshRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.AgentID == "" {
		Fail(w, http.StatusBadRequest, "missing_agent_id", "agent_id required")
		return
	}
	agent, err := s.store.AgentGet(r.Context(), req.AgentID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	status := "healthy"
	reason := "agent status is current in the control-plane database"
	if agent.Status == "running" && agent.LastHeartbeat == nil && agent.LastEventAt == nil {
		status = "unknown"
		reason = "running agent has no heartbeat or runtime event recorded"
	}
	_ = s.store.ControlObservationPut(r.Context(), &model.ControlObservation{
		TargetType: "agent",
		TargetID:   agent.ID,
		TaskID:     agent.TaskID,
		AgentID:    agent.ID,
		Kind:       "agent_health",
		Status:     status,
		Reason:     reason,
	})
	s.recordActuation(r, "refresh.agent", defaultActor(req.ActorID), "agent", agent.ID, agent.TaskID, "", agent.ID, "", agent.Status, "success", "", reason)
	OK(w, map[string]string{"agent_id": agent.ID, "status": status})
}

func (s *Server) handleRefreshWorktree(w http.ResponseWriter, r *http.Request) {
	var req model.RefreshRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	agent, err := s.store.AgentGetByTask(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	status := "unknown"
	reason := "no worktree path recorded"
	if agent.WorktreePath != "" {
		if info, err := os.Stat(agent.WorktreePath); err == nil && info.IsDir() {
			status = "present"
			reason = "worktree directory exists"
		} else {
			status = "missing"
			reason = "worktree directory is missing"
		}
	}
	_ = s.store.ControlObservationPut(r.Context(), &model.ControlObservation{
		TargetType:   "worktree",
		TargetID:     req.TaskID,
		TaskID:       req.TaskID,
		AgentID:      agent.ID,
		WorktreePath: agent.WorktreePath,
		Kind:         "worktree_state",
		Status:       status,
		Reason:       reason,
	})
	s.recordActuation(r, "refresh.worktree", defaultActor(req.ActorID), "worktree", req.TaskID, req.TaskID, "", agent.ID, "", status, "success", "", reason)
	OK(w, map[string]string{"task_id": req.TaskID, "status": status, "worktree_path": agent.WorktreePath})
}

func (s *Server) handleTasksRetryStep(w http.ResponseWriter, r *http.Request) {
	var req model.TaskResetStepRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	step := req.Step
	if step == "" {
		step = task.CurrentStep
	}
	if step == "" {
		Fail(w, http.StatusBadRequest, "missing_step", "step required when task has no current_step")
		return
	}
	if err := s.store.TaskSetStepForOperator(r.Context(), req.TaskID, step); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	s.resolveValidationLoopEscalations(r, req.TaskID, step, "operator retried step")
	s.recordActuation(r, "task.retry-step", defaultActor(req.ActorID), "task", req.TaskID, req.TaskID, step, "", task.Status, "pending", "success", "", "operator requested step retry")
	OK(w, map[string]string{"task_id": req.TaskID, "step": step, "status": "pending"})
}

func (s *Server) handleTasksResetStep(w http.ResponseWriter, r *http.Request) {
	var req model.TaskResetStepRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" || req.Step == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "task_id and step required")
		return
	}
	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err := s.store.TaskSetStepForOperator(r.Context(), req.TaskID, req.Step); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	s.resolveValidationLoopEscalations(r, req.TaskID, req.Step, "operator reset step")
	s.recordActuation(r, "task.reset-step", defaultActor(req.ActorID), "task", req.TaskID, req.TaskID, req.Step, "", task.Status+"/"+task.CurrentStep, "pending/"+req.Step, "success", "", "operator reset task step")
	OK(w, map[string]string{"task_id": req.TaskID, "step": req.Step, "status": "pending"})
}

func (s *Server) handleTasksUnblock(w http.ResponseWriter, r *http.Request) {
	var req model.TaskUnblockRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" || strings.TrimSpace(req.Reason) == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "task_id and reason required")
		return
	}

	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if task.Status != "blocked" {
		Fail(w, http.StatusBadRequest, "invalid_state", "task must be blocked to unblock")
		return
	}

	if err := s.store.TaskUnblock(r.Context(), req.TaskID, req.Step, req.Reason, defaultActor(req.ActorID)); err != nil {
		Fail(w, http.StatusBadRequest, "unblock_failed", err.Error())
		return
	}

	step := req.Step
	if step == "" {
		step = task.CurrentStep
	}
	s.recordActuation(r, "task.unblock", defaultActor(req.ActorID), "task", req.TaskID, req.TaskID, step, "", task.Status, "pending", "success", "", req.Reason)
	OK(w, map[string]string{"task_id": req.TaskID, "status": "pending", "step": step})
}

func (s *Server) resolveValidationLoopEscalations(r *http.Request, taskID, step, outcome string) {
	escalations, err := s.store.EscalationList(r.Context(), taskID, "open")
	if err != nil {
		return
	}
	targetRef := "validation:" + step
	for _, esc := range escalations {
		if esc.TargetType == "parent_controller" && esc.TargetRef == targetRef {
			_ = s.store.EscalationResolve(r.Context(), esc.ID, outcome, defaultActor(""))
		}
	}
}

func (s *Server) handleTasksEscalate(w http.ResponseWriter, r *http.Request) {
	var req model.TaskEscalateRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" || req.TargetType == "" || req.Reason == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "task_id, target_type, and reason required")
		return
	}
	if !validEscalationTarget(req.TargetType) {
		Fail(w, http.StatusBadRequest, "invalid_target_type", "target_type must be one of human, ticketing, parent_controller, runtime_operator, policy_engine, external_system")
		return
	}
	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if req.RequestedAction == "" {
		req.RequestedAction = "investigate"
	}
	if req.StepName == "" {
		req.StepName = task.CurrentStep
	}
	esc := &model.Escalation{
		TaskID:          req.TaskID,
		StepName:        req.StepName,
		TargetType:      req.TargetType,
		TargetRef:       req.TargetRef,
		RequestedAction: req.RequestedAction,
		Reason:          req.Reason,
		EvidenceRefs:    req.EvidenceRefs,
		Status:          "open",
		CreatedByType:   "user",
		CreatedByID:     defaultActor(req.ActorID),
	}
	if err := s.store.EscalationCreate(r.Context(), esc); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	_ = s.store.TaskSetStatus(r.Context(), req.TaskID, "blocked")
	s.recordActuation(r, "task.escalate", defaultActor(req.ActorID), "task", req.TaskID, req.TaskID, req.StepName, "", "", "blocked", "success", "", req.Reason)
	OK(w, esc)
}

func (s *Server) handleEscalationsList(w http.ResponseWriter, r *http.Request) {
	escalations, err := s.store.EscalationList(r.Context(), r.URL.Query().Get("task_id"), r.URL.Query().Get("status"))
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, escalations)
}

func (s *Server) handleEscalationsResolve(w http.ResponseWriter, r *http.Request) {
	var req model.EscalationResolveRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.EscalationID == "" || req.Outcome == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "escalation_id and outcome required")
		return
	}
	if err := s.store.EscalationResolve(r.Context(), req.EscalationID, req.Outcome, defaultActor(req.ActorID)); err != nil {
		Fail(w, http.StatusBadRequest, "resolve_failed", err.Error())
		return
	}
	s.recordActuation(r, "escalation.resolve", defaultActor(req.ActorID), "escalation", req.EscalationID, "", "", "", "open", "resolved", "success", "", req.Outcome)
	OK(w, map[string]string{"escalation_id": req.EscalationID, "status": "resolved"})
}

func (s *Server) handleEventsList(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			Fail(w, http.StatusBadRequest, "invalid_limit", "limit must be positive")
			return
		}
		limit = n
	}
	events, err := s.store.ControlPlaneEvents(r.Context(), r.URL.Query().Get("task_id"), r.URL.Query().Get("target"), limit)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, events)
}

func (s *Server) refreshTaskState(r *http.Request, task *model.Task, actor string) error {
	status := "known"
	reason := "task state read from control-plane database"
	if task.Status == "running" {
		if agent, err := s.store.AgentGetByTask(r.Context(), task.ID); err != nil || agent == nil || agent.Status != "running" {
			status = "inconsistent"
			reason = "task is running without a running observed agent"
		}
	}
	if err := s.store.ControlObservationPut(r.Context(), &model.ControlObservation{
		TargetType: "task",
		TargetID:   task.ID,
		TaskID:     task.ID,
		Kind:       "task_state",
		Status:     status,
		Reason:     reason,
		Payload:    mustJSON(map[string]string{"status": task.Status, "current_step": task.CurrentStep}),
	}); err != nil {
		return err
	}
	s.recordActuation(r, "refresh.task", actor, "task", task.ID, task.ID, task.CurrentStep, "", "", status, "success", "", reason)
	return nil
}

func (s *Server) recordActuation(r *http.Request, op, actor, targetType, targetID, taskID, stepName, agentID, prev, next, outcome, errMsg, reason string) {
	_ = s.store.ControllerActuationAppend(r.Context(), &model.ControllerActuation{
		RequestedOperation: op,
		ActorType:          "user",
		ActorID:            actor,
		TargetType:         targetType,
		TargetID:           targetID,
		TaskID:             taskID,
		StepName:           stepName,
		AgentID:            agentID,
		PreviousState:      prev,
		NewState:           next,
		Outcome:            outcome,
		Error:              errMsg,
		Reason:             reason,
	})
	payload := mustJSON(map[string]string{"operation": op, "actor": actor, "target_type": targetType, "target_id": targetID, "outcome": outcome, "error": errMsg, "reason": reason})
	_ = s.store.TraceAppend(r.Context(), taskID, agentID, "control_plane.actuation", payload)
}

func defaultActor(actor string) string {
	if strings.TrimSpace(actor) == "" {
		return "operator"
	}
	return actor
}

func validEscalationTarget(target string) bool {
	switch target {
	case "human", "ticketing", "parent_controller", "runtime_operator", "policy_engine", "external_system":
		return true
	default:
		return false
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%q", fmt.Sprint(v))
	}
	return string(b)
}
