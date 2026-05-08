package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/rot13maxi/clankwork/internal/model"
)

func shouldIndexTaskStatus(status string) bool {
	return status == "done" || status == "failed" || status == "merged" || status == "closed"
}

func shouldIndexMergeStatus(status string) bool {
	return status == "merged" || status == "failed" || status == "rejected" || status == "conflicted"
}

func (s *Store) PriorArtIndexTask(ctx context.Context, taskID string) error {
	task, err := s.TaskGet(ctx, taskID)
	if err != nil {
		return err
	}
	h, err := s.buildPriorArtHistory(ctx, task)
	if err != nil {
		return err
	}
	tagsJSON, _ := json.Marshal(h.Tags)
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO task_history_index
		  (id, task_id, repo_id, plan_id, title, body, template, status, summary, search_text, risk_score, rework_score, tags, metadata, created_at, updated_at)
		VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
		  repo_id=excluded.repo_id, plan_id=excluded.plan_id, title=excluded.title, body=excluded.body,
		  template=excluded.template, status=excluded.status, summary=excluded.summary, search_text=excluded.search_text,
		  risk_score=excluded.risk_score, rework_score=excluded.rework_score, tags=excluded.tags,
		  metadata=excluded.metadata, updated_at=excluded.updated_at`,
		h.ID, h.TaskID, h.RepoID, h.PlanID, h.Title, h.Body, h.Template, h.Status, h.Summary, h.SearchText,
		h.RiskScore, h.ReworkScore, string(tagsJSON), h.Metadata, h.CreatedAt.UTC().Format(time.RFC3339), now)
	return err
}

func (s *Store) PriorArtRebuild(ctx context.Context) (int, error) {
	tasks, err := s.TaskList(ctx, "", "", []string{"done", "failed", "merged", "closed"})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, task := range tasks {
		if err := s.PriorArtIndexTask(ctx, task.ID); err != nil {
			return count, err
		}
		count++
	}
	items, _ := s.MergeQueueList(ctx)
	seen := map[string]bool{}
	for _, task := range tasks {
		seen[task.ID] = true
	}
	for _, item := range items {
		if !shouldIndexMergeStatus(item.Status) || seen[item.TaskID] {
			continue
		}
		if err := s.PriorArtIndexTask(ctx, item.TaskID); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func (s *Store) PriorArtGetByTask(ctx context.Context, taskID string) (*model.PriorArtHistory, error) {
	row := s.db.QueryRowContext(ctx, priorArtSelect()+` WHERE task_id = ?`, taskID)
	h, err := scanPriorArtHistory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

func (s *Store) PriorArtSearch(ctx context.Context, req model.PriorArtSearchRequest) (*model.PriorArtSearchResponse, error) {
	if req.Limit <= 0 {
		req.Limit = 10
	}
	histories, err := s.priorArtSearch(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(histories) > req.Limit {
		histories = histories[:req.Limit]
	}
	results := make([]model.PriorArtSearchResult, 0, len(histories))
	for _, h := range histories {
		results = append(results, model.PriorArtSearchResult{
			TaskID:        h.TaskID,
			Title:         h.Title,
			Status:        h.Status,
			Summary:       h.Summary,
			ReworkScore:   h.ReworkScore,
			RiskScore:     h.RiskScore,
			MatchedReason: priorArtMatchedReason(req.Query, h),
			KeyLessons:    priorArtKeyLessons(h),
		})
	}
	return &model.PriorArtSearchResponse{Results: results}, nil
}

func priorArtSelect() string {
	return `SELECT id, task_id, COALESCE(repo_id,''), COALESCE(plan_id,''), title, COALESCE(body,''), COALESCE(template,''), COALESCE(status,''), COALESCE(summary,''), search_text, COALESCE(risk_score,0), COALESCE(rework_score,0), COALESCE(tags,'[]'), COALESCE(metadata,'{}'), created_at, updated_at FROM task_history_index`
}

func (s *Store) priorArtSearch(ctx context.Context, req model.PriorArtSearchRequest) ([]*model.PriorArtHistory, error) {
	query := strings.TrimSpace(req.Query)
	ftsQuery := priorArtFTSQuery(query)
	if ftsQuery != "" {
		return s.priorArtSearchFTS(ctx, req, ftsQuery)
	}
	return s.priorArtSearchByFilters(ctx, req)
}

func (s *Store) priorArtSearchByFilters(ctx context.Context, req model.PriorArtSearchRequest) ([]*model.PriorArtHistory, error) {
	where := " WHERE 1=1"
	var args []any
	if req.RepoID != "" {
		where += " AND repo_id = ?"
		args = append(args, req.RepoID)
	}
	if req.Template != "" {
		where += " AND template = ?"
		args = append(args, req.Template)
	}
	if req.Status != "" {
		where += " AND status = ?"
		args = append(args, req.Status)
	}
	if req.MinReworkScore > 0 {
		where += " AND rework_score >= ?"
		args = append(args, req.MinReworkScore)
	}
	if req.MinRiskScore > 0 {
		where += " AND risk_score >= ?"
		args = append(args, req.MinRiskScore)
	}
	args = append(args, req.Limit*4)
	rows, err := s.db.QueryContext(ctx, priorArtSelect()+where+" ORDER BY rework_score DESC, risk_score DESC, updated_at DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.PriorArtHistory
	for rows.Next() {
		h, err := scanPriorArtHistory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) priorArtSearchFTS(ctx context.Context, req model.PriorArtSearchRequest, ftsQuery string) ([]*model.PriorArtHistory, error) {
	where := " WHERE task_history_fts MATCH ?"
	args := []any{ftsQuery}
	if req.RepoID != "" {
		where += " AND h.repo_id = ?"
		args = append(args, req.RepoID)
	}
	if req.Template != "" {
		where += " AND h.template = ?"
		args = append(args, req.Template)
	}
	if req.Status != "" {
		where += " AND h.status = ?"
		args = append(args, req.Status)
	}
	if req.MinReworkScore > 0 {
		where += " AND h.rework_score >= ?"
		args = append(args, req.MinReworkScore)
	}
	if req.MinRiskScore > 0 {
		where += " AND h.risk_score >= ?"
		args = append(args, req.MinRiskScore)
	}
	args = append(args, req.Limit*4)
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.id, h.task_id, COALESCE(h.repo_id,''), COALESCE(h.plan_id,''), h.title,
		       COALESCE(h.body,''), COALESCE(h.template,''), COALESCE(h.status,''),
		       COALESCE(h.summary,''), h.search_text, COALESCE(h.risk_score,0),
		       COALESCE(h.rework_score,0), COALESCE(h.tags,'[]'), COALESCE(h.metadata,'{}'),
		       h.created_at, h.updated_at,
		       ((-bm25(task_history_fts)) * 10.0) + (COALESCE(h.rework_score,0) * 2.0) + COALESCE(h.risk_score,0) AS blended_score
		  FROM task_history_index h
		  JOIN task_history_fts ON h.rowid = task_history_fts.rowid`+where+`
		 ORDER BY blended_score DESC, h.updated_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.PriorArtHistory
	for rows.Next() {
		h, err := scanPriorArtHistoryWithScore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func priorArtFTSQuery(query string) string {
	seen := map[string]bool{}
	var terms []string
	for _, raw := range strings.Fields(query) {
		var b strings.Builder
		for _, r := range strings.ToLower(raw) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
				b.WriteRune(r)
			}
		}
		tok := b.String()
		if len(tok) < 3 || seen[tok] {
			continue
		}
		seen[tok] = true
		terms = append(terms, tok+"*")
	}
	return strings.Join(terms, " OR ")
}

func scanPriorArtHistoryWithScore(row priorArtScanner) (*model.PriorArtHistory, error) {
	var score float64
	var h model.PriorArtHistory
	var tagsJSON, createdAt, updatedAt string
	if err := row.Scan(&h.ID, &h.TaskID, &h.RepoID, &h.PlanID, &h.Title, &h.Body, &h.Template, &h.Status, &h.Summary, &h.SearchText, &h.RiskScore, &h.ReworkScore, &tagsJSON, &h.Metadata, &createdAt, &updatedAt, &score); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tagsJSON), &h.Tags)
	h.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	h.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &h, nil
}

type priorArtScanner interface{ Scan(dest ...any) error }

func scanPriorArtHistory(row priorArtScanner) (*model.PriorArtHistory, error) {
	var h model.PriorArtHistory
	var tagsJSON, createdAt, updatedAt string
	if err := row.Scan(&h.ID, &h.TaskID, &h.RepoID, &h.PlanID, &h.Title, &h.Body, &h.Template, &h.Status, &h.Summary, &h.SearchText, &h.RiskScore, &h.ReworkScore, &tagsJSON, &h.Metadata, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tagsJSON), &h.Tags)
	h.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	h.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	return &h, nil
}

func (s *Store) buildPriorArtHistory(ctx context.Context, task *model.Task) (*model.PriorArtHistory, error) {
	spec, _ := s.AcceptanceSpecGet(ctx, task.ID)
	bundle, _ := s.DoneBundleGet(ctx, task.ID)
	report, verdict, _ := s.VerificationReportGet(ctx, task.ID)
	traces, _ := s.TraceList(ctx, task.ID, 100)
	artifacts, _ := s.ArtifactList(ctx, task.ID)
	mergeItem, _ := s.MergeQueueGetByTask(ctx, task.ID)
	escalations, _ := s.EscalationList(ctx, task.ID, "")
	rework := computePriorArtRework(task, report, traces, mergeItem, escalations)
	risk := computePriorArtRisk(task, spec, bundle, report, traces, mergeItem, escalations)
	tags := priorArtTags(task, spec, bundle, report, mergeItem, rework, risk)
	summary := priorArtSummary(task, spec, bundle, report, verdict, mergeItem, rework, risk)
	searchText := priorArtSearchText(task, summary, spec, bundle, report, traces, artifacts, mergeItem, escalations, tags)
	meta, _ := json.Marshal(map[string]any{"current_step": task.CurrentStep, "retry_count": task.RetryCount, "step_attempts": task.StepAttempts, "acceptance": spec, "done_bundle": bundle, "verification": report, "verdict": verdict, "merge_outcome": mergeItem, "artifacts": artifacts, "human_actions": escalations, "trace_outcomes": traceCounts(traces)})
	status := task.Status
	if mergeItem != nil && mergeItem.Status != "merged" && shouldIndexMergeStatus(mergeItem.Status) {
		status = mergeItem.Status
	}
	return &model.PriorArtHistory{ID: "taskhist_" + task.ID, TaskID: task.ID, RepoID: task.RepoID, PlanID: task.PlanID, Title: task.Title, Body: task.Body, Template: task.Template, Status: status, Summary: summary, SearchText: searchText, RiskScore: risk, ReworkScore: rework, Tags: tags, Metadata: string(meta), CreatedAt: task.CreatedAt, UpdatedAt: time.Now().UTC()}, nil
}

func computePriorArtRework(task *model.Task, report *model.VerificationReport, traces []*model.Trace, mergeItem *model.MergeQueueItem, escalations []*model.Escalation) float64 {
	score := float64(task.RetryCount)
	for step, attempts := range task.StepAttempts {
		if strings.Contains(step, "implement") && attempts > 1 {
			score += float64(attempts - 1)
		}
	}
	for _, tr := range traces {
		if tr.EventType == "step.failure_context" {
			score += 2
		}
		if strings.Contains(tr.EventType, "verify_failed") {
			score += 3
		}
	}
	if report != nil {
		score += float64(len(report.Failures) * 2)
		for _, result := range report.Results {
			if result.Status == "fail" {
				score += 2
			}
		}
	}
	if mergeItem != nil {
		switch mergeItem.Status {
		case "conflicted", "failed":
			score += 3
		case "rejected":
			score += 5
		}
	}
	if len(escalations) > 0 {
		score += 5
	}
	if task.Status == "failed" || task.Status == "closed" {
		score += 5
	}
	return score
}

func computePriorArtRisk(task *model.Task, spec *model.AcceptanceSpec, bundle *model.DoneBundle, report *model.VerificationReport, traces []*model.Trace, mergeItem *model.MergeQueueItem, escalations []*model.Escalation) float64 {
	score := 0.0
	text := strings.ToLower(task.Title + " " + task.Body)
	if bundle != nil {
		text += " " + strings.ToLower(strings.Join(bundle.FilesChanged, " "))
	}
	for _, key := range []string{"auth", "security", "payment", "migration", "config", "deploy"} {
		if strings.Contains(text, key) {
			score++
			break
		}
	}
	if spec != nil {
		for _, c := range spec.Criteria {
			if c.RequiresNegativeAssertion || len(c.FailIf) > 0 {
				score++
				break
			}
			for _, p := range c.Probes {
				if p.NegativeAssertion != "" {
					score++
					break
				}
			}
		}
	}
	if bundle != nil && len(bundle.FilesChanged) > 10 {
		score++
	}
	if mergeItem != nil && (mergeItem.Status == "conflicted" || strings.Contains(strings.ToLower(mergeItem.FailureLog), "conflict")) {
		score++
	}
	if report != nil && len(report.Failures) > 0 {
		score++
	} else {
		for _, tr := range traces {
			if tr.EventType == "step.failure_context" {
				score++
				break
			}
		}
	}
	if len(escalations) > 0 {
		score += 2
	}
	return score
}

func priorArtSummary(task *model.Task, spec *model.AcceptanceSpec, bundle *model.DoneBundle, report *model.VerificationReport, verdict string, mergeItem *model.MergeQueueItem, rework, risk float64) string {
	parts := []string{fmt.Sprintf("Task ended as %s with rework score %.0f and risk score %.0f.", task.Status, rework, risk)}
	if bundle != nil && bundle.Summary != "" {
		parts = append(parts, "Done summary: "+bundle.Summary)
	}
	if spec != nil {
		parts = append(parts, fmt.Sprintf("Acceptance spec had %d criteria.", len(spec.Criteria)))
	}
	if report != nil {
		parts = append(parts, fmt.Sprintf("Verification verdict %s with confidence %.2f.", verdict, report.ComputedConfidence))
	}
	if mergeItem != nil {
		parts = append(parts, "Merge outcome: "+mergeItem.Status)
	}
	return strings.Join(parts, " ")
}

func priorArtSearchText(task *model.Task, summary string, spec *model.AcceptanceSpec, bundle *model.DoneBundle, report *model.VerificationReport, traces []*model.Trace, artifacts []*model.Artifact, mergeItem *model.MergeQueueItem, escalations []*model.Escalation, tags []string) string {
	var b strings.Builder
	writePriorArt(&b, task.Title, task.Body, task.Template, task.Status, summary, strings.Join(tags, " "))
	if spec != nil {
		for _, c := range spec.Criteria {
			writePriorArt(&b, c.ID, c.Description, strings.Join(c.RequiredArtifacts, " "), strings.Join(c.FailIf, " "))
			for _, p := range c.Probes {
				writePriorArt(&b, p.ID, p.Description, p.Type, p.Command, strings.Join(p.RequiredEvidence, " "), p.NegativeAssertion)
			}
		}
	}
	if bundle != nil {
		writePriorArt(&b, bundle.Summary, strings.Join(bundle.FilesChanged, " "), strings.Join(bundle.TestsRun, " "), strings.Join(bundle.KnownRisks, " "))
	}
	if report != nil {
		for _, r := range report.Results {
			writePriorArt(&b, r.CriterionID, r.Status, r.Reason)
		}
		for _, f := range report.Failures {
			writePriorArt(&b, f.CriterionID, f.Reason)
		}
	}
	for _, a := range artifacts {
		writePriorArt(&b, a.ArtifactType, a.Path, a.Command, a.Status)
	}
	for _, tr := range traces {
		writePriorArt(&b, tr.EventType, tr.StepName, tr.Payload)
	}
	if mergeItem != nil {
		writePriorArt(&b, mergeItem.Status, mergeItem.FailureLog)
	}
	for _, esc := range escalations {
		writePriorArt(&b, esc.Reason, esc.RequestedAction, esc.Outcome)
	}
	return b.String()
}

func writePriorArt(b *strings.Builder, parts ...string) {
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			b.WriteString(part)
			b.WriteByte('\n')
		}
	}
}

func priorArtTags(task *model.Task, spec *model.AcceptanceSpec, bundle *model.DoneBundle, report *model.VerificationReport, mergeItem *model.MergeQueueItem, rework, risk float64) []string {
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" {
			seen[s] = true
		}
	}
	add(task.Template)
	add(task.Status)
	if rework >= 5 {
		add("high-rework")
	}
	if risk >= 3 {
		add("high-risk")
	}
	if mergeItem != nil {
		add("merge-" + mergeItem.Status)
	}
	if spec != nil {
		for _, c := range spec.Criteria {
			if c.RequiresNegativeAssertion || len(c.FailIf) > 0 {
				add("negative-assertions")
			}
		}
	}
	if report != nil && len(report.Failures) > 0 {
		add("verification-failures")
	}
	if bundle != nil {
		for _, f := range bundle.FilesChanged {
			if strings.Contains(strings.ToLower(f), "auth") {
				add("auth")
			}
		}
	}
	out := make([]string, 0, len(seen))
	for tag := range seen {
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func priorArtMatchedReason(query string, h *model.PriorArtHistory) string {
	if h.ReworkScore >= 5 || h.RiskScore >= 3 {
		return fmt.Sprintf("matched query with elevated rework/risk scores (rework %.0f, risk %.0f)", h.ReworkScore, h.RiskScore)
	}
	if query != "" {
		return "matched prior task history for query: " + query
	}
	return "ranked by recent task history and rework/risk scores"
}

func priorArtKeyLessons(h *model.PriorArtHistory) []string {
	var lessons []string
	if h.ReworkScore >= 5 {
		lessons = append(lessons, "High rework history: inspect failed probes, retries, and merge outcome before planning similar work.")
	}
	if h.RiskScore >= 3 {
		lessons = append(lessons, "High risk history: encode explicit acceptance probes and negative assertions for comparable changes.")
	}
	if strings.Contains(h.SearchText, "negative") {
		lessons = append(lessons, "Prior evidence mentions negative cases; consider fresh negative probes in the new acceptance spec.")
	}
	if strings.Contains(h.SearchText, "verify failed") || strings.Contains(h.SearchText, "verification") {
		lessons = append(lessons, "Plan deterministic checks and required evidence up front.")
	}
	if len(lessons) == 0 {
		lessons = append(lessons, "Use the prior task's acceptance criteria, tests run, and evidence artifacts as planning input only.")
	}
	if len(lessons) > 3 {
		return lessons[:3]
	}
	return lessons
}

func traceCounts(traces []*model.Trace) map[string]int {
	counts := map[string]int{}
	for _, tr := range traces {
		counts[tr.EventType]++
	}
	return counts
}
