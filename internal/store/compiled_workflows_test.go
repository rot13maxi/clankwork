package store

import (
	"context"
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/model"
)

func TestCompiledWorkflowCreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Need a task for the foreign key.
	s.TaskCreate(ctx, "wf-task", "", "", "workflow task", "", "", "", "", 0)

	graphJSON := `{"nodes":[{"id":"start","type":"entry"}],"edges":[],"metadata":{"template":"feature"}}`

	wf := &model.CompiledWorkflow{
		ID:            "wf01",
		TaskID:        "wf-task",
		SourceType:    "template",
		SourceName:    "feature",
		SourceRef:     "v1",
		PolicyVersion: "2",
		GraphJSON:     graphJSON,
	}

	if err := s.CompiledWorkflowCreate(ctx, wf); err != nil {
		t.Fatalf("CompiledWorkflowCreate: %v", err)
	}

	// Graph should round-trip correctly.
	got, err := s.CompiledWorkflowGetByTask(ctx, "wf-task")
	if err != nil {
		t.Fatalf("CompiledWorkflowGetByTask: %v", err)
	}
	if got.ID != "wf01" {
		t.Errorf("id = %q, want wf01", got.ID)
	}
	if got.GraphJSON != graphJSON {
		t.Errorf("graph_json mismatch: got %q", got.GraphJSON)
	}
	if got.SourceType != "template" {
		t.Errorf("source_type = %q, want template", got.SourceType)
	}
	if got.SourceRef != "v1" {
		t.Errorf("source_ref = %q, want v1", got.SourceRef)
	}
	if got.PolicyVersion != "2" {
		t.Errorf("policy_version = %q, want 2", got.PolicyVersion)
	}
	if got.Status != "active" {
		t.Errorf("status = %q, want active", got.Status)
	}
	if !strings.HasPrefix(got.GraphHash, "sha256:") {
		t.Errorf("graph_hash = %q, want sha256 digest", got.GraphHash)
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("updated_at is zero")
	}
}

func TestCompiledWorkflowMissingTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.CompiledWorkflowGetByTask(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing task")
	}
	if !strings.Contains(err.Error(), "no compiled workflow") {
		t.Errorf("error message = %q, want 'no compiled workflow' substring", err.Error())
	}
}

func TestCompiledWorkflowImmutable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "wf-immutable-task", "", "", "immutable task", "", "", "", "", 0)

	// Create initial workflow.
	wf1 := &model.CompiledWorkflow{
		ID:            "wf-original",
		TaskID:        "wf-immutable-task",
		SourceType:    "template",
		SourceName:    "feature",
		SourceRef:     "v1",
		PolicyVersion: "1",
		GraphJSON:     `{"nodes":[{"id":"start"}],"edges":[]}`,
	}
	if err := s.CompiledWorkflowCreate(ctx, wf1); err != nil {
		t.Fatalf("initial create: %v", err)
	}
	if err := s.CompiledWorkflowCreate(ctx, wf1); err != nil {
		t.Fatalf("same graph should be idempotent: %v", err)
	}

	// Replacing with a different graph is rejected.
	wf2 := &model.CompiledWorkflow{
		ID:            "wf-replacement",
		TaskID:        "wf-immutable-task",
		SourceType:    "template",
		SourceName:    "feature",
		SourceRef:     "v2",
		PolicyVersion: "2",
		GraphJSON:     `{"nodes":[{"id":"start"},{"id":"implement"}],"edges":[{"from":"start","to":"implement"}]}`,
	}
	if err := s.CompiledWorkflowCreate(ctx, wf2); err == nil {
		t.Fatal("expected immutable graph replacement to fail")
	}

	got, err := s.CompiledWorkflowGetByTask(ctx, "wf-immutable-task")
	if err != nil {
		t.Fatalf("CompiledWorkflowGetByTask after replacement rejection: %v", err)
	}
	if got.PolicyVersion != "1" {
		t.Errorf("policy_version = %q, want original 1", got.PolicyVersion)
	}
	if got.SourceRef != "v1" {
		t.Errorf("source_ref = %q, want original v1", got.SourceRef)
	}
	if got.GraphJSON != wf1.GraphJSON {
		t.Errorf("graph_json changed after rejected replacement")
	}
}

func TestCompiledWorkflowValidation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.TaskCreate(ctx, "wf-val-task", "", "", "validation task", "", "", "", "", 0)

	// Missing task_id.
	err := s.CompiledWorkflowCreate(ctx, &model.CompiledWorkflow{
		ID:        "wf-no-task",
		GraphJSON: `{}`,
	})
	if err == nil {
		t.Fatal("expected error for missing task_id")
	}
	if !strings.Contains(err.Error(), "task_id is required") {
		t.Errorf("error = %q, want 'task_id is required'", err.Error())
	}

	// Missing graph_json.
	err = s.CompiledWorkflowCreate(ctx, &model.CompiledWorkflow{
		ID:     "wf-no-graph",
		TaskID: "wf-val-task",
	})
	if err == nil {
		t.Fatal("expected error for missing graph_json")
	}
	if !strings.Contains(err.Error(), "graph_json is required") {
		t.Errorf("error = %q, want 'graph_json is required'", err.Error())
	}
}
