package learning

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
)

const (
	metaKeyLastSynthesis = "synthesizer.last_run"

	// DefaultRetryThreshold is the minimum retry count to flag a task as "high-struggle".
	DefaultRetryThreshold = 2

	// DefaultInterval is how often the synthesizer runs if no interval is configured.
	DefaultInterval = 1 * time.Hour

	// maxTasksPerRun caps how many completed tasks we process in a single synthesis run.
	maxTasksPerRun = 200

	// crossTaskThreshold is the minimum number of tasks exhibiting a pattern
	// before we create a cross-task learning.
	crossTaskThreshold = 2

	// Categories for synthesized learnings.
	CategoryFailurePattern  = "auto:failure-pattern"
	CategoryStepBottleneck  = "auto:step-bottleneck"
	CategoryFileHotspot     = "auto:file-hotspot"
	CategoryTemplateInsight = "auto:template-insight"
	CategoryCleanRun        = "auto:clean-run"
)

// SynthesizerConfig holds tunable parameters for the batch synthesizer.
type SynthesizerConfig struct {
	// Interval between synthesis runs. Zero uses DefaultInterval.
	Interval time.Duration

	// RetryThreshold is the minimum retry_count to consider a task high-struggle.
	// Zero uses DefaultRetryThreshold.
	RetryThreshold int
}

// Synthesizer periodically processes completed task traces and creates learnings
// from failure patterns. This is Phase 2 of the learning system described in the
// architecture doc.
type Synthesizer struct {
	store  *store.Store
	config SynthesizerConfig
}

// NewSynthesizer creates a new batch synthesizer.
func NewSynthesizer(st *store.Store, cfg SynthesizerConfig) *Synthesizer {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.RetryThreshold <= 0 {
		cfg.RetryThreshold = DefaultRetryThreshold
	}
	return &Synthesizer{store: st, config: cfg}
}

// Interval returns the configured run interval.
func (s *Synthesizer) Interval() time.Duration {
	return s.config.Interval
}

// Run executes one synthesis cycle: find recently completed tasks, identify
// high-struggle ones, create per-task and cross-task learnings from failure patterns.
func (s *Synthesizer) Run(ctx context.Context) error {
	since, err := s.lastSynthesisTime(ctx)
	if err != nil {
		slog.Warn("synthesizer: could not read last run time, using epoch", "err", err)
		since = time.Time{}
	}

	tasks, err := s.store.TasksCompletedSince(ctx, since, maxTasksPerRun)
	if err != nil {
		return fmt.Errorf("synthesizer: query completed tasks: %w", err)
	}

	if len(tasks) == 0 {
		return nil
	}

	// Collect all traces for all tasks up-front for cross-task analysis.
	taskTraces := make(map[string][]*model.Trace)
	for _, task := range tasks {
		traces, err := s.store.TraceList(ctx, task.ID, 50)
		if err != nil {
			slog.Error("synthesizer: failed to list traces", "task", task.ID, "err", err)
			continue
		}
		taskTraces[task.ID] = traces
	}

	// Phase A: Cross-task pattern detection (across all completed tasks, not just high-struggle).
	crossCreated := s.synthesizeCrossTaskPatterns(ctx, tasks, taskTraces)

	// Phase B: Per-task synthesis for high-struggle tasks.
	highStruggle := s.identifyHighStruggle(tasks)
	perTaskCreated := 0
	for _, task := range highStruggle {
		traces := taskTraces[task.ID]
		if len(traces) == 0 {
			continue
		}
		n, err := s.synthesizeFromTask(ctx, task, traces)
		if err != nil {
			slog.Error("synthesizer: failed to create learning", "task", task.ID, "err", err)
			continue
		}
		perTaskCreated += n
	}

	// Record the completion time of the newest task we processed.
	newest := tasks[len(tasks)-1]
	if newest.CompletedAt != nil {
		if err := s.store.MetaSet(ctx, metaKeyLastSynthesis, newest.CompletedAt.Format(time.RFC3339)); err != nil {
			slog.Error("synthesizer: failed to update last run time", "err", err)
		}
	}

	total := crossCreated + perTaskCreated
	if total > 0 {
		slog.Info("synthesizer: created learnings",
			"total", total,
			"cross_task", crossCreated,
			"per_task", perTaskCreated,
			"tasks_processed", len(tasks),
			"high_struggle", len(highStruggle))
	}
	return nil
}

