package api

import (
	"net/http"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handleLearningsAdd(w http.ResponseWriter, r *http.Request) {
	var req model.AddLearningRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.Title == "" || req.Body == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "title and body required")
		return
	}
	if req.Category == "" {
		req.Category = "general"
	}
	id := ulid.Make().String()
	l, err := s.store.LearningCreate(r.Context(), id, req.Category, req.Title, req.Body)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, l)
}

func (s *Server) handleCandidateLearningAdd(w http.ResponseWriter, r *http.Request) {
	var req model.AddCandidateLearningRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.SourceTraceID == "" || req.ProposedLearning == "" || req.Reason == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "source_trace_id, proposed_learning, and reason required")
		return
	}
	candidate, err := s.store.CandidateLearningCreate(r.Context(), req)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, candidate)
}

func (s *Server) handleCandidateLearningList(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit := 50
	candidates, err := s.store.CandidateLearningList(r.Context(), status, limit)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, candidates)
}
