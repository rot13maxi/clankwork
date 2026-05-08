package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
)

func TestPrintAcceptanceJSON_Full(t *testing.T) {
	detail := map[string]any{
		"acceptance_spec": map[string]any{
			"task_id": "T1",
			"criteria": []any{
				map[string]any{
					"id":                 "C1",
					"description":        "Feature works",
					"probes":             []any{"run tests"},
					"required_artifacts": []any{"test_output"},
					"fail_if":            []any{"tests fail"},
				},
			},
		},
		"done_bundle": map[string]any{
			"task_id":   "T1",
			"summary":   "Implemented the feature",
			"claims":    []any{map[string]any{"criterion_id": "C1", "status": "satisfied"}},
			"artifacts": []any{map[string]any{"type": "test_output", "path": "artifacts/tests.txt"}},
		},
		"verification_report": map[string]any{
			"task_id":    "T1",
			"confidence": "high",
			"results": []any{
				map[string]any{
					"criterion_id": "C1",
					"status":       "pass",
					"reason":       "All tests passed",
					"evidence":     []any{map[string]any{"type": "test_output", "path": "artifacts/tests.txt"}},
				},
			},
			"failures": []any{},
		},
		"verification_verdict": "pass",
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := printAcceptanceJSON(detail, "T1")
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("printAcceptanceJSON: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)

	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	for _, key := range []string{"task_id", "acceptance_spec", "done_bundle", "verification_report", "verification_verdict"} {
		if _, ok := out[key]; !ok {
			t.Errorf("missing key %q in JSON output", key)
		}
	}
	if out["task_id"] != "T1" {
		t.Errorf("task_id = %v, want T1", out["task_id"])
	}
	if out["verification_verdict"] != "pass" {
		t.Errorf("verification_verdict = %v, want pass", out["verification_verdict"])
	}
	if out["acceptance_spec"] == nil {
		t.Error("acceptance_spec should not be nil")
	}
	if out["done_bundle"] == nil {
		t.Error("done_bundle should not be nil")
	}
	if out["verification_report"] == nil {
		t.Error("verification_report should not be nil")
	}
}

func TestPrintAcceptanceJSON_MissingArtifacts(t *testing.T) {
	detail := map[string]any{}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := printAcceptanceJSON(detail, "T2")
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("printAcceptanceJSON: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)

	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if out["task_id"] != "T2" {
		t.Errorf("task_id = %v, want T2", out["task_id"])
	}
	if out["acceptance_spec"] != nil {
		t.Errorf("acceptance_spec should be nil, got %v", out["acceptance_spec"])
	}
	if out["done_bundle"] != nil {
		t.Errorf("done_bundle should be nil, got %v", out["done_bundle"])
	}
	if out["verification_report"] != nil {
		t.Errorf("verification_report should be nil, got %v", out["verification_report"])
	}
	if out["verification_verdict"] != "" {
		t.Errorf("verification_verdict should be empty, got %v", out["verification_verdict"])
	}
}

func TestPrintAcceptanceHuman_MissingArtifacts(t *testing.T) {
	detail := map[string]any{}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := printAcceptanceHuman(detail, "T3")
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("printAcceptanceHuman: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "Task: T3") {
		t.Error("output should contain task ID")
	}
	for _, section := range []string{"Acceptance Spec", "Done Bundle", "Verification"} {
		if !strings.Contains(output, section) {
			t.Errorf("output should contain section %q", section)
		}
	}
	if strings.Count(output, "(none)") < 3 {
		t.Error("output should show (none) for all three missing sections")
	}
}

func TestPrintAcceptanceHuman_Full(t *testing.T) {
	detail := map[string]any{
		"acceptance_spec": map[string]any{
			"task_id": "T4",
			"criteria": []any{
				map[string]any{
					"id":                 "C1",
					"description":        "Works correctly",
					"probes":             []any{"run tests"},
					"required_artifacts": []any{"test_output"},
					"fail_if":            []any{"tests fail"},
				},
			},
		},
		"done_bundle": map[string]any{
			"task_id":     "T4",
			"summary":     "Did the thing",
			"claims":      []any{map[string]any{"criterion_id": "C1", "status": "satisfied"}},
			"artifacts":   []any{map[string]any{"type": "test_output", "path": "out.txt"}},
			"tests_run":   []any{"go test ./..."},
			"known_risks": []any{"none"},
		},
		"verification_report": map[string]any{
			"task_id":             "T4",
			"confidence":          "high",
			"computed_confidence": 0.92,
			"confidence_label":    "high",
			"results": []any{
				map[string]any{
					"criterion_id": "C1",
					"status":       "pass",
					"reason":       "All good",
					"evidence":     []any{map[string]any{"type": "test_output", "path": "out.txt"}},
				},
			},
			"failures": []any{},
		},
		"verification_verdict": "pass",
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := printAcceptanceHuman(detail, "T4")
	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("printAcceptanceHuman: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	for _, want := range []string{
		"Task: T4",
		"C1: Works correctly",
		"Did the thing",
		"C1: satisfied",
		"test_output: out.txt",
		"go test ./...",
		"Verdict: pass",
		"Agent confidence: high",
		"Computed confidence: 0.92 (high)",
		"C1: pass",
		"All good",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestJsonOrNull(t *testing.T) {
	m := map[string]any{"a": "val", "b": nil}
	if jsonOrNull(m, "a") != "val" {
		t.Error("existing key should return value")
	}
	if jsonOrNull(m, "b") != nil {
		t.Error("nil value should return nil")
	}
	if jsonOrNull(m, "c") != nil {
		t.Error("missing key should return nil")
	}
}

func TestStringOrEmpty(t *testing.T) {
	m := map[string]any{"a": "val", "b": nil, "c": 42}
	if stringOrEmpty(m, "a") != "val" {
		t.Error("string value should return string")
	}
	if stringOrEmpty(m, "b") != "" {
		t.Error("nil should return empty string")
	}
	if stringOrEmpty(m, "c") != "" {
		t.Error("non-string should return empty string")
	}
	if stringOrEmpty(m, "d") != "" {
		t.Error("missing key should return empty string")
	}
}

func TestSmokeCaseObserved(t *testing.T) {
	tests := []struct {
		name       string
		task       *model.Task
		validation *model.ControlObservation
		verdict    string
		want       bool
	}{
		{
			name:    "pass",
			task:    &model.Task{Status: "merged", CurrentStep: "acceptance"},
			verdict: "pass",
			want:    true,
		},
		{
			name:    "verification-fail",
			task:    &model.Task{Status: "pending", CurrentStep: "implement"},
			verdict: "fail",
			want:    true,
		},
		{
			name:       "done-bundle-reject",
			task:       &model.Task{Status: "running", CurrentStep: "implement"},
			validation: &model.ControlObservation{Reason: `claim C1 missing required artifact type "cli_transcript"`},
			want:       true,
		},
		{
			name:       "verification-report-reject",
			task:       &model.Task{Status: "running", CurrentStep: "acceptance"},
			validation: &model.ControlObservation{Reason: "result C1 evidence[0].content_hash required for path artifacts"},
			want:       true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, _, err := smokeCaseObserved(&fakeSmokeClient{
				task:       tt.task,
				validation: tt.validation,
				verdict:    tt.verdict,
			}, "T1", acceptanceSmokeCase{name: tt.name})
			if err != nil {
				t.Fatal(err)
			}
			if ok != tt.want {
				t.Fatalf("observed = %v, want %v", ok, tt.want)
			}
		})
	}
}

func TestAcceptanceRiskPolicyLoadsFromHomeConfig(t *testing.T) {
	home := t.TempDir()
	cfgFile := ` 
[acceptance.risk]
high_risk_labels = ["payments", "pii"]
high_risk_paths = ["internal/security/**", "pkg/crypto"]
`
	if err := os.WriteFile(home+"/config.toml", []byte(cfgFile), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	policy, err := acceptanceRiskPolicy(home)
	if err != nil {
		t.Fatalf("acceptanceRiskPolicy: %v", err)
	}
	if len(policy.HighRiskLabels) != 2 || policy.HighRiskLabels[0] != "payments" || policy.HighRiskLabels[1] != "pii" {
		t.Fatalf("policy labels = %#v", policy.HighRiskLabels)
	}
	if len(policy.HighRiskPaths) != 2 || policy.HighRiskPaths[0] != "internal/security/**" || policy.HighRiskPaths[1] != "pkg/crypto" {
		t.Fatalf("policy paths = %#v", policy.HighRiskPaths)
	}

	policy, err = acceptanceRiskPolicy("/no-such-dir")
	if err != nil {
		t.Fatalf("acceptanceRiskPolicy fallback default: %v", err)
	}
	if !contains(policy.HighRiskLabels, config.DefaultConfig().Acceptance.Risk.HighRiskLabels[0]) {
		t.Fatalf("fallback policy should include default labels, got %#v", policy.HighRiskLabels)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

type fakeSmokeClient struct {
	task       *model.Task
	validation *model.ControlObservation
	verdict    string
}

func (f *fakeSmokeClient) TasksGet(context.Context, string) (map[string]any, error) {
	return map[string]any{"verification_verdict": f.verdict}, nil
}

func (f *fakeSmokeClient) TaskDiagnose(context.Context, string) (*model.TaskDiagnosis, error) {
	return &model.TaskDiagnosis{
		Task: f.task,
		Observed: model.ObservedTaskState{
			LatestValidation: f.validation,
		},
	}, nil
}

func (f *fakeSmokeClient) DispatchPause(context.Context) error {
	return nil
}

func (f *fakeSmokeClient) DispatchResume(context.Context) error {
	return nil
}

func (f *fakeSmokeClient) AgentCancel(context.Context, string) error {
	return nil
}

func (f *fakeSmokeClient) Signal(context.Context, string, string, string) error {
	return nil
}