// lastSynthesisTime reads the last synthesis run time from the metadata table.
func (s *Synthesizer) lastSynthesisTime(ctx context.Context) (time.Time, error) {
	val, err := s.store.MetaGet(ctx, metaKeyLastSynthesis)
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, val)
}

// identifyHighStruggle filters tasks that had significant retry counts,
// indicating the agent struggled to complete them.
func (s *Synthesizer) identifyHighStruggle(tasks []*model.Task) []*model.Task {
	var result []*model.Task
	for _, t := range tasks {
		if t.RetryCount >= s.config.RetryThreshold {
			result = append(result, t)
		}
	}
	return result
}

// -------------------------------------------------------------------------
// Cross-task pattern detection
// -------------------------------------------------------------------------

// stepFailure records a single failure occurrence for cross-task aggregation.
type stepFailure struct {
	taskID  string
	step    string
	message string
}

// synthesizeCrossTaskPatterns looks across all tasks in this batch for recurring
// patterns: same steps failing, same errors, template struggles, file hotspots.
func (s *Synthesizer) synthesizeCrossTaskPatterns(ctx context.Context, tasks []*model.Task, taskTraces map[string][]*model.Trace) int {
	// Collect all failure events across tasks.
	var allFailures []stepFailure
	fileFailureTasks := make(map[string]map[string]bool) // file -> set of task IDs
	templateRetries := make(map[string][]int)            // template -> list of retry counts

	for _, task := range tasks {
		traces := taskTraces[task.ID]

		// Track template retry patterns.
		if task.Template != "" {
			templateRetries[task.Template] = append(templateRetries[task.Template], task.RetryCount)
		}

		for _, tr := range traces {
			switch tr.EventType {
			case "step.failure_context":
				var info model.StepFailureContextPayload
				if err := json.Unmarshal([]byte(tr.Payload), &info); err == nil {
					msg := info.Log
					if msg == "" {
						msg = info.Message
					}
					allFailures = append(allFailures, stepFailure{
						taskID:  task.ID,
						step:    info.Step,
						message: msg,
					})
				}

			case "step.deterministic_result":
				var info model.StepDeterministicResultPayload
				if err := json.Unmarshal([]byte(tr.Payload), &info); err == nil {
					if info.Outcome == "failure" {
						allFailures = append(allFailures, stepFailure{
							taskID:  task.ID,
							step:    info.Step,
							message: info.Log,
						})
					}
				}

			case "step.complete":
				var info model.StepCompletePayload
				if err := json.Unmarshal([]byte(tr.Payload), &info); err == nil {
					for _, f := range info.FilesTouched {
						if fileFailureTasks[f] == nil {
							fileFailureTasks[f] = make(map[string]bool)
						}
						// Only count files from tasks that had failures.
						if task.RetryCount > 0 {
							fileFailureTasks[f][task.ID] = true
						}
					}
				}
			}
		}
	}

	created := 0

	// Pattern 1: Steps that fail across multiple tasks.
	created += s.detectStepBottlenecks(ctx, allFailures)

	// Pattern 2: Same error messages across tasks.
	created += s.detectErrorPatterns(ctx, allFailures)

	// Pattern 3: Templates with consistently high retries.
	created += s.detectTemplateInsights(ctx, templateRetries)

	// Pattern 4: Files appearing in failure-prone tasks.
	created += s.detectFileHotspots(ctx, fileFailureTasks)

	// Pattern 5: Clean runs — tasks that completed without retries and passed verification.
	created += s.detectCleanRuns(ctx, tasks, taskTraces)

	return created
}

