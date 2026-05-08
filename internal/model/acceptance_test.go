package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeVerificationConfidence_HighConfidence(t *testing.T) {
	spec := &AcceptanceSpec{
		TaskID: "task01",
		Criteria: []AcceptanceCriterion{{
			ID:                "C1",
			Description:       "user can complete the flow",
			Probes:            []AcceptanceProbe{testProbe("probe_a"), testProbe("probe_b")},
			RequiredArtifacts: []string{"cli_transcript", "db_assertion"},
			FailIf:            []string{"flow exits nonzero"},
		}},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{
				testEvidence("cli_transcript", "probe_a"),
				testEvidence("db_assertion", "probe_b"),
			},
			Reason: "observed",
		}},
		Confidence: "high",
	}

	score := ComputeVerificationConfidence(spec, report, 0)
	// 0.35 * 1.0 (all probes covered) + 0.30 * 1.0 (all artifacts present) + 0.15 * 1.0 (all criteria passed) + 0.10 * 1.0 (0 retries) + 0.10 * 1.0 (2 evidence types / 4 cap)
	expected := 0.35*1.0 + 0.30*1.0 + 0.15*1.0 + 0.10*1.0 + 0.10*0.5
	if score < expected-0.01 || score > expected+0.01 {
		t.Fatalf("score = %.3f, want ~%.3f", score, expected)
	}
	if label := ConfidenceLabel(score); label != "high" {
		t.Fatalf("label = %q, want high", label)
	}
}

func TestComputeVerificationConfidence_LowConfidence_MissingEvidence(t *testing.T) {
	spec := &AcceptanceSpec{
		TaskID: "task01",
		Criteria: []AcceptanceCriterion{{
			ID:                "C1",
			Description:       "user can complete the flow",
			Probes:            []AcceptanceProbe{testProbe("probe_a"), testProbe("probe_b"), testProbe("probe_c")},
			RequiredArtifacts: []string{"cli_transcript", "db_assertion", "screenshot"},
			FailIf:            []string{"flow exits nonzero"},
		}},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{
				testEvidence("cli_transcript", "probe_a"),
			},
			Reason: "observed",
		}},
		Confidence: "high", // agent-provided, ignored
	}

	score := ComputeVerificationConfidence(spec, report, 0)
	// Evidence coverage: 1/3 probes covered (only probe_a has linked evidence) = 0.333
	// Artifact coverage: 1/3 (cli_transcript of 3 required) = 0.333
	// Failure score: 1/1 = 1.0
	// Retry: 1.0, Diversity: 1/4 = 0.25
	// Score = 0.35*0.333 + 0.30*0.333 + 0.15*1.0 + 0.10*1.0 + 0.10*0.25 = 0.492
	// Missing per-probe evidence is now low confidence.
	if label := ConfidenceLabel(score); label != "low" {
		t.Fatalf("score = %.3f, label = %q, want low", score, label)
	}
	if score >= ConfidenceThresholdMedium {
		t.Fatalf("score = %.3f, want < %.2f", score, ConfidenceThresholdMedium)
	}
}

func TestComputeVerificationConfidence_LowConfidence_HighRetries(t *testing.T) {
	spec := &AcceptanceSpec{
		TaskID: "task01",
		Criteria: []AcceptanceCriterion{{
			ID:                "C1",
			Description:       "user can complete the flow",
			Probes:            []AcceptanceProbe{testProbe("probe_a")},
			RequiredArtifacts: []string{"cli_transcript"},
			FailIf:            []string{"flow exits nonzero"},
		}},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence:    []Evidence{testEvidence("cli_transcript", "probe_a")},
			Reason:      "observed",
		}},
		Confidence: "high",
	}

	score := ComputeVerificationConfidence(spec, report, 3)
	// Retry score: 1.0 - 3/3 = 0.0
	// With retry penalty at 0, score = 0.35 + 0.30 + 0.15 + 0.0 + 0.025 = 0.825
	// That's still high-ish, but with 4+ retries it drops further
	score4 := ComputeVerificationConfidence(spec, report, 4)
	// Retry score: 1.0 - 4/3, clamped to 0
	// Same base + 0 retry
	if score4 > score {
		t.Fatalf("score with 4 retries (%.3f) should be <= score with 3 retries (%.3f)", score4, score)
	}
}

func TestComputeVerificationConfidence_FailedCriteria(t *testing.T) {
	spec := &AcceptanceSpec{
		TaskID: "task01",
		Criteria: []AcceptanceCriterion{
			{
				ID:                "C1",
				Description:       "flow works",
				Probes:            []AcceptanceProbe{testProbe("probe_a")},
				RequiredArtifacts: []string{"cli_transcript"},
				FailIf:            []string{"flow exits nonzero"},
			},
			{
				ID:                "C2",
				Description:       "rollback works",
				Probes:            []AcceptanceProbe{testProbe("probe_b")},
				RequiredArtifacts: []string{"db_assertion"},
				FailIf:            []string{"rollback fails"},
			},
		},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{
			{
				CriterionID: "C1",
				Status:      "pass",
				Evidence:    []Evidence{testEvidence("cli_transcript", "probe_a")},
				Reason:      "observed",
			},
			{
				CriterionID: "C2",
				Status:      "fail",
				Evidence:    []Evidence{},
				Reason:      "rollback did not execute",
			},
		},
		Failures:   []VerificationFailure{{CriterionID: "C2", Reason: "rollback did not execute"}},
		Confidence: "medium",
	}

	score := ComputeVerificationConfidence(spec, report, 0)
	// Failure score: 1/2 criteria passed = 0.5
	// Evidence coverage: C1 probes covered (1), C2 probes not covered (0) = 1/2
	// Artifact coverage: cli_transcript present (1/2 required types)
	// Retry: 1.0, Diversity: 1/4
	if score >= ConfidenceThresholdHigh {
		t.Fatalf("score = %.3f, want < %.2f (high threshold) when criteria failed", score, ConfidenceThresholdHigh)
	}
}

