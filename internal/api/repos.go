package api

import (
	"net/http"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handleReposCreate(w http.ResponseWriter, r *http.Request) {
	var req model.CreateRepoRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if req.Name == "" || req.Path == "" {
		Fail(w, http.StatusBadRequest, "missing_fields", "name and path are required")
		return
	}
	if req.TargetBranch == "" {
		req.TargetBranch = "main"
	}
	id := ulid.Make().String()
	repo, err := s.store.RepoCreate(r.Context(), id, req.Name, req.Path, req.TargetBranch, req.VerifyCommand, req.LintCommand, req.TypecheckCommand, req.AutoPush)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, repo)
}

func (s *Server) handleReposGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		Fail(w, http.StatusBadRequest, "missing_id", "id query parameter is required")
		return
	}
	repo, err := s.store.RepoGet(r.Context(), id)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	OK(w, repo)
}

func (s *Server) handleReposList(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.RepoList(r.Context())
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if repos == nil {
		repos = []*model.Repo{}
	}
	OK(w, repos)
}
