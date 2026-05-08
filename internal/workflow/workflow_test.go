package workflow

import (
	"testing"

	tmplpkg "github.com/rot13maxi/clankwork/internal/template"
)

// Helper: compile a builtin template by name.
func compileBuiltin(name string, t *testing.T) *Graph {
	t.Helper()
	tmpl, err := tmplpkg.Load(name, "", "")
	if err != nil {
		t.Fatalf("load template %q: %v", name, err)
	}
	g, diags := Compile(tmpl)
	for _, d := range diags {
		t.Errorf("unexpected diagnostic: %s: %s", d.Kind, d.Message)
	}
	return g
}

// ---------------------------------------------------------------------------
// Built-in template compilation tests
// ---------------------------------------------------------------------------

func TestCompile_Feature(t *testing.T) {
	g := compileBuiltin("feature", t)

	if g.Name != "feature" {
		t.Errorf("name = %q, want feature", g.Name)
	}
	if g.Entry != "acceptance_spec" {
		t.Errorf("entry = %q, want acceptance_spec", g.Entry)
	}
	if !g.AutoMerge {
		t.Error("auto_merge should be true")
	}

	// Check all expected nodes exist.
	expectedSteps := []string{"acceptance_spec", "implement", "lint", "typecheck", "test", "acceptance"}
	for _, name := range expectedSteps {
		if _, ok := g.Nodes[name]; !ok {
			t.Errorf("missing node %q", name)
		}
	}

	// Check gate classes.
	if g.Nodes["acceptance_spec"].GateClass != GateAcceptanceSpec {
		t.Errorf("acceptance_spec gate = %q, want %s", g.Nodes["acceptance_spec"].GateClass, GateAcceptanceSpec)
	}
	if g.Nodes["implement"].GateClass != GateImplementation {
		t.Errorf("implement gate = %q, want %s", g.Nodes["implement"].GateClass, GateImplementation)
	}
	if g.Nodes["lint"].GateClass != GateDeterministicVerification {
		t.Errorf("lint gate = %q, want %s", g.Nodes["lint"].GateClass, GateDeterministicVerification)
	}
	if g.Nodes["typecheck"].GateClass != GateDeterministicVerification {
		t.Errorf("typecheck gate = %q, want %s", g.Nodes["typecheck"].GateClass, GateDeterministicVerification)
	}
	if g.Nodes["test"].GateClass != GateDeterministicVerification {
		t.Errorf("test gate = %q, want %s", g.Nodes["test"].GateClass, GateDeterministicVerification)
	}
	if g.Nodes["acceptance"].GateClass != GateAcceptanceVerification {
		t.Errorf("acceptance gate = %q, want %s", g.Nodes["acceptance"].GateClass, GateAcceptanceVerification)
	}

	// Check node kinds.
	if g.Nodes["acceptance_spec"].Kind != KindAgent {
		t.Error("acceptance_spec should be agent kind")
	}
	if g.Nodes["implement"].Kind != KindAgent {
		t.Error("implement should be agent kind")
	}
	if g.Nodes["lint"].Kind != KindDeterministic {
		t.Error("lint should be deterministic kind")
	}

	// Check edges (success chain).
	chain := []struct{ from, to string }{
		{"acceptance_spec", "implement"},
		{"implement", "lint"},
		{"lint", "typecheck"},
		{"typecheck", "test"},
		{"test", "acceptance"},
	}
	for _, e := range chain {
		if g.Edges[e.from].Success != e.to {
			t.Errorf("edge %s.success = %q, want %q", e.from, g.Edges[e.from].Success, e.to)
		}
	}
	if g.Edges["acceptance"].Success != "complete" {
		t.Errorf("acceptance.success = %q, want complete", g.Edges["acceptance"].Success)
	}

	// Check max_retries.
	if g.Nodes["acceptance_spec"].MaxRetries != 2 {
		t.Errorf("acceptance_spec max_retries = %d, want 2", g.Nodes["acceptance_spec"].MaxRetries)
	}
	if g.Nodes["implement"].MaxRetries != 5 {
		t.Errorf("implement max_retries = %d, want 5", g.Nodes["implement"].MaxRetries)
	}
	if g.Nodes["acceptance"].MaxRetries != 3 {
		t.Errorf("acceptance max_retries = %d, want 3", g.Nodes["acceptance"].MaxRetries)
	}

	// Check roles.
	if g.Nodes["acceptance_spec"].Role != "acceptance-author" {
		t.Errorf("acceptance_spec role = %q, want acceptance-author", g.Nodes["acceptance_spec"].Role)
	}
	if g.Nodes["implement"].Role != "implementer" {
		t.Errorf("implement role = %q, want implementer", g.Nodes["implement"].Role)
	}
	if g.Nodes["acceptance"].Role != "acceptance" {
		t.Errorf("acceptance role = %q, want acceptance", g.Nodes["acceptance"].Role)
	}

	// Check deterministic commands.
	if g.Nodes["lint"].Command != "clankwork" {
		t.Errorf("lint command = %q, want clankwork", g.Nodes["lint"].Command)
	}
}

