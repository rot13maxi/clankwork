package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

func TestSignalDoneRejectsImplementationWithoutBundle(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	if _, err := st.TaskCreate(ctx, "task01", "", "", "Feature", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStepFromPending(ctx, "task01", "implement"); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStatus(ctx, "task01", "running"); err != nil {
		t.Fatal(err)
	}

	resp := postSignalDone(t, NewServer(st, t.TempDir()), model.SignalRequest{TaskID: "task01"})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", resp.Code, resp.Body.String())
	}

	task, err := st.TaskGet(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "running" {
		t.Fatalf("task status = %q, want running", task.Status)
	}
}

func TestSignalDoneStoresAcceptanceSpec(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	if _, err := st.TaskCreate(ctx, "task01", "", "", "Feature", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStepFromPending(ctx, "task01", "acceptance_spec"); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStatus(ctx, "task01", "running"); err != nil {
		t.Fatal(err)
	}

	resp := postSignalDone(t, NewServer(st, t.TempDir()), model.SignalRequest{
		TaskID:         "task01",
		AcceptanceSpec: signalTestSpec(),
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Code, resp.Body.String())
	}

	spec, err := st.AcceptanceSpecGet(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if spec == nil || len(spec.Criteria) != 1 {
		t.Fatalf("spec not stored: %+v", spec)
	}
}

func TestAcceptanceSpecEndpointRejectsWeakSpec(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	if _, err := st.TaskCreate(ctx, "task01", "", "", "Feature", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}

	spec := &model.AcceptanceSpec{
		TaskID: "task01",
		Criteria: []model.AcceptanceCriterion{{
			ID:                "C1",
			Description:       "weak status-only check",
			Probes:            []model.AcceptanceProbe{{ID: "P1", Description: "call API", Command: "curl /health", RequiredEvidence: []string{"api_response"}}},
			RequiredArtifacts: []string{"api_response"},
		}},
	}
	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/acceptance.spec", bytes.NewReader(body))
	resp := httptest.NewRecorder()
	NewServer(st, t.TempDir()).Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "fail_if") {
		t.Fatalf("body %s, want strength validation error", resp.Body.String())
	}
}

func TestSignalDoneEscalatesRepeatedValidationRejections(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	if _, err := st.TaskCreate(ctx, "task01", "", "", "Feature", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStepFromPending(ctx, "task01", "acceptance_spec"); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStatus(ctx, "task01", "running"); err != nil {
		t.Fatal(err)
	}
	spec := signalTestSpec()
	spec.Criteria[0].RequiredArtifacts = []string{"file_"}
	srv := NewServer(st, t.TempDir())

	for i := 0; i < validationRejectionEscalationThreshold; i++ {
		resp := postSignalDone(t, srv, model.SignalRequest{
			TaskID:         "task01",
			AcceptanceSpec: spec,
		})
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d status = %d, want 400; body %s", i+1, resp.Code, resp.Body.String())
		}
	}

	escalations, err := st.EscalationList(ctx, "task01", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(escalations) != 1 {
		t.Fatalf("open escalations = %d, want 1", len(escalations))
	}
	if escalations[0].TargetType != "parent_controller" || escalations[0].RequestedAction != "inspect_validation_loop" {
		t.Fatalf("unexpected escalation: %+v", escalations[0])
	}
}

func TestSignalDoneStoresPassingVerificationReport(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	if _, err := st.TaskCreate(ctx, "task01", "", "", "Feature", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptanceSpecPut(ctx, signalTestSpec()); err != nil {
		t.Fatal(err)
	}
	artifactID, artifactHash := registerSignalTestArtifact(t, ctx, st)
	if err := st.DoneBundlePut(ctx, signalTestDoneBundle(artifactHash)); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStepFromPending(ctx, "task01", "acceptance"); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStatus(ctx, "task01", "running"); err != nil {
		t.Fatal(err)
	}

	resp := postSignalDone(t, NewServer(st, t.TempDir()), model.SignalRequest{
		TaskID:             "task01",
		VerificationReport: signalTestReport("pass", artifactID, artifactHash),
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Code, resp.Body.String())
	}

	_, verdict, err := st.VerificationReportGet(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if verdict != "pass" {
		t.Fatalf("verdict = %q, want pass", verdict)
	}
}

func TestSignalDoneStoresFailingVerificationReportAndRoutesToImplementation(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	if _, err := st.TaskCreate(ctx, "task01", "", "", "Feature", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptanceSpecPut(ctx, signalTestSpec()); err != nil {
		t.Fatal(err)
	}
	artifactID, artifactHash := registerSignalTestArtifact(t, ctx, st)
	if err := st.DoneBundlePut(ctx, signalTestDoneBundle(artifactHash)); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStepFromPending(ctx, "task01", "acceptance"); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStatus(ctx, "task01", "running"); err != nil {
		t.Fatal(err)
	}

	disp := scheduler.New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), config.DefaultConfig())
	resp := postSignalDone(t, NewServerWithDispatcher(st, t.TempDir(), disp, &worker.FakeWorktreeCreator{}), model.SignalRequest{
		TaskID:             "task01",
		VerificationReport: signalTestReport("fail", artifactID, artifactHash),
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Code, resp.Body.String())
	}

	_, verdict, err := st.VerificationReportGet(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if verdict != "fail" {
		t.Fatalf("verdict = %q, want fail", verdict)
	}
	task, err := st.TaskGet(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "pending" || task.CurrentStep != "implement" {
		t.Fatalf("task status/step = %q/%q, want pending/implement", task.Status, task.CurrentStep)
	}
	traces, err := st.TraceListByType(ctx, "task01", "step.failure_context", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 || !strings.Contains(traces[0].Payload, "flow exited nonzero") {
		t.Fatalf("failure context = %+v, want concrete verifier failure", traces)
	}
}

func TestSignalDoneAppendsAdversarialFollowupProbes(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	if _, err := st.TaskCreate(ctx, "task01", "", "", "Auth feature", "Change auth token behavior", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptanceSpecPut(ctx, signalTestSpec()); err != nil {
		t.Fatal(err)
	}
	artifactID, artifactHash := registerSignalTestArtifact(t, ctx, st)
	if err := st.DoneBundlePut(ctx, signalTestDoneBundle(artifactHash)); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStepFromPending(ctx, "task01", "acceptance"); err != nil {
		t.Fatal(err)
	}
	if err := st.TaskSetStatus(ctx, "task01", "running"); err != nil {
		t.Fatal(err)
	}

	report := signalTestReport("pass", artifactID, artifactHash)
	report.AdversarialReview = &model.AdversarialReview{
		TaskID:           "task01",
		RequiredFollowup: true,
		AdversarialFindings: []model.AdversarialFinding{{
			Risk:           "expired tokens may still work",
			SuggestedProbe: "attempt auth with expired token",
			Severity:       "high",
		}},
	}
	resp := postSignalDone(t, NewServer(st, t.TempDir()), model.SignalRequest{
		TaskID:             "task01",
		VerificationReport: report,
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", resp.Code, resp.Body.String())
	}
	spec, err := st.AcceptanceSpecGet(ctx, "task01")
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Criteria[0].Probes) != 2 {
		t.Fatalf("probe count = %d, want appended follow-up probe", len(spec.Criteria[0].Probes))
	}
	if !strings.Contains(spec.Criteria[0].Probes[1].Description, "expired token") {
		t.Fatalf("appended probe = %+v", spec.Criteria[0].Probes[1])
	}
}

func newSignalTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func postSignalDone(t *testing.T, s *Server, req model.SignalRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/signals.done", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func signalTestSpec() *model.AcceptanceSpec {
	return &model.AcceptanceSpec{
		TaskID: "task01",
		Criteria: []model.AcceptanceCriterion{{
			ID:          "C1",
			Description: "user can complete the flow",
			Probes: []model.AcceptanceProbe{{
				ID:                   "run_flow",
				Description:          "verify the observable flow succeeds and failure case fails",
				Command:              "go test ./... -run TestFlow",
				RequiredEvidence:     []string{"cli_transcript"},
				Before:               "record initial state",
				After:                "assert final state changed",
				ObservableSideEffect: "cli transcript",
				NegativeAssertion:    "flow exits nonzero fails",
			}},
			RequiredArtifacts: []string{"cli_transcript"},
			FailIf:            []string{"flow exits nonzero"},
		}},
	}
}

func registerSignalTestArtifact(t *testing.T, ctx context.Context, st *store.Store) (string, string) {
	t.Helper()
	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("verification artifact\n")
	if err := os.WriteFile(filepath.Join(wd, "artifacts", "flow.txt"), data, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	hash := "sha256:" + hex.EncodeToString(sum[:])
	artifact, err := st.ArtifactAdd(ctx, model.AddArtifactRequest{
		TaskID:           "task01",
		StepID:           "acceptance",
		Producer:         "acceptance-verifier",
		ProducerType:     "agent",
		ArtifactType:     "cli_transcript",
		Path:             "artifacts/flow.txt",
		SHA256:           hash,
		Command:          "go test ./... -run TestFlow",
		WorkingDirectory: wd,
		ExitCode:         0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact.ID, hash
}

func signalTestReport(status string, artifactArgs ...string) *model.VerificationReport {
	id := "artifact_cli_transcript"
	hash := "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	if len(artifactArgs) > 0 {
		id = artifactArgs[0]
	}
	if len(artifactArgs) > 1 {
		hash = artifactArgs[1]
	}
	report := &model.VerificationReport{
		TaskID: "task01",
		Results: []model.VerificationResult{{
			CriterionID: "C1",
			Status:      status,
			Evidence: []model.Evidence{{
				ArtifactID:    id,
				Type:          "cli_transcript",
				Path:          "artifacts/flow.txt",
				ProbeID:       "run_flow",
				ProducerStep:  "acceptance",
				ProducerRole:  "verifier",
				Timestamp:     "2026-05-04T20:00:00Z",
				ContentHash:   hash,
				Authoritative: true,
			}},
			Reason: "observed",
		}},
		Confidence: "high",
	}
	if status == "fail" {
		report.Failures = []model.VerificationFailure{{CriterionID: "C1", Reason: "flow exited nonzero"}}
	}
	return report
}

func signalTestDoneBundle(hash string) *model.DoneBundle {
	return &model.DoneBundle{
		TaskID:  "task01",
		Summary: "implemented flow",
		Claims: []model.CompletionClaim{{
			CriterionID: "C1",
			Status:      "satisfied",
		}},
		Artifacts: []model.CompletionArtifact{{
			Type:          "cli_transcript",
			Path:          "artifacts/flow.txt",
			CriterionID:   "C1",
			ProbeID:       "run_flow",
			Command:       "go test ./... -run TestFlow",
			ProducerStep:  "implement",
			ProducerRole:  "worker",
			Timestamp:     "2026-05-04T20:00:00Z",
			ContentHash:   hash,
			Authoritative: true,
		}},
	}
}

func TestTasksRetryEndpoint(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	s := NewServer(st, t.TempDir())

	// Create and fail a task.
	_, err := st.TaskCreate(ctx, "t1", "", "", "Retry task", "body", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	st.TaskSetStatus(ctx, "t1", "running")
	st.TaskSetStatus(ctx, "t1", "failed")

	// Retry the failed task.
	r := httptest.NewRequest(http.MethodPost, "/v1/tasks.retry?id=t1", http.NoBody)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	task, err := st.TaskGet(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "pending" {
		t.Errorf("status = %q, want pending", task.Status)
	}
	if task.RetryCount != 1 {
		t.Errorf("retry_count = %d, want 1", task.RetryCount)
	}

	// Retry a non-failed task should fail.
	_, err = st.TaskCreate(ctx, "t2", "", "", "Pending task", "body", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	r2 := httptest.NewRequest(http.MethodPost, "/v1/tasks.retry?id=t2", http.NoBody)
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, r2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w2.Code)
	}

	// Missing id should fail.
	r3 := httptest.NewRequest(http.MethodPost, "/v1/tasks.retry", http.NoBody)
	w3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w3, r3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w3.Code)
	}
}

func TestTasksListStatusFilter(t *testing.T) {
	st := newSignalTestStore(t)
	ctx := context.Background()
	s := NewServer(st, t.TempDir())

	_, err := st.TaskCreate(ctx, "t1", "", "", "A", "", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.TaskCreate(ctx, "t2", "", "", "B", "", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	st.TaskSetStatus(ctx, "t1", "running")

	// Filter by single status.
	r := httptest.NewRequest(http.MethodGet, "/v1/tasks.list?status=running", http.NoBody)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp model.APIResponse
	json.NewDecoder(w.Body).Decode(&resp)
	tasks, _ := resp.Data.([]any)
	if len(tasks) != 1 {
		t.Errorf("single status count = %d, want 1", len(tasks))
	}

	// Filter by comma-separated statuses.
	r2 := httptest.NewRequest(http.MethodGet, "/v1/tasks.list?status=pending,running", http.NoBody)
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w2.Code)
	}
	var resp2 model.APIResponse
	json.NewDecoder(w2.Body).Decode(&resp2)
	tasks2, _ := resp2.Data.([]any)
	if len(tasks2) != 2 {
		t.Errorf("multi status count = %d, want 2", len(tasks2))
	}
}