// detectStepBottlenecks finds steps that fail across multiple distinct tasks.
func (s *Synthesizer) detectStepBottlenecks(ctx context.Context, failures []stepFailure) int {
	// step name -> set of task IDs that had failures in this step
	stepTasks := make(map[string]map[string]bool)
	for _, f := range failures {
		if f.step == "" {
			continue
		}
		if stepTasks[f.step] == nil {
			stepTasks[f.step] = make(map[string]bool)
		}
		stepTasks[f.step][f.taskID] = true
	}

	created := 0
	for step, tasks := range stepTasks {
		if len(tasks) < crossTaskThreshold {
			continue
		}

		title := fmt.Sprintf("Step %q is a common failure point (%d tasks affected)", step, len(tasks))
		body := fmt.Sprintf("The %q step failed across %d different tasks in the recent batch. "+
			"This suggests the step itself may need attention — review the verify command, "+
			"acceptance criteria, or role definition for this step.", step, len(tasks))

		if s.createIfNotDuplicate(ctx, CategoryStepBottleneck, title, body, "digest") {
			created++
		}
	}
	return created
}

// detectErrorPatterns finds error messages that recur across multiple tasks.
func (s *Synthesizer) detectErrorPatterns(ctx context.Context, failures []stepFailure) int {
	// Normalize error messages and group by similarity.
	// We use the first 100 chars as a rough fingerprint.
	type errorGroup struct {
		fingerprint string
		fullMessage string
		tasks       map[string]bool
	}

	groups := make(map[string]*errorGroup)
	for _, f := range failures {
		if f.message == "" {
			continue
		}
		fp := normalizeError(f.message)
		if fp == "" {
			continue
		}
		if groups[fp] == nil {
			groups[fp] = &errorGroup{
				fingerprint: fp,
				fullMessage: f.message,
				tasks:       make(map[string]bool),
			}
		}
		groups[fp].tasks[f.taskID] = true
	}

	created := 0
	for _, g := range groups {
		if len(g.tasks) < crossTaskThreshold {
			continue
		}

		snippet := truncate(g.fullMessage, 120)
		title := fmt.Sprintf("Recurring error across %d tasks: %s", len(g.tasks), truncate(snippet, 80))
		body := fmt.Sprintf("The same error pattern appeared in %d different tasks:\n\n"+
			"Error: %s\n\n"+
			"This recurring pattern may indicate a systemic issue — a flaky test, "+
			"a missing dependency, or a problematic code pattern that agents keep hitting.",
			len(g.tasks), truncate(g.fullMessage, 500))

		if s.createIfNotDuplicate(ctx, CategoryFailurePattern, title, body, "digest") {
			created++
		}
	}
	return created
}

// detectTemplateInsights finds templates where tasks consistently struggle.
func (s *Synthesizer) detectTemplateInsights(ctx context.Context, templateRetries map[string][]int) int {
	created := 0
	for template, retries := range templateRetries {
		if len(retries) < crossTaskThreshold {
			continue
		}

		total := 0
		highCount := 0
		for _, r := range retries {
			total += r
			if r >= DefaultRetryThreshold {
				highCount++
			}
		}
		avg := float64(total) / float64(len(retries))

		// Only report if more than half the tasks using this template struggled.
		if highCount*2 < len(retries) {
			continue
		}

		title := fmt.Sprintf("Template %q has high failure rate (%d/%d tasks struggled)", template, highCount, len(retries))
		body := fmt.Sprintf("Tasks using the %q template consistently require multiple retries. "+
			"Out of %d tasks, %d had retry counts >= %d (avg retries: %.1f). "+
			"Consider reviewing the template's step definitions, role prompts, or verification commands.",
			template, len(retries), highCount, DefaultRetryThreshold, avg)

		if s.createIfNotDuplicate(ctx, CategoryTemplateInsight, title, body, "digest") {
			created++
		}
	}
	return created
}