func TestComputeVerificationConfidence_NilInputs(t *testing.T) {
	spec := testAcceptanceSpec()
	report := &VerificationReport{TaskID: "task01", Results: []VerificationResult{{
		CriterionID: "C1",
		Status:      "pass",
		Evidence:    []Evidence{testEvidence("cli_transcript", "run_flow")},
		Reason:      "ok",
	}}, Confidence: "high"}

	if score := ComputeVerificationConfidence(nil, report, 0); score != 0 {
		t.Fatalf("nil spec should give 0, got %.3f", score)
	}
	if score := ComputeVerificationConfidence(spec, nil, 0); score != 0 {
		t.Fatalf("nil report should give 0, got %.3f", score)
	}
}

func TestConfidenceLabel(t *testing.T) {
	tests := []struct {
		score float64
		label string
	}{
		{0.0, "low"},
		{0.64, "low"},
		{0.65, "medium"},
		{0.84, "medium"},
		{0.85, "high"},
		{0.9, "high"},
		{1.0, "high"},
	}
	for _, tc := range tests {
		if got := ConfidenceLabel(tc.score); got != tc.label {
			t.Errorf("ConfidenceLabel(%.2f) = %q, want %q", tc.score, got, tc.label)
		}
	}
}

func TestIsLearningPromotable(t *testing.T) {
	// High confidence, merged, pass -> promotable
	if !IsLearningPromotable("merged", "pass", 0.85) {
		t.Error("merged + pass + high confidence should be promotable")
	}
	if IsLearningPromotable("done", "pass", 1.0) {
		t.Error("done status should not be promotable before merge")
	}

	// Low confidence -> not promotable
	if IsLearningPromotable("merged", "pass", 0.4) {
		t.Error("low confidence should not be promotable")
	}
	// Medium confidence -> not promotable
	if IsLearningPromotable("merged", "pass", 0.7) {
		t.Error("medium confidence should not be promotable")
	}

	// Failed task -> not promotable
	if IsLearningPromotable("failed", "pass", 0.9) {
		t.Error("failed task should not be promotable")
	}
	// Fail verdict -> not promotable
	if IsLearningPromotable("merged", "fail", 0.9) {
		t.Error("fail verdict should not be promotable")
	}
	// Running task -> not promotable
	if IsLearningPromotable("running", "pass", 0.9) {
		t.Error("running task should not be promotable")
	}
	// No verification (0 confidence) -> not promotable
	if IsLearningPromotable("merged", "pass", 0.0) {
		t.Error("zero confidence should not be promotable")
	}
}

func TestIsWorkflowLearningEligible(t *testing.T) {
	base := LearningEligibilityInput{
		FinalOutcomeMerged:  true,
		VerificationVerdict: "pass",
		ComputedConfidence:  0.7,
		RetryCount:          1,
		RetryThreshold:      3,
	}
	if !IsWorkflowLearningEligible(base) {
		t.Fatal("medium confidence merged passing workflow should be eligible")
	}
	base.ArtifactProvenanceViolation = true
	if IsWorkflowLearningEligible(base) {
		t.Fatal("artifact provenance violations should block automatic learning")
	}
	base.ArtifactProvenanceViolation = false
	base.RetryCount = 3
	if IsWorkflowLearningEligible(base) {
		t.Fatal("retry count at threshold should block automatic learning")
	}
	base.RetryCount = 1
	base.ComputedConfidence = 0.4
	if IsWorkflowLearningEligible(base) {
		t.Fatal("low confidence should block automatic learning")
	}
}

func TestVerifyConfidenceBreakdown(t *testing.T) {
	spec := &AcceptanceSpec{
		TaskID: "task01",
		Criteria: []AcceptanceCriterion{{
			ID:                "C1",
			Description:       "flow works",
			Probes:            []AcceptanceProbe{testProbe("probe_a")},
			RequiredArtifacts: []string{"cli_transcript"},
			FailIf:            []string{"flow exits nonzero"},
		}},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence:    []Evidence{testEvidence("cli_transcript", "probe_a")},
			Reason:      "ok",
		}},
		Confidence: "high",
	}

	breakdown := VerifyConfidenceBreakdown(spec, report, 2)
	if breakdown == "" {
		t.Fatal("breakdown should not be empty")
	}

	breakdownNil := VerifyConfidenceBreakdown(nil, nil, 0)
	if breakdownNil != "no spec or report available" {
		t.Fatalf("nil breakdown = %q, want 'no spec or report available'", breakdownNil)
	}
}

func TestValidateDoneBundleRequiresSpecArtifacts(t *testing.T) {
	spec := testAcceptanceSpec()
	bundle := &DoneBundle{
		TaskID:  "task01",
		Summary: "implemented flow",
		Claims:  []CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts: []CompletionArtifact{
			testArtifact("cli_transcript", "run_flow"),
		},
	}

	if err := ValidateDoneBundle(bundle, "task01", spec); err == nil {
		t.Fatal("expected missing artifact error")
	}

	bundle.Artifacts = append(bundle.Artifacts, testArtifact("db_assertion", "run_flow"))
	if err := ValidateDoneBundle(bundle, "task01", spec); err != nil {
		t.Fatalf("ValidateDoneBundle: %v", err)
	}
}

