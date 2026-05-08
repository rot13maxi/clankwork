package workflow

import (
	"encoding/json"
	"fmt"

	"github.com/rot13maxi/clankwork/internal/model"
)

// ValidationFinding records a single conformance violation or informational note
// found during trace conformance validation.
type ValidationFinding struct {
	// Kind is a machine-readable category: "error", "warning", "info".
	Kind string `json:"kind"`
	// Code is a stable identifier for the finding type.
	Code string `json:"code"`
	// Message is a human-readable description.
	Message string `json:"message"`
	// TraceID is the database row ID of the relevant trace event, if applicable.
	TraceID int64 `json:"trace_id,omitempty"`
	// TraceEventType is the event_type of the relevant trace, if applicable.
	TraceEventType string `json:"trace_event_type,omitempty"`
	// TracePayload is the full payload of the relevant trace, if applicable.
	TracePayload string `json:"trace_payload,omitempty"`
}

// ValidationReport summarizes the results of a trace conformance validation run.
type ValidationReport struct {
	// Valid is true only when zero error/warning findings were produced.
	Valid bool `json:"valid"`
	// TaskID is the task whose traces were validated.
	TaskID string `json:"task_id"`
	// GraphName is the compiled workflow graph name used for validation.
	GraphName string `json:"graph_name"`
	// Findings lists all findings ordered by severity (errors first).
	Findings []ValidationFinding `json:"findings"`
	// TraceCount is the total number of traces examined.
	TraceCount int `json:"trace_count"`
}

// ValidateTraceConformance validates a sequence of traces against a compiled workflow Graph.
//
// It checks:
//   - every step.routed transition references existing nodes (from and to)
//   - every routed transition follows the compiled success/failure edge
//   - terminal "done" (task completed) requires a success path to "complete"
//   - step.deterministic_result events reference existing graph nodes
//   - step.routed events with from/to="complete" are treated as terminal markers
//     and do not by themselves bypass graph routing
//
// The graph must be non-nil. If no traces are provided, an empty valid report is returned.
func ValidateTraceConformance(g *Graph, taskID string, traces []*model.Trace) *ValidationReport {
	if g == nil {
		return &ValidationReport{
			Valid:     false,
			TaskID:    taskID,
			GraphName: "<nil>",
			TraceCount: len(traces),
			Findings: []ValidationFinding{{
				Kind:    "error",
				Code:    "missing-graph",
				Message: fmt.Sprintf("no compiled graph available for task %q — cannot validate trace conformance", taskID),
			}},
		}
	}

	report := &ValidationReport{
		TaskID:     taskID,
		GraphName:  g.Name,
		Valid:      true,
		TraceCount: len(traces),
	}

	// Collect relevant events and process them in chronological order.
	var routedEvents, deterministicEvents, terminalEvents []traceEvent
	for _, tr := range traces {
		evt := parseTrace(tr)
		switch tr.EventType {
		case "step.routed":
			routedEvents = append(routedEvents, evt)
		case "step.deterministic_result":
			deterministicEvents = append(deterministicEvents, evt)
		case "signal.done":
			terminalEvents = append(terminalEvents, evt)
		}
	}

	// Validate step.routed transitions.
	for _, evt := range routedEvents {
		findings := validateRoutedEvent(g, evt)
		report.Findings = append(report.Findings, findings...)
	}

	// Validate step.deterministic_result events.
	for _, evt := range deterministicEvents {
		findings := validateDeterministicResult(g, evt)
		report.Findings = append(report.Findings, findings...)
	}

	// Validate terminal "done" — must be preceded by success path to complete.
	for _, evt := range terminalEvents {
		findings := validateTerminalDone(g, evt, routedEvents)
		report.Findings = append(report.Findings, findings...)
	}

	// Check if any terminal routing to "complete" exists on success edge.
	// If there are routed events but none route to complete on success,
	// that's informational unless a terminal done was emitted.
	if len(terminalEvents) > 0 && !hasSuccessPathToComplete(routedEvents) {
		report.Findings = append(report.Findings, ValidationFinding{
			Kind:    "warning",
			Code:    "terminal-no-success-route",
			Message: "task reached done status but no step.routed event routes to 'complete' via a success outcome",
		})
	}

	if len(report.Findings) > 0 {
		report.Valid = false
	}

	return report
}