// cleanRun represents a task that completed cleanly for cross-task pattern analysis.
type cleanRun struct {
	taskID       string
	title        string
	template     string
	filesChanged []string
	summary      string
	criteriaMet  []string
}

// detectCleanRuns identifies tasks that completed without retries and passed
// acceptance verification, creating learnings from these positive signals.
func (s *Synthesizer) detectCleanRuns(ctx context.Context, tasks []*model.Task, taskTraces map[string][]*model.Trace) int {
	// Phase 1: Filter to structural candidates (template + retry + step attempts).
	var candidates []*model.Task
	for _, task := range tasks {
		if task.Status != "merged" && task.Status != "done" {
			continue
		}
		if task.Template != "feature" {
			continue
		}
		if task.RetryCount != 0 {
			continue
		}
		if task.StepAttempts == nil {
			continue
		}
		if task.StepAttempts["acceptance"] > 1 || task.StepAttempts["acceptance_spec"] > 1 {
			continue
		}
		candidates = append(candidates, task)
	}

	// Phase 2: Apply verdict check and collect clean runs with bundle data.
	var cleanRuns []cleanRun
	featureTasksInBatch := 0
	for _, task := range tasks {
		if task.Template == "feature" {
			featureTasksInBatch++
		}
	}

	for _, task := range candidates {
		report, verdict, err := s.store.VerificationReportGet(ctx, task.ID)
		if err != nil {
			slog.Error("synthesizer: failed to get verification report", "task", task.ID, "err", err)
			continue
		}
		if verdict != "pass" {
			continue
		}

		if report == nil {
			continue
		}
		eligible := model.IsWorkflowLearningEligible(model.LearningEligibilityInput{
			FinalOutcomeMerged:  task.Status == "merged",
			VerificationVerdict: verdict,
			ComputedConfidence:  report.ComputedConfidence,
			RetryCount:          task.RetryCount,
			RetryThreshold:      s.config.RetryThreshold,
		})
		if !eligible {
			reason := fmt.Sprintf("workflow not eligible for automatic learning: status=%s verdict=%s confidence=%.2f retries=%d",
				task.Status, verdict, report.ComputedConfidence, task.RetryCount)
			_, err := s.store.CandidateLearningCreate(ctx, model.AddCandidateLearningRequest{
				SourceTraceID:    task.ID,
				ProposedLearning: fmt.Sprintf("Candidate clean-run learning for task %q requires review before promotion.", task.Title),
				Reason:           reason,
			})
			if err != nil {
				slog.Error("synthesizer: failed to create candidate learning", "task", task.ID, "err", err)
			}
			slog.Debug("synthesizer: stored candidate learning instead of durable promotion",
				"task", task.ID,
				"confidence", report.ComputedConfidence,
				"label", report.ConfidenceLabel)
			continue
		}

		var criteriaMet []string
		if report != nil {
			for _, r := range report.Results {
				if r.Status == "pass" {
					criteriaMet = append(criteriaMet, r.CriterionID)
				}
			}
		}

		var filesChanged []string
		var summary string
		bundle, err := s.store.DoneBundleGet(ctx, task.ID)
		if err != nil {
			slog.Error("synthesizer: failed to get done bundle", "task", task.ID, "err", err)
		} else if bundle != nil {
			filesChanged = bundle.FilesChanged
			summary = bundle.Summary
		}

		title := fmt.Sprintf("Clean verified run: %s", task.Title)
		body := fmt.Sprintf("Task %q completed without retries and passed acceptance verification.\n\n", task.Title)
		body += fmt.Sprintf("Template: %s\n", task.Template)
		body += fmt.Sprintf("Status: %s\n", task.Status)
		if summary != "" {
			body += fmt.Sprintf("\nSummary: %s\n", summary)
		}
		if len(filesChanged) > 0 {
			body += fmt.Sprintf("\nFiles changed: %s\n", strings.Join(filesChanged, ", "))
		}
		if len(criteriaMet) > 0 {
			body += fmt.Sprintf("\nAcceptance criteria met: %s\n", strings.Join(criteriaMet, ", "))
		}

		s.createIfNotDuplicate(ctx, CategoryCleanRun, title, body, "index")

		cleanRuns = append(cleanRuns, cleanRun{
			taskID:       task.ID,
			title:        task.Title,
			template:     task.Template,
			filesChanged: filesChanged,
			summary:      summary,
			criteriaMet:  criteriaMet,
		})
	}

	// Phase 3: Cross-task patterns.
	// `created` now includes per-task learnings created above.
	created := len(cleanRuns)

	// File reliability.
	fileCleanRuns := make(map[string][]cleanRun)
	for _, cr := range cleanRuns {
		for _, f := range cr.filesChanged {
			fileCleanRuns[f] = append(fileCleanRuns[f], cr)
		}
	}

	for file, runs := range fileCleanRuns {
		if len(runs) < crossTaskThreshold {
			continue
		}

		title := fmt.Sprintf("File %s has high implementation reliability (%d clean verified runs)", file, len(runs))
		var descParts []string
		for _, r := range runs {
			descParts = append(descParts, fmt.Sprintf("- %q: %s", file, r.title))
		}
		body := fmt.Sprintf("The file %q was changed by %d different tasks that all completed cleanly "+
			"(zero retries, passed acceptance verification). This file demonstrates high implementation reliability:\n\n%s",
			file, len(runs), strings.Join(descParts, "\n"))

		if s.createIfNotDuplicate(ctx, CategoryCleanRun, title, body, "digest") {
			created++
		}
	}

	// Template success rate.
	if featureTasksInBatch > 0 && len(cleanRuns) >= crossTaskThreshold {
		ratio := float64(len(cleanRuns)) / float64(featureTasksInBatch)
		if ratio >= 0.5 {
			title := fmt.Sprintf("Feature template clean run rate: %d/%d tasks completed without retries", len(cleanRuns), featureTasksInBatch)
			var taskDescs []string
			for _, cr := range cleanRuns {
				taskDescs = append(taskDescs, cr.title)
			}
			body := fmt.Sprintf("In the recent batch, %d out of %d feature template tasks completed cleanly "+
				"(zero retries, passed acceptance verification). Tasks that succeeded cleanly:\n\n%s",
				len(cleanRuns), featureTasksInBatch, strings.Join(taskDescs, "\n"))

			if s.createIfNotDuplicate(ctx, CategoryCleanRun, title, body, "digest") {
				created++
			}
		}
	}

	return created
}