func TestValidateAcceptanceSpecRejectsWeakProbes(t *testing.T) {
	spec := testAcceptanceSpec()
	spec.Criteria[0].Probes[0] = AcceptanceProbe{
		ID:               "status",
		Description:      "status",
		Command:          "clankwork status",
		RequiredEvidence: []string{"cli_transcript"},
		Before:           "record state",
		After:            "state changed",
	}
	err := ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "too shallow") {
		t.Fatalf("expected shallow probe error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Before = ""
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "before/after") {
		t.Fatalf("expected transition error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].NegativeAssertion = ""
	spec.Criteria[0].Probes[0].Description = "verify observable behavior changes"
	spec.Criteria[0].FailIf = []string{"everything looks good"}
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "machine-checkable") {
		t.Fatalf("expected machine-checkable fail_if error, got %v", err)
	}
}

func TestValidateAcceptanceSpecRejectsMalformedArtifactTypes(t *testing.T) {
	spec := testAcceptanceSpec()
	spec.Criteria[0].RequiredArtifacts = []string{"file_"}
	spec.Criteria[0].Probes[0].RequiredEvidence = []string{"file_"}
	err := ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed artifact type error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].RequiredArtifacts = []string{"<artifact-type>"}
	spec.Criteria[0].Probes[0].RequiredEvidence = []string{"<artifact-type>"}
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "unbound placeholder") {
		t.Fatalf("expected placeholder artifact type error, got %v", err)
	}
}

func TestValidateAcceptanceSpecRequiresProbeEvidenceMapping(t *testing.T) {
	spec := testAcceptanceSpec()
	spec.Criteria[0].Probes[0].RequiredEvidence = nil
	err := ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "required_evidence") {
		t.Fatalf("expected required_evidence error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].RequiredEvidence = []string{"email_log"}
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "not listed in required_artifacts") {
		t.Fatalf("expected required_artifacts mapping error, got %v", err)
	}
}

func TestValidateAcceptanceSpecDetailedComputesEffectiveRisk(t *testing.T) {
	spec := testAcceptanceSpec()
	task := &Task{ID: "task01", Title: "Change auth permissions"}
	result := ValidateAcceptanceSpecDetailed(spec, "task01", task)
	if result.RiskLevel != RiskLevelHigh {
		t.Fatalf("risk = %q, want high", result.RiskLevel)
	}
	if !result.Valid {
		t.Fatalf("expected valid high-risk spec, got errors %v", result.Errors)
	}
}

func TestValidateVerificationReportDetailedWithPolicy_UsesPolicyRisk(t *testing.T) {
	spec := &AcceptanceSpec{
		TaskID: "task01",
		Criteria: []AcceptanceCriterion{{
			ID:          "C1",
			Description: "normal operation",
			Probes: []AcceptanceProbe{
				{ID: "p1", RequiredEvidence: []string{"cli_transcript"}},
				{ID: "p2", RequiredEvidence: []string{"cli_transcript"}},
			},
			RequiredArtifacts: []string{"cli_transcript"},
			FailIf:            []string{"error"},
		}},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{
				{ArtifactID: "a1", Type: "cli_transcript", ProbeID: "p1", ProducerStep: "acceptance", ProducerRole: "control-plane", Timestamp: "2026-05-04T20:00:00Z", ContentHash: "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", Authoritative: true},
				{ArtifactID: "a2", Type: "cli_transcript", ProbeID: "p2", ProducerStep: "acceptance", ProducerRole: "control-plane", Timestamp: "2026-05-04T20:00:01Z", ContentHash: "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567891", Authoritative: true},
			},
			Reason: "pass",
		}},
		Confidence: "high",
	}
	policy := &AcceptanceRiskPolicy{HighRiskLabels: []string{"sensitive"}}
	spec.Criteria[0].Description = "sensitive operation"
	result := ValidateVerificationReportDetailedWithPolicy(report, report.TaskID, spec, 4, policy)
	if result.ComputedVerdict != "retry_or_escalate" {
		t.Fatalf("policy verdict = %q, want retry_or_escalate", result.ComputedVerdict)
	}

	// Push to medium risk when policy is absent.
	resultNoPolicy := ValidateVerificationReportDetailedWithPolicy(report, report.TaskID, spec, 4, nil)
	if resultNoPolicy.ComputedVerdict != "pass" {
		t.Fatalf("non-policy verdict = %q, want pass", resultNoPolicy.ComputedVerdict)
	}
}

func TestComputeVerdictUsesRiskPolicyPaths(t *testing.T) {
	spec := testAcceptanceSpec()
	bundle := &DoneBundle{
		TaskID:       "task01",
		Summary:      "implemented",
		FilesChanged: []string{"pkg/security/session.go"},
		Claims:       []CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts: []CompletionArtifact{
			testArtifact("cli_transcript", "run_flow"),
			testArtifact("db_assertion", "run_flow"),
		},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{
				testEvidence("cli_transcript", "run_flow"),
				testEvidence("db_assertion", "run_flow"),
			},
			Reason: "observed",
		}},
		Confidence: "high",
	}
	artifacts := testRegisteredArtifacts(t, "task01", "cli_transcript", "db_assertion")
	for _, artifact := range artifacts {
		for i := range report.Results[0].Evidence {
			if report.Results[0].Evidence[i].ArtifactID == artifact.ID {
				report.Results[0].Evidence[i].Path = artifact.Path
				report.Results[0].Evidence[i].ContentHash = artifact.SHA256
			}
		}
	}
	policy := &AcceptanceRiskPolicy{HighRiskPaths: []string{"pkg/security/**"}}
	verdict := ComputeVerdictWithPolicy(spec, bundle, report, artifacts, &Task{ID: "task01", Title: "Normal task"}, policy)
	if verdict.RiskLevel != RiskLevelHigh {
		t.Fatalf("risk = %q, want high", verdict.RiskLevel)
	}
	if verdict.Status != "retry_or_escalate" || verdict.Reason != "adversarial_check_failed" {
		t.Fatalf("verdict = %+v, want high-risk adversarial gate", verdict)
	}
}

