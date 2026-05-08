package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"strings"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
)

const validationRejectionEscalationThreshold = 5

// handleSignal returns a handler that sets a task status and appends a trace.
// Used for signals that do not need template routing (started, blocked).
func (s *Server) handleSignal(eventType, newStatus string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req model.SignalRequest
		if err := Decode(r, &req); err != nil {
			Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
			return
		}
		if req.TaskID == "" {
			Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
			return
		}
		if err := s.store.TaskSetStatus(r.Context(), req.TaskID, newStatus); err != nil {
			Fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		payload, _ := json.Marshal(req)
		if err := s.store.TraceAppend(r.Context(), req.TaskID, "", eventType, string(payload)); err != nil {
			Fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}

		// On terminal signals, close the agent row and clean up worktree.
		if newStatus == "done" || newStatus == "failed" || newStatus == "blocked" {
			agent, err := s.store.AgentGetByTask(r.Context(), req.TaskID)
			if err == nil && agent != nil {
				s.cleanupTerminalAgent(r.Context(), agent, true)
			}
		}

		OK(w, map[string]string{"task_id": req.TaskID, "status": newStatus})
	}
}

// handleSignalDone is template-aware: routes to next step or marks task done.
// When a task reaches done, it checks whether to enqueue it in the merge queue.
func (s *Server) handleSignalDone(w http.ResponseWriter, r *http.Request) {
	var req model.SignalRequest
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
	outcome, err := s.validateDoneSignal(r.Context(), task, &req)
	if err != nil {
		artifactKind := expectedSignalArtifactKind(task.CurrentStep)
		fcPayload, _ := json.Marshal(map[string]string{
			"step":          task.CurrentStep,
			"message":       err.Error(),
			"artifact_kind": artifactKind,
			"reason":        err.Error(),
			"source":        "validation_rejection",
		})
		_ = s.store.TraceAppend(r.Context(), req.TaskID, "", "step.failure_context", string(fcPayload))
		_ = s.store.ControlObservationPut(r.Context(), &model.ControlObservation{
			TargetType: "task",
			TargetID:   task.ID,
			TaskID:     task.ID,
			Kind:       "validation",
			Status:     "rejected",
			Reason:     err.Error(),
			Payload:    model.MarshalPayload(map[string]string{"step": task.CurrentStep}),
		})
		_ = s.store.ReconcilerDecisionAppend(r.Context(), &model.ReconcilerDecision{
			Controller:   "acceptance_controller",
			TaskID:       task.ID,
			StepName:     task.CurrentStep,
			TargetType:   "task",
			TargetID:     task.ID,
			DecisionKind: "validation_rejection",
			Action:       "reject_done_signal",
			Reason:       err.Error(),
			Retryable:    true,
		})
		s.maybeEscalateValidationLoop(r.Context(), task, err.Error())
		Fail(w, http.StatusBadRequest, "invalid_done_signal", err.Error())
		return
	}
	_ = s.store.ControlObservationPut(r.Context(), &model.ControlObservation{
		TargetType: "task",
		TargetID:   task.ID,
		TaskID:     task.ID,
		Kind:       "validation",
		Status:     "accepted",
		Reason:     "done signal payload passed step validation",
		Payload:    model.MarshalPayload(map[string]string{"step": task.CurrentStep, "outcome": outcome}),
	})
	_ = s.store.ReconcilerDecisionAppend(r.Context(), &model.ReconcilerDecision{
		Controller:   "acceptance_controller",
		TaskID:       task.ID,
		StepName:     task.CurrentStep,
		TargetType:   "task",
		TargetID:     task.ID,
		DecisionKind: "validation_acceptance",
		Action:       "accept_done_signal",
		Reason:       "done signal payload passed step validation",
		Retryable:    false,
	})

	payload, _ := json.Marshal(req)
	s.store.TraceAppend(r.Context(), req.TaskID, "", "signal.done", string(payload))

	if outcome == "failure" {
		fcPayload, _ := json.Marshal(map[string]string{"step": task.CurrentStep, "message": acceptanceFailureMessage(req.VerificationReport)})
		s.store.TraceAppend(r.Context(), req.TaskID, "", "step.failure_context", string(fcPayload))
	}

	if task.Template != "" && task.CurrentStep != "" && s.dispatcher != nil {
		agent, _ := s.store.AgentGetByTask(r.Context(), req.TaskID)
		if err := s.dispatcher.RouteStep(r.Context(), req.TaskID, task.CurrentStep, outcome); err != nil {
			slog.Error("route step on done", "task", req.TaskID, "err", err)
		}
		if agent != nil {
			s.cleanupTerminalAgent(r.Context(), agent, false)
		}
	} else {
		status := "done"
		if outcome == "failure" {
			status = "failed"
		}
		s.store.TaskSetStatus(r.Context(), req.TaskID, status)
		agent, _ := s.store.AgentGetByTask(r.Context(), req.TaskID)
		if agent != nil {
			s.cleanupTerminalAgent(r.Context(), agent, true)
		}
		// For non-template tasks, RouteStep is not called so we trigger merge enqueue here.
		if status == "done" {
			s.maybeEnqueueMerge(r, req.TaskID)
		}
	}

	OK(w, map[string]string{"task_id": req.TaskID})
}

