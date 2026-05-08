package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// createTestTask is a helper that creates a repo + task, sets retries, marks done,
// and returns the task.
func createTestTask(t *testing.T, st *store.Store, ctx context.Context, id, title, template string, retries int) *model.Task {
	t.Helper()
	repoID := "repo-" + id
	dir := t.TempDir()
	st.RepoCreate(ctx, repoID, "test-repo-"+id, dir, "main", "", "", "", false)

	task, err := st.TaskCreate(ctx, id, "", repoID, title, "", template, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < retries; i++ {
		st.TaskIncrRetry(ctx, task.ID)
	}
	st.TaskSetStatus(ctx, task.ID, "running")
	st.TaskSetStatus(ctx, task.ID, "merged")
	return task
}

func TestSynthesizerDefaults(t *testing.T) {
	s := NewSynthesizer(nil, SynthesizerConfig{})
	if s.Interval() != DefaultInterval {
		t.Errorf("interval = %v, want %v", s.Interval(), DefaultInterval)
	}
	if s.config.RetryThreshold != DefaultRetryThreshold {
		t.Errorf("retryThreshold = %d, want %d", s.config.RetryThreshold, DefaultRetryThreshold)
	}
}

func TestSynthesizerRunNoTasks(t *testing.T) {
	st := newTestStore(t)
	s := NewSynthesizer(st, SynthesizerConfig{})
	if err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSynthesizerRunHighStruggle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	createTestTask(t, st, ctx, "task1", "Fix broken tests", "", 3)

	// Add failure traces.
	st.TraceAppend(ctx, "task1", "", "step.failure_context",
		mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "test_foo.go:42: expected true, got false"}))
	st.TraceAppend(ctx, "task1", "", "step.failure_context",
		mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "compilation error in main.go"}))

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 2})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	// Check that a per-task failure learning was created.
	learnings, err := st.LearningList(ctx, CategoryFailurePattern, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(learnings) < 1 {
		t.Fatalf("want at least 1 learning, got %d", len(learnings))
	}

	// The per-task learning should be "index" tier.
	found := false
	for _, l := range learnings {
		if strings.Contains(l.Title, "Fix broken tests") {
			found = true
			if l.Tier != "index" {
				t.Errorf("per-task learning tier = %q, want index", l.Tier)
			}
		}
	}
	if !found {
		t.Error("expected a learning referencing 'Fix broken tests'")
	}
}

func TestSynthesizerSkipsLowRetry(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	createTestTask(t, st, ctx, "task1", "Easy task", "", 0)

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 2})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	all, _ := st.LearningList(ctx, "", 100)
	if len(all) != 0 {
		t.Errorf("want 0 learnings for low-retry task, got %d", len(all))
	}
}

func TestLastSynthesisTime(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	s := NewSynthesizer(st, SynthesizerConfig{})

	// First call should fail (no metadata entry yet).
	_, err := s.lastSynthesisTime(ctx)
	if err == nil {
		t.Error("expected error for missing metadata key")
	}

	// Set and read back.
	now := time.Now().UTC().Truncate(time.Second)
	st.MetaSet(ctx, "synthesizer.last_run", now.Format(time.RFC3339))

	got, err := s.lastSynthesisTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(now) {
		t.Errorf("got %v, want %v", got, now)
	}
}

// -------------------------------------------------------------------------
// Cross-task pattern detection tests
// -------------------------------------------------------------------------

func TestCrossTaskStepBottleneck(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create 3 tasks where the "verify" step fails in each.
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("task%d", i)
		createTestTask(t, st, ctx, id, fmt.Sprintf("Task %d", i), "", 1)
		st.TraceAppend(ctx, id, "", "step.failure_context",
			mustJSON(model.StepFailureContextPayload{Step: "verify", Log: fmt.Sprintf("test failure %d", i)}))
	}

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5}) // high threshold so no per-task learnings
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryStepBottleneck, 10)
	if len(learnings) != 1 {
		t.Fatalf("want 1 step-bottleneck learning, got %d", len(learnings))
	}
	l := learnings[0]
	if !strings.Contains(l.Title, "verify") {
		t.Errorf("expected title to mention 'verify', got: %s", l.Title)
	}
	if !strings.Contains(l.Title, "3 tasks") {
		t.Errorf("expected title to mention '3 tasks', got: %s", l.Title)
	}
	if l.Tier != "digest" {
		t.Errorf("tier = %q, want digest", l.Tier)
	}
}

