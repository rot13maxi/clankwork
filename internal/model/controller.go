package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Controller error category constants.
// These categorize the delta between desired and observed state.
const (
	ControllerErrorNone               = ""
	ControllerErrorSlotsUnderutilized = "slots_underutilized"
	ControllerErrorSlotsOvercommitted = "slots_overcommitted"
	ControllerErrorQueuePressured     = "queue_pressured"
	ControllerErrorAgentStale         = "agent_stale"
	ControllerErrorAgentDead          = "agent_dead"
	ControllerErrorAgentStalling      = "agent_stalling"
	ControllerErrorAgentContextLimit  = "agent_context_limit"
	ControllerErrorAgentNoProgress    = "agent_no_progress"
	ControllerErrorOscillation        = "oscillation"
)

// Controller action constants for ControllerDecision.Action.
const (
	ControllerActionNone          = ""
	ControllerActionDispatch      = "dispatch"
	ControllerActionPause         = "pause"
	ControllerActionResume        = "resume"
	ControllerActionEscalateModel = "escalate_model"
	ControllerActionKill          = "kill"
	ControllerActionNudge         = "nudge"
	ControllerActionHandoff       = "handoff"
	ControllerActionBlock         = "block"
	ControllerActionNoOp          = "no_op"
)

// Progress evidence states. These describe convergence evidence, not semantic
// correctness.
const (
	ProgressUnknown = "progress_unknown"
	ProgressPresent = "progress_present"
	ProgressAbsent  = "progress_absent"
)

// QueuePressureLevel values are ordered from least to most restrictive.
const (
	QueuePressureNone    = "none"
	QueuePressureReduced = "reduced"
	QueuePressureDrain   = "drain"
	QueuePressureHard    = "hard"
)

// DefaultOscillationThreshold is the first conservative threshold where the
// controller should change recovery category instead of repeating a low-cost fix.
const DefaultOscillationThreshold = 3

// Controller actuation outcome constants.
const (
	ActuationOutcomeSuccess = "success"
	ActuationOutcomeFailure = "failure"
	ActuationOutcomeSkipped = "skipped"
)

// AgentHealth constants for AgentDecision.Health.
const (
	AgentHealthHealthy = "healthy"
	AgentHealthWarning = "warning"
	AgentHealthStalled = "stalled"
	AgentHealthDead    = "dead"
)

// DesiredState describes the configuration targets the controller aims to maintain.
type DesiredState struct {
	// MaxSlots is the configured maximum number of concurrent agent slots.
	MaxSlots int
	// QueuePressureThreshold is the merge queue depth at which dispatch is paused.
	QueuePressureThreshold int
	// HeartbeatTimeout is how long an agent can go without a heartbeat before
	// the reconciler treats it as stale.
	HeartbeatTimeout time.Duration
}

// ObservedState describes a snapshot of the system's current condition.
type ObservedState struct {
	// RunningAgentCount is how many agents are currently in "running" status.
	RunningAgentCount int
	// ReadyTaskCount is how many tasks are ready to be dispatched.
	ReadyTaskCount int
	// MergeQueueDepth is how many items are currently in the merge queue.
	MergeQueueDepth int
	// RunningAgents is the list of agents that are currently running.
	RunningAgents []string
	// AvailableSlots is MaxSlots - RunningAgentCount.
	AvailableSlots int
}

// ComputedState combines desired and observed state and the derived delta between them.
type ComputedState struct {
	Desired  DesiredState
	Observed ObservedState
	// SlotsDelta is RunningAgentCount - MaxSlots.
	// Positive means overcommitted, negative means underutilized.
	SlotsDelta int
	// QueuePressured is true when MergeQueueDepth >= QueuePressureThreshold.
	QueuePressured bool
	// CanDispatch is true when AvailableSlots > 0 and ReadyTaskCount > 0 and not QueuePressured.
	CanDispatch bool
}

// ControllerError describes the delta between desired and observed state
// that requires a controller response.
type ControllerError struct {
	// Category classifies the error type using constants defined in this package.
	Category string
	// Message is a human-readable description of the error.
	Message string
	// Magnitude quantifies the error (e.g., number of excess/fewer agents,
	// seconds since last heartbeat). Zero for categories that don't need it.
	Magnitude int
	// AffectedAgentID is set for agent-level errors (e.g., stale heartbeat).
	AffectedAgentID string
}