func expectedSignalArtifactKind(step string) string {
	switch step {
	case "acceptance_spec":
		return "acceptance_spec"
	case "acceptance":
		return "verification_report"
	case "", "implement":
		return "done_bundle"
	default:
		return "done_bundle"
	}
}

func (s *Server) validateDoneSignal(ctx context.Context, task *model.Task, req *model.SignalRequest) (string, error) {
	step := task.CurrentStep
	if step == "acceptance_spec" {
		result := model.ValidateAcceptanceSpecDetailedWithPolicy(req.AcceptanceSpec, task.ID, task, s.acceptanceRiskPolicy())
		if !result.Valid {
			return "", fmt.Errorf("%s", strings.Join(result.Errors, "; "))
		}
		if err := s.store.AcceptanceSpecPutValidation(ctx, req.AcceptanceSpec, result); err != nil {
			return "", err
		}
		return "success", nil
	}

	spec, err := s.store.AcceptanceSpecGet(ctx, task.ID)
	if err != nil {
		return "", err
	}

	if step == "acceptance" {
		if _, err := model.ValidateVerificationReport(req.VerificationReport, task.ID, spec); err != nil {
			return "", err
		}
		bundle, err := s.store.DoneBundleGet(ctx, task.ID)
		if err != nil {
			return "", err
		}
		artifacts, err := s.store.ArtifactList(ctx, task.ID)
		if err != nil {
			return "", err
		}
		for _, artifact := range model.ArtifactHashMismatches(artifacts) {
			if err := s.store.ArtifactInvalidate(ctx, artifact.ID); err != nil {
				return "", err
			}
		}
		verdict := model.ComputeVerdictWithPolicy(spec, bundle, req.VerificationReport, artifacts, task, s.acceptanceRiskPolicy())
		if verdict.Status == "reject" {
			return "", fmt.Errorf("%s: %s", verdict.Reason, strings.Join(verdict.ValidationErrors, "; "))
		}
		req.VerificationReport.ComputedConfidence = verdict.ComputedConfidence
		req.VerificationReport.ConfidenceLabel = verdict.ConfidenceLabel
		storedVerdict := "fail"
		if verdict.Status == "pass" {
			storedVerdict = "pass"
		}
		if err := s.store.VerificationReportPutValidation(ctx, req.VerificationReport, storedVerdict, nil); err != nil {
			return "", err
		}

		if s.requiresAdversarialCheck(task.ID, verdict.RiskLevel) && !model.AdversarialReviewSatisfied(req.VerificationReport) {
			appended := model.AppendAdversarialProbes(spec, req.VerificationReport.AdversarialReview)
			if len(appended) > 0 {
				specValidation := model.ValidateAcceptanceSpecDetailedWithPolicy(spec, task.ID, task, s.acceptanceRiskPolicy())
				if err := s.store.AcceptanceSpecPutValidation(ctx, spec, specValidation); err != nil {
					return "", err
				}
				_ = s.store.TraceAppend(ctx, task.ID, "", "acceptance.adversarial_probes_appended", model.MarshalPayload(map[string]any{
					"probe_ids": appended,
				}))
			}
			slog.Warn("acceptance verification: adversarial check missing or failed",
				"task", task.ID,
				"risk_level", verdict.RiskLevel)
			return "failure", nil
		}

		// Routing decision: low confidence is treated as a soft failure
		// even if structurally valid, to prevent bad traces from becoming learnings.
		if verdict.ConfidenceLabel == "low" {
			slog.Warn("acceptance verification: low computed confidence",
				"task", task.ID,
				"confidence", verdict.ComputedConfidence,
				"label", verdict.ConfidenceLabel,
				"breakdown", model.VerifyConfidenceBreakdown(spec, req.VerificationReport, task.RetryCount))
			return "failure", nil
		}
		if verdict.Status == "retry_or_escalate" && verdict.Reason == "low_confidence" {
			slog.Warn("acceptance verification: computed confidence below task risk threshold",
				"task", task.ID,
				"risk_level", verdict.RiskLevel,
				"confidence", verdict.ComputedConfidence,
				"label", verdict.ConfidenceLabel,
				"required_label", model.RequiredConfidenceLabel(verdict.RiskLevel))
			return "failure", nil
		}
		if verdict.ConfidenceLabel == "medium" {
			slog.Warn("acceptance verification: medium computed confidence (proceeding with caution)",
				"task", task.ID,
				"confidence", verdict.ComputedConfidence,
				"label", verdict.ConfidenceLabel)
		}

		if verdict.Status == "pass" {
			return "success", nil
		}
		return "failure", nil
	}

	if shouldRequireDoneBundle(task) {
		if err := model.ValidateDoneBundle(req.DoneBundle, task.ID, spec); err != nil {
			return "", err
		}
		if err := s.store.DoneBundlePut(ctx, req.DoneBundle); err != nil {
			return "", err
		}
	}
	return "success", nil
}

