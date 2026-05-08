package model

import "time"

type Plan struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Repo struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Path             string    `json:"path"`
	TargetBranch     string    `json:"target_branch"`
	VerifyCommand    string    `json:"verify_command,omitempty"`
	LintCommand      string    `json:"lint_command,omitempty"`
	TypecheckCommand string    `json:"typecheck_command,omitempty"`
	AutoPush         bool      `json:"auto_push"`
	CreatedAt        time.Time `json:"created_at"`
}

type MergeQueueItem struct {
	ID             string     `json:"id"`
	TaskID         string     `json:"task_id"`
	RepoID         string     `json:"repo_id"`
	Branch         string     `json:"branch"`
	Target         string     `json:"target"`
	Status         string     `json:"status"`
	AttemptCount   int        `json:"attempt_count"`
	Priority       int        `json:"priority"`
	QueuedAt       time.Time  `json:"queued_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	MergeSHA       string     `json:"merge_sha,omitempty"`
	FailureLog     string     `json:"failure_log,omitempty"`
	WorktreePath   string     `json:"worktree_path,omitempty"`
	ConflictTaskID string     `json:"conflict_task_id,omitempty"`
}

type Task struct {
	ID           string         `json:"id"`
	Name         string         `json:"name,omitempty"`
	PlanID       string         `json:"plan_id,omitempty"`
	RepoID       string         `json:"repo_id,omitempty"`
	Title        string         `json:"title"`
	Body         string         `json:"body,omitempty"`
	Template     string         `json:"template,omitempty"`
	Role         string         `json:"role,omitempty"`
	Runtime      string         `json:"runtime,omitempty"`
	Priority     int            `json:"priority"`
	Status       string         `json:"status"`
	RetryCount   int            `json:"retry_count"`
	CurrentStep  string         `json:"current_step,omitempty"`
	StepAttempts map[string]int `json:"step_attempts,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	StartedAt    *time.Time     `json:"started_at,omitempty"`
	CompletedAt  *time.Time     `json:"completed_at,omitempty"`
}

type Agent struct {
	ID               string     `json:"id"`
	TaskID           string     `json:"task_id"`
	Slot             int        `json:"slot"`
	Status           string     `json:"status"`
	TmuxSession      string     `json:"tmux_session,omitempty"`
	Transport        string     `json:"transport,omitempty"`
	RuntimeSessionID string     `json:"runtime_session_id,omitempty"`
	PID              int        `json:"pid,omitempty"`
	LogfilePath      string     `json:"logfile_path,omitempty"`
	WorktreePath     string     `json:"worktree_path,omitempty"`
	Runtime          string     `json:"runtime,omitempty"`
	Model            string     `json:"model,omitempty"`
	StartedAt        time.Time  `json:"started_at"`
	LastHeartbeat    *time.Time `json:"last_heartbeat,omitempty"`
	LastEventAt      *time.Time `json:"last_event_at,omitempty"`
	LastStopReason   string     `json:"last_stop_reason,omitempty"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
}

type Trace struct {
	ID        int64     `json:"id"`
	TaskID    string    `json:"task_id,omitempty"`
	AgentID   string    `json:"agent_id,omitempty"`
	EventType string    `json:"event_type"`
	StepName  string    `json:"step_name,omitempty"`
	RetryNum  int       `json:"retry_num,omitempty"`
	Runtime   string    `json:"runtime,omitempty"`
	Model     string    `json:"model,omitempty"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentEvent struct {
	ID        int64     `json:"id"`
	AgentID   string    `json:"agent_id"`
	TaskID    string    `json:"task_id"`
	Seq       int64     `json:"seq"`
	Stream    string    `json:"stream"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

type ACPPermissionRequest struct {
	ID          string    `json:"id"`
	SessionName string    `json:"session_name"`
	SessionID   string    `json:"runtime_session_id,omitempty"`
	Command     string    `json:"command"`
	Policy      string    `json:"policy"`
	Options     []string  `json:"options"`
	CreatedAt   time.Time `json:"created_at"`
}

type Learning struct {
	ID           string     `json:"id"`
	Category     string     `json:"category"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	Tier         string     `json:"tier"`
	CreatedAt    time.Time  `json:"created_at"`
	LastAccessed *time.Time `json:"last_accessed,omitempty"`
	AccessCount  int        `json:"access_count"`
}

type PriorArtHistory struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	RepoID      string    `json:"repo_id,omitempty"`
	PlanID      string    `json:"plan_id,omitempty"`
	Title       string    `json:"title"`
	Body        string    `json:"body,omitempty"`
	Template    string    `json:"template,omitempty"`
	Status      string    `json:"status,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	SearchText  string    `json:"search_text,omitempty"`
	RiskScore   float64   `json:"risk_score"`
	ReworkScore float64   `json:"rework_score"`
	Tags        []string  `json:"tags"`
	Metadata    string    `json:"metadata,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type PriorArtSearchResult struct {
	TaskID        string   `json:"task_id"`
	Title         string   `json:"title"`
	Status        string   `json:"status"`
	Summary       string   `json:"summary"`
	ReworkScore   float64  `json:"rework_score"`
	RiskScore     float64  `json:"risk_score"`
	MatchedReason string   `json:"matched_reason"`
	KeyLessons    []string `json:"key_lessons"`
}