// ControllerDecision is the output of a single controller evaluation pass.
// It expresses what action (if any) the controller should take.
type ControllerDecision struct {
	// Action is one of the ControllerAction constants.
	Action string
	// Reason is a human-readable explanation of why this action was chosen.
	Reason string
	// Error is the ControllerError that drove this decision, if any.
	// nil when Action is NoOp or Resume.
	Error *ControllerError
	// TasksToDispatch is the number of tasks the dispatcher should attempt
	// in this tick. Set by dispatch decisions.
	TasksToDispatch int
	// TargetAgentID is the agent affected by this decision (kill, nudge, etc.).
	TargetAgentID string
	// TargetRuntime is the runtime to escalate to, for escalate_model actions.
	TargetRuntime string
}

// AgentObservedState describes the observed state of a single agent.
type AgentObservedState struct {
	AgentID          string
	TaskID           string
	SessionAlive     bool
	HeartbeatStale   bool
	PaneActive       bool
	LastHeartbeatAge time.Duration
	LastPaneActivity time.Duration
	Progress         string
	FailureSignature *FailureSignature
	NoSignalTurns    int
	RepeatedFailures int
	NudgeTimeouts    int
}

// AgentDecision is the output of the reconciler's per-agent evaluation.
// It expresses the health assessment and recommended action for one agent.
type AgentDecision struct {
	AgentID string
	Health  string // one of the AgentHealth constants

	// Action is the recommended controller action for this agent.
	Action string

	// Reason is a human-readable explanation.
	Reason string

	// Error is the controller error for this agent, if any.
	// nil when Health is Healthy.
	Error *ControllerError

	// NudgeSent is true if a nudge has already been sent to this agent
	// in this stall event. Used to track nudge timeout.
	NudgeSent bool

	// Progress records the progress-evidence bucket used for this decision.
	Progress string
	// OscillationScore records repeated unresolved-error evidence.
	OscillationScore int
	// EscalationLevel is a coarse 0..3 policy band for increasingly expensive
	// recovery.
	EscalationLevel int
	// FailureSignature records the stable failure cluster used for oscillation
	// decisions, when one is available.
	FailureSignature *FailureSignature
}

// TaskControlState is a compact controller-facing summary of one task/agent pair.
// It is deliberately small enough to trace and inspect without pretending to be
// a semantic-correctness proof.
type TaskControlState struct {
	TaskID           string
	DesiredStep      string
	ObservedStep     string
	RuntimeHealth    string
	Progress         string
	ErrorCategory    string
	LastActuation    string
	EscalationLevel  int
	OscillationScore int
	FailureSignature *FailureSignature
	UpdatedAt        time.Time
}

// FailureSignature is a stable, normalized identifier for a failure cluster.
type FailureSignature struct {
	Source         string            `json:"source"`
	Step           string            `json:"step,omitempty"`
	Command        string            `json:"command,omitempty"`
	Class          string            `json:"class,omitempty"`
	StableFields   map[string]string `json:"stable_fields,omitempty"`
	NormalizedHash string            `json:"normalized_hash"`
}

// QueuePressureSnapshot is the observed queue state used to choose a pressure
// level. Age and failures let the controller respond before depth alone becomes
// catastrophic.
type QueuePressureSnapshot struct {
	Depth          int
	OldestAge      time.Duration
	RecentFailures int
	ConflictCount  int
}

// QueuePressureDecision is the deterministic dispatch-pressure policy output.
type QueuePressureDecision struct {
	Level        string
	MaxDispatch  int
	ShouldPause  bool
	Reason       string
	Observed     QueuePressureSnapshot
	TargetDepth  int
	OldestAgeMax time.Duration
}

// ComputeState derives a ComputedState from desired and observed state.
func ComputeState(desired DesiredState, observed ObservedState) ComputedState {
	observed.AvailableSlots = desired.MaxSlots - observed.RunningAgentCount
	slotsDelta := observed.RunningAgentCount - desired.MaxSlots
	queuePressured := desired.QueuePressureThreshold > 0 && observed.MergeQueueDepth >= desired.QueuePressureThreshold
	canDispatch := observed.AvailableSlots > 0 && observed.ReadyTaskCount > 0 && !queuePressured
	return ComputedState{
		Desired:        desired,
		Observed:       observed,
		SlotsDelta:     slotsDelta,
		QueuePressured: queuePressured,
		CanDispatch:    canDispatch,
	}
}