// detectFileHotspots finds files that appear in multiple failure-prone tasks.
func (s *Synthesizer) detectFileHotspots(ctx context.Context, fileTaskMap map[string]map[string]bool) int {
	// Sort by number of tasks descending, report top hotspots.
	type hotspot struct {
		path  string
		count int
	}
	var hotspots []hotspot
	for path, tasks := range fileTaskMap {
		if len(tasks) >= crossTaskThreshold {
			hotspots = append(hotspots, hotspot{path: path, count: len(tasks)})
		}
	}

	sort.Slice(hotspots, func(i, j int) bool { return hotspots[i].count > hotspots[j].count })

	// Cap at 5 hotspots per run.
	if len(hotspots) > 5 {
		hotspots = hotspots[:5]
	}

	created := 0
	for _, h := range hotspots {
		title := fmt.Sprintf("File hotspot: %s (%d failure-prone tasks)", h.path, h.count)
		body := fmt.Sprintf("The file %q was touched by %d different tasks that required retries. "+
			"Changes to this file frequently cause issues — consider adding more targeted tests, "+
			"better documentation, or tighter linting rules for this area.",
			h.path, h.count)

		if s.createIfNotDuplicate(ctx, CategoryFileHotspot, title, body, "digest") {
			created++
		}
	}
	return created
}