func TestCompile_Bugfix(t *testing.T) {
	g := compileBuiltin("bugfix", t)

	if g.Name != "bugfix" {
		t.Errorf("name = %q, want bugfix", g.Name)
	}
	if g.Entry != "implement" {
		t.Errorf("entry = %q, want implement", g.Entry)
	}
	if !g.AutoMerge {
		t.Error("auto_merge should be true")
	}

	// Bugfix has implementation and deterministic verification but no acceptance-spec gate.
	// It should compile cleanly without substantive-graph gate errors.
	if g.Nodes["implement"].Kind != KindAgent {
		t.Error("implement should be agent kind")
	}
	if g.Nodes["test"].Kind != KindDeterministic {
		t.Error("test should be deterministic kind")
	}

	// Bugfix has no acceptance-verification gate — that's expected and valid.
	if _, ok := g.Nodes["acceptance"]; ok {
		t.Error("bugfix template should not have an acceptance node")
	}
}

func TestCompile_Simple(t *testing.T) {
	g := compileBuiltin("simple", t)

	if g.Name != "simple" {
		t.Errorf("name = %q, want simple", g.Name)
	}
	if g.Entry != "implement" {
		t.Errorf("entry = %q, want implement", g.Entry)
	}

	// Single node — routes to complete on success.
	if len(g.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(g.Nodes))
	}
	if g.Edges["implement"].Success != "complete" {
		t.Errorf("implement.success = %q, want complete", g.Edges["implement"].Success)
	}
	if g.Edges["implement"].Failure != "implement" {
		t.Errorf("implement.failure = %q, want implement", g.Edges["implement"].Failure)
	}
}

func TestCompile_Refactor(t *testing.T) {
	g := compileBuiltin("refactor", t)

	if g.Name != "refactor" {
		t.Errorf("name = %q, want refactor", g.Name)
	}
	if g.Nodes["implement"].Role != "refactorer" {
		t.Errorf("implement role = %q, want refactorer", g.Nodes["implement"].Role)
	}
	if g.Nodes["implement"].Kind != KindAgent {
		t.Error("implement should be agent kind")
	}
	// Deterministic verification gates exist.
	if g.Nodes["lint"].GateClass != GateDeterministicVerification {
		t.Errorf("lint gate = %q, want %s", g.Nodes["lint"].GateClass, GateDeterministicVerification)
	}
	if g.Nodes["test"].GateClass != GateDeterministicVerification {
		t.Errorf("test gate = %q, want %s", g.Nodes["test"].GateClass, GateDeterministicVerification)
	}
}

