package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/model"
	tmplpkg "github.com/rot13maxi/clankwork/internal/template"
)

func (s *Server) handleTasksCreate(w http.ResponseWriter, r *http.Request) {
	var req model.CreateTaskRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.Title == "" {
		Fail(w, http.StatusBadRequest, "missing_title", "title is required")
		return
	}

	if req.Template != "" {
		var repoPath string
		if req.RepoID != "" {
			if repo, err := s.store.RepoGet(r.Context(), req.RepoID); err == nil {
				repoPath = repo.Path
			}
		}
		if _, err := tmplpkg.Load(req.Template, repoPath, s.homeDir); err != nil {
			Fail(w, http.StatusBadRequest, "invalid_template", err.Error())
			return
		}
	}

	id := ulid.Make().String()
	task, err := s.store.TaskCreate(r.Context(), id, req.PlanID, req.RepoID, req.Title, req.Body, req.Template, req.Role, req.Runtime, req.Priority)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, task)
}

func (s *Server) handleTasksList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	planID := q.Get("plan_id")
	repoID := q.Get("repo_id")
	statusParam := q.Get("status")
	var statuses []string
	if statusParam != "" {
		statuses = strings.Split(statusParam, ",")
	}
	tasks, err := s.store.TaskList(r.Context(), planID, repoID, statuses)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if tasks == nil {
		tasks = []*model.Task{}
	}
	OK(w, tasks)
}

func (s *Server) handleTasksGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		Fail(w, http.StatusBadRequest, "missing_id", "id query param required")
		return
	}
	task, err := s.store.TaskGet(r.Context(), id)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	// Attach traces.
	traces, _ := s.store.TraceList(r.Context(), id, 20)
	spec, _ := s.store.AcceptanceSpecGet(r.Context(), id)
	bundle, _ := s.store.DoneBundleGet(r.Context(), id)
	report, verdict, _ := s.store.VerificationReportGet(r.Context(), id)
	type taskDetail struct {
		*model.Task
		AcceptanceSpec      *model.AcceptanceSpec     `json:"acceptance_spec,omitempty"`
		DoneBundle          *model.DoneBundle         `json:"done_bundle,omitempty"`
		VerificationReport  *model.VerificationReport `json:"verification_report,omitempty"`
		VerificationVerdict string                    `json:"verification_verdict,omitempty"`
		Traces              []*model.Trace            `json:"traces"`
	}
	if traces == nil {
		traces = []*model.Trace{}
	}
	OK(w, taskDetail{Task: task, AcceptanceSpec: spec, DoneBundle: bundle, VerificationReport: report, VerificationVerdict: verdict, Traces: traces})
}

func (s *Server) handleTasksGetByName(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		Fail(w, http.StatusBadRequest, "missing_name", "name query param required")
		return
	}
	task, err := s.store.TaskGetByName(r.Context(), name)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	OK(w, task)
}

func (s *Server) handleTasksAddDep(w http.ResponseWriter, r *http.Request) {
	var req model.AddDepRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" || req.DependsOnID == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "task_id and depends_on_id required")
		return
	}
	if err := s.store.TaskAddDep(r.Context(), req.TaskID, req.DependsOnID); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, map[string]string{"task_id": req.TaskID, "depends_on_id": req.DependsOnID})
}

func (s *Server) handleTasksSetPriority(w http.ResponseWriter, r *http.Request) {
	var req model.SetPriorityRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	if err := s.store.TaskSetPriority(r.Context(), req.TaskID, req.Priority); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, map[string]string{"task_id": req.TaskID, "priority": strconv.Itoa(req.Priority)})
}

func (s *Server) handleTasksRetry(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		Fail(w, http.StatusBadRequest, "missing_id", "id query param required")
		return
	}
	if err := s.store.TaskRetry(r.Context(), id); err != nil {
		Fail(w, http.StatusBadRequest, "retry_failed", err.Error())
		return
	}
	OK(w, map[string]string{"task_id": id, "status": "pending"})
}

func (s *Server) handleTasksClose(w http.ResponseWriter, r *http.Request) {
	var req model.CloseTaskRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.TaskID == "" || req.Outcome == "" || req.Reason == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "task_id, outcome, and reason required")
		return
	}
	if !validCloseOutcome(req.Outcome) {
		Fail(w, http.StatusBadRequest, "invalid_outcome", "outcome must be one of obsolete, superseded, expected_failure, manual_abandon")
		return
	}
	task, err := s.store.TaskGet(r.Context(), req.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if task.Status == "merged" {
		Fail(w, http.StatusBadRequest, "invalid_state", "merged tasks cannot be closed")
		return
	}
	if task.Status == "closed" {
		OK(w, map[string]string{"task_id": req.TaskID, "status": "closed", "outcome": req.Outcome})
		return
	}

	agent, _ := s.store.AgentGetByTask(r.Context(), req.TaskID)
	if err := s.store.TaskSetStatus(r.Context(), req.TaskID, "closed"); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if agent != nil {
		s.cleanupTerminalAgent(r.Context(), agent, true)
	}
	if escalations, err := s.store.EscalationList(r.Context(), req.TaskID, "open"); err == nil {
		for _, esc := range escalations {
			_ = s.store.EscalationResolve(r.Context(), esc.ID, req.Outcome+": "+req.Reason, defaultActor(req.ActorID))
		}
	}
	_ = s.store.TraceAppend(r.Context(), req.TaskID, "", "task.closed", model.MarshalPayload(map[string]string{
		"outcome": req.Outcome,
		"reason":  req.Reason,
	}))
	s.recordActuation(r, "task.close", defaultActor(req.ActorID), "task", req.TaskID, req.TaskID, task.CurrentStep, "", task.Status, "closed", "success", "", req.Outcome+": "+req.Reason)
	OK(w, map[string]string{"task_id": req.TaskID, "status": "closed", "outcome": req.Outcome})
}

func validCloseOutcome(outcome string) bool {
	switch outcome {
	case "obsolete", "superseded", "expected_failure", "manual_abandon":
		return true
	default:
		return false
	}
}
