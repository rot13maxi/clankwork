package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handlePriorArtSearch(w http.ResponseWriter, r *http.Request) {
	req := model.PriorArtSearchRequest{
		Query:    r.URL.Query().Get("q"),
		RepoID:   r.URL.Query().Get("repo_id"),
		Template: r.URL.Query().Get("template"),
		Status:   r.URL.Query().Get("status"),
	}
	if req.Query == "" {
		req.Query = r.URL.Query().Get("query")
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Limit = n
		}
	}
	if v := r.URL.Query().Get("min_rework_score"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			req.MinReworkScore = n
		}
	}
	if v := r.URL.Query().Get("min_risk_score"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			req.MinRiskScore = n
		}
	}
	resp, err := s.store.PriorArtSearch(r.Context(), req)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, resp)
}

func (s *Server) handlePriorArtShow(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		taskID = r.URL.Query().Get("id")
	}
	if taskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	history, err := s.store.PriorArtGetByTask(r.Context(), taskID)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if history == nil {
		if err := s.store.PriorArtIndexTask(r.Context(), taskID); err != nil {
			Fail(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		history, err = s.store.PriorArtGetByTask(r.Context(), taskID)
		if err != nil {
			Fail(w, http.StatusInternalServerError, "db_error", err.Error())
			return
		}
	}
	OK(w, history)
}

func (s *Server) handlePriorArtRebuild(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.PriorArtRebuild(r.Context())
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, map[string]any{"indexed": count})
}

func formatRelevantPriorArt(results []model.PriorArtSearchResult) string {
	if len(results) == 0 {
		return ""
	}
	out := "## Relevant Prior Art\n\n"
	out += "The following previous tasks appear similar or instructive. Use them as planning input only; new tasks still need fresh evidence.\n\n"
	for _, r := range results {
		out += fmt.Sprintf("### %s\n", r.Title)
		out += fmt.Sprintf("- Final status: %s\n", r.Status)
		out += fmt.Sprintf("- Rework score: %.0f\n", r.ReworkScore)
		out += fmt.Sprintf("- Risk score: %.0f\n", r.RiskScore)
		out += fmt.Sprintf("- Why relevant: %s\n", r.MatchedReason)
		for _, lesson := range r.KeyLessons {
			out += fmt.Sprintf("- Planning implication: %s\n", lesson)
		}
		if r.Summary != "" {
			out += fmt.Sprintf("- Summary: %s\n", r.Summary)
		}
		out += "\n"
	}
	return out
}
