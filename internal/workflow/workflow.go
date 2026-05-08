package workflow

import (
	"encoding/json"
	"fmt"

	tmplpkg "github.com/rot13maxi/clankwork/internal/template"
)

// NodeKind classifies a node as an agent step or a deterministic step.
type NodeKind string

const (
	// KindAgent is a step executed by an AI agent (requires a role).
	KindAgent NodeKind = "agent"
	// KindDeterministic is a step executed as a subprocess (requires a command).
	KindDeterministic NodeKind = "deterministic"
)

// GateClass describes the logical purpose of a node in the workflow.
type GateClass string

const (
	// GateAcceptanceSpec is the gate that authors the acceptance specification.
	GateAcceptanceSpec GateClass = "acceptance-spec"
	// GateImplementation is the gate that implements the task.
	GateImplementation GateClass = "implementation"
	// GateDeterministicVerification is the gate that runs automated checks (lint, test, etc.).
	GateDeterministicVerification GateClass = "deterministic-verification"
	// GateAcceptanceVerification is the gate that performs acceptance testing.
	GateAcceptanceVerification GateClass = "acceptance-verification"
)

// Graph is a compiled workflow graph derived from a template.
type Graph struct {
	// Name is the template name.
	Name string `json:"name"`
	// Description is the template description.
	Description string `json:"description"`
	// Entry is the name of the entry node.
	Entry string `json:"entry"`
	// AutoMerge indicates whether the task auto-merges on completion.
	AutoMerge bool `json:"auto_merge"`
	// Nodes maps node names to their definitions.
	Nodes map[string]*Node `json:"nodes"`
	// Edges maps source node names to outgoing edges.
	Edges map[string]*Edges `json:"edges"`
}

// Node is a single node in the workflow graph.
type Node struct {
	// Name is the unique node identifier.
	Name string `json:"name"`
	// Kind is the node type (agent or deterministic).
	Kind NodeKind `json:"kind"`
	// GateClass is the logical purpose of the node.
	GateClass GateClass `json:"gate_class,omitempty"`
	// Role is the agent role to assign (agent nodes only).
	Role string `json:"role,omitempty"`
	// Runtime is the runtime name (empty = inherit from task).
	Runtime string `json:"runtime,omitempty"`
	// Command is the binary name for deterministic nodes.
	Command string `json:"command,omitempty"`
	// Args are the arguments for the command.
	Args []string `json:"args,omitempty"`
	// MaxRetries is the maximum number of attempts; 0 means unlimited.
	MaxRetries int `json:"max_retries,omitempty"`
}

// Edges holds the success and failure edges for a node.
type Edges struct {
	// Success is the target node on success, or "complete" for terminal.
	Success string `json:"success"`
	// Failure is the target node on failure, or "complete" for terminal.
	Failure string `json:"failure"`
}

// CompileDiagnostic records a policy violation found during compilation.
type CompileDiagnostic struct {
	// Kind describes the category of the diagnostic.
	Kind string
	// Message is the human-readable description.
	Message string
}

// Compile compiles a Template into a typed Graph.
//
// It validates the template structurally (edge targets exist or are "complete",
// supported node kinds, deterministic nodes have commands) and enforces policy
// rules for substantive graphs (feature-style workflows).
//
// If any diagnostics are produced, the Graph is still returned (partially
// populated) alongside the diagnostics.
func Compile(tmpl *tmplpkg.Template) (*Graph, []CompileDiagnostic) {
	var diags []CompileDiagnostic
	g := &Graph{
		Name:        tmpl.Name,
		Description: tmpl.Description,
		Entry:       tmpl.Entry,
		AutoMerge:   tmpl.AutoMerge,
		Nodes:       make(map[string]*Node),
		Edges:       make(map[string]*Edges),
	}

	// Build node map.
	for name, step := range tmpl.Steps {
		kind := NodeKind(step.Type)
		gc := classifyGate(name, step)
		node := &Node{
			Name:      name,
			Kind:      kind,
			GateClass: gc,
			Role:      step.Role,
			Runtime:   step.Runtime,
			Command:   step.Command,
			Args:      step.Args,
			MaxRetries: step.MaxRetries,
		}

		// Policy: supported node kinds.
		switch kind {
		case KindAgent, KindDeterministic:
			// valid
		default:
			diags = append(diags, CompileDiagnostic{
				Kind:    "unsupported-node-kind",
				Message: fmt.Sprintf("step %q: unsupported node kind %q (must be %s or %s)", name, step.Type, KindAgent, KindDeterministic),
			})
		}

		// Policy: deterministic nodes require a command.
		if kind == KindDeterministic && step.Command == "" {
			diags = append(diags, CompileDiagnostic{
				Kind:    "missing-command",
				Message: fmt.Sprintf("step %q: deterministic node requires a command", name),
			})
		}

		g.Nodes[name] = node
		g.Edges[name] = &Edges{
			Success: step.OnSuccess,
			Failure: step.OnFailure,
		}
	}

	// Policy: entry node must exist.
	if _, ok := g.Nodes[g.Entry]; !ok {
		diags = append(diags, CompileDiagnostic{
			Kind:    "missing-entry",
			Message: fmt.Sprintf("entry node %q not found", g.Entry),
		})
	}

	// Policy: edge targets must exist or be "complete".
	for name, edges := range g.Edges {
		if edges.Success != "" && edges.Success != "complete" {
			if _, ok := g.Nodes[edges.Success]; !ok {
				diags = append(diags, CompileDiagnostic{
					Kind:    "missing-target",
					Message: fmt.Sprintf("step %q: on_success target %q not found", name, edges.Success),
				})
			}
		}
		if edges.Failure != "" && edges.Failure != "complete" {
			if _, ok := g.Nodes[edges.Failure]; !ok {
				diags = append(diags, CompileDiagnostic{
					Kind:    "missing-target",
					Message: fmt.Sprintf("step %q: on_failure target %q not found", name, edges.Failure),
				})
			}
		}
	}

	// Policy: substantive graph gate requirements (feature-style templates).
	checkSubstantiveGraphPolicy(g, &diags)

	return g, diags
}