func TestCrossTaskErrorPattern(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create 3 tasks with the same error message.
	sameError := "cannot find module 'lodash' -- did you mean to install it?"
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("task%d", i)
		createTestTask(t, st, ctx, id, fmt.Sprintf("Feature %d", i), "", 1)
		st.TraceAppend(ctx, id, "", "step.failure_context",
			mustJSON(model.StepFailureContextPayload{Step: "implement", Log: sameError}))
	}

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryFailurePattern, 10)
	found := false
	for _, l := range learnings {
		if strings.Contains(l.Title, "Recurring error") && strings.Contains(l.Body, "lodash") {
			found = true
			if l.Tier != "digest" {
				t.Errorf("cross-task learning tier = %q, want digest", l.Tier)
			}
		}
	}
	if !found {
		t.Error("expected a recurring error pattern learning mentioning 'lodash'")
	}
}

func TestCrossTaskTemplateInsight(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create 4 tasks using the "feature" template, 3 of which struggle.
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("task%d", i)
		retries := 3 // most tasks struggle
		if i == 4 {
			retries = 0 // one succeeds easily
		}
		createTestTask(t, st, ctx, id, fmt.Sprintf("Feature %d", i), "feature", retries)
	}

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryTemplateInsight, 10)
	if len(learnings) != 1 {
		t.Fatalf("want 1 template insight learning, got %d", len(learnings))
	}
	l := learnings[0]
	if !strings.Contains(l.Title, "feature") {
		t.Errorf("expected title to mention 'feature' template, got: %s", l.Title)
	}
	if !strings.Contains(l.Title, "3/4") {
		t.Errorf("expected title to mention '3/4', got: %s", l.Title)
	}
}

func TestCrossTaskFileHotspot(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Create 3 tasks that all touch "src/auth/login.go" and have retries.
	hotFile := "src/auth/login.go"
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("task%d", i)
		createTestTask(t, st, ctx, id, fmt.Sprintf("Auth fix %d", i), "", 2)
		st.TraceAppend(ctx, id, "", "step.complete",
			mustJSON(model.StepCompletePayload{
				DurationMs:   5000,
				FilesTouched: []string{hotFile, fmt.Sprintf("src/other%d.go", i)},
			}))
	}

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryFileHotspot, 10)
	found := false
	for _, l := range learnings {
		if strings.Contains(l.Title, hotFile) {
			found = true
			if !strings.Contains(l.Title, "3 failure-prone tasks") {
				t.Errorf("expected 3 tasks in title, got: %s", l.Title)
			}
		}
	}
	if !found {
		t.Errorf("expected a file hotspot learning for %s", hotFile)
	}
}

// -------------------------------------------------------------------------
// Per-task richer analysis tests
// -------------------------------------------------------------------------

func TestPerTaskDeterministicFailure(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	createTestTask(t, st, ctx, "task1", "Add user endpoint", "", 3)

	// Add a deterministic verify failure.
	st.TraceAppend(ctx, "task1", "", "step.deterministic_result",
		mustJSON(model.StepDeterministicResultPayload{Step: "test", Outcome: "failure", Log: "FAIL: TestUserCreate"}))

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 2})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryFailurePattern, 10)
	found := false
	for _, l := range learnings {
		if strings.Contains(l.Body, "Deterministic verification failures") &&
			strings.Contains(l.Body, "TestUserCreate") {
			found = true
		}
	}
	if !found {
		t.Error("expected a learning with deterministic verification failure info")
	}
}

func TestPerTaskErrorProgression(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	createTestTask(t, st, ctx, "task1", "Fix auth bug", "", 3)

	// Two different errors in the same step — error progressed.
	st.TraceAppend(ctx, "task1", "", "step.failure_context",
		mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "undefined variable: authToken"}))
	st.TraceAppend(ctx, "task1", "", "step.failure_context",
		mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "403 Forbidden: invalid credentials"}))

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 2})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryFailurePattern, 10)
	found := false
	for _, l := range learnings {
		if strings.Contains(l.Body, "error changed between retries") {
			found = true
		}
	}
	if !found {
		t.Error("expected a learning noting error progression")
	}
}

func TestPerTaskStuckLoop(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	createTestTask(t, st, ctx, "task1", "Fix flaky test", "", 4)

	// Same error repeated — stuck in a loop.
	for i := 0; i < 3; i++ {
		st.TraceAppend(ctx, "task1", "", "step.failure_context",
			mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "timeout waiting for server"}))
	}

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 2})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryFailurePattern, 10)
	found := false
	for _, l := range learnings {
		if strings.Contains(l.Body, "stuck in a loop") {
			found = true
		}
	}
	if !found {
		t.Error("expected a learning noting the agent was stuck in a loop")
	}
}