type PriorArtSearchResponse struct {
	Results []PriorArtSearchResult `json:"results"`
}

type PriorArtSearchRequest struct {
	Query          string  `json:"query,omitempty"`
	RepoID         string  `json:"repo_id,omitempty"`
	Template       string  `json:"template,omitempty"`
	Status         string  `json:"status,omitempty"`
	MinReworkScore float64 `json:"min_rework_score,omitempty"`
	MinRiskScore   float64 `json:"min_risk_score,omitempty"`
	Limit          int     `json:"limit,omitempty"`
}

// API request/response types

type CreatePlanRequest struct {
	Title        string `json:"title"`
	Body         string `json:"body"`
	WithPriorArt bool   `json:"with_prior_art,omitempty"`
}

type CreateRepoRequest struct {
	Name             string `json:"name"`
	Path             string `json:"path"`
	TargetBranch     string `json:"target_branch,omitempty"`
	VerifyCommand    string `json:"verify_command,omitempty"`
	LintCommand      string `json:"lint_command,omitempty"`
	TypecheckCommand string `json:"typecheck_command,omitempty"`
	AutoPush         bool   `json:"auto_push,omitempty"`
}

type CreateTaskRequest struct {
	PlanID   string `json:"plan_id,omitempty"`
	RepoID   string `json:"repo_id,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Template string `json:"template,omitempty"`
	Role     string `json:"role,omitempty"`
	Runtime  string `json:"runtime,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

type AddDepRequest struct {
	TaskID      string `json:"task_id"`
	DependsOnID string `json:"depends_on_id"`
}

type SetPriorityRequest struct {
	TaskID   string `json:"task_id"`
	Priority int    `json:"priority"`
}

type CloseTaskRequest struct {
	TaskID  string `json:"task_id"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	ActorID string `json:"actor_id,omitempty"`
}

type SignalRequest struct {
	TaskID             string              `json:"task_id"`
	Message            string              `json:"message,omitempty"`
	AcceptanceSpec     *AcceptanceSpec     `json:"acceptance_spec,omitempty"`
	DoneBundle         *DoneBundle         `json:"done_bundle,omitempty"`
	VerificationReport *VerificationReport `json:"verification_report,omitempty"`
}

type AgentSendRequest struct {
	AgentID string `json:"agent_id"`
	Message string `json:"message,omitempty"`
}

type AgentPermissionDecisionRequest struct {
	AgentID   string `json:"agent_id"`
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
}

type BootstrapRequest struct {
	TaskID string `json:"task_id"`
	Role   string `json:"role"`
	RepoID string `json:"repo_id"`
}

type BootstrapResponse struct {
	Task             *Task           `json:"task"`
	Repo             *Repo           `json:"repo,omitempty"`
	Role             string          `json:"role"`
	RoleBody         string          `json:"role_body,omitempty"`
	AcceptanceSpec   *AcceptanceSpec `json:"acceptance_spec,omitempty"`
	FailureContext   string          `json:"failure_context,omitempty"`
	Learnings        []Learning      `json:"learnings"`
	CLIReference     []string        `json:"cli_reference"`
	LintCommand      string          `json:"lint_command,omitempty"`
	TypecheckCommand string          `json:"typecheck_command,omitempty"`
}

type AcceptanceSpec struct {
	TaskID    string                `json:"task_id"`
	StepID    string                `json:"step_id,omitempty"`
	Path      string                `json:"path,omitempty"`
	SHA256    string                `json:"sha256,omitempty"`
	RiskLevel string                `json:"risk_level,omitempty"`
	Criteria  []AcceptanceCriterion `json:"criteria"`
}

type AcceptanceCriterion struct {
	ID                        string            `json:"id"`
	Description               string            `json:"description"`
	Probes                    []AcceptanceProbe `json:"probes"`
	RequiredArtifacts         []string          `json:"required_artifacts"`
	FailIf                    []string          `json:"fail_if"`
	RiskLevel                 string            `json:"risk_level,omitempty"`
	RequiresStateTransition   bool              `json:"requires_state_transition,omitempty"`
	RequiresNegativeAssertion bool              `json:"requires_negative_assertion,omitempty"`
}

type AcceptanceProbe struct {
	ID                   string   `json:"id"`
	Description          string   `json:"description"`
	Type                 string   `json:"type,omitempty"`
	Command              string   `json:"command,omitempty"`
	RequiredEvidence     []string `json:"required_evidence,omitempty"`
	Before               string   `json:"before,omitempty"`
	After                string   `json:"after,omitempty"`
	ObservableSideEffect string   `json:"observable_side_effect,omitempty"`
	NegativeAssertion    string   `json:"negative_assertion,omitempty"`
}

type DoneBundle struct {
	TaskID       string               `json:"task_id"`
	Summary      string               `json:"summary"`
	FilesChanged []string             `json:"files_changed"`
	TestsRun     []string             `json:"tests_run"`
	Claims       []CompletionClaim    `json:"claims"`
	Artifacts    []CompletionArtifact `json:"artifacts"`
	KnownRisks   []string             `json:"known_risks"`
}

type CompletionClaim struct {
	CriterionID string `json:"criterion_id"`
	Status      string `json:"status"`
}

type CompletionArtifact struct {
	Type          string `json:"type"`
	Path          string `json:"path,omitempty"`
	CriterionID   string `json:"criterion_id,omitempty"`
	ProbeID       string `json:"probe_id,omitempty"`
	Command       string `json:"command,omitempty"`
	ProducerStep  string `json:"producer_step,omitempty"`
	ProducerRole  string `json:"producer_role,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
	ContentHash   string `json:"content_hash,omitempty"`
	Authoritative bool   `json:"authoritative,omitempty"`
}

type VerificationReport struct {
	TaskID             string                `json:"task_id"`
	StepID             string                `json:"step_id,omitempty"`
	Path               string                `json:"path,omitempty"`
	SHA256             string                `json:"sha256,omitempty"`
	Results            []VerificationResult  `json:"results"`
	Failures           []VerificationFailure `json:"failures"`
	Confidence         string                `json:"confidence"`                    // agent-provided, decorative
	ComputedConfidence float64               `json:"computed_confidence,omitempty"` // deterministic, control-plane computed
	ConfidenceLabel    string                `json:"confidence_label,omitempty"`    // "low", "medium", "high"
	AdversarialReview  *AdversarialReview    `json:"adversarial_review,omitempty"`
}

type VerificationResult struct {
	CriterionID string     `json:"criterion_id"`
	Status      string     `json:"status"`
	Evidence    []Evidence `json:"evidence"`
	Reason      string     `json:"reason"`
}

type VerificationFailure struct {
	CriterionID string     `json:"criterion_id,omitempty"`
	Reason      string     `json:"reason"`
	Evidence    []Evidence `json:"evidence,omitempty"`
}

type Evidence struct {
	ArtifactID    string `json:"artifact_id,omitempty"`
	Type          string `json:"type"`
	Path          string `json:"path,omitempty"`
	Query         string `json:"query,omitempty"`
	Result        any    `json:"result,omitempty"`
	ProbeID       string `json:"probe_id,omitempty"`
	Command       string `json:"command,omitempty"`
	ProducerStep  string `json:"producer_step,omitempty"`
	ProducerRole  string `json:"producer_role,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
	ContentHash   string `json:"content_hash,omitempty"`
	Authoritative bool   `json:"authoritative,omitempty"`
}

type AdversarialReview struct {
	TaskID              string               `json:"task_id"`
	AdversarialFindings []AdversarialFinding `json:"adversarial_findings"`
	RequiredFollowup    bool                 `json:"required_followup"`
	AppendedProbeIDs    []string             `json:"appended_probe_ids,omitempty"`
	FollowupEvidence    []Evidence           `json:"followup_evidence,omitempty"`
	DismissalReason     string               `json:"dismissal_reason,omitempty"`
}

type AdversarialFinding struct {
	Risk           string `json:"risk"`
	SuggestedProbe string `json:"suggested_probe"`
	Severity       string `json:"severity"`
}

type Artifact struct {
	ID               string `json:"artifact_id"`
	TaskID           string `json:"task_id"`
	StepID           string `json:"step_id"`
	Producer         string `json:"producer"`
	ProducerType     string `json:"producer_type"`
	Path             string `json:"path"`
	ArtifactType     string `json:"artifact_type"`
	CreatedAt        string `json:"created_at"`
	SHA256           string `json:"sha256"`
	Command          string `json:"command,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	ExitCode         int    `json:"exit_code,omitempty"`
	Status           string `json:"status,omitempty"`
	InvalidatedAt    string `json:"invalidated_at,omitempty"`
}

type AddArtifactRequest struct {
	TaskID           string `json:"task_id"`
	StepID           string `json:"step_id,omitempty"`
	Producer         string `json:"producer"`
	ProducerType     string `json:"producer_type,omitempty"`
	Path             string `json:"path"`
	ArtifactType     string `json:"artifact_type"`
	SHA256           string `json:"sha256"`
	Command          string `json:"command,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	ExitCode         int    `json:"exit_code,omitempty"`
}

type VerificationExecutionPlan struct {
	TaskID string                 `json:"task_id"`
	Steps  []VerificationPlanStep `json:"steps"`
}

type VerificationPlanStep struct {
	ID               string         `json:"id"`
	ProbeID          string         `json:"probe_id"`
	Type             string         `json:"type"`
	Command          string         `json:"command,omitempty"`
	ExpectedExitCode *int           `json:"expected_exit_code,omitempty"`
	Method           string         `json:"method,omitempty"`
	URL              string         `json:"url,omitempty"`
	Body             map[string]any `json:"body,omitempty"`
	ExpectedStatus   int            `json:"expected_status,omitempty"`
	Script           string         `json:"script,omitempty"`
	Path             string         `json:"path,omitempty"`
	Assertion        string         `json:"assertion,omitempty"`
	Query            string         `json:"query,omitempty"`
	Produces         []string       `json:"produces"`
}

type ExecutionPlanValidationResult struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}

type Verdict struct {
	Status             string   `json:"status"`
	Reason             string   `json:"reason,omitempty"`
	ComputedConfidence float64  `json:"computed_confidence,omitempty"`
	ConfidenceLabel    string   `json:"confidence_label,omitempty"`
	RiskLevel          string   `json:"risk_level,omitempty"`
	ValidationErrors   []string `json:"validation_errors,omitempty"`
}

type AddLearningRequest struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

type CandidateLearning struct {
	ID               string `json:"learning_id"`
	SourceTraceID    string `json:"source_trace_id"`
	ProposedLearning string `json:"proposed_learning"`
	Reason           string `json:"reason"`
	Status           string `json:"status"`
	CreatedAt        string `json:"created_at"`
	ReviewedAt       string `json:"reviewed_at,omitempty"`
}

type AddCandidateLearningRequest struct {
	SourceTraceID    string `json:"source_trace_id"`
	ProposedLearning string `json:"proposed_learning"`
	Reason           string `json:"reason"`
}

type LearningEligibilityInput struct {
	FinalOutcomeMerged            bool    `json:"final_outcome_merged"`
	VerificationVerdict           string  `json:"verification_verdict"`
	ComputedConfidence            float64 `json:"computed_confidence"`
	RetryCount                    int     `json:"retry_count"`
	RetryThreshold                int     `json:"retry_threshold"`
	UnresolvedAcceptanceAmbiguity bool    `json:"unresolved_acceptance_ambiguity"`
	ArtifactProvenanceViolation   bool    `json:"artifact_provenance_violation"`
	ManualOverrideRequired        bool    `json:"manual_override_required"`
}

type AcceptanceRiskPolicy struct {
	HighRiskLabels []string `json:"high_risk_labels,omitempty"`
	HighRiskPaths  []string `json:"high_risk_paths,omitempty"`
}

type QueueSkipRequest struct {
	ItemID string `json:"item_id"`
}

type StatusResponse struct {
	Tasks         TaskStats             `json:"tasks"`
	Agents        AgentStats            `json:"agents"`
	Plans         PlanStats             `json:"plans"`
	MergeQueue    MergeQueueStat        `json:"merge_queue"`
	QueuePressure QueuePressureDecision `json:"queue_pressure,omitempty"`
}

type TaskStats struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
	Blocked int `json:"blocked"`
	Merged  int `json:"merged"`
}

type AgentStats struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Done    int `json:"done"`
	Killed  int `json:"killed"`
}

type PlanStats struct {
	Total  int `json:"total"`
	Active int `json:"active"`
}

type MergeQueueStat struct {
	Queued     int `json:"queued"`
	InProgress int `json:"in_progress"`
	Merged     int `json:"merged"`
	Failed     int `json:"failed"`
}

// CompiledWorkflow represents a compiled workflow graph persisted for a task.
// GraphJSON stores the graph as opaque JSON; GraphHash is the immutable content
// identity used to reject accidental recompilation drift for an existing task.
type CompiledWorkflow struct {
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	SourceType    string    `json:"source_type"`
	SourceName    string    `json:"source_name"`
	SourceRef     string    `json:"source_ref,omitempty"`
	PolicyVersion string    `json:"policy_version,omitempty"`
	GraphHash     string    `json:"graph_hash"`
	GraphJSON     string    `json:"graph_json"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// APIResponse is the standard envelope for all HTTP responses.
type APIResponse struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
