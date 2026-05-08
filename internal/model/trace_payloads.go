package model

import (
	"encoding/json"
	"time"
)

// Trace payload types provide structured marshaling/unmarshaling for the
// Trace.Payload JSON string field. The Payload column remains an unstructured
// JSON string in the database; these types are helpers for producers and consumers.

// SignalPayload is used by signal.started, signal.done, signal.failed,
// signal.progress, and signal.blocked events.
type SignalPayload struct {
	Message string `json:"message,omitempty"`
}

// StepFailureContextPayload is used by step.failure_context events.
// Contains the step name and either a human message or a log snippet.
type StepFailureContextPayload struct {
	Step    string `json:"step"`
	Message string `json:"message,omitempty"`
	Log     string `json:"log,omitempty"`
}

// StepDeterministicResultPayload is used by step.deterministic_result events.
type StepDeterministicResultPayload struct {
	Step    string `json:"step"`
	Outcome string `json:"outcome"` // "success" or "failure"
	Log     string `json:"log,omitempty"`
}

// StepRoutedPayload is used by step.routed events when the scheduler
// advances a template task to its next step.
type StepRoutedPayload struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Outcome string `json:"outcome"` // "success" or "failure"
}

// MergeMergedPayload is used by merge.merged events.
type MergeMergedPayload struct {
	SHA    string `json:"sha"`
	Branch string `json:"branch"`
}

// MergeConflictedPayload is used by merge.conflicted events.
type MergeConflictedPayload struct {
	ConflictTaskID string `json:"conflict_task_id"`
}

// MergeVerifyFailedPayload is used by merge.verify_failed events.
type MergeVerifyFailedPayload struct {
	Reason string `json:"reason"`
	Log    string `json:"log,omitempty"`
}

// MergeConflictResolvedPayload is used by merge.conflict_resolved events.
type MergeConflictResolvedPayload struct {
	ConflictTaskID string `json:"conflict_task_id"`
}

// StepCompletePayload captures structured step completion data as described
// in the architecture spec: duration, files touched, and exit code.
// Producers should marshal this as the Payload when emitting step completion traces.
type StepCompletePayload struct {
	DurationMs   int64    `json:"duration_ms"`
	FilesTouched []string `json:"files_touched,omitempty"`
	ExitCode     int      `json:"exit_code,omitempty"`
}

// SetDuration sets DurationMs from a time.Duration.
func (p *StepCompletePayload) SetDuration(d time.Duration) {
	p.DurationMs = d.Milliseconds()
}

// Duration returns DurationMs as a time.Duration.
func (p *StepCompletePayload) Duration() time.Duration {
	return time.Duration(p.DurationMs) * time.Millisecond
}

// MarshalPayload marshals any payload type to a JSON string suitable for Trace.Payload.
func MarshalPayload(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ControllerDecisionPayload is used by controller.decision events.
// Produced by the scheduler's tick loop when it evaluates dispatch conditions.
type ControllerDecisionPayload struct {
	Action          string `json:"action"`
	Reason          string `json:"reason"`
	ErrorCategory   string `json:"error_category,omitempty"`
	ErrorMagnitude  int    `json:"error_magnitude,omitempty"`
	SlotsAvailable  int    `json:"slots_available"`
	TasksToDispatch int    `json:"tasks_to_dispatch,omitempty"`
}

// AgentControllerDecisionPayload is used by agent_controller.decision events.
// Produced by the reconciler when it evaluates an individual agent's health.
type AgentControllerDecisionPayload struct {
	AgentID          string            `json:"agent_id"`
	Health           string            `json:"health"`
	Action           string            `json:"action"`
	Reason           string            `json:"reason"`
	ErrorCategory    string            `json:"error_category,omitempty"`
	ErrorMagnitude   int               `json:"error_magnitude,omitempty"`
	Progress         string            `json:"progress,omitempty"`
	OscillationScore int               `json:"oscillation_score,omitempty"`
	EscalationLevel  int               `json:"escalation_level,omitempty"`
	FailureSignature string            `json:"failure_signature,omitempty"`
	FailureSigFields map[string]string `json:"failure_signature_fields,omitempty"`
}

// AgentControllerActuationPayload is used by agent_controller.actuation events.
// Produced by the reconciler to record the outcome of a controller action.
type AgentControllerActuationPayload struct {
	AgentID  string `json:"agent_id"`
	Action   string `json:"action"`
	Outcome  string `json:"outcome"` // "success", "failure", "skipped"
	ErrorMsg string `json:"error_msg,omitempty"`
	Detail   string `json:"detail,omitempty"` // e.g. "runtime cleaned", "nudge sent"
}

// UnmarshalPayload unmarshals a Trace.Payload JSON string into the given type.
func UnmarshalPayload(payload string, v any) error {
	return json.Unmarshal([]byte(payload), v)
}
