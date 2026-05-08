package model

import "time"

type ControlObservation struct {
	ID           string    `json:"id"`
	TargetType   string    `json:"target_type"`
	TargetID     string    `json:"target_id"`
	TaskID       string    `json:"task_id,omitempty"`
	AgentID      string    `json:"agent_id,omitempty"`
	WorktreePath string    `json:"worktree_path,omitempty"`
	Kind         string    `json:"kind"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason,omitempty"`
	EvidenceRefs []string  `json:"evidence_refs,omitempty"`
	Payload      string    `json:"payload,omitempty"`
	ObservedAt   time.Time `json:"observed_at"`
}

type ReconcilerDecision struct {
	ID                string    `json:"id"`
	Controller        string    `json:"controller"`
	ControllerVersion string    `json:"controller_version,omitempty"`
	TaskID            string    `json:"task_id,omitempty"`
	StepName          string    `json:"step_name,omitempty"`
	AgentID           string    `json:"agent_id,omitempty"`
	TargetType        string    `json:"target_type"`
	TargetID          string    `json:"target_id"`
	DecisionKind      string    `json:"decision_kind"`
	Action            string    `json:"action"`
	Reason            string    `json:"reason"`
	Retryable         bool      `json:"retryable"`
	EvidenceRefs      []string  `json:"evidence_refs,omitempty"`
	Payload           string    `json:"payload,omitempty"`
	DecidedAt         time.Time `json:"decided_at"`
}

type ControllerActuation struct {
	ID                 string    `json:"id"`
	RequestedOperation string    `json:"requested_operation"`
	ActorType          string    `json:"actor_type"`
	ActorID            string    `json:"actor_id"`
	IntentID           string    `json:"intent_id"`
	CorrelationID      string    `json:"correlation_id"`
	TargetType         string    `json:"target_type"`
	TargetID           string    `json:"target_id"`
	TaskID             string    `json:"task_id,omitempty"`
	StepName           string    `json:"step_name,omitempty"`
	AgentID            string    `json:"agent_id,omitempty"`
	PreviousState      string    `json:"previous_state,omitempty"`
	NewState           string    `json:"new_state,omitempty"`
	Outcome            string    `json:"outcome"`
	Error              string    `json:"error,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	EvidenceRefs       []string  `json:"evidence_refs,omitempty"`
	Payload            string    `json:"payload,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

type Escalation struct {
	ID                string     `json:"id"`
	TaskID            string     `json:"task_id,omitempty"`
	StepName          string     `json:"step_name,omitempty"`
	TargetType        string     `json:"target_type"`
	TargetRef         string     `json:"target_ref,omitempty"`
	RequestedAction   string     `json:"requested_action"`
	Reason            string     `json:"reason"`
	EvidenceRefs      []string   `json:"evidence_refs,omitempty"`
	Status            string     `json:"status"`
	Outcome           string     `json:"outcome,omitempty"`
	CreatedByType     string     `json:"created_by_type"`
	CreatedByID       string     `json:"created_by_id"`
	ResolvedByType    string     `json:"resolved_by_type,omitempty"`
	ResolvedByID      string     `json:"resolved_by_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	ResolvedAt        *time.Time `json:"resolved_at,omitempty"`
	FailureSignature  string     `json:"failure_signature,omitempty"`
	SuggestedCommands []string   `json:"suggested_commands,omitempty"`
}

type DesiredTaskState struct {
	TaskID            string            `json:"task_id"`
	TargetStatus      string            `json:"target_status"`
	CurrentStep       string            `json:"current_step,omitempty"`
	StepRequirements  []string          `json:"step_requirements,omitempty"`
	Runtime           string            `json:"runtime,omitempty"`
	RetryPolicy       string            `json:"retry_policy,omitempty"`
	TimeoutPolicy     string            `json:"timeout_policy,omitempty"`
	StepAttemptCounts map[string]int    `json:"step_attempt_counts,omitempty"`
	Fields            map[string]string `json:"fields,omitempty"`
}

type ObservedTaskState struct {
	TaskStatus             string              `json:"task_status"`
	Agent                  *Agent              `json:"agent,omitempty"`
	LatestObservation      *ControlObservation `json:"latest_observation,omitempty"`
	LatestValidation       *ControlObservation `json:"latest_validation,omitempty"`
	OpenEscalations        []*Escalation       `json:"open_escalations,omitempty"`
	AcceptanceSpecStored   bool                `json:"acceptance_spec_stored"`
	DoneBundleStored       bool                `json:"done_bundle_stored"`
	VerificationStored     bool                `json:"verification_stored"`
	VerificationVerdict    string              `json:"verification_verdict,omitempty"`
	LatestTrace            *Trace              `json:"latest_trace,omitempty"`
	LastAgentEventAt       *time.Time          `json:"last_agent_event_at,omitempty"`
	WorktreePath           string              `json:"worktree_path,omitempty"`
	WorktreeObservedStatus string              `json:"worktree_observed_status,omitempty"`
}

type TaskDiagnosis struct {
	Task           *Task                `json:"task"`
	Desired        DesiredTaskState     `json:"desired"`
	Observed       ObservedTaskState    `json:"observed"`
	ControlState   *TaskControlState    `json:"control_state,omitempty"`
	LatestDecision *ReconcilerDecision  `json:"latest_decision,omitempty"`
	LatestAction   *ControllerActuation `json:"latest_action,omitempty"`
	NextAction     string               `json:"next_action"`
	Reason         string               `json:"reason"`
	EvidenceRefs   []string             `json:"evidence_refs,omitempty"`
	RecentEvents   []*ControlPlaneEvent `json:"recent_events,omitempty"`
}

type ControlPlaneEvent struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"`
	Type       string    `json:"type"`
	TaskID     string    `json:"task_id,omitempty"`
	AgentID    string    `json:"agent_id,omitempty"`
	TargetType string    `json:"target_type,omitempty"`
	TargetID   string    `json:"target_id,omitempty"`
	Summary    string    `json:"summary"`
	Payload    string    `json:"payload,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type ReconcileRequest struct {
	TaskID  string `json:"task_id,omitempty"`
	ActorID string `json:"actor_id,omitempty"`
}

type RefreshRequest struct {
	TaskID  string `json:"task_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	ActorID string `json:"actor_id,omitempty"`
}

type TaskResetStepRequest struct {
	TaskID  string `json:"task_id"`
	Step    string `json:"step"`
	ActorID string `json:"actor_id,omitempty"`
}

type TaskEscalateRequest struct {
	TaskID          string   `json:"task_id"`
	StepName        string   `json:"step_name,omitempty"`
	TargetType      string   `json:"target_type"`
	TargetRef       string   `json:"target_ref,omitempty"`
	RequestedAction string   `json:"requested_action,omitempty"`
	Reason          string   `json:"reason"`
	EvidenceRefs    []string `json:"evidence_refs,omitempty"`
	ActorID         string   `json:"actor_id,omitempty"`
}

type TaskUnblockRequest struct {
	TaskID  string `json:"task_id"`
	Step    string `json:"step,omitempty"`
	Reason  string `json:"reason"`
	ActorID string `json:"actor_id,omitempty"`
}

type EscalationResolveRequest struct {
	EscalationID string `json:"escalation_id"`
	Outcome      string `json:"outcome"`
	ActorID      string `json:"actor_id,omitempty"`
}
