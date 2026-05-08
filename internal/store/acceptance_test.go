package store

import (
	"context"
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/model"
)

func TestTaskSetStepForOperatorClearsStaleAcceptanceArtifacts(t *testing.T) {
	tests := []struct {
		name       string
		resetStep  string
		wantSpec   bool
		wantBundle bool
		wantReport bool
	}{
		{name: "acceptance_spec", resetStep: "acceptance_spec", wantSpec: false, wantBundle: false, wantReport: false},
		{name: "implement", resetStep: "implement", wantSpec: true, wantBundle: false, wantReport: false},
		{name: "test", resetStep: "test", wantSpec: true, wantBundle: false, wantReport: false},
		{name: "acceptance", resetStep: "acceptance", wantSpec: true, wantBundle: true, wantReport: false},
		{name: "unknown", resetStep: "custom", wantSpec: true, wantBundle: true, wantReport: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			taskID := "task-" + tt.name
			if _, err := s.TaskCreate(ctx, taskID, "", "", "Reset artifacts", "", "feature", "", "", 0); err != nil {
				t.Fatal(err)
			}
			putAcceptanceArtifacts(t, ctx, s, taskID)

			if err := s.TaskSetStepForOperator(ctx, taskID, tt.resetStep); err != nil {
				t.Fatalf("TaskSetStepForOperator: %v", err)
			}

			spec, err := s.AcceptanceSpecGet(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if (spec != nil) != tt.wantSpec {
				t.Fatalf("spec present = %v, want %v", spec != nil, tt.wantSpec)
			}
			bundle, err := s.DoneBundleGet(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if (bundle != nil) != tt.wantBundle {
				t.Fatalf("bundle present = %v, want %v", bundle != nil, tt.wantBundle)
			}
			report, verdict, err := s.VerificationReportGet(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if (report != nil) != tt.wantReport {
				t.Fatalf("report present = %v, want %v", report != nil, tt.wantReport)
			}
			if !tt.wantReport && verdict != "" {
				t.Fatalf("verdict = %q, want empty when report is cleared", verdict)
			}
		})
	}
}

func TestArtifactAddAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.TaskCreate(ctx, "task01", "", "", "Artifact task", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	artifact, err := s.ArtifactAdd(ctx, model.AddArtifactRequest{
		TaskID:       "task01",
		StepID:       "acceptance",
		Producer:     "acceptance-verifier",
		ProducerType: "agent",
		ArtifactType: "cli_transcript",
		Path:         "artifacts/flow.txt",
		SHA256:       "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Command:      "go test ./...",
		ExitCode:     0,
	})
	if err != nil {
		t.Fatalf("ArtifactAdd: %v", err)
	}
	if artifact.ID == "" {
		t.Fatal("artifact ID required")
	}
	artifacts, err := s.ArtifactList(ctx, "task01")
	if err != nil {
		t.Fatalf("ArtifactList: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].ID != artifact.ID {
		t.Fatalf("artifacts = %+v, want registered artifact", artifacts)
	}
	if artifacts[0].Status != "valid" {
		t.Fatalf("artifact status = %q, want valid", artifacts[0].Status)
	}
	if err := s.ArtifactInvalidate(ctx, artifact.ID); err != nil {
		t.Fatalf("ArtifactInvalidate: %v", err)
	}
	artifacts, err = s.ArtifactList(ctx, "task01")
	if err != nil {
		t.Fatalf("ArtifactList after invalidation: %v", err)
	}
	if artifacts[0].Status != "invalidated" || artifacts[0].InvalidatedAt == "" {
		t.Fatalf("artifact after invalidation = %+v", artifacts[0])
	}
}

func TestAcceptanceSpecValidationMetadataPersists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.TaskCreate(ctx, "task01", "", "", "Spec task", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	spec := &model.AcceptanceSpec{TaskID: "task01", Path: "artifacts/acceptance-spec.json", SHA256: "sha256:spec"}
	if err := s.AcceptanceSpecPutValidation(ctx, spec, model.AcceptanceSpecValidationResult{
		Valid:         true,
		StrengthScore: 5,
		RiskLevel:     model.RiskLevelHigh,
	}); err != nil {
		t.Fatalf("AcceptanceSpecPutValidation: %v", err)
	}
	var score int
	var risk, status, step, path, hash string
	if err := s.DB().QueryRowContext(ctx, `SELECT strength_score, risk_level, validation_status, step_id, path, sha256 FROM acceptance_specs WHERE task_id = ?`, "task01").Scan(&score, &risk, &status, &step, &path, &hash); err != nil {
		t.Fatal(err)
	}
	if score != 5 || risk != model.RiskLevelHigh || status != "valid" || step != "acceptance_spec" || path != spec.Path || hash != spec.SHA256 {
		t.Fatalf("metadata = score %d risk %q status %q step %q path %q hash %q", score, risk, status, step, path, hash)
	}
}

func TestVerificationReportValidationMetadataPersists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.TaskCreate(ctx, "task01", "", "", "Report task", "", "feature", "", "", 0); err != nil {
		t.Fatal(err)
	}
	report := &model.VerificationReport{TaskID: "task01", Path: "artifacts/verification-report.json", SHA256: "sha256:report", Confidence: "high"}
	if err := s.VerificationReportPutValidation(ctx, report, "pass", []string{"bad provenance"}); err != nil {
		t.Fatalf("VerificationReportPutValidation: %v", err)
	}
	var status, errors, step, path, hash string
	if err := s.DB().QueryRowContext(ctx, `SELECT validation_status, validation_errors, step_id, path, sha256 FROM verification_reports WHERE task_id = ?`, "task01").Scan(&status, &errors, &step, &path, &hash); err != nil {
		t.Fatal(err)
	}
	if status != "invalid" || !strings.Contains(errors, "bad provenance") || step != "acceptance" || path != report.Path || hash != report.SHA256 {
		t.Fatalf("metadata = status %q errors %q step %q path %q hash %q", status, errors, step, path, hash)
	}
}

func TestCandidateLearningCreateAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	candidate, err := s.CandidateLearningCreate(ctx, model.AddCandidateLearningRequest{
		SourceTraceID:    "trace01",
		ProposedLearning: "Use registered artifacts for verifier evidence.",
		Reason:           "low confidence verification",
	})
	if err != nil {
		t.Fatalf("CandidateLearningCreate: %v", err)
	}
	if candidate.ID == "" || candidate.Status != "candidate" {
		t.Fatalf("candidate = %+v, want generated candidate", candidate)
	}
	candidates, err := s.CandidateLearningList(ctx, "candidate", 10)
	if err != nil {
		t.Fatalf("CandidateLearningList: %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != candidate.ID {
		t.Fatalf("candidates = %+v, want stored candidate", candidates)
	}
}

func putAcceptanceArtifacts(t *testing.T, ctx context.Context, s *Store, taskID string) {
	t.Helper()
	if err := s.AcceptanceSpecPut(ctx, &model.AcceptanceSpec{
		TaskID: taskID,
		Criteria: []model.AcceptanceCriterion{{
			ID:                "C1",
			Description:       "observable behavior changes",
			RequiredArtifacts: []string{"test_output"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DoneBundlePut(ctx, &model.DoneBundle{
		TaskID:       taskID,
		Summary:      "implemented",
		FilesChanged: []string{"internal/example.go"},
		Artifacts: []model.CompletionArtifact{{
			Type: "test_output",
			Path: "artifacts/test.txt",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.VerificationReportPut(ctx, &model.VerificationReport{
		TaskID:     taskID,
		Confidence: "high",
		Results: []model.VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []model.Evidence{{
				Type: "test_output",
				Path: "artifacts/test.txt",
			}},
		}},
	}, "pass"); err != nil {
		t.Fatal(err)
	}
}
