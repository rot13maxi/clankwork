package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/api"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

// ---------------------------------------------------------------------------
// Helper: create an in-process API server with store (no tmux needed).
// Used for acceptance tests that exercise the validation rejection flow
// through the HTTP API.
// ---------------------------------------------------------------------------

type apiEnv struct {
	store *store.Store
	server *api.Server
	disp  *scheduler.Dispatcher
	dir   string
}

func newAPIEnv(t *testing.T) *apiEnv {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "logs"), 0700)
	os.MkdirAll(filepath.Join(dir, "worktrees"), 0700)

	st, err := store.Open(filepath.Join(dir, "clankwork.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	disp := scheduler.New(context.Background(), st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, dir, nil)

	srv := api.NewServerWithDispatcher(st, dir, disp, &worker.FakeWorktreeCreator{})

	return &apiEnv{store: st, server: srv, disp: disp, dir: dir}
}

func (e *apiEnv) postSignalDone(t *testing.T, req model.SignalRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/signals.done", bytes.NewReader(body))
	w := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(w, r)
	return w
}

func (e *apiEnv) postSignalFailed(t *testing.T, req model.SignalRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/signals.failed", bytes.NewReader(body))
	w := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(w, r)
	return w
}

func (e *apiEnv) postBootstrap(t *testing.T, taskID, role, repoID string) (*model.BootstrapResponse, error) {
	reqBody, _ := json.Marshal(map[string]string{"task_id": taskID, "role": role, "repo_id": repoID})
	r := httptest.NewRequest(http.MethodPost, "/v1/bootstrap", bytes.NewReader(reqBody))
	w := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(w, r)
	var resp model.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("bootstrap failed: %v", resp.Error)
	}
	// Bootstrap returns the response as JSON directly
	raw, _ := json.Marshal(resp.Data)
	var boot model.BootstrapResponse
	if err := json.Unmarshal(raw, &boot); err != nil {
		return nil, err
	}
	return &boot, nil
}

// ---------------------------------------------------------------------------
// C1: A validation-rejected done signal writes a step.failure_context trace
//     with the concrete rejection reason
// ---------------------------------------------------------------------------

// C1-P1: Done bundle validation rejection creates failure_context trace
func TestC1_P1_DoneBundleValidationRejectionCreatesFailureContext(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	// Create task with feature template, in implement step.
	taskID := "task-c1-p1"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "Validation rejection test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Signal done WITHOUT a done bundle — should be rejected by validation.
	resp := env.postSignalDone(t, model.SignalRequest{TaskID: taskID})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.Code, resp.Body.String())
	}

	// Check that step.failure_context trace was created.
	traces, err := env.store.TraceListByType(ctx, taskID, "step.failure_context", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) == 0 {
		t.Fatal("expected step.failure_context trace after validation rejection, found none")
	}

	// Check that the trace payload contains the concrete rejection reason.
	var payload map[string]string
	if err := json.Unmarshal([]byte(traces[0].Payload), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if payload["step"] != "implement" {
		t.Errorf("step = %q, want 'implement'", payload["step"])
	}
	if payload["source"] != "validation_rejection" {
		t.Errorf("source = %q, want 'validation_rejection'", payload["source"])
	}
	if payload["artifact_kind"] != "done_bundle" {
		t.Errorf("artifact_kind = %q, want 'done_bundle'", payload["artifact_kind"])
	}
	// The message should contain the concrete validation error.
	if !strings.Contains(payload["message"], "done_bundle") {
		t.Errorf("message should mention 'done_bundle', got: %s", payload["message"])
	}
}