func shouldRequireDoneBundle(task *model.Task) bool {
	if task.Template == "" {
		return true
	}
	switch task.CurrentStep {
	case "", "implement":
		return true
	default:
		return false
	}
}

func (s *Server) requiresAdversarialCheck(taskID, riskLevel string) bool {
	cfg, err := config.Load(s.homeDir)
	if err != nil {
		cfg = config.DefaultConfig()
	}
	adv := cfg.Acceptance.Adversarial
	if !adv.Enabled {
		return false
	}
	if model.RequiresAdversarialCheck(riskLevel) {
		return adv.AlwaysForHighRisk
	}
	if adv.SampleRate <= 0 {
		return false
	}
	if adv.SampleRate >= 1 {
		return true
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(taskID))
	bucket := float64(h.Sum32()%10000) / 10000.0
	return bucket < adv.SampleRate
}

func (s *Server) acceptanceRiskPolicy() *model.AcceptanceRiskPolicy {
	cfg, err := config.Load(s.homeDir)
	if err != nil {
		cfg = config.DefaultConfig()
	}
	return &model.AcceptanceRiskPolicy{
		HighRiskLabels: cfg.Acceptance.Risk.HighRiskLabels,
		HighRiskPaths:  cfg.Acceptance.Risk.HighRiskPaths,
	}
}

func acceptanceFailureMessage(report *model.VerificationReport) string {
	if report == nil {
		return "acceptance verification failed"
	}
	for _, failure := range report.Failures {
		if failure.Reason != "" {
			return failure.Reason
		}
	}
	for _, result := range report.Results {
		if result.Status == "fail" && result.Reason != "" {
			return result.Reason
		}
	}
	return "acceptance verification failed"
}

func (s *Server) maybeEscalateValidationLoop(ctx context.Context, task *model.Task, reason string) {
	if task == nil || task.ID == "" || task.CurrentStep == "" {
		return
	}
	count, err := s.store.ValidationRejectionCount(ctx, task.ID, task.CurrentStep)
	if err != nil || count < validationRejectionEscalationThreshold {
		return
	}
	targetRef := "validation:" + task.CurrentStep
	openEscalations, _ := s.store.EscalationList(ctx, task.ID, "open")
	for _, esc := range openEscalations {
		if esc.TargetType == "parent_controller" && esc.TargetRef == targetRef {
			return
		}
	}
	escalationReason := fmt.Sprintf("validation rejected %d done signals for step %s; latest: %s", count, task.CurrentStep, reason)
	_ = s.store.EscalationCreate(ctx, &model.Escalation{
		TaskID:          task.ID,
		StepName:        task.CurrentStep,
		TargetType:      "parent_controller",
		TargetRef:       targetRef,
		RequestedAction: "inspect_validation_loop",
		Reason:          escalationReason,
		EvidenceRefs:    []string{"controller_decisions.validation_rejection"},
		CreatedByType:   "controller",
		CreatedByID:     "acceptance_controller",
	})
	_ = s.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "acceptance_controller",
		TaskID:       task.ID,
		StepName:     task.CurrentStep,
		TargetType:   "task",
		TargetID:     task.ID,
		DecisionKind: "validation_rejection_loop",
		Action:       "escalate",
		Reason:       escalationReason,
		Retryable:    true,
		EvidenceRefs: []string{"controller_decisions.validation_rejection"},
		Payload:      model.MarshalPayload(map[string]string{"count": fmt.Sprintf("%d", count), "step": task.CurrentStep}),
	})
}