func TestCompile_Critique(t *testing.T) {
	g := compileBuiltin("critique", t)

	if g.Name != "critique" {
		t.Errorf("name = %q, want critique", g.Name)
	}
	if g.Entry != "implement" {
		t.Errorf("entry = %q, want implement", g.Entry)
	}

	// Critic is an agent node — should be kind Agent.
	if g.Nodes["critic"].Kind != KindAgent {
		t.Errorf("critic kind = %q, want %s", g.Nodes["critic"].Kind, KindAgent)
	}
	// Critic has no recognized gate class (not implement, spec, or acceptance-verification).
	if g.Nodes["critic"].GateClass != "" {
		t.Errorf("critic gate class = %q, want empty (unrecognized gate)", g.Nodes["critic"].GateClass)
	}
	// Verify step is deterministic-verification.
	if g.Nodes["verify"].GateClass != GateDeterministicVerification {
		t.Errorf("verify gate = %q, want %s", g.Nodes["verify"].GateClass, GateDeterministicVerification)
	}
}

// ---------------------------------------------------------------------------
// Edge target validation tests
// ---------------------------------------------------------------------------

func TestCompile_InvalidEdgeTarget(t *testing.T) {
	tmpl := &tmplpkg.Template{
		Name:  "bad-edges",
		Entry: "start",
		Steps: map[string]tmplpkg.Step{
			"start": {
				Type:      "agent",
				Role:      "implementer",
				OnSuccess: "nonexistent",
			},
		},
	}

	_, diags := Compile(tmpl)
	if len(diags) == 0 {
		t.Error("expected diagnostics for invalid edge target")
	}
	found := false
	for _, d := range diags {
		if d.Kind == "missing-target" {
			found = true
		}
	}
	if !found {
		t.Error("expected missing-target diagnostic")
	}
}

func TestCompile_InvalidFailureTarget(t *testing.T) {
	tmpl := &tmplpkg.Template{
		Name:  "bad-failure",
		Entry: "start",
		Steps: map[string]tmplpkg.Step{
			"start": {
				Type:      "agent",
				Role:      "implementer",
				OnFailure: "nowhere",
			},
		},
	}

	_, diags := Compile(tmpl)
	found := false
	for _, d := range diags {
		if d.Kind == "missing-target" {
			found = true
		}
	}
	if !found {
		t.Error("expected missing-target diagnostic for invalid on_failure")
	}
}

func TestCompile_CompleteIsTerminal(t *testing.T) {
	tmpl := &tmplpkg.Template{
		Name:  "terminal",
		Entry: "start",
		Steps: map[string]tmplpkg.Step{
			"start": {
				Type:      "agent",
				Role:      "implementer",
				OnSuccess: "complete",
				OnFailure: "complete",
			},
		},
	}

	_, diags := Compile(tmpl)
	if len(diags) > 0 {
		for _, d := range diags {
			t.Errorf("unexpected diagnostic: %s: %s", d.Kind, d.Message)
		}
		t.Error("complete should be accepted as a valid terminal target")
	}
}

// ---------------------------------------------------------------------------
// Node kind validation tests
// ---------------------------------------------------------------------------

func TestCompile_UnsupportedNodeKind(t *testing.T) {
	tmpl := &tmplpkg.Template{
		Name:  "bad-kind",
		Entry: "start",
		Steps: map[string]tmplpkg.Step{
			"start": {Type: "manual"},
		},
	}

	_, diags := Compile(tmpl)
	found := false
	for _, d := range diags {
		if d.Kind == "unsupported-node-kind" {
			found = true
		}
	}
	if !found {
		t.Error("expected unsupported-node-kind diagnostic")
	}
}

// ---------------------------------------------------------------------------
// Deterministic command requirement tests
// ---------------------------------------------------------------------------

func TestCompile_DeterministicMissingCommand(t *testing.T) {
	tmpl := &tmplpkg.Template{
		Name:  "bad-deterministic",
		Entry: "run",
		Steps: map[string]tmplpkg.Step{
			"run": {Type: "deterministic"},
		},
	}

	_, diags := Compile(tmpl)
	found := false
	for _, d := range diags {
		if d.Kind == "missing-command" {
			found = true
		}
	}
	if !found {
		t.Error("expected missing-command diagnostic for deterministic node without command")
	}
}

// ---------------------------------------------------------------------------
// Substantive graph policy tests
// ---------------------------------------------------------------------------