// ComputeError derives a ControllerError from a ComputedState.
// Returns nil when the error is benign (no error).
func ComputeError(cs ComputedState) *ControllerError {
	if cs.SlotsDelta > 0 {
		return &ControllerError{
			Category:  ControllerErrorSlotsOvercommitted,
			Message:   fmt.Sprintf("system is overcommitted by %d slots (%d running, max %d)", cs.SlotsDelta, cs.Observed.RunningAgentCount, cs.Desired.MaxSlots),
			Magnitude: cs.SlotsDelta,
		}
	}
	if cs.SlotsDelta < 0 && cs.CanDispatch {
		return &ControllerError{
			Category:  ControllerErrorSlotsUnderutilized,
			Message:   fmt.Sprintf("system is underutilized by %d slots (%d available, %d tasks ready)", -cs.SlotsDelta, cs.Observed.AvailableSlots, cs.Observed.ReadyTaskCount),
			Magnitude: -cs.SlotsDelta,
		}
	}
	if cs.QueuePressured {
		return &ControllerError{
			Category:  ControllerErrorQueuePressured,
			Message:   fmt.Sprintf("merge queue depth %d exceeds pressure threshold %d", cs.Observed.MergeQueueDepth, cs.Desired.QueuePressureThreshold),
			Magnitude: cs.Observed.MergeQueueDepth,
		}
	}
	return nil
}

// DecisionFromError constructs a ControllerDecision from a computed error
// and the desired dispatch count. The action is determined by the error category.
func DecisionFromError(err *ControllerError, tasksToDispatch int) ControllerDecision {
	if err == nil {
		return ControllerDecision{Action: ControllerActionNoOp, Reason: "system in desired state"}
	}
	switch err.Category {
	case ControllerErrorSlotsOvercommitted:
		return ControllerDecision{
			Action: ControllerActionNoOp,
			Reason: "overcommitted, waiting for agents to complete",
			Error:  err,
		}
	case ControllerErrorSlotsUnderutilized:
		return ControllerDecision{
			Action:          ControllerActionDispatch,
			Reason:          err.Message,
			Error:           err,
			TasksToDispatch: min(tasksToDispatch, err.Magnitude),
		}
	case ControllerErrorQueuePressured:
		return ControllerDecision{
			Action: ControllerActionPause,
			Reason: err.Message,
			Error:  err,
		}
	}
	return ControllerDecision{Action: ControllerActionNoOp, Reason: "unknown error category", Error: err}
}

// ComputeAgentError derives a ControllerError for an agent from its observed state.
func ComputeAgentError(s AgentObservedState, heartbeatTimeout time.Duration) *ControllerError {
	if !s.SessionAlive {
		return &ControllerError{
			Category:        ControllerErrorAgentDead,
			Message:         "agent session is not alive",
			Magnitude:       0,
			AffectedAgentID: s.AgentID,
		}
	}
	if s.HeartbeatStale && !s.PaneActive {
		return &ControllerError{
			Category:        ControllerErrorAgentStalling,
			Message:         "agent heartbeat is stale and pane is silent",
			Magnitude:       int(s.LastHeartbeatAge.Seconds()),
			AffectedAgentID: s.AgentID,
		}
	}
	if s.Progress == ProgressAbsent {
		return &ControllerError{
			Category:        ControllerErrorAgentNoProgress,
			Message:         "agent has activity but no useful progress evidence",
			Magnitude:       s.OscillationScore(),
			AffectedAgentID: s.AgentID,
		}
	}
	if s.HeartbeatStale {
		return &ControllerError{
			Category:        ControllerErrorAgentStale,
			Message:         "agent heartbeat is stale but pane is active",
			Magnitude:       int(s.LastHeartbeatAge.Seconds()),
			AffectedAgentID: s.AgentID,
		}
	}
	return nil
}

