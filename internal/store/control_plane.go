package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Store) ControlObservationPut(ctx context.Context, obs *model.ControlObservation) error {
	if obs.ID == "" {
		obs.ID = ulid.Make().String()
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now().UTC()
	}
	refs := marshalStringSlice(obs.EvidenceRefs)
	payload := obs.Payload
	if payload == "" {
		payload = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO control_observations
		 (id, target_type, target_id, task_id, agent_id, worktree_path, kind, status, reason, evidence_refs, payload, observed_at)
		 VALUES (?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), ?, ?, ?)`,
		obs.ID, obs.TargetType, obs.TargetID, obs.TaskID, obs.AgentID, obs.WorktreePath, obs.Kind, obs.Status, obs.Reason, refs, payload, obs.ObservedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ReconcilerDecisionAppend(ctx context.Context, d *model.ReconcilerDecision) error {
	if d.ID == "" {
		d.ID = ulid.Make().String()
	}
	if d.DecidedAt.IsZero() {
		d.DecidedAt = time.Now().UTC()
	}
	payload := d.Payload
	if payload == "" {
		payload = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO controller_decisions
		 (id, controller, controller_version, task_id, step_name, agent_id, target_type, target_id, decision_kind, action, reason, retryable, evidence_refs, payload, decided_at)
		 VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Controller, d.ControllerVersion, d.TaskID, d.StepName, d.AgentID, d.TargetType, d.TargetID, d.DecisionKind, d.Action, d.Reason, boolInt(d.Retryable), marshalStringSlice(d.EvidenceRefs), payload, d.DecidedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) ControllerActuationAppend(ctx context.Context, a *model.ControllerActuation) error {
	if a.ID == "" {
		a.ID = ulid.Make().String()
	}
	if a.IntentID == "" {
		a.IntentID = a.ID
	}
	if a.CorrelationID == "" {
		a.CorrelationID = a.IntentID
	}
	if a.ActorType == "" {
		a.ActorType = "system"
	}
	if a.ActorID == "" {
		a.ActorID = a.ActorType
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	payload := a.Payload
	if payload == "" {
		payload = "{}"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO controller_actuations
		 (id, requested_operation, actor_type, actor_id, intent_id, correlation_id, target_type, target_id, task_id, step_name, agent_id, previous_state, new_state, outcome, error, reason, evidence_refs, payload, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		a.ID, a.RequestedOperation, a.ActorType, a.ActorID, a.IntentID, a.CorrelationID, a.TargetType, a.TargetID, a.TaskID, a.StepName, a.AgentID, a.PreviousState, a.NewState, a.Outcome, a.Error, a.Reason, marshalStringSlice(a.EvidenceRefs), payload, a.CreatedAt.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) EscalationCreate(ctx context.Context, e *model.Escalation) error {
	if e.ID == "" {
		e.ID = ulid.Make().String()
	}
	if e.Status == "" {
		e.Status = "open"
	}
	if e.RequestedAction == "" {
		e.RequestedAction = "investigate"
	}
	if e.CreatedByType == "" {
		e.CreatedByType = "system"
	}
	if e.CreatedByID == "" {
		e.CreatedByID = e.CreatedByType
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	commands := marshalStringSlice(e.SuggestedCommands)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO escalations
		 (id, task_id, step_name, target_type, target_ref, requested_action, reason, evidence_refs, status, outcome, created_by_type, created_by_id, resolved_by_type, resolved_by_id, created_at, resolved_at, failure_signature, suggested_commands)
		 VALUES (?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULL, ?, ?)`,
		e.ID, e.TaskID, e.StepName, e.TargetType, e.TargetRef, e.RequestedAction, e.Reason, marshalStringSlice(e.EvidenceRefs), e.Status, e.Outcome, e.CreatedByType, e.CreatedByID, e.ResolvedByType, e.ResolvedByID, e.CreatedAt.UTC().Format(time.RFC3339), e.FailureSignature, commands)
	return err
}