func TestCompile_SubstantiveGraph_MissingImplementation(t *testing.T) {
	tmpl := &tmplpkg.Template{
		Name:  "bad-substantive",
		Entry: "acceptance_spec",
		Steps: map[string]tmplpkg.Step{
			"acceptance_spec": {
				Type:      "agent",
				Role:      "acceptance-author",
				OnSuccess: "complete",
			},
		},
	}

	_, diags := Compile(tmpl)
	// Should have multiple diagnostics: missing implementation, missing deterministic-verification,
	// missing acceptance-verification.
	var kinds []string
	for _, d := range diags {
		kinds = append(kinds, d.Kind)
	}
	foundMissingGate := false
	for _, k := range kinds {
		if k == "missing-gate" {
			foundMissingGate = true
			break
		}
	}
	if !foundMissingGate {
		t.Error("expected missing-gate diagnostics for substantive graph missing gates")
	}
}

func TestCompile_SubstantiveGraph_OrderingViolation(t *testing.T) {
	// Build a substantive graph where implementation does NOT reach deterministic verification.
	// acceptance_spec → implement → complete (skips verification)
	tmpl := &tmplpkg.Template{
		Name:  "bad-ordering",
		Entry: "acceptance_spec",
		Steps: map[string]tmplpkg.Step{
			"acceptance_spec": {
				Type:      "agent",
				Role:      "acceptance-author",
				OnSuccess: "implement",
			},
			"implement": {
				Type:      "agent",
				Role:      "implementer",
				OnSuccess: "complete", // skips lint/test
				OnFailure: "implement",
			},
		},
	}

	_, diags := Compile(tmpl)
	// Should have ordering-violation and missing-gate diagnostics.
	var kinds []string
	for _, d := range diags {
		kinds = append(kinds, d.Kind)
	}
	foundOrdering := false
	for _, k := range kinds {
		if k == "ordering-violation" || k == "missing-gate" {
			foundOrdering = true
			break
		}
	}
	if !foundOrdering {
		t.Errorf("expected ordering-violation or missing-gate diagnostics, got: %v", kinds)
	}
}

func TestCompile_SubstantiveGraph_CorrectOrdering(t *testing.T) {
	// This is essentially the feature template — should compile cleanly.
	tmpl := &tmplpkg.Template{
		Name:  "good-substantive",
		Entry: "acceptance_spec",
		Steps: map[string]tmplpkg.Step{
			"acceptance_spec": {
				Type:      "agent",
				Role:      "acceptance-author",
				OnSuccess: "implement",
			},
			"implement": {
				Type:      "agent",
				Role:      "implementer",
				OnSuccess: "test",
				OnFailure: "implement",
			},
			"test": {
				Type:      "deterministic",
				Command:   "go",
				Args:      []string{"test", "./..."},
				OnSuccess: "acceptance",
				OnFailure: "implement",
			},
			"acceptance": {
				Type:      "agent",
				Role:      "acceptance",
				OnSuccess: "complete",
				OnFailure: "implement",
			},
		},
	}

	g, diags := Compile(tmpl)
	if len(diags) > 0 {
		for _, d := range diags {
			t.Errorf("unexpected diagnostic: %s: %s", d.Kind, d.Message)
		}
	}

	// Verify gate ordering is correct.
	if g.Nodes["implement"].GateClass != GateImplementation {
		t.Error("implement should be GateImplementation")
	}
	if g.Nodes["test"].GateClass != GateDeterministicVerification {
		t.Error("test should be GateDeterministicVerification")
	}
	if g.Nodes["acceptance"].GateClass != GateAcceptanceVerification {
		t.Error("acceptance should be GateAcceptanceVerification")
	}
}

func TestCompile_NonSubstantive_NoGateChecks(t *testing.T) {
	// A simple template without acceptance_spec should NOT get substantive graph
	// gate checks.
	tmpl := &tmplpkg.Template{
		Name:  "simple-no-spec",
		Entry: "run",
		Steps: map[string]tmplpkg.Step{
			"run": {
				Type:      "agent",
				Role:      "implementer",
				OnSuccess: "complete",
			},
		},
	}

	_, diags := Compile(tmpl)
	// Should have no diagnostics — this is a valid minimal graph.
	if len(diags) > 0 {
		for _, d := range diags {
			t.Errorf("unexpected diagnostic: %s: %s", d.Kind, d.Message)
		}
		t.Error("non-substantive graph should not trigger gate policy checks")
	}
}