// EvaluateAgentHealth returns the health status and recommended action for an agent.
func EvaluateAgentHealth(s AgentObservedState, heartbeatTimeout time.Duration) AgentDecision {
	err := ComputeAgentError(s, heartbeatTimeout)
	oscillation := s.OscillationScore()
	progress := s.Progress
	if progress == "" {
		progress = ProgressUnknown
	}

	if err == nil {
		return AgentDecision{
			AgentID:          s.AgentID,
			Health:           AgentHealthHealthy,
			Action:           ControllerActionNone,
			Reason:           "agent is healthy",
			Progress:         progress,
			OscillationScore: oscillation,
			FailureSignature: s.FailureSignature,
			EscalationLevel:  escalationLevel(oscillation),
		}
	}

	if oscillation >= DefaultOscillationThreshold {
		return AgentDecision{
			AgentID:          s.AgentID,
			Health:           AgentHealthStalled,
			Action:           ControllerActionBlock,
			Reason:           "repeated unresolved controller error; blocking for human input",
			Error:            &ControllerError{Category: ControllerErrorOscillation, Message: "repeated unresolved controller error", Magnitude: oscillation, AffectedAgentID: s.AgentID},
			Progress:         progress,
			OscillationScore: oscillation,
			EscalationLevel:  escalationLevel(oscillation),
			FailureSignature: s.FailureSignature,
		}
	}

	switch err.Category {
	case ControllerErrorAgentDead:
		return AgentDecision{
			AgentID:          s.AgentID,
			Health:           AgentHealthDead,
			Action:           ControllerActionKill,
			Reason:           err.Message,
			Error:            err,
			Progress:         progress,
			OscillationScore: oscillation,
			EscalationLevel:  escalationLevel(oscillation),
			FailureSignature: s.FailureSignature,
		}
	case ControllerErrorAgentStalling:
		return AgentDecision{
			AgentID:          s.AgentID,
			Health:           AgentHealthStalled,
			Action:           ControllerActionNudge,
			Reason:           err.Message,
			Error:            err,
			Progress:         progress,
			OscillationScore: oscillation,
			EscalationLevel:  escalationLevel(oscillation),
			FailureSignature: s.FailureSignature,
		}
	case ControllerErrorAgentNoProgress:
		return AgentDecision{
			AgentID:          s.AgentID,
			Health:           AgentHealthWarning,
			Action:           ControllerActionNudge,
			Reason:           err.Message,
			Error:            err,
			Progress:         progress,
			OscillationScore: oscillation,
			EscalationLevel:  escalationLevel(oscillation),
			FailureSignature: s.FailureSignature,
		}
	case ControllerErrorAgentStale:
		return AgentDecision{
			AgentID:          s.AgentID,
			Health:           AgentHealthWarning,
			Action:           ControllerActionNoOp,
			Reason:           err.Message,
			Error:            err,
			Progress:         progress,
			OscillationScore: oscillation,
			EscalationLevel:  escalationLevel(oscillation),
			FailureSignature: s.FailureSignature,
		}
	}
	return AgentDecision{
		AgentID:          s.AgentID,
		Health:           AgentHealthWarning,
		Action:           ControllerActionNoOp,
		Reason:           err.Message,
		Error:            err,
		Progress:         progress,
		OscillationScore: oscillation,
		EscalationLevel:  escalationLevel(oscillation),
		FailureSignature: s.FailureSignature,
	}
}

func (s AgentObservedState) OscillationScore() int {
	score := 0
	if s.NoSignalTurns > score {
		score = s.NoSignalTurns
	}
	if s.RepeatedFailures > score {
		score = s.RepeatedFailures
	}
	if s.NudgeTimeouts > score {
		score = s.NudgeTimeouts
	}
	return score
}

func escalationLevel(score int) int {
	switch {
	case score >= DefaultOscillationThreshold:
		return 3
	case score == DefaultOscillationThreshold-1:
		return 2
	case score > 0:
		return 1
	default:
		return 0
	}
}