// maybeEnqueueMerge delegates to the merge processor (if wired up) after a task signals done.
// Handles both auto-merge enqueueing and conflict-resolver re-queueing.
func (s *Server) maybeEnqueueMerge(r *http.Request, taskID string) {
	if s.mergeProcessor == nil {
		return
	}
	go s.mergeProcessor.EnqueueIfAutoMerge(context.Background(), taskID)
}

// maybeHandleConflictFailed delegates to the merge processor (if wired up) after a task signals failed.
// Handles conflict-resolver failure recovery.
func (s *Server) maybeHandleConflictFailed(r *http.Request, taskID string) {
	if s.mergeProcessor == nil {
		return
	}
	go s.mergeProcessor.HandleConflictFailed(context.Background(), taskID)
}

// handleSignalFailed is template-aware: routes to failure step or marks task failed.
func (s *Server) handleSignalFailed(w http.ResponseWriter, r *http.Request) {
	var req model.SignalRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}

	payload, _ := json.Marshal(req)
	s.store.TraceAppend(r.Context(), req.TaskID, "", "signal.failed", string(payload))

	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	if task.Template != "" && task.CurrentStep != "" && s.dispatcher != nil {
		fcPayload, _ := json.Marshal(map[string]string{"step": task.CurrentStep, "message": req.Message})
		s.store.TraceAppend(r.Context(), req.TaskID, "", "step.failure_context", string(fcPayload))

		agent, _ := s.store.AgentGetByTask(r.Context(), req.TaskID)
		routeErr := s.dispatcher.RouteStep(r.Context(), req.TaskID, task.CurrentStep, "failure")
		if routeErr != nil {
			slog.Error("route step on failed", "task", req.TaskID, "err", routeErr)
			// If RouteStep couldn't advance the task (e.g. missing template),
			// ensure it's still marked failed so downstream recovery hooks fire.
			s.store.TaskSetStatus(r.Context(), req.TaskID, "failed")
		}
		if agent != nil {
			s.cleanupTerminalAgent(r.Context(), agent, false)
		}
	} else {
		s.store.TaskSetStatus(r.Context(), req.TaskID, "failed")
		agent, _ := s.store.AgentGetByTask(r.Context(), req.TaskID)
		if agent != nil {
			s.cleanupTerminalAgent(r.Context(), agent, true)
		}
	}
	// Check if this failed task is a conflict-resolver whose parent needs re-queuing.
	// Called for ALL failed signals — both template-aware and non-template paths.
	s.maybeHandleConflictFailed(r, req.TaskID)

	OK(w, map[string]string{"task_id": req.TaskID, "status": "failed"})
}

// handleSignalProgress updates the trace and agent heartbeat; does not change task status.
func (s *Server) handleSignalProgress(w http.ResponseWriter, r *http.Request) {
	var req model.SignalRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	payload, _ := json.Marshal(req)
	if err := s.store.TraceAppend(r.Context(), req.TaskID, "", "signal.progress", string(payload)); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	agent, err := s.store.AgentGetByTask(r.Context(), req.TaskID)
	if err == nil && agent != nil {
		s.store.AgentUpdateHeartbeat(r.Context(), agent.ID)
	}

	OK(w, map[string]string{"task_id": req.TaskID, "status": "running"})
}

func (s *Server) cleanupTerminalAgent(ctx context.Context, agent *model.Agent, removeWorktree bool) {
	s.store.AgentSetEnded(ctx, agent.ID)
	if s.dispatcher != nil && agent.TmuxSession != "" {
		payload, _ := json.Marshal(map[string]string{"reason": "terminal signal", "status": agent.Status})
		s.store.TraceAppend(ctx, agent.TaskID, agent.ID, "signal.cleaned_runtime", string(payload))
		s.dispatcher.KillSession(agent.TmuxSession)
	}
	// Clear PID for all agent types (tmux and ACP) so stale PIDs don't trigger
	// false-positive orphan kills on the next daemon restart.
	s.store.AgentSetRuntimePID(ctx, agent.ID, 0)
	if removeWorktree && agent.WorktreePath != "" && s.worktree != nil {
		go s.worktree.Remove(agent.WorktreePath)
	}
}