func TestComputeVerdictPassesWithRegisteredEvidence(t *testing.T) {
	spec := testAcceptanceSpec()
	bundle := &DoneBundle{
		TaskID:  "task01",
		Summary: "implemented",
		Claims:  []CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts: []CompletionArtifact{
			testArtifact("cli_transcript", "run_flow"),
			testArtifact("db_assertion", "run_flow"),
		},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{
				testEvidence("cli_transcript", "run_flow"),
				testEvidence("db_assertion", "run_flow"),
			},
			Reason: "observed",
		}},
		Confidence: "high",
	}
	artifacts := testRegisteredArtifacts(t, "task01", "cli_transcript", "db_assertion")
	for _, artifact := range artifacts {
		for i := range report.Results[0].Evidence {
			if report.Results[0].Evidence[i].ArtifactID == artifact.ID {
				report.Results[0].Evidence[i].Path = artifact.Path
				report.Results[0].Evidence[i].ContentHash = artifact.SHA256
			}
		}
	}
	verdict := ComputeVerdict(spec, bundle, report, artifacts, &Task{ID: "task01", Title: "Normal task"})
	if verdict.Status != "pass" {
		t.Fatalf("verdict = %+v, want pass", verdict)
	}
}

func TestComputeVerdictRejectsUnregisteredEvidence(t *testing.T) {
	spec := testAcceptanceSpec()
	bundle := &DoneBundle{
		TaskID:  "task01",
		Summary: "implemented",
		Claims:  []CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts: []CompletionArtifact{
			testArtifact("cli_transcript", "run_flow"),
			testArtifact("db_assertion", "run_flow"),
		},
	}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{
				testEvidence("cli_transcript", "run_flow"),
				testEvidence("db_assertion", "run_flow"),
			},
			Reason: "observed",
		}},
		Confidence: "high",
	}
	verdict := ComputeVerdict(spec, bundle, report, nil, &Task{ID: "task01"})
	if verdict.Status != "reject" || verdict.Reason != "invalid_artifact_provenance" {
		t.Fatalf("verdict = %+v, want artifact provenance rejection", verdict)
	}
}