// -------------------------------------------------------------------------
// Per-task synthesis (richer failure analysis)
// -------------------------------------------------------------------------

// synthesizeFromTask creates learnings from a high-struggle task's failure traces.
// Returns the number of learnings created.
func (s *Synthesizer) synthesizeFromTask(ctx context.Context, task *model.Task, traces []*model.Trace) (int, error) {
	analysis := analyzeTaskTraces(task, traces)
	if analysis.isEmpty() {
		return 0, nil
	}

	created := 0

	// Build a rich learning from the analysis.
	title := buildTaskLearningTitle(task, analysis)
	body := buildTaskLearningBody(task, analysis)

	if body != "" {
		// Single-task observations get "index" tier.
		if s.createIfNotDuplicate(ctx, CategoryFailurePattern, title, body, "index") {
			created++
		}
	}

	return created, nil
}

// taskAnalysis holds parsed failure information for a single task.
type taskAnalysis struct {
	// Failures grouped by step name.
	stepFailures map[string][]string
	// Whether the error changed between retries (progression).
	errorProgressed bool
	// Most common failure step.
	worstStep string
	// Deterministic step failures (verify commands).
	deterministicFailures []string
	// Retry routing info.
	retryRoutes []string
}

func (a *taskAnalysis) isEmpty() bool {
	return len(a.stepFailures) == 0 && len(a.deterministicFailures) == 0 && len(a.retryRoutes) == 0
}

// analyzeTaskTraces performs richer failure analysis on a single task's traces.
func analyzeTaskTraces(task *model.Task, traces []*model.Trace) *taskAnalysis {
	a := &taskAnalysis{
		stepFailures: make(map[string][]string),
	}

	for _, tr := range traces {
		switch tr.EventType {
		case "step.failure_context":
			var info model.StepFailureContextPayload
			if err := json.Unmarshal([]byte(tr.Payload), &info); err == nil {
				msg := info.Log
				if msg == "" {
					msg = info.Message
				}
				if msg != "" {
					a.stepFailures[info.Step] = append(a.stepFailures[info.Step], msg)
				}
			}

		case "step.deterministic_result":
			var info model.StepDeterministicResultPayload
			if err := json.Unmarshal([]byte(tr.Payload), &info); err == nil {
				if info.Outcome == "failure" {
					detail := fmt.Sprintf("Step %q verify failed", info.Step)
					if info.Log != "" {
						detail += ": " + truncate(info.Log, 300)
					}
					a.deterministicFailures = append(a.deterministicFailures, detail)
				}
			}

		case "step.routed":
			var info model.StepRoutedPayload
			if err := json.Unmarshal([]byte(tr.Payload), &info); err == nil {
				if info.Outcome == "failure" {
					a.retryRoutes = append(a.retryRoutes, fmt.Sprintf("%s -> %s (failure)", info.From, info.To))
				}
			}
		}
	}

	// Find the worst step (most failures).
	maxCount := 0
	for step, msgs := range a.stepFailures {
		if len(msgs) > maxCount {
			maxCount = len(msgs)
			a.worstStep = step
		}
	}

	// Check if errors progressed (changed between retries) or were stuck on the same error.
	for _, msgs := range a.stepFailures {
		if len(msgs) >= 2 {
			fp0 := normalizeError(msgs[0])
			for _, m := range msgs[1:] {
				if normalizeError(m) != fp0 {
					a.errorProgressed = true
					break
				}
			}
		}
		if a.errorProgressed {
			break
		}
	}

	return a
}

// buildTaskLearningTitle creates a clear, actionable title for a per-task learning.
func buildTaskLearningTitle(task *model.Task, a *taskAnalysis) string {
	if a.worstStep != "" {
		return fmt.Sprintf("Task struggled at %q step: %s", a.worstStep, truncate(task.Title, 60))
	}
	if len(a.deterministicFailures) > 0 {
		return fmt.Sprintf("Verification failures in: %s", truncate(task.Title, 70))
	}
	return fmt.Sprintf("High-retry task: %s", truncate(task.Title, 70))
}