// -------------------------------------------------------------------------
// Deduplication tests
// -------------------------------------------------------------------------

func TestDeduplication(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Run synthesis twice with the same data — should not create duplicates.
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("task%d", i)
		createTestTask(t, st, ctx, id, fmt.Sprintf("Task %d", i), "", 1)
		st.TraceAppend(ctx, id, "", "step.failure_context",
			mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "same error everywhere"}))
	}

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})

	// First run.
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	first, _ := st.LearningList(ctx, "", 100)
	firstCount := len(first)
	if firstCount == 0 {
		t.Fatal("expected at least 1 learning after first run")
	}

	// Reset the last synthesis time so Run processes the same tasks again.
	st.MetaSet(ctx, metaKeyLastSynthesis, time.Time{}.Format(time.RFC3339))

	// Second run.
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	second, _ := st.LearningList(ctx, "", 100)
	if len(second) != firstCount {
		t.Errorf("deduplication failed: first run created %d, second run created %d total", firstCount, len(second))
	}
}

func TestDeduplicationIgnoresCountsInTitle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Pre-create a learning with a count in the title.
	st.LearningCreate(ctx, "existing1", CategoryStepBottleneck,
		`Step "verify" is a common failure point (2 tasks affected)`,
		"body")

	// Now run synthesis that would create the same pattern with a different count.
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("task%d", i)
		createTestTask(t, st, ctx, id, fmt.Sprintf("Task %d", i), "", 1)
		st.TraceAppend(ctx, id, "", "step.failure_context",
			mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "some failure"}))
	}

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	// Should still have only 1 step-bottleneck learning (the existing one).
	learnings, _ := st.LearningList(ctx, CategoryStepBottleneck, 10)
	if len(learnings) != 1 {
		t.Errorf("want 1 step-bottleneck learning (deduplicated), got %d", len(learnings))
	}
}

// -------------------------------------------------------------------------
// Helper: extractFailurePatterns unit test (backward compat)
// -------------------------------------------------------------------------

func TestAnalyzeTaskTraces(t *testing.T) {
	task := &model.Task{
		ID:         "t1",
		Title:      "Fix tests",
		RetryCount: 3,
		Status:     "done",
	}
	traces := []*model.Trace{
		{EventType: "step.failure_context",
			Payload: mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "error: cannot find module"})},
		{EventType: "step.routed",
			Payload: mustJSON(model.StepRoutedPayload{From: "verify", To: "implement", Outcome: "failure"})},
		{EventType: "step.deterministic_result",
			Payload: mustJSON(model.StepDeterministicResultPayload{Step: "test", Outcome: "failure", Log: "FAIL: TestFoo"})},
	}

	a := analyzeTaskTraces(task, traces)

	if a.worstStep != "verify" {
		t.Errorf("worstStep = %q, want verify", a.worstStep)
	}
	if len(a.stepFailures["verify"]) != 1 {
		t.Errorf("expected 1 verify failure, got %d", len(a.stepFailures["verify"]))
	}
	if len(a.deterministicFailures) != 1 {
		t.Errorf("expected 1 deterministic failure, got %d", len(a.deterministicFailures))
	}
	if len(a.retryRoutes) != 1 {
		t.Errorf("expected 1 retry route, got %d", len(a.retryRoutes))
	}
}

// -------------------------------------------------------------------------
// Normalization tests
// -------------------------------------------------------------------------

func TestNormalizeError(t *testing.T) {
	tests := []struct {
		a, b string
		same bool
	}{
		{"error: cannot find module", "error:  cannot  find  module", true},
		{"ERROR: Cannot Find Module", "error: cannot find module", true},
		{"totally different error", "error: cannot find module", false},
	}
	for _, tt := range tests {
		na := normalizeError(tt.a)
		nb := normalizeError(tt.b)
		if (na == nb) != tt.same {
			t.Errorf("normalizeError(%q) == normalizeError(%q): got %v, want %v", tt.a, tt.b, na == nb, tt.same)
		}
	}
}

func TestNormalizeLearningTitle(t *testing.T) {
	a := normalizeLearningTitle(`Step "verify" is a common failure point (2 tasks affected)`)
	b := normalizeLearningTitle(`Step "verify" is a common failure point (5 tasks affected)`)
	if a != b {
		t.Errorf("titles should normalize the same:\n  a: %q\n  b: %q", a, b)
	}

	c := normalizeLearningTitle(`Step "implement" is a common failure point (3 tasks affected)`)
	if a == c {
		t.Error("different steps should not normalize the same")
	}
}

// -------------------------------------------------------------------------
// Integration: full realistic scenario
// -------------------------------------------------------------------------