func TestValidateAcceptanceSpecRejectsNonExecutableOrAspirationalProbes(t *testing.T) {
	spec := testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = ""
	err := ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "executable command") {
		t.Fatalf("expected executable command error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "clankwork task show <task-id> --format json"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "unbound placeholder") {
		t.Fatalf("expected placeholder error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "clankwork task show task01 --format json"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported command surface error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "grep -n boundary /Users/anon/code/clankwork/docs/acceptance-verification.md"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "worktree-relative") {
		t.Fatalf("expected worktree-relative command error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "go test ./... 2>&1 | tail -5"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "pipefail") {
		t.Fatalf("expected pipefail command error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "grep -n 'acceptance-spec author|only write.*acceptance-spec' docs/acceptance-verification.md"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err != nil {
		t.Fatalf("expected quoted grep alternation pipe to be allowed, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "grep -n 'acceptance-spec authors' docs/acceptance-verification.md; grep -n 'only write' docs/acceptance-verification.md"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "earlier command failures") {
		t.Fatalf("expected semicolon masking error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "grep -c 'acceptance_spec' docs/acceptance-verification.md"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "grep -c") {
		t.Fatalf("expected grep -c comparison error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "git diff --name-only FETCH_HEAD"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "FETCH_HEAD") {
		t.Fatalf("expected volatile ref error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "set -o pipefail; go test ./... 2>&1 | tee artifacts/test-output.txt; echo EXIT:${PIPESTATUS[0]}"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "PIPESTATUS") {
		t.Fatalf("expected PIPESTATUS exit error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "go test ./... 2>&1; echo \"EXIT:$?\""
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "echo $?") {
		t.Fatalf("expected echo $? masking error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "set -o pipefail; go test ./... > /dev/null 2>&1 && echo \"PROBE_PASS\" || echo \"PROBE_FAIL\""
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "mask failure with echo") {
		t.Fatalf("expected echo failure masking error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "awk '/## Acceptance Spec/{found=1} found && /control plane/{print; exit}' docs/acceptance-verification.md"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "awk probes") {
		t.Fatalf("expected awk END error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "awk '/## Acceptance Spec/{start=NR} /acceptance-spec.json/ && start && NR-start<=40{found=1; print \"PROBE_PASS\"; exit} END{if(!found) print \"PROBE_FAIL\"}' docs/acceptance-verification.md"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "exit non-zero") {
		t.Fatalf("expected awk non-zero END error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "awk '/## Acceptance Spec/{start=NR} /acceptance-spec.json/ && start && NR-start<=40{found=1; print \"PROBE_PASS\"} END{exit !found}' docs/acceptance-verification.md"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err != nil {
		t.Fatalf("expected awk exit !found assertion to be allowed, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = "git diff HEAD --name-only -- '*.go'"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "git diff --name-only") {
		t.Fatalf("expected git diff assertion error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Command = `python3 -c "import subprocess; result = subprocess.run(['git', 'diff', '--name-only', 'master'], capture_output=True, text=True); files = [f for f in result.stdout.splitlines() if f]; assert len(files) == 1, files; assert files[0] == 'docs/acceptance-verification.md', files"`
	err = ValidateAcceptanceSpec(spec, "task01")
	if err != nil {
		t.Fatalf("expected asserted git diff --name-only probe to be allowed, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].Probes[0].Before = "unknown"
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "weak state transition") {
		t.Fatalf("expected weak transition error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].FailIf = []string{"does not work"}
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "machine-checkable") {
		t.Fatalf("expected machine-checkable fail_if error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].FailIf = []string{"grep -c 'source' docs/acceptance-verification.md returns 0"}
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "grep -c") {
		t.Fatalf("expected fail_if unsupported command error, got %v", err)
	}

	spec = testAcceptanceSpec()
	spec.Criteria[0].FailIf = []string{"the note omits the prohibition on editing source files"}
	err = ValidateAcceptanceSpec(spec, "task01")
	if err == nil || !strings.Contains(err.Error(), "machine-checkable") {
		t.Fatalf("expected narrative fail_if error, got %v", err)
	}
}

func TestAcceptanceProbeUnmarshalLegacyString(t *testing.T) {
	var spec AcceptanceSpec
	raw := []byte(`{"task_id":"task01","criteria":[{"id":"C1","description":"flow","probes":["run flow assertion"],"required_artifacts":["cli_transcript"],"fail_if":["flow fails"]}]}`)
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if got := spec.Criteria[0].Probes[0].ID; got != "run_flow_assertion" {
		t.Fatalf("probe id = %q, want stable id", got)
	}
	if got := spec.Criteria[0].Probes[0].Description; got != "run flow assertion" {
		t.Fatalf("probe description = %q", got)
	}
}

func TestValidateVerificationReportComputesVerdict(t *testing.T) {
	spec := testAcceptanceSpec()
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{
				testEvidence("cli_transcript", "run_flow"),
				testEvidence("db_assertion", "run_flow"),
			},
			Reason: "observed",
		}},
		Confidence: "high",
	}

	pass, err := ValidateVerificationReport(report, "task01", spec)
	if err != nil {
		t.Fatalf("ValidateVerificationReport: %v", err)
	}
	if !pass {
		t.Fatal("expected pass verdict")
	}

	report.Results[0].Status = "fail"
	report.Failures = []VerificationFailure{{CriterionID: "C1", Reason: "old password still works"}}
	pass, err = ValidateVerificationReport(report, "task01", spec)
	if err != nil {
		t.Fatalf("ValidateVerificationReport failed report should remain structurally valid: %v", err)
	}
	if pass {
		t.Fatal("expected fail verdict")
	}
}

func TestValidateVerificationReportRequiresProbeCoverageAndProvenance(t *testing.T) {
	spec := testAcceptanceSpec()
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{{
				ArtifactID:    "artifact_cli_transcript",
				Type:          "cli_transcript",
				Path:          "artifacts/flow.txt",
				ProbeID:       "unknown_probe",
				ProducerStep:  "acceptance",
				ProducerRole:  "verifier",
				Timestamp:     "2026-05-04T20:00:00Z",
				ContentHash:   "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567891",
				Authoritative: true,
			}, testEvidence("db_assertion", "run_flow")},
			Reason: "observed",
		}},
		Confidence: "high",
	}

	_, err := ValidateVerificationReport(report, "task01", spec)
	if err == nil || !strings.Contains(err.Error(), "unknown probe_id") {
		t.Fatalf("expected unknown probe error, got %v", err)
	}

	report.Results[0].Evidence[0] = testEvidence("cli_transcript", "run_flow")
	report.Results[0].Evidence[0].ProducerRole = ""
	_, err = ValidateVerificationReport(report, "task01", spec)
	if err == nil || !strings.Contains(err.Error(), "producer_role") {
		t.Fatalf("expected provenance error, got %v", err)
	}

	report.Results[0].Evidence[0] = testEvidence("cli_transcript", "run_flow")
	report.Results[0].Evidence[0].ProducerRole = "worker"
	_, err = ValidateVerificationReport(report, "task01", spec)
	if err == nil || !strings.Contains(err.Error(), "cannot satisfy verifier-required evidence") {
		t.Fatalf("expected worker evidence rejection, got %v", err)
	}
}

func TestValidateVerificationReportRequiresProbeEvidenceTypes(t *testing.T) {
	spec := testAcceptanceSpec()
	spec.Criteria[0].Probes[0].RequiredEvidence = []string{"db_assertion"}
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{
				testEvidence("cli_transcript", "run_flow"),
				testEvidence("db_assertion", "other_probe"),
			},
			Reason: "observed",
		}},
		Confidence: "high",
	}
	_, err := ValidateVerificationReport(report, "task01", spec)
	if err == nil || !strings.Contains(err.Error(), "unknown probe_id") {
		t.Fatalf("expected unknown probe error first, got %v", err)
	}

	report.Results[0].Evidence = []Evidence{
		testEvidence("cli_transcript", "run_flow"),
		testEvidence("db_assertion", "run_flow"),
	}
	if _, err := ValidateVerificationReport(report, "task01", spec); err != nil {
		t.Fatalf("expected required probe evidence type to validate: %v", err)
	}
}

func TestValidateArtifactReferences(t *testing.T) {
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{{
				ArtifactID:  "artifact01",
				Type:        "cli_transcript",
				Path:        "artifacts/flow.txt",
				ContentHash: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Command:     "go test ./...",
			}},
		}},
	}
	artifacts := []*Artifact{{
		ID:           "artifact01",
		TaskID:       "task01",
		ArtifactType: "cli_transcript",
		Path:         "artifacts/flow.txt",
		SHA256:       "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Command:      "go test ./...",
	}}
	if err := ValidateArtifactReferences(report, artifacts); err != nil {
		t.Fatalf("ValidateArtifactReferences: %v", err)
	}

	report.AdversarialReview = &AdversarialReview{
		TaskID:           "task01",
		RequiredFollowup: true,
		AppendedProbeIDs: []string{"adv_probe"},
		FollowupEvidence: []Evidence{{
			ArtifactID:    "artifact01",
			Type:          "cli_transcript",
			Path:          "artifacts/flow.txt",
			ProbeID:       "adv_probe",
			ProducerStep:  "acceptance",
			ProducerRole:  "verifier",
			Timestamp:     "2026-05-04T20:00:00Z",
			ContentHash:   "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Authoritative: true,
		}},
	}
	if err := ValidateArtifactReferences(report, artifacts); err != nil {
		t.Fatalf("ValidateArtifactReferences adversarial evidence: %v", err)
	}
	report.AdversarialReview = nil

	report.Results[0].Evidence[0].ArtifactID = "missing"
	err := ValidateArtifactReferences(report, artifacts)
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("expected unregistered artifact error, got %v", err)
	}

	report.Results[0].Evidence[0].ArtifactID = "artifact01"
	artifacts[0].Status = "invalidated"
	err = ValidateArtifactReferences(report, artifacts)
	if err == nil || !strings.Contains(err.Error(), "invalidated") {
		t.Fatalf("expected invalidated artifact error, got %v", err)
	}
}

func TestValidateExecutionPlanRequiresProbeCoverage(t *testing.T) {
	spec := testAcceptanceSpec()
	exitCode := 0
	plan := &VerificationExecutionPlan{
		TaskID: "task01",
		Steps: []VerificationPlanStep{{
			ID:               "E1",
			ProbeID:          "run_flow",
			Type:             "shell",
			Command:          "go test ./... -run TestFlow",
			ExpectedExitCode: &exitCode,
			Produces:         []string{"cli_transcript"},
		}},
	}
	if err := ValidateExecutionPlan(plan, spec); err != nil {
		t.Fatalf("ValidateExecutionPlan: %v", err)
	}
	plan.Steps[0].Produces = []string{"test_output"}
	err := ValidateExecutionPlan(plan, spec)
	if err == nil || !strings.Contains(err.Error(), "do not produce required evidence") {
		t.Fatalf("expected missing required evidence error, got %v", err)
	}
	plan.Steps[0].Produces = []string{"cli_transcript"}
	plan.Steps[0].ProbeID = "unknown"
	err = ValidateExecutionPlan(plan, spec)
	if err == nil || !strings.Contains(err.Error(), "unknown probe_id") {
		t.Fatalf("expected unknown probe error, got %v", err)
	}
}

func TestValidateExecutionPlanDBQueryRequiresPath(t *testing.T) {
	spec := testAcceptanceSpec()
	spec.Criteria[0].Probes[0].RequiredEvidence = []string{"db_assertion"}
	plan := &VerificationExecutionPlan{
		TaskID: "task01",
		Steps: []VerificationPlanStep{{
			ID:       "E1",
			ProbeID:  "run_flow",
			Type:     "db_query",
			Query:    "select 1",
			Produces: []string{"db_assertion"},
		}},
	}
	err := ValidateExecutionPlan(plan, spec)
	if err == nil || !strings.Contains(err.Error(), "path and query") {
		t.Fatalf("expected db path/query error, got %v", err)
	}
	plan.Steps[0].Path = "test.db"
	if err := ValidateExecutionPlan(plan, spec); err != nil {
		t.Fatalf("expected db_query with path to validate: %v", err)
	}
}

func TestAdversarialCheckBlocksHighRiskFollowup(t *testing.T) {
	report := &VerificationReport{TaskID: "task01"}
	if AdversarialCheckPassed(report, RiskLevelHigh) {
		t.Fatal("high risk report without adversarial review should not pass")
	}
	report.AdversarialReview = &AdversarialReview{
		TaskID:           "task01",
		RequiredFollowup: true,
		AdversarialFindings: []AdversarialFinding{{
			Risk:           "expired token might be accepted",
			SuggestedProbe: "attempt reset with expired token",
			Severity:       "high",
		}},
	}
	if err := ValidateAdversarialReview(report.AdversarialReview, "task01"); err != nil {
		t.Fatalf("pending adversarial follow-up should be structurally valid: %v", err)
	}
	if AdversarialCheckPassed(report, RiskLevelHigh) {
		t.Fatal("high severity required follow-up should block")
	}
	report.AdversarialReview.AppendedProbeIDs = []string{"expired_token_probe"}
	report.AdversarialReview.FollowupEvidence = nil
	if err := ValidateAdversarialReview(report.AdversarialReview, "task01"); err == nil || !strings.Contains(err.Error(), "missing followup_evidence") {
		t.Fatalf("expected appended probe evidence error, got %v", err)
	}
	report.AdversarialReview.FollowupEvidence = []Evidence{{
		ArtifactID:    "artifact_cli_transcript",
		Type:          "cli_transcript",
		Path:          "artifacts/cli_transcript.txt",
		ProbeID:       "expired_token_probe",
		Command:       "go test ./... -run TestExpiredToken",
		ProducerStep:  "acceptance",
		ProducerRole:  "verifier",
		Timestamp:     "2026-05-04T20:00:00Z",
		ContentHash:   "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Authoritative: true,
	}}
	if err := ValidateAdversarialReview(report.AdversarialReview, "task01"); err != nil {
		t.Fatalf("expected adversarial follow-up evidence to validate: %v", err)
	}
	if !AdversarialCheckPassed(report, RiskLevelHigh) {
		t.Fatal("high-risk adversarial follow-up evidence should pass")
	}
	report.AdversarialReview.FollowupEvidence = nil
	report.AdversarialReview.AppendedProbeIDs = nil
	report.AdversarialReview.DismissalReason = "probe appended and passed"
	if err := ValidateAdversarialReview(report.AdversarialReview, "task01"); err != nil {
		t.Fatalf("ValidateAdversarialReview: %v", err)
	}
	if !AdversarialCheckPassed(report, RiskLevelHigh) {
		t.Fatal("dismissed high-risk adversarial finding should pass")
	}
	if !AdversarialReviewSatisfied(report) {
		t.Fatal("satisfied adversarial review should pass independent of risk level")
	}
}

func TestAppendAdversarialProbes(t *testing.T) {
	spec := testAcceptanceSpec()
	review := &AdversarialReview{
		TaskID:           "task01",
		RequiredFollowup: true,
		AdversarialFindings: []AdversarialFinding{{
			Risk:           "expired token might be accepted",
			SuggestedProbe: "attempt reset with expired token",
			Severity:       "high",
		}},
	}
	appended := AppendAdversarialProbes(spec, review)
	if len(appended) != 1 {
		t.Fatalf("appended = %+v, want one probe", appended)
	}
	probes := spec.Criteria[0].Probes
	got := probes[len(probes)-1]
	if got.ID != appended[0] || got.Type != "negative_assertion" || len(got.RequiredEvidence) == 0 {
		t.Fatalf("appended probe = %+v", got)
	}
	if second := AppendAdversarialProbes(spec, review); len(second) != 0 {
		t.Fatalf("duplicate append = %+v, want none", second)
	}
}

func testAcceptanceSpec() *AcceptanceSpec {
	return &AcceptanceSpec{
		TaskID: "task01",
		Criteria: []AcceptanceCriterion{{
			ID:                        "C1",
			Description:               "user can complete the flow",
			Probes:                    []AcceptanceProbe{testProbe("run_flow")},
			RequiredArtifacts:         []string{"cli_transcript", "db_assertion"},
			FailIf:                    []string{"flow exits nonzero"},
			RequiresStateTransition:   true,
			RequiresNegativeAssertion: true,
		}},
	}
}

func testProbe(id string) AcceptanceProbe {
	return AcceptanceProbe{
		ID:                   id,
		Description:          "verify observable behavior changes and failure case",
		Command:              "go test ./... -run TestFlow",
		RequiredEvidence:     []string{"cli_transcript"},
		Before:               "record initial state",
		After:                "assert final state changed",
		ObservableSideEffect: "test output and database assertion",
		NegativeAssertion:    "old behavior fails",
	}
}

func testArtifact(kind, probeID string) CompletionArtifact {
	return CompletionArtifact{
		Type:          kind,
		Path:          "artifacts/" + kind + ".txt",
		ProbeID:       probeID,
		ProducerStep:  "implement",
		ProducerRole:  "worker",
		Timestamp:     "2026-05-04T20:00:00Z",
		ContentHash:   "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Authoritative: true,
	}
}

func testEvidence(kind, probeID string) Evidence {
	return Evidence{
		ArtifactID:    "artifact_" + kind,
		Type:          kind,
		Path:          "artifacts/" + kind + ".txt",
		ProbeID:       probeID,
		Command:       "go test ./...",
		ProducerStep:  "acceptance",
		ProducerRole:  "verifier",
		Timestamp:     "2026-05-04T20:00:00Z",
		ContentHash:   "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Authoritative: true,
	}
}

func testRegisteredArtifacts(t *testing.T, taskID string, kinds ...string) []*Artifact {
	t.Helper()
	wd := t.TempDir()
	var artifacts []*Artifact
	for _, kind := range kinds {
		path := filepath.Join("artifacts", kind+".txt")
		fullPath := filepath.Join(wd, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatal(err)
		}
		data := []byte(kind + "\n")
		if err := os.WriteFile(fullPath, data, 0644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		artifacts = append(artifacts, &Artifact{
			ID:               "artifact_" + kind,
			TaskID:           taskID,
			ArtifactType:     kind,
			Path:             filepath.ToSlash(path),
			SHA256:           "sha256:" + hex.EncodeToString(sum[:]),
			WorkingDirectory: wd,
			Command:          "go test ./...",
		})
	}
	return artifacts
}

// C1: Placeholder content_hash values (e.g. sha256:test) are rejected
func TestPlaceholderHashRejected(t *testing.T) {
	placeholders := []string{
		"sha256:test",
		"sha256:todo",
		"sha256:placeholder",
		"sha256:example",
		"sha256:abc",
		"sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"test",
		"placeholder",
		"abcdef_ghijklmnop", // non-hex characters
	}
	for _, ph := range placeholders {
		err := validateArtifactProvenance("test", "probe_a", "go test", "acceptance", "verifier", "2026-01-01T00:00:00Z", ph, "artifacts/test.txt", true)
		if err == nil || !strings.Contains(err.Error(), "placeholder") {
			t.Errorf("expected placeholder rejection for %q, got: %v", ph, err)
		}
	}

	// Valid non-placeholder hash should pass
	err := validateArtifactProvenance("test", "probe_a", "go test", "acceptance", "verifier", "2026-01-01T00:00:00Z", "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", "artifacts/test.txt", true)
	if err != nil {
		t.Errorf("expected valid hash to pass, got: %v", err)
	}
}

// C2: Evidence content_hash mismatch against registered artifact sha256 is rejected
func TestHashMismatchRejected(t *testing.T) {
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{{
				ArtifactID:  "artifact01",
				Type:        "cli_transcript",
				Path:        "artifacts/flow.txt",
				ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa11",
				Command:     "go test ./...",
			}},
		}},
	}
	artifacts := []*Artifact{{
		ID:           "artifact01",
		TaskID:       "task01",
		ArtifactType: "cli_transcript",
		Path:         "artifacts/flow.txt",
		SHA256:       "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb22",
		Command:      "go test ./...",
	}}
	err := ValidateArtifactReferences(report, artifacts)
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
	if !strings.Contains(err.Error(), "C1") {
		t.Errorf("expected error to identify criterion C1, got: %v", err)
	}
	if !strings.Contains(err.Error(), "content_hash") {
		t.Errorf("expected error to mention content_hash, got: %v", err)
	}
}

// C3: Unregistered artifact_id is rejected with a useful error
func TestUnregisteredArtifactRejected(t *testing.T) {
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{{
				ArtifactID: "nonexistent_artifact",
				Type:       "cli_transcript",
			}},
		}},
	}
	err := ValidateArtifactReferences(report, []*Artifact{})
	if err == nil {
		t.Fatal("expected unregistered artifact error")
	}
	if !strings.Contains(err.Error(), "C1") {
		t.Errorf("expected error to identify criterion C1, got: %v", err)
	}
	if !strings.Contains(err.Error(), "nonexistent_artifact") {
		t.Errorf("expected error to identify the missing artifact_id, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("expected error to say artifact is not registered, got: %v", err)
	}
}

// C4: Valid registered verifier artifact with matching hash passes
func TestValidArtifactPasses(t *testing.T) {
	report := &VerificationReport{
		TaskID: "task01",
		Results: []VerificationResult{{
			CriterionID: "C1",
			Status:      "pass",
			Evidence: []Evidence{{
				ArtifactID:  "artifact01",
				Type:        "test_output",
				Path:        "artifacts/test.txt",
				ContentHash: "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
				Command:     "go test ./...",
			}},
		}},
	}
	artifacts := []*Artifact{{
		ID:           "artifact01",
		TaskID:       "task01",
		ArtifactType: "test_output",
		Path:         "artifacts/test.txt",
		SHA256:       "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Command:      "go test ./...",
	}}
	err := ValidateArtifactReferences(report, artifacts)
	if err != nil {
		t.Fatalf("expected valid artifact to pass, got: %v", err)
	}
}

// C5: Rejection messages identify which criterion, probe, or artifact failed and how to fix it
func TestRejectionMessagesSpecific(t *testing.T) {
	tests := []struct {
		name         string
		report       *VerificationReport
		artifacts    []*Artifact
		wantContains []string
	}{
		{
			name: "unregistered artifact identifies criterion and artifact_id",
			report: &VerificationReport{
				TaskID: "task01",
				Results: []VerificationResult{{
					CriterionID: "C2",
					Status:      "pass",
					Evidence: []Evidence{{
						ArtifactID: "missing_123",
						Type:       "test_output",
					}},
				}},
			},
			artifacts:    []*Artifact{},
			wantContains: []string{"C2", "missing_123", "not registered"},
		},
		{
			name: "hash mismatch shows both hashes and fix instruction",
			report: &VerificationReport{
				TaskID: "task01",
				Results: []VerificationResult{{
					CriterionID: "C3",
					Status:      "pass",
					Evidence: []Evidence{{
						ArtifactID:  "artifact01",
						Type:        "test_output",
						ContentHash: "sha256:evidenceeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee11",
						Command:     "go test ./...",
					}},
				}},
			},
			artifacts: []*Artifact{{
				ID:           "artifact01",
				TaskID:       "task01",
				ArtifactType: "test_output",
				SHA256:       "sha256:artifactffffffffffffffffffffffffffffffffffffffffffffffffffffff22",
				Command:      "go test ./...",
			}},
			wantContains: []string{"C3", "content_hash", "does not match", "sha256sum"},
		},
		{
			name: "placeholder hash explains what is wrong and how to fix",
			report: &VerificationReport{
				TaskID:     "task01",
				Confidence: "high",
				Results: []VerificationResult{{
					CriterionID: "C1",
					Status:      "pass",
					Evidence: []Evidence{{
						ArtifactID:    "a1",
						Type:          "test_output",
						ProbeID:       "probe_a",
						ProducerStep:  "acceptance",
						ProducerRole:  "verifier",
						Timestamp:     "2026-01-01T00:00:00Z",
						ContentHash:   "sha256:test",
						Authoritative: true,
					}},
				}},
			},
			artifacts:    nil,
			wantContains: []string{"C1", "placeholder", "sha256 of the artifact"},
		},
		{
			name: "missing command metadata for cli_transcript identifies artifact",
			report: &VerificationReport{
				TaskID: "task01",
				Results: []VerificationResult{{
					CriterionID: "C4",
					Status:      "pass",
					Evidence: []Evidence{{
						ArtifactID: "no_command_artifact",
						Type:       "cli_transcript",
					}},
				}},
			},
			artifacts: []*Artifact{{
				ID:           "no_command_artifact",
				TaskID:       "task01",
				ArtifactType: "cli_transcript",
			}},
			wantContains: []string{"C4", "cli_transcript", "requires command metadata", "no_command_artifact"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.artifacts != nil {
				err = ValidateArtifactReferences(tt.report, tt.artifacts)
			} else {
				// For placeholder hash test, go through ValidateVerificationReport
				_, err = ValidateVerificationReport(tt.report, "task01", nil)
			}
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error message missing %q: got %v", want, err)
				}
			}
		})
	}
}

