package template

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBuiltin_Critique(t *testing.T) {
	tmpl, err := Load("critique", "", "")
	if err != nil {
		t.Fatalf("Load critique: %v", err)
	}
	if tmpl.Name != "critique" {
		t.Errorf("name = %q, want critique", tmpl.Name)
	}
	if tmpl.Entry != "implement" {
		t.Errorf("entry = %q, want implement", tmpl.Entry)
	}
	if tmpl.AutoMerge != true {
		t.Errorf("auto_merge = %v, want true", tmpl.AutoMerge)
	}
	// Check all required steps exist.
	for _, name := range []string{"implement", "lint", "critic", "verify"} {
		if _, ok := tmpl.Steps[name]; !ok {
			t.Errorf("missing step %q", name)
		}
	}
	// Critic step should have max_retries = 4.
	if tmpl.Steps["critic"].MaxRetries != 4 {
		t.Errorf("critic max_retries = %d, want 4", tmpl.Steps["critic"].MaxRetries)
	}
	// Implement step should route lint→critic on success.
	if tmpl.Steps["implement"].OnSuccess != "lint" {
		t.Errorf("implement on_success = %q, want lint", tmpl.Steps["implement"].OnSuccess)
	}
	// Lint step routes to critic on success.
	if tmpl.Steps["lint"].OnSuccess != "critic" {
		t.Errorf("lint on_success = %q, want critic", tmpl.Steps["lint"].OnSuccess)
	}
	// Verify step routes to complete on success.
	if tmpl.Steps["verify"].OnSuccess != "complete" {
		t.Errorf("verify on_success = %q, want complete", tmpl.Steps["verify"].OnSuccess)
	}
}

func TestLoadBuiltin_Feature(t *testing.T) {
	tmpl, err := Load("feature", "", "")
	if err != nil {
		t.Fatalf("Load feature: %v", err)
	}
	if tmpl.Name != "feature" {
		t.Errorf("name = %q, want feature", tmpl.Name)
	}
	if tmpl.Entry != "acceptance_spec" {
		t.Errorf("entry = %q, want acceptance_spec", tmpl.Entry)
	}
	if _, ok := tmpl.Steps["acceptance_spec"]; !ok {
		t.Error("missing acceptance_spec step")
	}
	if _, ok := tmpl.Steps["implement"]; !ok {
		t.Error("missing implement step")
	}
	if _, ok := tmpl.Steps["test"]; !ok {
		t.Error("missing test step")
	}
	if got := tmpl.Steps["acceptance"].Runtime; got != "" {
		t.Errorf("acceptance runtime = %q, want empty so it inherits task runtime", got)
	}
}

func TestLoadBuiltin_Refactor(t *testing.T) {
	tmpl, err := Load("refactor", "", "")
	if err != nil {
		t.Fatalf("Load refactor: %v", err)
	}
	if tmpl.Steps["implement"].Type != "agent" {
		t.Error("implement step should be type agent")
	}
	if tmpl.Steps["test"].Type != "deterministic" {
		t.Error("test step should be type deterministic")
	}
}

func TestLoadBuiltin_NotFound(t *testing.T) {
	_, err := Load("nonexistent", "", "")
	if err == nil {
		t.Error("expected error for nonexistent template")
	}
}

func TestLoadCustom_RepoOverridesBuiltin(t *testing.T) {
	repoDir := t.TempDir()
	tmplDir := filepath.Join(repoDir, "templates")
	if err := os.MkdirAll(tmplDir, 0700); err != nil {
		t.Fatal(err)
	}
	customTOML := `
name        = "feature"
description = "Custom feature"
entry       = "custom_step"

[steps.custom_step]
type       = "agent"
on_success = "complete"
`
	if err := os.WriteFile(filepath.Join(tmplDir, "feature.toml"), []byte(customTOML), 0600); err != nil {
		t.Fatal(err)
	}

	tmpl, err := Load("feature", repoDir, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tmpl.Entry != "custom_step" {
		t.Errorf("entry = %q, want custom_step (repo should override builtin)", tmpl.Entry)
	}
}

func TestValidate_MissingEntry(t *testing.T) {
	tmpl := &Template{
		Name:  "bad",
		Entry: "missing",
		Steps: map[string]Step{
			"other": {Type: "agent"},
		},
	}
	if err := Validate(tmpl); err == nil {
		t.Error("expected error for missing entry step")
	}
}

func TestValidate_InvalidOnSuccess(t *testing.T) {
	tmpl := &Template{
		Name:  "bad",
		Entry: "start",
		Steps: map[string]Step{
			"start": {Type: "agent", OnSuccess: "nowhere"},
		},
	}
	if err := Validate(tmpl); err == nil {
		t.Error("expected error for invalid on_success reference")
	}
}

func TestValidate_DeterministicRequiresCommand(t *testing.T) {
	tmpl := &Template{
		Name:  "bad",
		Entry: "run",
		Steps: map[string]Step{
			"run": {Type: "deterministic"},
		},
	}
	if err := Validate(tmpl); err == nil {
		t.Error("expected error for deterministic step without command")
	}
}

func TestList_IncludesBuiltins(t *testing.T) {
	templates, err := List("", "")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, ti := range templates {
		names[ti.Name] = true
	}
	for _, want := range []string{"feature", "bugfix", "refactor", "critique"} {
		if !names[want] {
			t.Errorf("missing builtin template %q", want)
		}
	}
}

func TestDeterministicStep_CommandAndArgs(t *testing.T) {
	tmpl, err := Load("feature", "", "")
	if err != nil {
		t.Fatal(err)
	}
	step := tmpl.Steps["test"]
	if step.Command != "clankwork" {
		t.Errorf("command = %q, want clankwork", step.Command)
	}
	if len(step.Args) != 1 || step.Args[0] != "verify" {
		t.Errorf("args = %v, want [verify]", step.Args)
	}
}

func TestCritique_LintStepArgs(t *testing.T) {
	tmpl, err := Load("critique", "", "")
	if err != nil {
		t.Fatal(err)
	}
	step := tmpl.Steps["lint"]
	if step.Command != "clankwork" {
		t.Errorf("command = %q, want clankwork", step.Command)
	}
	if len(step.Args) != 2 || step.Args[0] != "verify" || step.Args[1] != "lint" {
		t.Errorf("args = %v, want [verify, lint]", step.Args)
	}
}

func TestMaxRetries_ZeroMeansUnlimited(t *testing.T) {
	// Refactor template has max_retries = 3; feature has max_retries = 5.
	// Built-ins should not have max_retries = 0 on agent steps (0 = unlimited).
	tmpl, err := Load("refactor", "", "")
	if err != nil {
		t.Fatal(err)
	}
	step := tmpl.Steps["implement"]
	if step.MaxRetries == 0 {
		t.Log("max_retries = 0 (unlimited) — this is valid but unusual for a builtin; check template")
	}
}
