package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
)

func (s *Server) handleAcceptanceSpecGet(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id query param required")
		return
	}
	spec, err := s.store.AcceptanceSpecGet(r.Context(), taskID)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if spec == nil {
		Fail(w, http.StatusNotFound, "not_found", "acceptance spec not found")
		return
	}
	OK(w, spec)
}

func (s *Server) handleAcceptanceSpecPut(w http.ResponseWriter, r *http.Request) {
	var spec model.AcceptanceSpec
	if err := Decode(r, &spec); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	task, err := s.store.TaskGet(r.Context(), spec.TaskID)
	if err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	result := model.ValidateAcceptanceSpecDetailedWithPolicy(&spec, spec.TaskID, task, s.acceptanceRiskPolicy())
	if !result.Valid {
		Fail(w, http.StatusBadRequest, "invalid_acceptance_spec", strings.Join(result.Errors, "; "))
		return
	}
	if err := s.store.AcceptanceSpecPutValidation(r.Context(), &spec, result); err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, &spec)
}

func (s *Server) handleDoneBundleGet(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id query param required")
		return
	}
	bundle, err := s.store.DoneBundleGet(r.Context(), taskID)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if bundle == nil {
		Fail(w, http.StatusNotFound, "not_found", "done bundle not found")
		return
	}
	OK(w, bundle)
}

func (s *Server) handleVerificationReportGet(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id query param required")
		return
	}
	report, verdict, err := s.store.VerificationReportGet(r.Context(), taskID)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if report == nil {
		Fail(w, http.StatusNotFound, "not_found", "verification report not found")
		return
	}
	OK(w, map[string]any{"report": report, "verdict": verdict})
}

func (s *Server) handleArtifactAdd(w http.ResponseWriter, r *http.Request) {
	var req model.AddArtifactRequest
	if err := Decode(r, &req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}
	if err := model.ValidateAddArtifactRequest(req); err != nil {
		Fail(w, http.StatusBadRequest, "invalid_artifact", err.Error())
		return
	}
	if _, err := s.store.TaskGet(r.Context(), req.TaskID); err != nil {
		Fail(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	capturedWD, err := s.captureArtifactBytes(req)
	if err != nil {
		Fail(w, http.StatusBadRequest, "capture_failed", err.Error())
		return
	}
	req.WorkingDirectory = capturedWD
	artifact, err := s.store.ArtifactAdd(r.Context(), req)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, artifact)
}

func (s *Server) handleArtifactsList(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		Fail(w, http.StatusBadRequest, "missing_task_id", "task_id required")
		return
	}
	artifacts, err := s.store.ArtifactList(r.Context(), taskID)
	if err != nil {
		Fail(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	OK(w, artifacts)
}

func (s *Server) captureArtifactBytes(req model.AddArtifactRequest) (string, error) {
	if filepath.IsAbs(req.Path) {
		return "", fmt.Errorf("artifact path must be relative so reports can replay against captured bytes")
	}
	source := req.Path
	if req.WorkingDirectory != "" {
		source = filepath.Join(req.WorkingDirectory, req.Path)
	}
	source = filepath.Clean(source)
	hash, err := fileSHA256(source)
	if err != nil {
		return "", err
	}
	if modelHash := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(req.SHA256)), "sha256:"); modelHash != hash {
		return "", fmt.Errorf("sha256 %q does not match artifact file hash sha256:%s", req.SHA256, hash)
	}
	captureRoot := filepath.Join(s.homeDir, "artifact-store", req.TaskID, hash)
	dest := filepath.Join(captureRoot, filepath.FromSlash(req.Path))
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", err
	}
	if err := copyFile(dest, source); err != nil {
		return "", err
	}
	return captureRoot, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(dest, source string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return nil
}