// buildTaskLearningBody creates an actionable body describing the failure pattern.
func buildTaskLearningBody(task *model.Task, a *taskAnalysis) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("Task %q required %d retries (status: %s).",
		truncate(task.Title, 80), task.RetryCount, task.Status))

	if a.worstStep != "" {
		msgs := a.stepFailures[a.worstStep]
		parts = append(parts, fmt.Sprintf("\nMost problematic step: %q (%d failures)", a.worstStep, len(msgs)))
		if a.errorProgressed {
			parts = append(parts, "The error changed between retries, suggesting the agent made partial progress but hit different issues.")
		} else if len(msgs) >= 2 {
			parts = append(parts, "The same error repeated across retries, suggesting the agent was stuck in a loop.")
		}
		// Show the first error as a sample.
		if len(msgs) > 0 {
			parts = append(parts, fmt.Sprintf("Sample error: %s", truncate(msgs[0], 300)))
		}
	}

	if len(a.deterministicFailures) > 0 {
		parts = append(parts, "\nDeterministic verification failures:")
		for _, d := range a.deterministicFailures {
			parts = append(parts, "- "+d)
		}
	}

	if len(a.retryRoutes) > 0 {
		parts = append(parts, "\nRetry routing:")
		for _, r := range a.retryRoutes {
			parts = append(parts, "- "+r)
		}
	}

	return strings.Join(parts, "\n")
}

// -------------------------------------------------------------------------
// Deduplication
// -------------------------------------------------------------------------

// createIfNotDuplicate checks for existing learnings with the same category and
// a similar title before creating a new one. Returns true if a learning was created.
func (s *Synthesizer) createIfNotDuplicate(ctx context.Context, category, title, body, tier string) bool {
	// Check for existing learnings in this category.
	existing, err := s.store.LearningList(ctx, category, 100)
	if err != nil {
		slog.Error("synthesizer: failed to check for duplicates", "err", err)
		// Proceed with creation on error.
	}

	// Check for duplicates by comparing normalized titles.
	normTitle := normalizeLearningTitle(title)
	for _, l := range existing {
		if normalizeLearningTitle(l.Title) == normTitle {
			return false // duplicate found
		}
	}

	id := ulid.Make().String()
	_, err = s.store.LearningCreate(ctx, id, category, title, body)
	if err != nil {
		slog.Error("synthesizer: failed to create learning", "category", category, "err", err)
		return false
	}

	// Update tier (LearningCreate defaults to "source").
	if tier != "source" {
		s.store.DB().ExecContext(ctx, `UPDATE learnings SET tier = ? WHERE id = ?`, tier, id)
	}

	return true
}

// -------------------------------------------------------------------------
// Helpers
// -------------------------------------------------------------------------

// normalizeError extracts a rough fingerprint from an error message for grouping.
// Takes the first 100 non-whitespace-normalized characters.
func normalizeError(msg string) string {
	// Collapse whitespace, trim, take first 100 chars.
	fields := strings.Fields(msg)
	joined := strings.Join(fields, " ")
	if len(joined) > 100 {
		joined = joined[:100]
	}
	return strings.ToLower(joined)
}

// normalizeLearningTitle normalizes a title for deduplication comparison.
func normalizeLearningTitle(title string) string {
	// Lowercase, collapse whitespace, remove count-specific suffixes like "(3 tasks affected)".
	t := strings.ToLower(title)
	// Remove parenthesized counts — they change between runs but the pattern is the same.
	// Match patterns like "(3 tasks affected)" or "(2/5 tasks struggled)".
	result := strings.Builder{}
	depth := 0
	for _, c := range t {
		if c == '(' {
			depth++
		} else if c == ')' {
			if depth > 0 {
				depth--
			}
		} else if depth == 0 {
			result.WriteRune(c)
		}
	}
	return strings.Join(strings.Fields(result.String()), " ")
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
