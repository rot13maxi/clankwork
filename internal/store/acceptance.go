package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/model"
)

type acceptanceResetExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *Store) AcceptanceSpecPut(ctx context.Context, spec *model.AcceptanceSpec) error {
	return s.AcceptanceSpecPutValidation(ctx, spec, model.AcceptanceSpecValidationResult{Valid: true, RiskLevel: model.RiskLevelNormal})
}

func (s *Store) AcceptanceSpecPutValidation(ctx context.Context, spec *model.AcceptanceSpec, validation model.AcceptanceSpecValidationResult) error {
	if spec.StepID == "" {
		spec.StepID = "acceptance_spec"
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	status := "valid"
	if !validation.Valid {
		status = "invalid"
	}
	errorsJSON, err := json.Marshal(validation.Errors)
	if err != nil {
		return err
	}
	riskLevel := validation.RiskLevel
	if riskLevel == "" {
		riskLevel = model.RiskLevelNormal
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO acceptance_specs (task_id, spec_json, step_id, path, sha256, strength_score, risk_level, validation_status, validation_errors, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET spec_json = excluded.spec_json, strength_score = excluded.strength_score,
		   step_id = excluded.step_id, path = excluded.path, sha256 = excluded.sha256,
		   risk_level = excluded.risk_level, validation_status = excluded.validation_status,
		   validation_errors = excluded.validation_errors, updated_at = excluded.updated_at`,
		spec.TaskID, string(data), spec.StepID, spec.Path, spec.SHA256, validation.StrengthScore, riskLevel, status, string(errorsJSON), now, now)
	return err
}

func (s *Store) AcceptanceSpecGet(ctx context.Context, taskID string) (*model.AcceptanceSpec, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT spec_json FROM acceptance_specs WHERE task_id = ?`, taskID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var spec model.AcceptanceSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, err
	}
	return &spec, nil
}

func (s *Store) DoneBundlePut(ctx context.Context, bundle *model.DoneBundle) error {
	data, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO done_bundles (task_id, bundle_json, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET bundle_json = excluded.bundle_json, updated_at = excluded.updated_at`,
		bundle.TaskID, string(data), now, now)
	return err
}

func (s *Store) DoneBundleGet(ctx context.Context, taskID string) (*model.DoneBundle, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT bundle_json FROM done_bundles WHERE task_id = ?`, taskID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var bundle model.DoneBundle
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, err
	}
	return &bundle, nil
}

func (s *Store) VerificationReportPut(ctx context.Context, report *model.VerificationReport, verdict string) error {
	return s.VerificationReportPutValidation(ctx, report, verdict, nil)
}

func (s *Store) VerificationReportPutValidation(ctx context.Context, report *model.VerificationReport, verdict string, validationErrors []string) error {
	if report.StepID == "" {
		report.StepID = "acceptance"
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	status := "valid"
	if len(validationErrors) > 0 {
		status = "invalid"
	}
	errorsJSON, err := json.Marshal(validationErrors)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO verification_reports (task_id, report_json, step_id, path, sha256, verdict, computed_confidence, confidence_label, validation_status, validation_errors, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET report_json = excluded.report_json, verdict = excluded.verdict,
		   step_id = excluded.step_id, path = excluded.path, sha256 = excluded.sha256,
		   computed_confidence = excluded.computed_confidence, confidence_label = excluded.confidence_label,
		   validation_status = excluded.validation_status, validation_errors = excluded.validation_errors,
		   updated_at = excluded.updated_at`,
		report.TaskID, string(data), report.StepID, report.Path, report.SHA256, verdict, report.ComputedConfidence, report.ConfidenceLabel, status, string(errorsJSON), now, now)
	return err
}

func (s *Store) VerificationReportGet(ctx context.Context, taskID string) (*model.VerificationReport, string, error) {
	var raw, verdict string
	err := s.db.QueryRowContext(ctx, `SELECT report_json, verdict FROM verification_reports WHERE task_id = ?`, taskID).Scan(&raw, &verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var report model.VerificationReport
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return nil, "", err
	}
	return &report, verdict, nil
}

func (s *Store) ArtifactAdd(ctx context.Context, req model.AddArtifactRequest) (*model.Artifact, error) {
	artifact := &model.Artifact{
		ID:               ulid.Make().String(),
		TaskID:           req.TaskID,
		StepID:           req.StepID,
		Producer:         req.Producer,
		ProducerType:     req.ProducerType,
		Path:             req.Path,
		ArtifactType:     req.ArtifactType,
		SHA256:           req.SHA256,
		Command:          req.Command,
		WorkingDirectory: req.WorkingDirectory,
		ExitCode:         req.ExitCode,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		Status:           "valid",
	}
	if artifact.StepID == "" {
		artifact.StepID = "acceptance"
	}
	if artifact.ProducerType == "" {
		artifact.ProducerType = "agent"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO artifacts (id, task_id, step_id, producer, producer_type, artifact_type, path, sha256, command, working_directory, exit_code, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		artifact.ID, artifact.TaskID, artifact.StepID, artifact.Producer, artifact.ProducerType, artifact.ArtifactType,
		artifact.Path, artifact.SHA256, artifact.Command, artifact.WorkingDirectory, artifact.ExitCode, artifact.Status, artifact.CreatedAt)
	if err != nil {
		return nil, err
	}
	return artifact, nil
}

func (s *Store) ArtifactList(ctx context.Context, taskID string) ([]*model.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, task_id, step_id, producer, producer_type, artifact_type, path, sha256,
		       COALESCE(command, ''), COALESCE(working_directory, ''), COALESCE(exit_code, 0),
		       COALESCE(status, 'valid'), COALESCE(invalidated_at, ''), created_at
		FROM artifacts
		WHERE task_id = ?
		ORDER BY created_at ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var artifacts []*model.Artifact
	for rows.Next() {
		var a model.Artifact
		if err := rows.Scan(&a.ID, &a.TaskID, &a.StepID, &a.Producer, &a.ProducerType, &a.ArtifactType, &a.Path, &a.SHA256, &a.Command, &a.WorkingDirectory, &a.ExitCode, &a.Status, &a.InvalidatedAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, &a)
	}
	return artifacts, rows.Err()
}

func (s *Store) ArtifactInvalidate(ctx context.Context, artifactID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		UPDATE artifacts
		SET status = 'invalidated', invalidated_at = ?
		WHERE id = ?`, now, artifactID)
	return err
}

func clearAcceptanceArtifactsForReset(ctx context.Context, execer acceptanceResetExecer, taskID, step string) error {
	var tables []string
	switch step {
	case "acceptance_spec":
		tables = []string{"verification_reports", "done_bundles", "acceptance_specs"}
	case "implement", "test":
		tables = []string{"verification_reports", "done_bundles"}
	case "acceptance":
		tables = []string{"verification_reports", "artifacts"}
	default:
		return nil
	}
	for _, table := range tables {
		if _, err := execer.ExecContext(ctx, `DELETE FROM `+table+` WHERE task_id = ?`, taskID); err != nil {
			return err
		}
	}
	return nil
}