// classifyGate maps a step name and type to a logical gate class.
//
// This uses naming conventions and role hints. More sophisticated classification
// can be added later as the IR evolves.
func classifyGate(name string, step tmplpkg.Step) GateClass {
	switch name {
	case "acceptance_spec":
		return GateAcceptanceSpec
	case "acceptance":
		return GateAcceptanceVerification
	}

	// Deterministic verification steps: lint, typecheck, test, verify.
	if step.Type == "deterministic" {
		switch name {
		case "lint", "typecheck", "test", "verify":
			return GateDeterministicVerification
		}
	}

	// Agent implementation steps.
	if step.Type == "agent" {
		switch name {
		case "implement":
			return GateImplementation
		}
		// Other agent roles that are clearly implementation (not spec or acceptance).
		switch step.Role {
		case "implementer", "bugfixer", "refactorer":
			return GateImplementation
		case "acceptance":
			return GateAcceptanceVerification
		case "acceptance-author":
			return GateAcceptanceSpec
		}
	}

	return ""
}

// checkSubstantiveGraphPolicy enforces that feature-style substantive graphs
// contain the full verification chain:
//
//   - acceptance-spec (entry for feature templates)
//   - implementation
//   - deterministic verification
//   - acceptance verification
//
// And that the ordering is correct:
//   - implementation must precede deterministic verification
//   - deterministic verification must precede acceptance verification
func checkSubstantiveGraphPolicy(g *Graph, diags *[]CompileDiagnostic) {
	hasAcceptanceSpec := false
	hasImplementation := false
	hasDeterministicVerification := false
	hasAcceptanceVerification := false

	for _, node := range g.Nodes {
		switch node.GateClass {
		case GateAcceptanceSpec:
			hasAcceptanceSpec = true
		case GateImplementation:
			hasImplementation = true
		case GateDeterministicVerification:
			hasDeterministicVerification = true
		case GateAcceptanceVerification:
			hasAcceptanceVerification = true
		}
	}

	// If the graph has an acceptance-spec gate, it is a substantive graph
	// and must have all four gate classes.
	if hasAcceptanceSpec {
		if !hasImplementation {
			*diags = append(*diags, CompileDiagnostic{
				Kind:    "missing-gate",
				Message: fmt.Sprintf("graph %q: acceptance-spec gate present but no implementation gate found", g.Name),
			})
		}
		if !hasDeterministicVerification {
			*diags = append(*diags, CompileDiagnostic{
				Kind:    "missing-gate",
				Message: fmt.Sprintf("graph %q: substantive graph missing deterministic-verification gate", g.Name),
			})
		}
		if !hasAcceptanceVerification {
			*diags = append(*diags, CompileDiagnostic{
				Kind:    "missing-gate",
				Message: fmt.Sprintf("graph %q: substantive graph missing acceptance-verification gate", g.Name),
			})
		}

		// Check ordering: implementation must precede deterministic verification.
		// We do this by checking that at least one implementation node can reach
		// at least one deterministic-verification node via success edges.
		if hasImplementation && hasDeterministicVerification {
			if !canReach(g, GateImplementation, GateDeterministicVerification) {
				*diags = append(*diags, CompileDiagnostic{
					Kind:    "ordering-violation",
					Message: fmt.Sprintf("graph %q: implementation gate does not lead to deterministic-verification gate", g.Name),
				})
			}
		}

		// Check ordering: deterministic verification must precede acceptance verification.
		if hasDeterministicVerification && hasAcceptanceVerification {
			if !canReach(g, GateDeterministicVerification, GateAcceptanceVerification) {
				*diags = append(*diags, CompileDiagnostic{
					Kind:    "ordering-violation",
					Message: fmt.Sprintf("graph %q: deterministic-verification gate does not lead to acceptance-verification gate", g.Name),
				})
			}
		}
	}
}

// canReach returns true if at least one node with gateClass from can reach
// at least one node with gateClass to via success edges.
func canReach(g *Graph, from, to GateClass) bool {
	// Collect source nodes.
	var sources []string
	for name, node := range g.Nodes {
		if node.GateClass == from {
			sources = append(sources, name)
		}
	}

	// BFS from all sources along success edges.
	visited := make(map[string]bool)
	queue := make([]string, 0, len(sources))
	for _, s := range sources {
		queue = append(queue, s)
		visited[s] = true
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		edges := g.Edges[current]
		if edges == nil || edges.Success == "" {
			continue
		}

		// "complete" is terminal — no further traversal.
		if edges.Success == "complete" {
			continue
		}

		target := edges.Success
		if visited[target] {
			continue
		}
		visited[target] = true

		// Check if this target has the desired gate class.
		if node, ok := g.Nodes[target]; ok && node.GateClass == to {
			return true
		}

		queue = append(queue, target)
	}

	return false
}

// MarshalGraph serializes a Graph to JSON bytes.
func MarshalGraph(g *Graph) ([]byte, error) {
	return json.Marshal(g)
}

// MarshalGraphString serializes a Graph to a JSON string.
func MarshalGraphString(g *Graph) (string, error) {
	b, err := MarshalGraph(g)
	return string(b), err
}