func TestSynthesizerFullScenario(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Simulate a realistic batch of completed tasks:
	// - 3 tasks using "feature" template, all struggling at "verify"
	// - 2 tasks touching the same file
	// - 1 task that succeeded easily (should not generate learnings)

	// Feature tasks with verify failures.
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("feat%d", i)
		createTestTask(t, st, ctx, id, fmt.Sprintf("Feature %d: add endpoint", i), "feature", 3)
		st.TraceAppend(ctx, id, "", "step.failure_context",
			mustJSON(model.StepFailureContextPayload{Step: "verify", Log: "FAIL: TestEndpoint - 404 not found"}))
		st.TraceAppend(ctx, id, "", "step.routed",
			mustJSON(model.StepRoutedPayload{From: "verify", To: "implement", Outcome: "failure"}))
		st.TraceAppend(ctx, id, "", "step.complete",
			mustJSON(model.StepCompletePayload{DurationMs: 30000, FilesTouched: []string{"src/api/handler.go", fmt.Sprintf("src/api/endpoint%d.go", i)}}))
	}

	// Easy task — no retries.
	createTestTask(t, st, ctx, "easy1", "Update README", "", 0)

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 2})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify we got the expected pattern types.
	allLearnings, _ := st.LearningList(ctx, "", 100)
	if len(allLearnings) == 0 {
		t.Fatal("expected learnings to be created")
	}

	categories := make(map[string]int)
	for _, l := range allLearnings {
		categories[l.Category]++
	}

	// Should have step bottleneck (verify fails across 3 tasks).
	if categories[CategoryStepBottleneck] < 1 {
		t.Errorf("expected at least 1 step-bottleneck learning, got %d", categories[CategoryStepBottleneck])
	}

	// Should have template insight (feature template struggles).
	if categories[CategoryTemplateInsight] < 1 {
		t.Errorf("expected at least 1 template-insight learning, got %d", categories[CategoryTemplateInsight])
	}

	// Should have file hotspot (src/api/handler.go touched by 3 failing tasks).
	if categories[CategoryFileHotspot] < 1 {
		t.Errorf("expected at least 1 file-hotspot learning, got %d", categories[CategoryFileHotspot])
	}

	// Should have per-task failure patterns.
	if categories[CategoryFailurePattern] < 1 {
		t.Errorf("expected at least 1 failure-pattern learning, got %d", categories[CategoryFailurePattern])
	}

	// Verify tiers: cross-task should be digest, per-task should be index.
	for _, l := range allLearnings {
		switch l.Category {
		case CategoryStepBottleneck, CategoryTemplateInsight, CategoryFileHotspot:
			if l.Tier != "digest" {
				t.Errorf("cross-task learning %q has tier %q, want digest", l.Title, l.Tier)
			}
		}
	}
}

// -------------------------------------------------------------------------
// Clean run detection tests
// -------------------------------------------------------------------------

func TestCleanRunDetection(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	task := createTestTask(t, st, ctx, "clean1", "Add API endpoint", "feature", 0)
	st.TaskSetStepFromPending(ctx, task.ID, "acceptance_spec")
	st.TaskSetStatus(ctx, task.ID, "running")
	st.TaskSetStepIfRunning(ctx, task.ID, "acceptance_spec", "acceptance", map[string]int{
		"acceptance_spec": 1,
		"acceptance":      1,
	})
	st.TaskSetStatus(ctx, task.ID, "running")
	st.TaskSetStatus(ctx, task.ID, "merged")

	st.DoneBundlePut(ctx, &model.DoneBundle{
		TaskID:       task.ID,
		Summary:      "Added new API endpoint for user management",
		FilesChanged: []string{"src/api/handler.go", "src/api/user.go"},
		Claims:       []model.CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts:    []model.CompletionArtifact{{Type: "cli_transcript", Path: "/tmp/transcript.log", CriterionID: "C1"}},
	})

	st.VerificationReportPut(ctx, &model.VerificationReport{
		TaskID:             task.ID,
		Results:            []model.VerificationResult{{CriterionID: "C1", Status: "pass", Evidence: []model.Evidence{{Type: "cli_transcript"}}}},
		Confidence:         "high",
		ComputedConfidence: 0.9,
		ConfidenceLabel:    "high",
	}, "pass")

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, err := st.LearningList(ctx, CategoryCleanRun, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(learnings) < 1 {
		t.Fatalf("want at least 1 clean-run learning, got %d", len(learnings))
	}

	found := false
	for _, l := range learnings {
		if strings.Contains(l.Title, "Add API endpoint") && l.Tier == "index" {
			found = true
			if !strings.Contains(l.Body, "passed acceptance verification") {
				t.Errorf("expected body to mention acceptance verification")
			}
			if !strings.Contains(l.Body, "src/api/handler.go") {
				t.Errorf("expected body to mention files changed")
			}
		}
	}
	if !found {
		t.Error("expected a clean-run learning referencing 'Add API endpoint'")
	}
}

func TestCleanRunSkipsRetriedTask(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	task := createTestTask(t, st, ctx, "retry1", "Fix auth bug", "feature", 2)

	st.DoneBundlePut(ctx, &model.DoneBundle{
		TaskID:       task.ID,
		Summary:      "Fixed authentication",
		FilesChanged: []string{"src/auth/login.go"},
		Claims:       []model.CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts:    []model.CompletionArtifact{{Type: "cli_transcript", Path: "/tmp/t.log"}},
	})
	st.VerificationReportPut(ctx, &model.VerificationReport{
		TaskID:     task.ID,
		Results:    []model.VerificationResult{{CriterionID: "C1", Status: "pass", Evidence: []model.Evidence{{Type: "cli_transcript"}}}},
		Confidence: "high",
	}, "pass")

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryCleanRun, 100)
	for _, l := range learnings {
		if strings.Contains(l.Title, "Fix auth bug") {
			t.Error("expected no clean-run learning for a retried task")
		}
	}
}