// ValidateTraceConformanceWithGraphJSON validates traces against a graph stored
// as JSON in a CompiledWorkflow. This is the store-level convenience function.
func ValidateTraceConformanceWithGraphJSON(graphJSON, taskID string, traces []*model.Trace) (*ValidationReport, error) {
	g, err := UnmarshalGraphString(graphJSON)
	if err != nil {
		return &ValidationReport{
			Valid:    false,
			TaskID:   taskID,
			GraphName: "<invalid-json>",
			TraceCount: len(traces),
			Findings: []ValidationFinding{{
				Kind:    "error",
				Code:    "invalid-graph-json",
				Message: fmt.Sprintf("cannot parse compiled graph JSON: %v", err),
			}},
		}, nil
	}
	return ValidateTraceConformance(g, taskID, traces), nil
}

// UnmarshalGraphString deserializes a Graph from a JSON string.
// The expected format matches MarshalGraph/MarshalGraphString.
func UnmarshalGraphString(data string) (*Graph, error) {
	if data == "" {
		return nil, fmt.Errorf("empty graph JSON")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	g := &Graph{
		Nodes: make(map[string]*Node),
		Edges: make(map[string]*Edges),
	}

	// Decode string fields.
	if v, ok := raw["name"]; ok {
		json.Unmarshal(v, &g.Name)
	}
	if v, ok := raw["description"]; ok {
		json.Unmarshal(v, &g.Description)
	}
	if v, ok := raw["entry"]; ok {
		json.Unmarshal(v, &g.Entry)
	}
	if v, ok := raw["auto_merge"]; ok {
		json.Unmarshal(v, &g.AutoMerge)
	}

	// Decode nodes.
	if v, ok := raw["nodes"]; ok {
		var nodesMap map[string]*Node
		if err := json.Unmarshal(v, &nodesMap); err != nil {
			return nil, fmt.Errorf("invalid nodes: %w", err)
		}
		g.Nodes = nodesMap
	}

	// Decode edges.
	if v, ok := raw["edges"]; ok {
		var edgesMap map[string]*Edges
		if err := json.Unmarshal(v, &edgesMap); err != nil {
			return nil, fmt.Errorf("invalid edges: %w", err)
		}
		g.Edges = edgesMap
	}

	return g, nil
}

// UnmarshalGraph deserializes a Graph from a JSON string.
// Deprecated: use UnmarshalGraphString for clarity. Kept for backward compatibility.
func UnmarshalGraph(data string) (*Graph, error) {
	return UnmarshalGraphString(data)
}

// GraphMarshal serializes a Graph to JSON suitable for persistence.
func GraphMarshal(g *Graph) (string, error) {
	type graphJSON struct {
		Name        string                  `json:"name"`
		Description string                  `json:"description"`
		Entry       string                  `json:"entry"`
		AutoMerge   bool                    `json:"auto_merge"`
		Nodes       map[string]*Node        `json:"nodes"`
		Edges       map[string]*Edges       `json:"edges"`
	}
	data, err := json.Marshal(graphJSON{
		Name:        g.Name,
		Description: g.Description,
		Entry:       g.Entry,
		AutoMerge:   g.AutoMerge,
		Nodes:       g.Nodes,
		Edges:       g.Edges,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

type traceEvent struct {
	TraceID     int64
	EventType   string
	Payload     string
	Routed      *stepRoutedInfo
	DetResult   *stepDetResultInfo
}

type stepRoutedInfo struct {
	From    string
	To      string
	Outcome string // "success" or "failure"
}

type stepDetResultInfo struct {
	Step    string
	Outcome string // "success" or "failure"
}

func parseTrace(tr *model.Trace) traceEvent {
	evt := traceEvent{
		TraceID:   tr.ID,
		EventType: tr.EventType,
		Payload:   tr.Payload,
	}

	switch tr.EventType {
	case "step.routed":
		var p model.StepRoutedPayload
		if err := json.Unmarshal([]byte(tr.Payload), &p); err == nil {
			evt.Routed = &stepRoutedInfo{
				From:    p.From,
				To:      p.To,
				Outcome: p.Outcome,
			}
		}
	case "step.deterministic_result":
		var p model.StepDeterministicResultPayload
		if err := json.Unmarshal([]byte(tr.Payload), &p); err == nil {
			evt.DetResult = &stepDetResultInfo{
				Step:    p.Step,
				Outcome: p.Outcome,
			}
		}
	}

	return evt
}

// validateRoutedEvent checks a single step.routed trace event against the graph.
func validateRoutedEvent(g *Graph, evt traceEvent) []ValidationFinding {
	var findings []ValidationFinding
	if evt.Routed == nil {
		// Payload unparseable — record as warning.
		findings = append(findings, ValidationFinding{
			Kind:           "warning",
			Code:           "routed-invalid-payload",
			Message:        fmt.Sprintf("step.routed event has unparseable payload"),
			TraceID:        evt.TraceID,
			TraceEventType: evt.EventType,
			TracePayload:   evt.Payload,
		})
		return findings
	}

	r := evt.Routed

	// Check "from" node exists (unless it's "complete" which is terminal marker).
	if r.From != "complete" {
		if _, ok := g.Nodes[r.From]; !ok {
			findings = append(findings, ValidationFinding{
				Kind:           "error",
				Code:           "routed-missing-source",
				Message:        fmt.Sprintf("step.routed: source node %q does not exist in graph %q", r.From, g.Name),
				TraceID:        evt.TraceID,
				TraceEventType: evt.EventType,
				TracePayload:   evt.Payload,
			})
		}
	}

	// Check "to" node exists (unless it's "complete" which is the terminal target).
	if r.To != "complete" {
		if _, ok := g.Nodes[r.To]; !ok {
			findings = append(findings, ValidationFinding{
				Kind:           "error",
				Code:           "routed-missing-target",
				Message:        fmt.Sprintf("step.routed: target node %q does not exist in graph %q", r.To, g.Name),
				TraceID:        evt.TraceID,
				TraceEventType: evt.EventType,
				TracePayload:   evt.Payload,
			})
		}
	}

	// Check edge follows the compiled graph's success/failure edge.
	// Only check if both from and to are graph nodes (not "complete").
	if r.From != "complete" && r.To != "complete" {
		if edges, ok := g.Edges[r.From]; ok {
			expectedTarget := ""
			switch r.Outcome {
			case "success":
				expectedTarget = edges.Success
			case "failure":
				expectedTarget = edges.Failure
			default:
				// Unknown outcome — can't validate edge.
				findings = append(findings, ValidationFinding{
					Kind:           "warning",
					Code:           "routed-unknown-outcome",
					Message:        fmt.Sprintf("step.routed: unknown outcome %q for edge from %q", r.Outcome, r.From),
					TraceID:        evt.TraceID,
					TraceEventType: evt.EventType,
					TracePayload:   evt.Payload,
				})
				return findings
			}

			// Resolve "complete" edges — if the graph says success goes to "complete",
			// that's valid as long as the trace says "complete" too.
			if r.To != expectedTarget {
				findings = append(findings, ValidationFinding{
					Kind:           "error",
					Code:           "routed-wrong-edge",
					Message:        fmt.Sprintf("step.routed: %s edge from %q routes to %q but graph specifies %q", r.Outcome, r.From, r.To, expectedTarget),
					TraceID:        evt.TraceID,
					TraceEventType: evt.EventType,
					TracePayload:   evt.Payload,
				})
			}
		} else if r.From != "complete" {
			// Source node exists but has no edges — that means it's a leaf
			// node with no outgoing edges defined, which shouldn't happen in a
			// well-formed graph. Only flag if the from node exists.
			if _, nodeExists := g.Nodes[r.From]; nodeExists {
				findings = append(findings, ValidationFinding{
					Kind:           "error",
					Code:           "routed-no-edges",
					Message:        fmt.Sprintf("step.routed: source node %q exists but has no outgoing edges in graph %q", r.From, g.Name),
					TraceID:        evt.TraceID,
					TraceEventType: evt.EventType,
					TracePayload:   evt.Payload,
				})
			}
		}
	} else if r.From != "complete" && r.To == "complete" {
		// Terminal routing: check that the from node's success edge points to "complete".
		if edges, ok := g.Edges[r.From]; ok {
			expectedTarget := ""
			switch r.Outcome {
			case "success":
				expectedTarget = edges.Success
			case "failure":
				expectedTarget = edges.Failure
			}
			if expectedTarget != "complete" {
				findings = append(findings, ValidationFinding{
					Kind:           "error",
					Code:           "routed-wrong-edge-terminal",
					Message:        fmt.Sprintf("step.routed: %s outcome from %q routes to 'complete' but graph %s edge points to %q", r.Outcome, r.From, r.Outcome, expectedTarget),
					TraceID:        evt.TraceID,
					TraceEventType: evt.EventType,
					TracePayload:   evt.Payload,
				})
			}
		}
	}

	return findings
}

// validateDeterministicResult checks a single step.deterministic_result event against the graph.
func validateDeterministicResult(g *Graph, evt traceEvent) []ValidationFinding {
	var findings []ValidationFinding
	if evt.DetResult == nil {
		return findings // unparseable payload, not critical
	}

	dr := evt.DetResult
	if dr.Step == "" {
		return findings // empty step name, skip
	}

	// "complete" is not a graph node — it's a terminal marker.
	if dr.Step == "complete" {
		return findings
	}

	if _, ok := g.Nodes[dr.Step]; !ok {
		findings = append(findings, ValidationFinding{
			Kind:           "error",
			Code:           "det-result-missing-node",
			Message:        fmt.Sprintf("step.deterministic_result: step %q does not exist in graph %q", dr.Step, g.Name),
			TraceID:        evt.TraceID,
			TraceEventType: evt.EventType,
			TracePayload:   evt.Payload,
		})
	}

	return findings
}

// validateTerminalDone checks that a signal.done event is consistent with
// a success path to "complete" in the routed events.
func validateTerminalDone(g *Graph, evt traceEvent, routedEvents []traceEvent) []ValidationFinding {
	var findings []ValidationFinding

	// If there are routed events that reach "complete" on success, the done signal is valid.
	if hasSuccessPathToComplete(routedEvents) {
		return findings
	}

	// No success path to complete found in routed events — this is a warning
	// (the done signal might be legitimate if routing happened before trace capture).
	findings = append(findings, ValidationFinding{
		Kind:           "warning",
		Code:           "terminal-no-success-path",
		Message:        fmt.Sprintf("signal.done received but no step.routed event reaches 'complete' via success — possible trace gap or premature completion in graph %q", g.Name),
		TraceID:        evt.TraceID,
		TraceEventType: evt.EventType,
		TracePayload:   evt.Payload,
	})

	return findings
}

// hasSuccessPathToComplete returns true if any routed event routes to "complete" with outcome "success".
func hasSuccessPathToComplete(events []traceEvent) bool {
	for _, evt := range events {
		if evt.Routed != nil && evt.Routed.To == "complete" && evt.Routed.Outcome == "success" {
			return true
		}
	}
	return false
}