// NewFailureSignature canonicalizes noisy failure text into a stable signature.
func NewFailureSignature(source, step, command, class, raw string, fields map[string]string) *FailureSignature {
	normalized := NormalizeFailureText(raw)
	if normalized == "" && len(fields) == 0 && class == "" && command == "" && step == "" {
		return nil
	}
	parts := []string{source, step, command, class, normalized}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	stable := make(map[string]string, len(fields))
	for _, k := range keys {
		stable[k] = fields[k]
		parts = append(parts, k+"="+fields[k])
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return &FailureSignature{
		Source:         source,
		Step:           step,
		Command:        command,
		Class:          class,
		StableFields:   stable,
		NormalizedHash: hex.EncodeToString(sum[:])[:16],
	}
}

var (
	reTimestamp  = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ][0-9:.+-]+Z?\b`)
	reDuration   = regexp.MustCompile(`\b\d+(\.\d+)?(ms|s|m|h)\b`)
	reTempPath   = regexp.MustCompile(`(/private)?/tmp/[^\s]+|/var/folders/[^\s]+`)
	reHomePath   = regexp.MustCompile(`/Users/[^/\s]+|/home/[^/\s]+`)
	reHexAddress = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	reULID       = regexp.MustCompile(`\b[0-9A-HJKMNP-TV-Z]{26}\b`)
	reLineNo     = regexp.MustCompile(`:[0-9]+`)
)

// NormalizeFailureText removes volatile details before hashing failure text.
func NormalizeFailureText(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	replacers := []struct {
		re   *regexp.Regexp
		with string
	}{
		{reTimestamp, "<time>"},
		{reDuration, "<duration>"},
		{reTempPath, "<tmp>"},
		{reHomePath, "<home>"},
		{reHexAddress, "<addr>"},
		{reULID, "<id>"},
		{reLineNo, ":<line>"},
	}
	for _, r := range replacers {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// ComputeQueuePressure returns a graduated dispatch pressure decision.
func ComputeQueuePressure(obs QueuePressureSnapshot, targetDepth int, oldestAgeMax time.Duration, maxSlots int) QueuePressureDecision {
	if targetDepth <= 0 {
		targetDepth = 1
	}
	if oldestAgeMax <= 0 {
		oldestAgeMax = 30 * time.Minute
	}
	if maxSlots <= 0 {
		maxSlots = 1
	}
	decision := QueuePressureDecision{
		Level:        QueuePressureNone,
		MaxDispatch:  maxSlots,
		Observed:     obs,
		TargetDepth:  targetDepth,
		OldestAgeMax: oldestAgeMax,
		Reason:       "merge queue within target",
	}
	switch {
	case obs.Depth >= targetDepth*3 || obs.OldestAge >= oldestAgeMax*2 || obs.RecentFailures >= targetDepth:
		decision.Level = QueuePressureHard
		decision.MaxDispatch = 0
		decision.ShouldPause = true
		decision.Reason = "merge queue is in hard pressure"
	case obs.Depth >= targetDepth*2 || obs.OldestAge >= oldestAgeMax || obs.RecentFailures >= max(1, targetDepth/2):
		decision.Level = QueuePressureDrain
		decision.MaxDispatch = 0
		decision.ShouldPause = true
		decision.Reason = "merge queue should drain before new dispatch"
	case obs.Depth >= targetDepth || obs.ConflictCount > 0:
		decision.Level = QueuePressureReduced
		decision.MaxDispatch = 1
		decision.Reason = "merge queue elevated; reducing dispatch"
	}
	return decision
}

// ControllerErrorCategories returns all known controller error category constants.
// Useful for exhaustive switch statements and testing.
func ControllerErrorCategories() []string {
	return []string{
		ControllerErrorNone,
		ControllerErrorSlotsUnderutilized,
		ControllerErrorSlotsOvercommitted,
		ControllerErrorQueuePressured,
		ControllerErrorAgentStale,
		ControllerErrorAgentDead,
		ControllerErrorAgentStalling,
		ControllerErrorAgentContextLimit,
		ControllerErrorAgentNoProgress,
		ControllerErrorOscillation,
	}
}

// ControllerActionCategories returns all known controller action constants.
func ControllerActionCategories() []string {
	return []string{
		ControllerActionNone,
		ControllerActionDispatch,
		ControllerActionPause,
		ControllerActionResume,
		ControllerActionEscalateModel,
		ControllerActionKill,
		ControllerActionNudge,
		ControllerActionHandoff,
		ControllerActionBlock,
		ControllerActionNoOp,
	}
}

// ValidAgentHealthCategories returns all known agent health constants.
func ValidAgentHealthCategories() []string {
	return []string{
		AgentHealthHealthy,
		AgentHealthWarning,
		AgentHealthStalled,
		AgentHealthDead,
	}
}

// IsValidHealthCategory returns true if h is a known agent health constant.
func IsValidHealthCategory(h string) bool {
	return slices.Contains(ValidAgentHealthCategories(), h)
}