// TestIsPlaceholderHash covers the placeholder detection logic directly
func TestIsPlaceholderHash(t *testing.T) {
	placeholders := []string{
		"sha256:test",
		"sha256:todo",
		"sha256:placeholder",
		"sha256:example",
		"sha256:abc",
		"sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"test",
		"placeholder",
		"abcdef_ghijklmnop",
		"not a hash at all",
	}
	for _, ph := range placeholders {
		if !isPlaceholderHash(ph) {
			t.Errorf("expected %q to be detected as placeholder", ph)
		}
	}

	valid := []string{
		"sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	for _, v := range valid {
		if isPlaceholderHash(v) {
			t.Errorf("expected %q to NOT be detected as placeholder", v)
		}
	}
}

// TestRequiresCommandMetadata covers the command metadata type check
func TestRequiresCommandMetadata(t *testing.T) {
	requireCmd := []string{"cli_transcript", "test_output", "shell_output", "command_output"}
	for _, typ := range requireCmd {
		if !requiresCommandMetadata(typ) {
			t.Errorf("expected %q to require command metadata", typ)
		}
	}
	notRequire := []string{"screenshot", "db_assertion", "log", "screenshot"}
	for _, typ := range notRequire {
		if requiresCommandMetadata(typ) {
			t.Errorf("expected %q to NOT require command metadata", typ)
		}
	}
}

// TestCommandMatches covers the command matching logic
func TestCommandMatches(t *testing.T) {
	// Exact match
	if !commandMatches("go test ./...", "go test ./...") {
		t.Error("exact match should pass")
	}
	// Contains
	if !commandMatches("bash -c 'go test ./...'", "go test ./...") {
		t.Error("contained command should pass")
	}
	if !commandMatches("go test ./...", "bash -c 'go test ./...'") {
		t.Error("containing command should pass")
	}
	// No match
	if commandMatches("go test ./...", "go build ./...") {
		t.Error("different commands should not match")
	}
	// Shell variable expansion
	if !commandMatches(`test -f file.txt && grep -q "$VAR" file.txt`, `test -f file.txt && grep -q "actual value" file.txt`) {
		t.Error("shell variable expansion should match")
	}
	if !commandMatches(`grep -q "$TASK_TITLE" agent-output.txt`, `grep -q "Add widget feature" agent-output.txt`) {
		t.Error("shell variable expansion in grep should match")
	}
}
