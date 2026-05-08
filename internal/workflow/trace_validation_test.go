package workflow

import (
	"encoding/json"
	"testing"

	"github.com/rot13maxi/clankwork/internal/model"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func makeGraph() *Graph {
	return &Graph{
		Name:        "test",
		Description: "test graph",
		Entry:       "implement",
		Nodes: map[string]*Node{
			"implement": {Name: "implement", Kind: KindAgent, GateClass: GateImplementation},
			"lint":      {Name: "lint", Kind: KindDeterministic, GateClass: GateDeterministicVerification},
			"test":      {Name: "test", Kind: KindDeterministic, GateClass: GateDeterministicVerification},
			"acceptance": {Name: "acceptance", Kind: KindAgent, GateClass: GateAcceptanceVerification},
		},
		Edges: map[string]*Edges{
			"implement":  {Success: "lint", Failure: "implement"},
			"lint":       {Success: "test", Failure: "implement"},
			"test":       {Success: "acceptance", Failure: "implement"},
			"acceptance": {Success: "complete", Failure: "implement"},
		},
	}
}

func makeTrace(eventType, payload string, id int64) *model.Trace {
	return &model.Trace{
		ID:        id,
		EventType: eventType,
		Payload:   payload,
	}
}

func routedPayload(from, to, outcome string) string {
	p := model.StepRoutedPayload{From: from, To: to, Outcome: outcome}
	return model.MarshalPayload(p)
}

func detResultPayload(step, outcome string) string {
	p := model.StepDeterministicResultPayload{Step: step, Outcome: outcome}
	return model.MarshalPayload(p)
}

func signalDonePayload() string {
	p := model.SignalPayload{Message: "done"}
	return model.MarshalPayload(p)
}

// ---------------------------------------------------------------------------
// Happy path: full route sequence conforms
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_HappyPath(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		// Implement → lint (success)
		makeTrace("step.routed", routedPayload("implement", "lint", "success"), 1),
		// lint → test (success)
		makeTrace("step.routed", routedPayload("lint", "test", "success"), 2),
		// test → acceptance (success)
		makeTrace("step.routed", routedPayload("test", "acceptance", "success"), 3),
		// acceptance → complete (success)
		makeTrace("step.routed", routedPayload("acceptance", "complete", "success"), 4),
		// Deterministic results
		makeTrace("step.deterministic_result", detResultPayload("lint", "success"), 5),
		makeTrace("step.deterministic_result", detResultPayload("test", "success"), 6),
		// Terminal
		makeTrace("signal.done", signalDonePayload(), 7),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if !report.Valid {
		for _, f := range report.Findings {
			t.Errorf("unexpected finding: %s: %s", f.Code, f.Message)
		}
		t.Error("expected valid report")
	}
	if report.TraceCount != len(traces) {
		t.Errorf("trace_count = %d, want %d", report.TraceCount, len(traces))
	}
	if report.TaskID != "task-1" {
		t.Errorf("task_id = %q, want task-1", report.TaskID)
	}
}

// ---------------------------------------------------------------------------
// Bad edge target is rejected
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_BadEdgeTarget(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		// Routes to a nonexistent node "nonexistent"
		makeTrace("step.routed", routedPayload("implement", "nonexistent", "success"), 1),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if report.Valid {
		t.Fatal("expected invalid report for bad edge target")
	}

	var foundCode string
	for _, f := range report.Findings {
		if f.Code == "routed-missing-target" {
			foundCode = f.Code
			break
		}
	}
	if foundCode == "" {
		t.Error("expected routed-missing-target finding")
	}
}

func TestValidateTraceConformance_BadSourceNode(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		// Routes from a nonexistent node
		makeTrace("step.routed", routedPayload("ghost", "lint", "success"), 1),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if report.Valid {
		t.Fatal("expected invalid report for bad source node")
	}

	var foundCode string
	for _, f := range report.Findings {
		if f.Code == "routed-missing-source" {
			foundCode = f.Code
			break
		}
	}
	if foundCode == "" {
		t.Error("expected routed-missing-source finding")
	}
}

// ---------------------------------------------------------------------------
// Wrong outcome edge is rejected
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_WrongOutcomeEdge(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		// In the graph, implement.success → lint.
		// This traces a success outcome routing to "test" instead.
		makeTrace("step.routed", routedPayload("implement", "test", "success"), 1),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if report.Valid {
		t.Fatal("expected invalid report for wrong outcome edge")
	}

	var foundCode string
	for _, f := range report.Findings {
		if f.Code == "routed-wrong-edge" {
			foundCode = f.Code
			break
		}
	}
	if foundCode == "" {
		t.Error("expected routed-wrong-edge finding")
	}
}

func TestValidateTraceConformance_FailureEdgeFollowedCorrectly(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		// In the graph, lint.failure → implement.
		// This traces a failure outcome routing back to implement.
		makeTrace("step.routed", routedPayload("lint", "implement", "failure"), 1),
		// Then succeed from implement to lint
		makeTrace("step.routed", routedPayload("implement", "lint", "success"), 2),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if !report.Valid {
		for _, f := range report.Findings {
			t.Errorf("unexpected finding: %s: %s", f.Code, f.Message)
		}
		t.Error("expected valid report for correct failure edge")
	}
}

func TestValidateTraceConformance_WrongFailureEdge(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		// In the graph, lint.failure → implement.
		// This traces a failure outcome routing to "test" instead.
		makeTrace("step.routed", routedPayload("lint", "test", "failure"), 1),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if report.Valid {
		t.Fatal("expected invalid report for wrong failure edge")
	}

	var foundCode string
	for _, f := range report.Findings {
		if f.Code == "routed-wrong-edge" {
			foundCode = f.Code
			break
		}
	}
	if foundCode == "" {
		t.Error("expected routed-wrong-edge finding")
	}
}

// ---------------------------------------------------------------------------
// Missing compiled graph returns a useful error
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_MissingGraph(t *testing.T) {
	traces := []*model.Trace{
		makeTrace("step.routed", routedPayload("implement", "lint", "success"), 1),
	}

	report := ValidateTraceConformance(nil, "task-1", traces)
	if report.Valid {
		t.Fatal("expected invalid report for missing graph")
	}

	if len(report.Findings) == 0 {
		t.Fatal("expected findings for missing graph")
	}
	if report.Findings[0].Code != "missing-graph" {
		t.Errorf("finding code = %q, want missing-graph", report.Findings[0].Code)
	}
	if report.Findings[0].Kind != "error" {
		t.Errorf("finding kind = %q, want error", report.Findings[0].Kind)
	}
}

// ---------------------------------------------------------------------------
// Deterministic result events must reference existing graph nodes
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_DetResult_MissingNode(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		makeTrace("step.deterministic_result", detResultPayload("nonexistent-step", "success"), 1),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if report.Valid {
		t.Fatal("expected invalid report for deterministic result referencing nonexistent node")
	}

	var foundCode string
	for _, f := range report.Findings {
		if f.Code == "det-result-missing-node" {
			foundCode = f.Code
			break
		}
	}
	if foundCode == "" {
		t.Error("expected det-result-missing-node finding")
	}
}

func TestValidateTraceConformance_DetResult_ValidNode(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		makeTrace("step.deterministic_result", detResultPayload("lint", "success"), 1),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if !report.Valid {
		for _, f := range report.Findings {
			t.Errorf("unexpected finding: %s: %s", f.Code, f.Message)
		}
		t.Error("expected valid report for deterministic result referencing valid node")
	}
}

// ---------------------------------------------------------------------------
// Terminal "done" requires a success path to "complete"
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_TerminalDone_NoSuccessPath(t *testing.T) {
	g := makeGraph()

	// Only routed events, no route to "complete"
	traces := []*model.Trace{
		makeTrace("step.routed", routedPayload("implement", "lint", "success"), 1),
		makeTrace("signal.done", signalDonePayload(), 2),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	// Should have a warning about no success path to complete
	var foundWarning bool
	for _, f := range report.Findings {
		if f.Code == "terminal-no-success-path" || f.Code == "terminal-no-success-route" {
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Error("expected warning about terminal done with no success path to complete")
	}
}

func TestValidateTraceConformance_TerminalDone_WithSuccessPath(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		makeTrace("step.routed", routedPayload("implement", "lint", "success"), 1),
		makeTrace("step.routed", routedPayload("lint", "test", "success"), 2),
		makeTrace("step.routed", routedPayload("test", "acceptance", "success"), 3),
		makeTrace("step.routed", routedPayload("acceptance", "complete", "success"), 4),
		makeTrace("signal.done", signalDonePayload(), 5),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if !report.Valid {
		for _, f := range report.Findings {
			t.Errorf("unexpected finding: %s: %s", f.Code, f.Message)
		}
		t.Error("expected valid report when terminal done has success path to complete")
	}
}

// ---------------------------------------------------------------------------
// Validation acceptance/rejection events remain auditable and do not
// by themselves bypass graph routing
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_AcceptanceEvents_DontBypassRouting(t *testing.T) {
	g := makeGraph()

	// A signal.done without any routed events reaching "complete"
	// should produce a warning regardless of other event types
	traces := []*model.Trace{
		makeTrace("step.routed", routedPayload("implement", "lint", "success"), 1),
		makeTrace("step.routed", routedPayload("lint", "test", "success"), 2),
		// Missing test → acceptance and acceptance → complete routes
		makeTrace("signal.done", signalDonePayload(), 3),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if report.Valid {
		t.Fatal("expected warnings when done is signaled without complete route")
	}
}

// ---------------------------------------------------------------------------
// Empty traces
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_EmptyTraces(t *testing.T) {
	g := makeGraph()

	report := ValidateTraceConformance(g, "task-1", nil)
	if !report.Valid {
		t.Error("expected valid report for empty traces")
	}
	if report.TraceCount != 0 {
		t.Errorf("trace_count = %d, want 0", report.TraceCount)
	}
}

// ---------------------------------------------------------------------------
// Wrong edge to terminal
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_TerminalWrongEdge(t *testing.T) {
	g := makeGraph()

	// In the graph, lint.success → test (not complete).
	// Routing to complete from lint on success should fail.
	traces := []*model.Trace{
		makeTrace("step.routed", routedPayload("lint", "complete", "success"), 1),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if report.Valid {
		t.Fatal("expected invalid report for routing to complete from wrong node")
	}

	var foundCode string
	for _, f := range report.Findings {
		if f.Code == "routed-wrong-edge-terminal" {
			foundCode = f.Code
			break
		}
	}
	if foundCode == "" {
		t.Error("expected routed-wrong-edge-terminal finding")
	}
}

// ---------------------------------------------------------------------------
// GraphMarshal / UnmarshalGraph round-trip
// ---------------------------------------------------------------------------

func TestGraphMarshal_RoundTrip(t *testing.T) {
	g := makeGraph()

	jsonStr, err := GraphMarshal(g)
	if err != nil {
		t.Fatalf("GraphMarshal: %v", err)
	}

	g2, err := UnmarshalGraph(jsonStr)
	if err != nil {
		t.Fatalf("UnmarshalGraph: %v", err)
	}

	if g2.Name != g.Name {
		t.Errorf("name = %q, want %q", g2.Name, g.Name)
	}
	if g2.Entry != g.Entry {
		t.Errorf("entry = %q, want %q", g2.Entry, g.Entry)
	}
	if len(g2.Nodes) != len(g.Nodes) {
		t.Errorf("nodes count = %d, want %d", len(g2.Nodes), len(g.Nodes))
	}
	for name, node := range g.Nodes {
		if node2, ok := g2.Nodes[name]; !ok {
			t.Errorf("missing node %q after round-trip", name)
		} else if node2.Name != node.Name {
			t.Errorf("node %q name mismatch", name)
		}
	}
	for name, edges := range g.Edges {
		if edges2, ok := g2.Edges[name]; !ok {
			t.Errorf("missing edges %q after round-trip", name)
		} else {
			if edges2.Success != edges.Success {
				t.Errorf("edge %q success = %q, want %q", name, edges2.Success, edges.Success)
			}
			if edges2.Failure != edges.Failure {
				t.Errorf("edge %q failure = %q, want %q", name, edges2.Failure, edges.Failure)
			}
		}
	}
}

func TestUnmarshalGraph_EmptyJSON(t *testing.T) {
	_, err := UnmarshalGraph("")
	if err == nil {
		t.Fatal("expected error for empty graph JSON")
	}
}

func TestUnmarshalGraph_InvalidJSON(t *testing.T) {
	_, err := UnmarshalGraph("{invalid json}")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// ValidateTraceConformanceWithGraphJSON
// ---------------------------------------------------------------------------

func TestValidateTraceConformanceWithGraphJSON_Valid(t *testing.T) {
	g := makeGraph()
	graphJSON, err := GraphMarshal(g)
	if err != nil {
		t.Fatalf("GraphMarshal: %v", err)
	}

	traces := []*model.Trace{
		makeTrace("step.routed", routedPayload("implement", "lint", "success"), 1),
	}

	report, err := ValidateTraceConformanceWithGraphJSON(graphJSON, "task-1", traces)
	if err != nil {
		t.Fatalf("ValidateTraceConformanceWithGraphJSON: %v", err)
	}
	if !report.Valid {
		for _, f := range report.Findings {
			t.Errorf("unexpected finding: %s: %s", f.Code, f.Message)
		}
		t.Error("expected valid report")
	}
}

func TestValidateTraceConformanceWithGraphJSON_InvalidJSON(t *testing.T) {
	traces := []*model.Trace{}

	report, err := ValidateTraceConformanceWithGraphJSON("{bad json}", "task-1", traces)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Valid {
		t.Fatal("expected invalid report for unparseable graph JSON")
	}
	var foundCode string
	for _, f := range report.Findings {
		if f.Code == "invalid-graph-json" {
			foundCode = f.Code
			break
		}
	}
	if foundCode == "" {
		t.Error("expected invalid-graph-json finding")
	}
}

// ---------------------------------------------------------------------------
// Multiple findings in one report
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_MultipleFindings(t *testing.T) {
	g := makeGraph()

	traces := []*model.Trace{
		// Bad source
		makeTrace("step.routed", routedPayload("ghost", "lint", "success"), 1),
		// Bad target
		makeTrace("step.routed", routedPayload("implement", "phantom", "success"), 2),
		// Wrong edge
		makeTrace("step.routed", routedPayload("implement", "test", "success"), 3),
		// Bad deterministic result
		makeTrace("step.deterministic_result", detResultPayload("fake-step", "success"), 4),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if report.Valid {
		t.Fatal("expected invalid report")
	}

	// Should have at least 4 findings.
	if len(report.Findings) < 4 {
		t.Errorf("expected at least 4 findings, got %d", len(report.Findings))
	}

	var codes []string
	for _, f := range report.Findings {
		codes = append(codes, f.Code)
	}

	hasCode := func(code string) bool {
		for _, c := range codes {
			if c == code {
				return true
			}
		}
		return false
	}

	if !hasCode("routed-missing-source") {
		t.Error("expected routed-missing-source")
	}
	if !hasCode("routed-missing-target") {
		t.Error("expected routed-missing-target")
	}
	if !hasCode("routed-wrong-edge") {
		t.Error("expected routed-wrong-edge")
	}
	if !hasCode("det-result-missing-node") {
		t.Error("expected det-result-missing-node")
	}
}

// ---------------------------------------------------------------------------
// Finding details
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_FindingContainsTraceInfo(t *testing.T) {
	g := makeGraph()

	traceID := int64(42)
	payload := routedPayload("implement", "nonexistent", "success")
	traces := []*model.Trace{
		{ID: traceID, EventType: "step.routed", Payload: payload},
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if len(report.Findings) == 0 {
		t.Fatal("expected findings")
	}

	f := report.Findings[0]
	if f.TraceID != traceID {
		t.Errorf("trace_id = %d, want %d", f.TraceID, traceID)
	}
	if f.TraceEventType != "step.routed" {
		t.Errorf("trace_event_type = %q, want step.routed", f.TraceEventType)
	}
	if f.TracePayload != payload {
		t.Errorf("trace_payload mismatch")
	}
}

// ---------------------------------------------------------------------------
// Edge case: failure edge to "complete" (e.g., terminal failure)
// ---------------------------------------------------------------------------

func TestValidateTraceConformance_FailureToComplete(t *testing.T) {
	// Graph where failure routes to complete (task gives up).
	g := &Graph{
		Name:  "fail-complete",
		Entry: "run",
		Nodes: map[string]*Node{
			"run": {Name: "run", Kind: KindAgent},
		},
		Edges: map[string]*Edges{
			"run": {Success: "complete", Failure: "complete"},
		},
	}

	traces := []*model.Trace{
		makeTrace("step.routed", routedPayload("run", "complete", "failure"), 1),
	}

	report := ValidateTraceConformance(g, "task-1", traces)
	if !report.Valid {
		for _, f := range report.Findings {
			t.Errorf("unexpected finding: %s: %s", f.Code, f.Message)
		}
		t.Error("expected valid when failure edge routes to complete")
	}
}

// ---------------------------------------------------------------------------
// ValidateTraceConformanceJSON: JSON marshaling of report
// ---------------------------------------------------------------------------

func TestValidationReport_MarshalJSON(t *testing.T) {
	report := &ValidationReport{
		Valid:     false,
		TaskID:    "task-1",
		GraphName: "test",
		TraceCount: 2,
		Findings: []ValidationFinding{{
			Kind:           "error",
			Code:           "test-code",
			Message:        "test message",
			TraceID:        1,
			TraceEventType: "step.routed",
			TracePayload:   `{"from":"a","to":"b"}`,
		}},
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded ValidationReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.Valid != report.Valid {
		t.Errorf("valid mismatch")
	}
	if decoded.TaskID != report.TaskID {
		t.Errorf("task_id mismatch")
	}
	if len(decoded.Findings) != 1 {
		t.Errorf("findings count = %d, want 1", len(decoded.Findings))
	}
}