// ---------------------------------------------------------------------------
// canReach helper tests
// ---------------------------------------------------------------------------

func TestCanReach_LinearChain(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"a": {Name: "a", GateClass: GateImplementation},
			"b": {Name: "b", GateClass: GateDeterministicVerification},
			"c": {Name: "c", GateClass: GateAcceptanceVerification},
		},
		Edges: map[string]*Edges{
			"a": {Success: "b"},
			"b": {Success: "c"},
			"c": {Success: "complete"},
		},
	}

	if !canReach(g, GateImplementation, GateDeterministicVerification) {
		t.Error("implementation should reach deterministic-verification")
	}
	if !canReach(g, GateDeterministicVerification, GateAcceptanceVerification) {
		t.Error("deterministic-verification should reach acceptance-verification")
	}
	if !canReach(g, GateImplementation, GateAcceptanceVerification) {
		t.Error("implementation should reach acceptance-verification (transitively)")
	}
}

func TestCanReach_BranchingPath(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"impl": {Name: "impl", GateClass: GateImplementation},
			"lint": {Name: "lint", GateClass: GateDeterministicVerification},
			"acc":  {Name: "acc", GateClass: GateAcceptanceVerification},
		},
		Edges: map[string]*Edges{
			"impl": {Success: "lint"},
			"lint": {Success: "acc"},
			"acc":  {Success: "complete"},
		},
	}

	if !canReach(g, GateImplementation, GateAcceptanceVerification) {
		t.Error("implementation should reach acceptance-verification through chain")
	}
}

func TestCanReach_NoPath(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"impl": {Name: "impl", GateClass: GateImplementation},
			"acc":  {Name: "acc", GateClass: GateAcceptanceVerification},
		},
		Edges: map[string]*Edges{
			"impl": {Success: "complete"}, // skips acc
			"acc":  {Success: "complete"},
		},
	}

	if canReach(g, GateImplementation, GateAcceptanceVerification) {
		t.Error("implementation should NOT reach acceptance-verification when path is broken")
	}
}

func TestCanReach_CompleteTerminal(t *testing.T) {
	g := &Graph{
		Nodes: map[string]*Node{
			"impl": {Name: "impl", GateClass: GateImplementation},
		},
		Edges: map[string]*Edges{
			"impl": {Success: "complete"},
		},
	}

	if canReach(g, GateImplementation, GateAcceptanceVerification) {
		t.Error("complete should be terminal — no further reach")
	}
}

// ---------------------------------------------------------------------------
// compile diagnostic integration test
// ---------------------------------------------------------------------------

func TestCompile_MultipleDiagnostics(t *testing.T) {
	tmpl := &tmplpkg.Template{
		Name:  "many-errors",
		Entry: "acceptance_spec",
		Steps: map[string]tmplpkg.Step{
			"acceptance_spec": {
				Type:      "manual", // unsupported node kind
				OnSuccess: "missing", // missing target
			},
		},
	}

	_, diags := Compile(tmpl)
	if len(diags) < 2 {
		t.Errorf("expected at least 2 diagnostics, got %d", len(diags))
	}
	var kinds []string
	for _, d := range diags {
		kinds = append(kinds, d.Kind)
	}
	// Should have unsupported-node-kind, missing-target, and possibly missing-gate.
	hasUnsupported := false
	hasMissingTarget := false
	hasMissingGate := false
	for _, k := range kinds {
		if k == "unsupported-node-kind" {
			hasUnsupported = true
		}
		if k == "missing-target" {
			hasMissingTarget = true
		}
		if k == "missing-gate" {
			hasMissingGate = true
		}
	}
	if !hasUnsupported {
		t.Error("expected unsupported-node-kind")
	}
	if !hasMissingTarget {
		t.Error("expected missing-target")
	}
	if !hasMissingGate {
		t.Error("expected missing-gate (substantive graph missing implementation)")
	}
}
