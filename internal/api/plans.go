package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handlePlansCreate(w http.ResponseWriter, r *http.Request) {
	var req model.CreatePlanRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.Title == "" {
		Fail(w, http.StatusBadRequest, "missing_title", "title is required")
		return
	}
	if req.WithPriorArt {
		search, err := s.store.PriorArtSearch(r.Context(), model.PriorArtSearchRequest{Query: req.Title + " " + req.Body, Limit: 5})
		if err != nil {
			Fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
		if section := formatRelevantPriorArt(search.Results); section != "" {
			req.Body = req.Body + "\n\n---\n\n" + section
		}
	}

	id := ulid.Make().String()
	planDir := filepath.Join(s.homeDir, "plans")
	if err := os.MkdirAll(planDir, 0700); err != nil {
		Fail(w, http.StatusInternalServerError, "fs_error", err.Error())
		return
	}
	planPath := filepath.Join(planDir, fmt.Sprintf("%s.md", id))
	if err := os.WriteFile(planPath, []byte(req.Body), 0600); err != nil {
		Fail(w, http.StatusInternalServerError, "fs_error", err.Error())
		return
	}

	plan, err := s.store.PlanCreate(r.Context(), id, req.Title, planPath)
	if err != nil {
		os.Remove(planPath)
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, plan)
}

func (s *Server) handlePlansList(w http.ResponseWriter, r *http.Request) {
	plans, err := s.store.PlanList(r.Context())
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if plans == nil {
		plans = []*model.Plan{}
	}
	OK(w, plans)
}

func (s *Server) handlePlansGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		Fail(w, http.StatusBadRequest, "missing_id", "id query param required")
		return
	}
	plan, err := s.store.PlanGet(r.Context(), id)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	// Also attach tasks.
	tasks, _ := s.store.TaskList(r.Context(), id, "", nil)
	type planDetail struct {
		*model.Plan
		Tasks []*model.Task `json:"tasks"`
	}
	if tasks == nil {
		tasks = []*model.Task{}
	}
	OK(w, planDetail{Plan: plan, Tasks: tasks})
}

// Ensure time import is used
var _ = time.Now
