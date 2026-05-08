package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
	tmplpkg "github.com/rot13maxi/clankwork/internal/template"
)

var cliReference = []string{
	"clankwork bootstrap                  -- load this task's context",
	"clankwork signal started             -- mark task as running",
	"clankwork signal progress <msg>      -- heartbeat with status update",
	"clankwork signal done --bundle <file> -- mark implementation complete with evidence",
	"clankwork signal done --spec <file>   -- submit acceptance spec from acceptance_spec step",
	"clankwork signal done --report <file> -- submit verification report from acceptance step",
	"clankwork signal failed <reason>     -- mark task failed",
	"clankwork signal blocked <question>  -- mark task blocked, request human input",
	"clankwork context <task-id>          -- get task/plan context",
	"clankwork acceptance show <task-id>  -- inspect stored acceptance spec, done bundle, and report",
	"clankwork acceptance smoke --repo <repo-id> --runtime default --case all --wait -- run acceptance smoke controls",
	"clankwork task diagnose <task-id>    -- inspect current desired/observed state and latest validation failure",
	"clankwork prior-art search <query>   -- planner-only search over prior task histories",
	"clankwork verify lint                -- run repo's lint command",
	"clankwork verify typecheck           -- run repo's typecheck command",
	"clankwork verify                     -- run repo's test/verify command",
}

// buildLearningQuery builds an FTS5 OR query from task title and body.
// Uses OR semantics so any matching term scores a hit, avoiding the AND
// default which requires every word to appear in a single learning.
func buildLearningQuery(title, body string) string {
	combined := title + " " + body
	if len(combined) > 200 {
		combined = combined[:200]
	}
	// Keep only alphanumeric characters and spaces; everything else is a token boundary.
	var buf strings.Builder
	for _, ch := range combined {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			buf.WriteRune(ch)
		} else {
			buf.WriteByte(' ')
		}
	}
	// Join unique tokens (min 3 chars) with OR for any-match semantics.
	seen := map[string]bool{}
	var terms []string
	for _, tok := range strings.Fields(buf.String()) {
		if len(tok) >= 3 && !seen[tok] {
			seen[tok] = true
			terms = append(terms, tok)
		}
	}
	return strings.Join(terms, " OR ")
}

// Tier caps for progressive disclosure. Index learnings are compact one-liners,
// digest are paragraph summaries, source is full material.
const (
	tierCapIndex  = 5
	tierCapDigest = 3
	tierCapSource = 1
)

// filterLearningsByTier implements progressive disclosure by ordering learnings
// by tier (index first, then digest, then source) and capping each tier.
// For index-tier learnings, only the title is returned (body is cleared) to keep
// the payload compact. Digest-tier learnings are returned as-is. Source-tier
// learnings are included only up to the cap.
func filterLearningsByTier(all []*model.Learning) []model.Learning {
	var index, digest, source []model.Learning
	for _, l := range all {
		switch l.Tier {
		case "index":
			if len(index) < tierCapIndex {
				cp := *l
				cp.Body = "" // index tier: title only
				index = append(index, cp)
			}
		case "digest":
			if len(digest) < tierCapDigest {
				digest = append(digest, *l)
			}
		default: // "source" or any unknown tier
			if len(source) < tierCapSource {
				source = append(source, *l)
			}
		}
	}
	result := make([]model.Learning, 0, len(index)+len(digest)+len(source))
	result = append(result, index...)
	result = append(result, digest...)
	result = append(result, source...)
	return result
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var req model.BootstrapRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}

	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	var repo *model.Repo
	if req.RepoID != "" {
		repo, _ = s.store.RepoGet(r.Context(), req.RepoID)
	}
	if repo == nil && task.RepoID != "" {
		repo, _ = s.store.RepoGet(r.Context(), task.RepoID)
	}

	// Derive role: explicit request > template step definition > task.Role.
	role := req.Role
	if role == "" && task.Template != "" && task.CurrentStep != "" {
		repoPath := ""
		if repo != nil {
			repoPath = repo.Path
		}
		if tmpl, err := tmplpkg.Load(task.Template, repoPath, s.homeDir); err == nil {
			if step, ok := tmpl.Steps[task.CurrentStep]; ok {
				role = step.Role
			}
		}
	}
	if role == "" {
		role = task.Role
	}

	// Load role definition from repo if available.
	var roleBody string
	if role != "" && repo != nil {
		rolePath := filepath.Join(repo.Path, "roles", role+".md")
		if data, err := os.ReadFile(rolePath); err == nil {
			roleBody = string(data)
		}
	}

	// Inject failure context for template tasks (last 3 step.failure_context traces, ≤4KB).
	// Each entry is labeled with its step name and attempt count so the implementer can
	// distinguish "critic said X" from "lint failed with Y".
	var failureContext string
	if task.Template != "" {
		traces, _ := s.store.TraceListByType(r.Context(), task.ID, "step.failure_context", 3)
		if len(traces) > 0 {
			var parts []string
			for _, tr := range traces {
				var fc struct {
					Step    string `json:"step"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal([]byte(tr.Payload), &fc); err != nil {
					parts = append(parts, tr.Payload)
					continue
				}
				if fc.Step == "" {
					parts = append(parts, fc.Message)
					continue
				}
				attempts := task.StepAttempts[fc.Step]
				parts = append(parts, fmt.Sprintf("[%s, attempt %d]: %s", fc.Step, attempts, fc.Message))
			}
			combined := strings.Join(parts, "\n")
			if len(combined) > 4096 {
				combined = combined[len(combined)-4096:]
			}
			failureContext = combined
		}
	}

	// Surface lint/typecheck commands so agents know what's available.
	var lintCmd, typecheckCmd string
	if repo != nil {
		lintCmd = repo.LintCommand
		typecheckCmd = repo.TypecheckCommand
	}
	acceptanceSpec, _ := s.store.AcceptanceSpecGet(r.Context(), task.ID)

	resp := model.BootstrapResponse{
		Task:             task,
		Repo:             repo,
		Role:             role,
		RoleBody:         roleBody,
		AcceptanceSpec:   acceptanceSpec,
		FailureContext:   failureContext,
		Learnings:        []model.Learning{},
		CLIReference:     cliReference,
		LintCommand:      lintCmd,
		TypecheckCommand: typecheckCmd,
	}
	OK(w, resp)
}

func (s *Server) handleContextGet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
	}
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}

	var plan *model.Plan
	if task.PlanID != "" {
		plan, _ = s.store.PlanGet(r.Context(), task.PlanID)
	}

	type contextResp struct {
		Task      *model.Task       `json:"task"`
		Plan      *model.Plan       `json:"plan,omitempty"`
		Learnings []*model.Learning `json:"learnings"`
	}
	OK(w, contextResp{Task: task, Plan: plan, Learnings: []*model.Learning{}})
}