func (s *Store) EscalationResolve(ctx context.Context, id, outcome, actorID string) error {
	if actorID == "" {
		actorID = "operator"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE escalations
		 SET status='resolved', outcome=?, resolved_by_type='user', resolved_by_id=?, resolved_at=?
		 WHERE id=? AND status != 'resolved'`,
		outcome, actorID, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("escalation %q not found or already resolved", id)
	}
	return nil
}

func (s *Store) EscalationList(ctx context.Context, taskID, status string) ([]*model.Escalation, error) {
	q := `SELECT id, COALESCE(task_id,''), COALESCE(step_name,''), target_type, COALESCE(target_ref,''), requested_action,
	             reason, evidence_refs, status, COALESCE(outcome,''), created_by_type, created_by_id,
	             COALESCE(resolved_by_type,''), COALESCE(resolved_by_id,''), created_at, COALESCE(resolved_at,''),
	             COALESCE(failure_signature,''), COALESCE(suggested_commands,'[]')
	      FROM escalations WHERE 1=1`
	var args []any
	if taskID != "" {
		q += ` AND task_id = ?`
		args = append(args, taskID)
	}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Escalation
	for rows.Next() {
		e, err := scanEscalation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EscalationGetOpenByTaskAndSignature(ctx context.Context, taskID, failureSignature string) (*model.Escalation, error) {
	e, err := scanEscalation(s.db.QueryRowContext(ctx,
		`SELECT id, COALESCE(task_id,''), COALESCE(step_name,''), target_type, COALESCE(target_ref,''), requested_action,
	                 reason, evidence_refs, status, COALESCE(outcome,''), created_by_type, created_by_id,
	                 COALESCE(resolved_by_type,''), COALESCE(resolved_by_id,''), created_at, COALESCE(resolved_at,''),
	                 COALESCE(failure_signature,''), COALESCE(suggested_commands,'[]')
	          FROM escalations
	         WHERE task_id = ? AND status = 'open' AND failure_signature = ?
	         ORDER BY created_at DESC
	         LIMIT 1`,
		taskID, failureSignature))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func (s *Store) ValidationRejectionCount(ctx context.Context, taskID, step string) (int, error) {
	var n int
	row := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		   FROM controller_decisions
		  WHERE task_id = ?
		    AND step_name = ?
		    AND controller = 'acceptance_controller'
		    AND decision_kind = 'validation_rejection'`,
		taskID, step)
	return n, row.Scan(&n)
}