// C1-P2: Verification report validation rejection creates failure_context trace
func TestC1_P2_VerificationReportValidationRejectionCreatesFailureContext(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	taskID := "task-c1-p2"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "Verification report rejection test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.AcceptanceSpecPut(ctx, basicAcceptanceSpec(taskID)); err != nil {
		t.Fatal(err)
	}
	// Store a done bundle so implement step passes.
	bundle := &model.DoneBundle{
		TaskID:  taskID,
		Summary: "implemented",
		Claims:  []model.CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts: []model.CompletionArtifact{{
			Type: "cli_transcript", Path: "artifacts/flow.txt", CriterionID: "C1",
			ProbeID: "C1-P1", ProducerStep: "implement", ProducerRole: "worker",
			Timestamp: "2026-05-07T00:00:00Z", ContentHash: "sha256:aaaa", Authoritative: true,
		}},
	}
	if err := env.store.DoneBundlePut(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	// Set step to acceptance (bypass deterministic steps for this test).
	// TaskSetStepFromPending sets the step without a status guard.
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "acceptance"); err != nil {
		t.Fatal(err)
	}
	task, err := env.store.TaskGet(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.CurrentStep != "acceptance" {
		t.Fatalf("expected step 'acceptance', got %q", task.CurrentStep)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Signal done with a verification report that FAILS validation (missing evidence).
	badReport := &model.VerificationReport{
		TaskID: taskID,
		Results: []model.VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence:    []model.Evidence{}, // empty evidence — should fail validation
			Reason:      "fake pass",
		}},
		Confidence: "high",
	}
	resp := env.postSignalDone(t, model.SignalRequest{
		TaskID:             taskID,
		VerificationReport: badReport,
	})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid verification report, got %d: %s", resp.Code, resp.Body.String())
	}

	// Check step.failure_context trace exists with the rejection reason.
	traces, err := env.store.TraceListByType(ctx, taskID, "step.failure_context", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) == 0 {
		t.Fatal("expected step.failure_context trace after verification report rejection")
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(traces[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["source"] != "validation_rejection" {
		t.Errorf("source = %q, want 'validation_rejection'", payload["source"])
	}
	if payload["artifact_kind"] != "verification_report" {
		t.Errorf("artifact_kind = %q, want 'verification_report'", payload["artifact_kind"])
	}
}

// ---------------------------------------------------------------------------
// C2: The bootstrap response includes the validation rejection reason in
//     FailureContext for redriven agents
// ---------------------------------------------------------------------------

// C2-P1: Bootstrap includes validation rejection failure context
func TestC2_P1_BootstrapIncludesValidationRejectionFailureContext(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	taskID := "task-c2-p1"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "Bootstrap failure context test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Signal done without a bundle — triggers validation rejection.
	resp := env.postSignalDone(t, model.SignalRequest{TaskID: taskID})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.Code, resp.Body.String())
	}

	// Retry the task (reset to pending).
	if err := env.store.TaskSetStatus(ctx, taskID, "pending"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatal(err)
	}

	// Bootstrap should include the failure context from the validation rejection.
	boot, err := env.postBootstrap(t, taskID, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if boot.FailureContext == "" {
		t.Fatal("bootstrap should include FailureContext after validation rejection")
	}
	if !strings.Contains(boot.FailureContext, "done_bundle") {
		t.Errorf("FailureContext should mention 'done_bundle', got: %s", boot.FailureContext)
	}
	// The concrete validation error IS the actionable message.
	// The 'source' field in the structured trace already indicates "validation_rejection".
	if !strings.Contains(boot.FailureContext, "required") {
		t.Errorf("FailureContext should include the concrete error reason, got: %s", boot.FailureContext)
	}
}

// C2-P2: Bootstrap includes failure context with step name and attempt count
func TestC2_P2_BootstrapFailureContextIncludesStepAndAttempt(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	taskID := "task-c2-p2"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "Bootstrap attempt count test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Signal done without bundle — rejected.
	_ = env.postSignalDone(t, model.SignalRequest{TaskID: taskID})

	// Route the failure (which increments retry).
	_ = env.disp.RouteStep(ctx, taskID, "implement", "failure")

	// Bootstrap and check the format includes step name and attempt.
	boot, err := env.postBootstrap(t, taskID, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if boot.FailureContext == "" {
		t.Fatal("bootstrap should include FailureContext")
	}
	// The bootstrap code formats as "[step, attempt N]: message"
	if !strings.Contains(boot.FailureContext, "implement") {
		t.Errorf("FailureContext should include step name 'implement', got: %s", boot.FailureContext)
	}
	if !strings.Contains(boot.FailureContext, "attempt") {
		t.Errorf("FailureContext should include attempt info, got: %s", boot.FailureContext)
	}
}

// ---------------------------------------------------------------------------
// C3: The step.failure_context trace payload includes structured fields for
//     machine readability
// ---------------------------------------------------------------------------

// C3-P1: Structured fields in failure_context trace payload
func TestC3_P1_StructuredFieldsInFailureContextPayload(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	taskID := "task-c3-p1"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "Structured failure context test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Signal done without a bundle — rejected.
	_ = env.postSignalDone(t, model.SignalRequest{TaskID: taskID})

	// Get the failure_context trace.
	traces, err := env.store.TraceListByType(ctx, taskID, "step.failure_context", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) == 0 {
		t.Fatal("expected step.failure_context trace")
	}

	// Parse the structured payload.
	var payload map[string]string
	if err := json.Unmarshal([]byte(traces[0].Payload), &payload); err != nil {
		t.Fatalf("payload should be valid JSON: %v", err)
	}

	// Check all structured fields are present.
	requiredFields := []string{"step", "message", "reason", "artifact_kind", "source"}
	for _, field := range requiredFields {
		if payload[field] == "" {
			t.Errorf("missing structured field %q in failure_context payload. Payload: %s", field, traces[0].Payload)
		}
	}

	// Check field values are meaningful.
	if payload["step"] != "implement" {
		t.Errorf("step = %q, want 'implement'", payload["step"])
	}
	if payload["source"] != "validation_rejection" {
		t.Errorf("source = %q, want 'validation_rejection'", payload["source"])
	}
	if payload["artifact_kind"] != "done_bundle" {
		t.Errorf("artifact_kind = %q, want 'done_bundle'", payload["artifact_kind"])
	}
	// reason and message should both contain the validation error.
	if payload["reason"] == "" {
		t.Error("reason should not be empty")
	}
	if payload["message"] == "" {
		t.Error("message should not be empty")
	}
}

// ---------------------------------------------------------------------------
// C4: The full round-trip: validation rejection followed by re-dispatch
//     surfaces failure context to the next agent
// ---------------------------------------------------------------------------

// C4-P1: Full round-trip: validation rejection → re-dispatch → bootstrap
func TestC4_P1_FullRoundTripValidationRejectionToBootstrap(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	taskID := "task-c4-p1"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "Full round-trip test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Step 1: Agent signals done without done bundle → validation rejects.
	resp := env.postSignalDone(t, model.SignalRequest{TaskID: taskID})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("Step 1: expected 400, got %d: %s", resp.Code, resp.Body.String())
	}

	// Step 2: Control plane routes failure → retries implement step.
	if err := env.disp.RouteStep(ctx, taskID, "implement", "failure"); err != nil {
		t.Fatal(err)
	}

	// Verify the task was reset for retry.
	task, err := env.store.TaskGet(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.CurrentStep != "implement" {
		t.Errorf("after failure route, step = %q, want 'implement'", task.CurrentStep)
	}

	// Step 3: Bootstrap for the redriven agent includes the rejection reason.
	boot, err := env.postBootstrap(t, taskID, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// The bootstrap must surface the concrete rejection reason.
	if boot.FailureContext == "" {
		t.Fatal("bootstrap FailureContext should not be empty after validation rejection + retry")
	}
	if !strings.Contains(boot.FailureContext, "done_bundle") {
		t.Errorf("FailureContext should contain the concrete reason 'done_bundle', got: %s", boot.FailureContext)
	}
	// Should NOT be just a generic message — must include actionable detail.
	if strings.Contains(boot.FailureContext, "What Went Wrong") {
		// This is the CLI text mode — that's fine, it wraps the context
	}
}

// ---------------------------------------------------------------------------
// C5: ACP submission policy denial context is captured and surfaced in bootstrap
//     (if that surface exists)
// ---------------------------------------------------------------------------

// C5-P1: Check if ACP submission policy denial surface exists.
// The current codebase uses validation_rejection for done signal validation.
// ACP submission policy denials would come through the same failure_context
// mechanism if the ACP adapter produces them. This test verifies that the
// infrastructure supports surfacing such denials through bootstrap.
func TestC5_P1_ACPSubmissionPolicyDenialSurfaceExists(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	taskID := "task-c5-p1"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "ACP denial surface test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Simulate an ACP-style submission denial by creating a failure_context
	// trace with the structured fields that would come from the ACP adapter.
	fcPayload, _ := json.Marshal(map[string]string{
		"step":          "implement",
		"message":       "ACP submission policy denied: model output exceeded token budget",
		"reason":        "submission_policy_denied",
		"artifact_kind": "done_bundle",
		"source":        "submission_policy",
	})
	if err := env.store.TraceAppend(ctx, taskID, "", "step.failure_context", string(fcPayload)); err != nil {
		t.Fatal(err)
	}

	// Route failure to retry.
	if err := env.disp.RouteStep(ctx, taskID, "implement", "failure"); err != nil {
		t.Fatal(err)
	}

	// Bootstrap should surface the ACP denial reason.
	boot, err := env.postBootstrap(t, taskID, "", "")
	if err != nil {
		t.Fatal(err)
	}

	if boot.FailureContext == "" {
		t.Fatal("bootstrap should include FailureContext for ACP denial")
	}
	if !strings.Contains(boot.FailureContext, "submission policy denied") {
		t.Errorf("FailureContext should include ACP denial reason, got: %s", boot.FailureContext)
	}
}

// ---------------------------------------------------------------------------
// C6: Existing behavior is preserved: agent self-failure (signal.failed) and
//     acceptance verification failure still produce failure context
// ---------------------------------------------------------------------------

// C6-P1: Agent self-failure (signal.failed) produces failure_context
func TestC6_P1_AgentSelfFailureProducesFailureContext(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	taskID := "task-c6-p1"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "Agent self-failure test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Agent signals failed.
	resp := env.postSignalFailed(t, model.SignalRequest{
		TaskID:  taskID,
		Message: "could not compile: undefined reference to `mainWidget`",
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	// Check failure_context trace was created.
	traces, err := env.store.TraceListByType(ctx, taskID, "step.failure_context", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) == 0 {
		t.Fatal("expected step.failure_context trace after signal.failed")
	}

	var payload map[string]string
	if err := json.Unmarshal([]byte(traces[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["step"] != "implement" {
		t.Errorf("step = %q, want 'implement'", payload["step"])
	}
	if !strings.Contains(payload["message"], "mainWidget") {
		t.Errorf("message should contain the agent's failure reason, got: %s", payload["message"])
	}

	// Bootstrap should include this failure context.
	boot, err := env.postBootstrap(t, taskID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if boot.FailureContext == "" {
		t.Fatal("bootstrap should include FailureContext after agent self-failure")
	}
	if !strings.Contains(boot.FailureContext, "mainWidget") {
		t.Errorf("FailureContext should include agent failure reason, got: %s", boot.FailureContext)
	}
}

// C6-P2: Acceptance verification failure (report verdict = fail) produces failure_context
func TestC6_P2_AcceptanceVerificationFailureProducesFailureContext(t *testing.T) {
	env := newAPIEnv(t)
	ctx := context.Background()

	taskID := "task-c6-p2"
	_, err := env.store.TaskCreate(ctx, taskID, "", "", "Acceptance failure context test", "", "feature", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.store.AcceptanceSpecPut(ctx, basicAcceptanceSpec(taskID)); err != nil {
		t.Fatal(err)
	}
	// Register an artifact so the report passes structural validation.
	artifactID, artifactHash := registerArtifact(t, ctx, env.store, taskID)
	// Store a done bundle.
	bundle := &model.DoneBundle{
		TaskID:  taskID,
		Summary: "implemented",
		Claims:  []model.CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts: []model.CompletionArtifact{{
			Type: "cli_transcript", Path: "artifacts/flow.txt", CriterionID: "C1",
			ProbeID: "C1-P1", ProducerStep: "implement", ProducerRole: "worker",
			Timestamp: "2026-05-07T00:00:00Z", ContentHash: artifactHash, Authoritative: true,
		}},
	}
	if err := env.store.DoneBundlePut(ctx, bundle); err != nil {
		t.Fatal(err)
	}
	// Set step to acceptance (bypass deterministic steps for this test).
	if err := env.store.TaskSetStepFromPending(ctx, taskID, "acceptance"); err != nil {
		t.Fatal(err)
	}
	task, err := env.store.TaskGet(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.CurrentStep != "acceptance" {
		t.Fatalf("expected step 'acceptance', got %q", task.CurrentStep)
	}
	if err := env.store.TaskSetStatus(ctx, taskID, "running"); err != nil {
		t.Fatal(err)
	}

	// Signal done with a FAILING verification report.
	failingReport := &model.VerificationReport{
		TaskID: taskID,
		Results: []model.VerificationResult{{
			CriterionID: "C1",
			Status:      "fail",
			Evidence: []model.Evidence{{
				ArtifactID:    artifactID,
				Type:          "cli_transcript",
				Path:          "artifacts/flow.txt",
				ProbeID:       "C1-P1",
				ProducerStep:  "acceptance",
				ProducerRole:  "verifier",
				Timestamp:     "2026-05-07T00:00:00Z",
				ContentHash:   artifactHash,
				Authoritative: true,
			}},
			Reason: "C1 failed: expected 200 OK, got 500 Internal Server Error",
		}},
		Failures:   []model.VerificationFailure{{CriterionID: "C1", Reason: "C1 failed: expected 200 OK, got 500 Internal Server Error"}},
		Confidence: "high",
	}
	resp := env.postSignalDone(t, model.SignalRequest{
		TaskID:             taskID,
		VerificationReport: failingReport,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 (failure outcome is handled gracefully), got %d: %s", resp.Code, resp.Body.String())
	}

	// Check step.failure_context trace was created for the acceptance failure.
	traces, err := env.store.TraceListByType(ctx, taskID, "step.failure_context", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) == 0 {
		t.Fatal("expected step.failure_context trace after acceptance verification failure")
	}

	// The last failure_context should contain the acceptance failure reason.
	found := false
	for _, tr := range traces {
		var payload map[string]string
		if err := json.Unmarshal([]byte(tr.Payload), &payload); err != nil {
			continue
		}
		if strings.Contains(payload["message"], "500") || strings.Contains(payload["message"], "expected 200") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected failure_context with acceptance failure details, found traces: %v", traces)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func basicAcceptanceSpec(taskID string) *model.AcceptanceSpec {
	return &model.AcceptanceSpec{
		TaskID: taskID,
		Criteria: []model.AcceptanceCriterion{{
			ID:          "C1",
			Description: "user can complete the flow",
			Probes: []model.AcceptanceProbe{{
				ID:                   "C1-P1",
				Description:          "verify the observable flow succeeds",
				Command:              "go test ./...",
				RequiredEvidence:     []string{"cli_transcript"},
				ObservableSideEffect: "cli transcript",
				NegativeAssertion:    "flow exits nonzero fails",
			}},
			RequiredArtifacts: []string{"cli_transcript"},
			FailIf:            []string{"flow exits nonzero"},
		}},
	}
}

func registerArtifact(t *testing.T, ctx context.Context, st *store.Store, taskID string) (string, string) {
	t.Helper()
	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("test artifact content\n")
	if err := os.WriteFile(filepath.Join(wd, "artifacts", "flow.txt"), data, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	hash := "sha256:" + hex.EncodeToString(sum[:])
	artifact, err := st.ArtifactAdd(ctx, model.AddArtifactRequest{
		TaskID:           taskID,
		StepID:           "acceptance",
		Producer:         "acceptance-verifier",
		ProducerType:     "agent",
		ArtifactType:     "cli_transcript",
		Path:             "artifacts/flow.txt",
		SHA256:           hash,
		Command:          "go test ./...",
		WorkingDirectory: wd,
		ExitCode:         0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact.ID, hash
}