func TestCleanRunSkipsLowConfidenceVerification(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	task := createTestTask(t, st, ctx, "low-confidence", "Add weakly verified feature", "feature", 0)
	st.TaskSetStatus(ctx, task.ID, "merged")
	st.DoneBundlePut(ctx, &model.DoneBundle{
		TaskID:       task.ID,
		Summary:      "Added weakly verified feature",
		FilesChanged: []string{"src/feature.go"},
		Claims:       []model.CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts:    []model.CompletionArtifact{{Type: "cli_transcript", Path: "/tmp/t.log"}},
	})
	st.VerificationReportPut(ctx, &model.VerificationReport{
		TaskID:             task.ID,
		Results:            []model.VerificationResult{{CriterionID: "C1", Status: "pass", Evidence: []model.Evidence{{Type: "cli_transcript"}}}},
		Confidence:         "high",
		ComputedConfidence: 0.49,
		ConfidenceLabel:    "low",
	}, "pass")

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryCleanRun, 100)
	for _, l := range learnings {
		if strings.Contains(l.Title, "Add weakly verified feature") {
			t.Error("expected no clean-run learning for low-confidence verification")
		}
	}
	candidates, err := st.CandidateLearningList(ctx, "candidate", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) == 0 {
		t.Fatal("expected low-confidence trace to produce candidate learning")
	}
}

func TestCleanRunFileReliability(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	sharedFile := "src/shared/utils.go"

	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("clean%d", i)
		task := createTestTask(t, st, ctx, id, fmt.Sprintf("Feature %d: add functionality", i), "feature", 0)
		st.TaskSetStatus(ctx, task.ID, "merged")

		st.DoneBundlePut(ctx, &model.DoneBundle{
			TaskID:       task.ID,
			Summary:      fmt.Sprintf("Summary %d", i),
			FilesChanged: []string{sharedFile, fmt.Sprintf("src/feature%d.go", i)},
			Claims:       []model.CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
			Artifacts:    []model.CompletionArtifact{{Type: "cli_transcript", Path: "/tmp/t.log"}},
		})

		st.VerificationReportPut(ctx, &model.VerificationReport{
			TaskID:             task.ID,
			Results:            []model.VerificationResult{{CriterionID: "C1", Status: "pass", Evidence: []model.Evidence{{Type: "cli_transcript"}}}},
			Confidence:         "high",
			ComputedConfidence: 0.9,
			ConfidenceLabel:    "high",
		}, "pass")
	}

	s := NewSynthesizer(st, SynthesizerConfig{RetryThreshold: 5})
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}

	learnings, _ := st.LearningList(ctx, CategoryCleanRun, 100)

	foundFileReliability := false
	for _, l := range learnings {
		if strings.Contains(l.Title, "high implementation reliability") &&
			strings.Contains(l.Title, sharedFile) &&
			l.Tier == "digest" {
			foundFileReliability = true
		}
	}
	if !foundFileReliability {
		t.Error("expected a file reliability cross-task learning")
	}

	perTaskCount := 0
	for _, l := range learnings {
		if l.Tier == "index" {
			perTaskCount++
		}
	}
	if perTaskCount != 2 {
		t.Errorf("expected 2 per-task clean-run learnings, got %d", perTaskCount)
	}
}

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