func (s *Store) TaskSetStepForOperator(ctx context.Context, id, step string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	attemptsJSON := "{}"
	if task, err := s.TaskGet(ctx, id); err == nil {
		delete(task.StepAttempts, step)
		if b, err := json.Marshal(task.StepAttempts); err == nil {
			attemptsJSON = string(b)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE tasks
		 SET current_step=?, step_attempts=?, status='pending', completed_at=NULL, updated_at=?
		 WHERE id=?`,
		step, attemptsJSON, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("task %q not found", id)
	}
	if err := clearAcceptanceArtifactsForReset(ctx, tx, id, step); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TaskDiagnose(ctx context.Context, taskID string) (*model.TaskDiagnosis, error) {
	task, err := s.TaskGet(ctx, taskID)
	if err != nil {
		return nil, err
	}
	agent, _ := s.AgentGetByTask(ctx, taskID)
	latestObs, _ := s.latestObservation(ctx, taskID, "")
	latestValidation, _ := s.latestObservation(ctx, taskID, "validation")
	decision, _ := s.latestDecision(ctx, taskID)
	action, _ := s.latestActuation(ctx, taskID)
	traces, _ := s.TraceList(ctx, taskID, 1)
	var latestTrace *model.Trace
	if len(traces) > 0 {
		latestTrace = traces[0]
	}
	spec, _ := s.AcceptanceSpecGet(ctx, taskID)
	bundle, _ := s.DoneBundleGet(ctx, taskID)
	report, verdict, _ := s.VerificationReportGet(ctx, taskID)
	openEscalations, _ := s.EscalationList(ctx, taskID, "open")
	events, _ := s.ControlPlaneEvents(ctx, taskID, "", 20)

	observed := model.ObservedTaskState{
		TaskStatus:             task.Status,
		Agent:                  agent,
		LatestObservation:      latestObs,
		LatestValidation:       latestValidation,
		OpenEscalations:        openEscalations,
		AcceptanceSpecStored:   spec != nil,
		DoneBundleStored:       bundle != nil,
		VerificationStored:     report != nil,
		VerificationVerdict:    verdict,
		LatestTrace:            latestTrace,
		WorktreeObservedStatus: "",
	}
	if agent != nil {
		observed.WorktreePath = agent.WorktreePath
		observed.LastAgentEventAt = agent.LastEventAt
	}
	if latestObs != nil && latestObs.TargetType == "worktree" {
		observed.WorktreeObservedStatus = latestObs.Status
	}

	nextAction, reason := deriveNextAction(task, agent, latestObs, latestValidation, openEscalations)
	controlState := &model.TaskControlState{
		TaskID:        task.ID,
		DesiredStep:   task.CurrentStep,
		ObservedStep:  task.CurrentStep,
		RuntimeHealth: "unknown",
		Progress:      model.ProgressUnknown,
		UpdatedAt:     time.Now().UTC(),
	}
	if latestObs != nil {
		controlState.RuntimeHealth = latestObs.Status
		if latestObs.Kind == "agent_health" {
			controlState.LastActuation = latestObs.Reason
		}
	}
	if decision != nil {
		controlState.ErrorCategory = decision.DecisionKind
		controlState.LastActuation = decision.Action
		var payload model.AgentControllerDecisionPayload
		if err := json.Unmarshal([]byte(decision.Payload), &payload); err == nil {
			if payload.Progress != "" {
				controlState.Progress = payload.Progress
			}
			controlState.OscillationScore = payload.OscillationScore
			controlState.EscalationLevel = payload.EscalationLevel
		}
	}
	_ = s.TaskControlStatePut(ctx, controlState)
	if persisted, err := s.TaskControlStateGet(ctx, task.ID); err == nil && persisted != nil {
		controlState = persisted
	}
	return &model.TaskDiagnosis{
		Task: task,
		Desired: model.DesiredTaskState{
			TaskID:            task.ID,
			TargetStatus:      desiredTargetStatus(task),
			CurrentStep:       task.CurrentStep,
			StepRequirements:  requirementsForStep(task.CurrentStep),
			Runtime:           task.Runtime,
			RetryPolicy:       "bounded automatic retry; escalation after budget exhaustion",
			TimeoutPolicy:     "controller-owned stale progress detection",
			StepAttemptCounts: task.StepAttempts,
			Fields: map[string]string{
				"template": task.Template,
				"role":     task.Role,
			},
		},
		Observed:       observed,
		ControlState:   controlState,
		LatestDecision: decision,
		LatestAction:   action,
		NextAction:     nextAction,
		Reason:         reason,
		RecentEvents:   events,
	}, nil
}

func (s *Store) ControlPlaneEvents(ctx context.Context, taskID, target string, limit int) ([]*model.ControlPlaneEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `
		SELECT 'trace', CAST(id AS TEXT), event_type, COALESCE(task_id,''), COALESCE(agent_id,''), '', '', event_type, payload, created_at
		  FROM traces WHERE (? = '' OR task_id = ? OR agent_id = ?)
		UNION ALL
		SELECT 'observation', id, kind, COALESCE(task_id,''), COALESCE(agent_id,''), target_type, target_id, status || ': ' || COALESCE(reason,''), payload, observed_at
		  FROM control_observations WHERE (? = '' OR task_id = ? OR agent_id = ? OR target_id = ?)
		UNION ALL
		SELECT 'decision', id, decision_kind, COALESCE(task_id,''), COALESCE(agent_id,''), target_type, target_id, action || ': ' || reason, payload, decided_at
		  FROM controller_decisions WHERE (? = '' OR task_id = ? OR agent_id = ? OR target_id = ?)
		UNION ALL
		SELECT 'actuation', id, requested_operation, COALESCE(task_id,''), COALESCE(agent_id,''), target_type, target_id, outcome || ': ' || COALESCE(reason,''), payload, created_at
		  FROM controller_actuations WHERE (? = '' OR task_id = ? OR agent_id = ? OR target_id = ?)
		UNION ALL
		SELECT 'escalation', id, status, COALESCE(task_id,''), '', target_type, COALESCE(target_ref,''), requested_action || ': ' || reason, '', created_at
		  FROM escalations WHERE (? = '' OR task_id = ? OR target_ref = ?)
		ORDER BY created_at DESC
		LIMIT ?`
	filter := taskID
	if target != "" {
		filter = target
	}
	rows, err := s.db.QueryContext(ctx, q,
		filter, filter, filter,
		filter, filter, filter, filter,
		filter, filter, filter, filter,
		filter, filter, filter, filter,
		filter, filter, filter,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*model.ControlPlaneEvent
	for rows.Next() {
		var ev model.ControlPlaneEvent
		var created string
		if err := rows.Scan(&ev.Source, &ev.ID, &ev.Type, &ev.TaskID, &ev.AgentID, &ev.TargetType, &ev.TargetID, &ev.Summary, &ev.Payload, &created); err != nil {
			return nil, err
		}
		ev.CreatedAt, _ = time.Parse(time.RFC3339, created)
		events = append(events, &ev)
	}
	return events, rows.Err()
}

func (s *Store) latestObservation(ctx context.Context, taskID, kind string) (*model.ControlObservation, error) {
	q := `SELECT id, target_type, target_id, COALESCE(task_id,''), COALESCE(agent_id,''), COALESCE(worktree_path,''), kind, status,
	             COALESCE(reason,''), evidence_refs, payload, observed_at
	      FROM control_observations WHERE task_id = ?`
	args := []any{taskID}
	if kind != "" {
		q += ` AND kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY observed_at DESC LIMIT 1`
	obs, err := scanControlObservation(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return obs, err
}

func (s *Store) latestDecision(ctx context.Context, taskID string) (*model.ReconcilerDecision, error) {
	d, err := scanReconcilerDecision(s.db.QueryRowContext(ctx,
		`SELECT id, controller, COALESCE(controller_version,''), COALESCE(task_id,''), COALESCE(step_name,''), COALESCE(agent_id,''), target_type, target_id,
		        decision_kind, action, reason, retryable, evidence_refs, payload, decided_at
		 FROM controller_decisions WHERE task_id = ? ORDER BY decided_at DESC LIMIT 1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

func (s *Store) latestActuation(ctx context.Context, taskID string) (*model.ControllerActuation, error) {
	a, err := scanControllerActuation(s.db.QueryRowContext(ctx,
		`SELECT id, requested_operation, actor_type, actor_id, intent_id, correlation_id, target_type, target_id, COALESCE(task_id,''), COALESCE(step_name,''), COALESCE(agent_id,''),
		        COALESCE(previous_state,''), COALESCE(new_state,''), outcome, COALESCE(error,''), COALESCE(reason,''), evidence_refs, payload, created_at
		 FROM controller_actuations WHERE task_id = ? ORDER BY created_at DESC LIMIT 1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

func (s *Store) TaskControlStatePut(ctx context.Context, state *model.TaskControlState) error {
	if state == nil {
		return fmt.Errorf("state is required")
	}
	if state.TaskID == "" {
		return fmt.Errorf("state.task_id is required")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	failureSignature := marshalFailureSignature(state.FailureSignature)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO task_control_states
		 (task_id, desired_step, observed_step, runtime_health, progress, error_category, last_actuation,
		  escalation_level, oscillation_score, failure_signature, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(task_id) DO UPDATE SET
		   desired_step = excluded.desired_step,
		   observed_step = excluded.observed_step,
		   runtime_health = excluded.runtime_health,
		   progress = excluded.progress,
		   error_category = excluded.error_category,
		   last_actuation = excluded.last_actuation,
		   escalation_level = excluded.escalation_level,
		   oscillation_score = excluded.oscillation_score,
		   failure_signature = excluded.failure_signature,
		   updated_at = excluded.updated_at`,
		state.TaskID, state.DesiredStep, state.ObservedStep, state.RuntimeHealth, state.Progress,
		state.ErrorCategory, state.LastActuation, state.EscalationLevel, state.OscillationScore, failureSignature, state.UpdatedAt.Format(time.RFC3339))
	return err
}

func (s *Store) TaskControlStateGet(ctx context.Context, taskID string) (*model.TaskControlState, error) {
	state, err := scanTaskControlState(s.db.QueryRowContext(ctx,
		`SELECT task_id, desired_step, observed_step, runtime_health, progress, error_category, last_actuation,
		        escalation_level, oscillation_score, failure_signature, updated_at
		   FROM task_control_states WHERE task_id = ?`, taskID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return state, err
}

func scanControlObservation(row interface{ Scan(dest ...any) error }) (*model.ControlObservation, error) {
	var obs model.ControlObservation
	var refs, observed string
	if err := row.Scan(&obs.ID, &obs.TargetType, &obs.TargetID, &obs.TaskID, &obs.AgentID, &obs.WorktreePath, &obs.Kind, &obs.Status, &obs.Reason, &refs, &obs.Payload, &observed); err != nil {
		return nil, err
	}
	obs.EvidenceRefs = unmarshalStringSlice(refs)
	obs.ObservedAt, _ = time.Parse(time.RFC3339, observed)
	return &obs, nil
}

func scanReconcilerDecision(row interface{ Scan(dest ...any) error }) (*model.ReconcilerDecision, error) {
	var d model.ReconcilerDecision
	var retryable int
	var refs, decided string
	if err := row.Scan(&d.ID, &d.Controller, &d.ControllerVersion, &d.TaskID, &d.StepName, &d.AgentID, &d.TargetType, &d.TargetID, &d.DecisionKind, &d.Action, &d.Reason, &retryable, &refs, &d.Payload, &decided); err != nil {
		return nil, err
	}
	d.Retryable = retryable != 0
	d.EvidenceRefs = unmarshalStringSlice(refs)
	d.DecidedAt, _ = time.Parse(time.RFC3339, decided)
	return &d, nil
}

func scanControllerActuation(row interface{ Scan(dest ...any) error }) (*model.ControllerActuation, error) {
	var a model.ControllerActuation
	var refs, created string
	if err := row.Scan(&a.ID, &a.RequestedOperation, &a.ActorType, &a.ActorID, &a.IntentID, &a.CorrelationID, &a.TargetType, &a.TargetID, &a.TaskID, &a.StepName, &a.AgentID, &a.PreviousState, &a.NewState, &a.Outcome, &a.Error, &a.Reason, &refs, &a.Payload, &created); err != nil {
		return nil, err
	}
	a.EvidenceRefs = unmarshalStringSlice(refs)
	a.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return &a, nil
}

func scanTaskControlState(row interface{ Scan(dest ...any) error }) (*model.TaskControlState, error) {
	var state model.TaskControlState
	var rawFailureSig string
	var updated string
	if err := row.Scan(&state.TaskID, &state.DesiredStep, &state.ObservedStep, &state.RuntimeHealth, &state.Progress, &state.ErrorCategory, &state.LastActuation,
		&state.EscalationLevel, &state.OscillationScore, &rawFailureSig, &updated); err != nil {
		return nil, err
	}
	state.FailureSignature, _ = unmarshalFailureSignature(rawFailureSig)
	state.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return &state, nil
}

func scanEscalation(row interface{ Scan(dest ...any) error }) (*model.Escalation, error) {
	var e model.Escalation
	var refs, created, resolved, commands string
	if err := row.Scan(&e.ID, &e.TaskID, &e.StepName, &e.TargetType, &e.TargetRef, &e.RequestedAction, &e.Reason, &refs, &e.Status, &e.Outcome, &e.CreatedByType, &e.CreatedByID, &e.ResolvedByType, &e.ResolvedByID, &created, &resolved, &e.FailureSignature, &commands); err != nil {
		return nil, err
	}
	e.EvidenceRefs = unmarshalStringSlice(refs)
	e.SuggestedCommands = unmarshalStringSlice(commands)
	e.CreatedAt, _ = time.Parse(time.RFC3339, created)
	if resolved != "" {
		t, _ := time.Parse(time.RFC3339, resolved)
		e.ResolvedAt = &t
	}
	return &e, nil
}

func deriveNextAction(task *model.Task, agent *model.Agent, latestObservation, validation *model.ControlObservation, escalations []*model.Escalation) (string, string) {
	if len(escalations) > 0 {
		return "await_escalation_resolution", "task has an open escalation"
	}
	switch task.Status {
	case "pending":
		if latestObservation != nil && latestObservation.Kind == "dispatch" && latestObservation.Status == "failed" {
			return "inspect_runtime", "latest dispatch failed: " + latestObservation.Reason
		}
		return "dispatch_when_eligible", "task is pending"
	case "running":
		if agent == nil {
			return "reconcile_task", "task is running with no observed agent"
		}
		if agent.Status != "running" {
			return "reconcile_task", "task is running but latest agent is not running"
		}
		if validation != nil && validation.Status == "rejected" && !validation.ObservedAt.Before(agent.StartedAt) {
			return "retry_or_reset_step", validation.Reason
		}
		if agent.LastEventAt != nil {
			staleFor := time.Since(*agent.LastEventAt)
			if staleFor > 2*time.Minute {
				return "inspect_or_nudge_agent", fmt.Sprintf("latest agent is running but ACP events are stale for %s", staleFor.Round(time.Second))
			}
		}
		return "wait_for_progress", "latest agent is running"
	case "failed":
		return "retry_or_escalate", "task failed"
	case "blocked":
		if validation != nil && validation.Status == "rejected" {
			return "retry_or_reset_step", validation.Reason
		}
		return "resolve_blocker_or_escalate", "task is blocked"
	case "done", "merged", "closed":
		return "none", "task is terminal"
	default:
		if validation != nil && validation.Status == "rejected" {
			return "retry_or_reset_step", validation.Reason
		}
		return "inspect", "task status is not recognized by diagnosis"
	}
}

func desiredTargetStatus(task *model.Task) string {
	switch task.Status {
	case "done", "merged", "failed", "closed":
		return task.Status
	default:
		return "done"
	}
}

func requirementsForStep(step string) []string {
	switch step {
	case "acceptance_spec":
		return []string{"stored valid acceptance spec"}
	case "implement":
		return []string{"stored valid done bundle", "files changed match acceptance claims"}
	case "acceptance":
		return []string{"stored valid verification report", "per-probe evidence coverage", "high computed confidence"}
	case "test":
		return []string{"verification command evidence", "test artifacts"}
	case "":
		return nil
	default:
		return []string{"step-specific valid output"}
	}
}

func marshalStringSlice(values []string) string {
	if values == nil {
		return "[]"
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func unmarshalStringSlice(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func marshalFailureSignature(sig *model.FailureSignature) string {
	if sig == nil {
		return "{}"
	}
	b, err := json.Marshal(sig)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func unmarshalFailureSignature(raw string) (*model.FailureSignature, bool) {
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return nil, true
	}
	var sig model.FailureSignature
	if err := json.Unmarshal([]byte(raw), &sig); err != nil {
		return nil, false
	}
	return &sig, true
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
