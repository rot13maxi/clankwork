package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
	_ "modernc.org/sqlite"
)

func acceptanceCmd() *cli.Command {
	return &cli.Command{
		Name:  "acceptance",
		Usage: "Inspect acceptance artifacts for tasks",
		Commands: []*cli.Command{
			{
				Name:      "validate-spec",
				Usage:     "Validate acceptance spec strength and schema",
				ArgsUsage: "<path>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) != 1 {
						return fmt.Errorf("usage: clankwork acceptance validate-spec <path>")
					}
					var spec model.AcceptanceSpec
					if err := readJSONFile(args[0], &spec); err != nil {
						return err
					}
					home, err := config.Home(cmd.Root().String("home"))
					if err != nil {
						return err
					}
					policy, err := acceptanceRiskPolicy(home)
					if err != nil {
						return err
					}
					result := model.ValidateAcceptanceSpecDetailedWithPolicy(&spec, spec.TaskID, nil, policy)
					return printJSON(result)
				},
			},
			{
				Name:      "validate-report",
				Usage:     "Validate verification report coverage and computed confidence",
				ArgsUsage: "<path>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "spec", Usage: "Path to acceptance spec JSON; defaults to fetching by report task_id from daemon"},
					&cli.IntFlag{Name: "retry-count", Usage: "Verification retry count to include in confidence computation"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) != 1 {
						return fmt.Errorf("usage: clankwork acceptance validate-report <path>")
					}
					var report model.VerificationReport
					if err := readJSONFile(args[0], &report); err != nil {
						return err
					}
					home, err := config.Home(cmd.Root().String("home"))
					if err != nil {
						return err
					}
					policy, err := acceptanceRiskPolicy(home)
					if err != nil {
						return err
					}
					var spec *model.AcceptanceSpec
					if specPath := cmd.String("spec"); specPath != "" {
						var localSpec model.AcceptanceSpec
						if err := readJSONFile(specPath, &localSpec); err != nil {
							return err
						}
						spec = &localSpec
					} else {
						if report.TaskID == "" {
							return fmt.Errorf("report task_id required when --spec is not provided")
						}
						c, err := newClient(cmd)
						if err != nil {
							return err
						}
						spec, err = c.AcceptanceSpecGet(okCtx(), report.TaskID)
						if err != nil {
							return err
						}
						artifacts, err := c.ArtifactsList(okCtx(), report.TaskID)
						if err != nil {
							return err
						}
						if err := model.ValidateArtifactReferences(&report, artifacts); err != nil {
							result := model.ValidateVerificationReportDetailedWithPolicy(&report, report.TaskID, spec, cmd.Int("retry-count"), policy)
							result.Valid = false
							result.ComputedVerdict = "reject"
							result.Errors = append(result.Errors, err.Error())
							return printJSON(result)
						}
						if err := model.ValidateArtifactHashes(artifacts); err != nil {
							result := model.ValidateVerificationReportDetailedWithPolicy(&report, report.TaskID, spec, cmd.Int("retry-count"), policy)
							result.Valid = false
							result.ComputedVerdict = "reject"
							result.Errors = append(result.Errors, err.Error())
							return printJSON(result)
						}
					}
					result := model.ValidateVerificationReportDetailedWithPolicy(&report, report.TaskID, spec, cmd.Int("retry-count"), policy)
					return printJSON(result)
				},
			},
			{
				Name:      "validate-plan",
				Usage:     "Validate a verification execution plan against an acceptance spec",
				ArgsUsage: "<path>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "spec", Required: true, Usage: "Path to acceptance spec JSON"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) != 1 {
						return fmt.Errorf("usage: clankwork acceptance validate-plan --spec <spec> <path>")
					}
					var spec model.AcceptanceSpec
					if err := readJSONFile(cmd.String("spec"), &spec); err != nil {
						return err
					}
					var plan model.VerificationExecutionPlan
					if err := readJSONFile(args[0], &plan); err != nil {
						return err
					}
					result := model.ValidateExecutionPlanDetailed(&plan, &spec)
					return printJSON(result)
				},
			},
			{
				Name:      "run-plan",
				Usage:     "Run a verification execution plan and write a verification report",
				ArgsUsage: "<path>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "spec", Required: true, Usage: "Path to acceptance spec JSON"},
					&cli.StringFlag{Name: "out", Value: "artifacts/verification-report.json", Usage: "Path to write verification report JSON"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) != 1 {
						return fmt.Errorf("usage: clankwork acceptance run-plan --spec <spec> <path>")
					}
					var spec model.AcceptanceSpec
					if err := readJSONFile(cmd.String("spec"), &spec); err != nil {
						return err
					}
					var plan model.VerificationExecutionPlan
					if err := readJSONFile(args[0], &plan); err != nil {
						return err
					}
					if err := model.ValidateExecutionPlan(&plan, &spec); err != nil {
						return err
					}
					report, err := runExecutionPlan(cmd, &plan, &spec)
					if err != nil {
						return err
					}
					outPath := cmd.String("out")
					if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
						return err
					}
					data, err := json.MarshalIndent(report, "", "  ")
					if err != nil {
						return err
					}
					if err := os.WriteFile(outPath, append(data, '\n'), 0644); err != nil {
						return err
					}
					fmt.Printf("%s  wrote verification report\n", outPath)
					return nil
				},
			},
			{
				Name:      "show",
				Usage:     "Show acceptance artifacts for a task",
				ArgsUsage: "<task-id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "format", Usage: "Output format: human (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork acceptance show <task-id>")
					}
					taskID := args[0]
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					detail, err := c.TasksGet(okCtx(), taskID)
					if err != nil {
						return err
					}

					if cmd.String("format") == "json" {
						return printAcceptanceJSON(detail, taskID)
					}
					return printAcceptanceHuman(detail, taskID)
				},
			},
			{
				Name:  "smoke",
				Usage: "Create standardized acceptance smoke-control tasks",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "repo", Required: true, Usage: "Repo ID to run controls against"},
					&cli.StringFlag{Name: "runtime", Value: "default", Usage: "Runtime name to exercise"},
					&cli.StringFlag{Name: "case", Value: "all", Usage: "Smoke case: all, pass, verification-fail, done-bundle-reject, verification-report-reject"},
					&cli.IntFlag{Name: "priority", Value: 100, Usage: "Base task priority"},
					&cli.BoolFlag{Name: "wait", Usage: "Wait until each smoke case reaches its expected observation"},
					&cli.DurationFlag{Name: "timeout", Value: 20 * time.Minute, Usage: "Per-case wait timeout"},
					&cli.DurationFlag{Name: "poll", Value: 5 * time.Second, Usage: "Polling interval when --wait is set"},
					&cli.BoolFlag{Name: "park-negative", Value: true, Usage: "Block negative-control tasks after the expected observation is seen"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					cases, err := smokeCases(cmd.String("case"))
					if err != nil {
						return err
					}
					for i, sc := range cases {
						task, err := c.TasksCreate(okCtx(), model.CreateTaskRequest{
							RepoID:   cmd.String("repo"),
							Title:    sc.title,
							Body:     sc.body,
							Template: "feature",
							Runtime:  cmd.String("runtime"),
							Priority: cmd.Int("priority") + len(cases) - i,
						})
						if err != nil {
							return err
						}
						fmt.Printf("%s  created  %s\n", task.ID, sc.name)
						if cmd.Bool("wait") {
							if err := waitSmokeCase(c, task.ID, sc, cmd.Duration("timeout"), cmd.Duration("poll"), cmd.Bool("park-negative")); err != nil {
								return err
							}
						}
					}
					return nil
				},
			},
		},
	}
}

func runExecutionPlan(cmd *cli.Command, plan *model.VerificationExecutionPlan, spec *model.AcceptanceSpec) (*model.VerificationReport, error) {
	c, err := newClient(cmd)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	resultsByCriterion := map[string]*model.VerificationResult{}
	for _, criterion := range spec.Criteria {
		resultsByCriterion[criterion.ID] = &model.VerificationResult{
			CriterionID: criterion.ID,
			Status:      "pass",
			Evidence:    []model.Evidence{},
			Reason:      "execution plan probes passed",
		}
	}
	failures := []model.VerificationFailure{}
	criterionByProbe := map[string]string{}
	for _, criterion := range spec.Criteria {
		for _, probe := range criterion.Probes {
			criterionByProbe[probe.ID] = criterion.ID
		}
	}
	for _, step := range plan.Steps {
		output, exitCode, stepErr := executePlanStep(step)
		status := "pass"
		if stepErr != nil {
			status = "fail"
			output = append(output, []byte("\nERROR: "+stepErr.Error()+"\n")...)
		}
		artifactPath := filepath.Join("artifacts", "execution-"+step.ID+".txt")
		if err := os.MkdirAll(filepath.Dir(artifactPath), 0755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(artifactPath, output, 0644); err != nil {
			return nil, err
		}
		hash, err := sha256File(artifactPath)
		if err != nil {
			return nil, err
		}
		for _, produced := range step.Produces {
			artifact, err := c.ArtifactAdd(okCtx(), model.AddArtifactRequest{
				TaskID:           plan.TaskID,
				StepID:           "acceptance",
				Producer:         "acceptance-run-plan",
				ProducerType:     "deterministic_command",
				Path:             filepath.ToSlash(artifactPath),
				ArtifactType:     produced,
				SHA256:           "sha256:" + hash,
				Command:          planStepCommand(step),
				WorkingDirectory: mustGetwd(),
				ExitCode:         exitCode,
			})
			if err != nil {
				return nil, err
			}
			criterionID := criterionByProbe[step.ProbeID]
			result := resultsByCriterion[criterionID]
			result.Evidence = append(result.Evidence, model.Evidence{
				ArtifactID:    artifact.ID,
				Type:          produced,
				Path:          filepath.ToSlash(artifactPath),
				ProbeID:       step.ProbeID,
				Command:       planStepCommand(step),
				ProducerStep:  "acceptance",
				ProducerRole:  "control_plane",
				Timestamp:     now,
				ContentHash:   "sha256:" + hash,
				Authoritative: true,
			})
			if status == "fail" {
				result.Status = "fail"
				result.Reason = "execution step " + step.ID + " failed"
			}
		}
		if status == "fail" {
			criterionID := criterionByProbe[step.ProbeID]
			failures = append(failures, model.VerificationFailure{
				CriterionID: criterionID,
				Reason:      "execution step " + step.ID + " failed",
			})
		}
	}
	report := &model.VerificationReport{
		TaskID:     plan.TaskID,
		Results:    []model.VerificationResult{},
		Failures:   failures,
		Confidence: "high",
	}
	for _, criterion := range spec.Criteria {
		report.Results = append(report.Results, *resultsByCriterion[criterion.ID])
	}
	confidence := model.ComputeVerificationConfidence(spec, report, 0)
	report.ComputedConfidence = confidence
	report.ConfidenceLabel = model.ConfidenceLabel(confidence)
	return report, nil
}

func acceptanceRiskPolicy(home string) (*model.AcceptanceRiskPolicy, error) {
	cfg, err := config.Load(home)
	if err != nil {
		cfg = config.DefaultConfig()
	}
	return &model.AcceptanceRiskPolicy{
		HighRiskLabels: cfg.Acceptance.Risk.HighRiskLabels,
		HighRiskPaths:  cfg.Acceptance.Risk.HighRiskPaths,
	}, nil
}

func executePlanStep(step model.VerificationPlanStep) ([]byte, int, error) {
	switch step.Type {
	case "shell":
		return runShellStep(step.Command, step.ExpectedExitCode)
	case "playwright":
		return runShellStep("npx playwright test "+step.Script, nil)
	case "http":
		return runHTTPStep(step)
	case "file_assertion":
		return runFileAssertionStep(step)
	case "db_query":
		return runDBQueryStep(step)
	default:
		return nil, 1, fmt.Errorf("unsupported execution step type %q", step.Type)
	}
}

func runDBQueryStep(step model.VerificationPlanStep) ([]byte, int, error) {
	db, err := sql.Open("sqlite", step.Path)
	if err != nil {
		return nil, 1, err
	}
	defer db.Close()
	rows, err := db.Query(step.Query)
	if err != nil {
		return nil, 1, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, 1, err
	}
	var out []map[string]any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, 1, err
		}
		row := map[string]any{}
		for i, col := range cols {
			switch v := values[i].(type) {
			case []byte:
				row[col] = string(v)
			default:
				row[col] = v
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 1, err
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, 1, err
	}
	return append(data, '\n'), 0, nil
}

func runShellStep(command string, expected *int) ([]byte, int, error) {
	c := exec.Command("sh", "-c", command)
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	err := c.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	if expected != nil && exitCode != *expected {
		return out.Bytes(), exitCode, fmt.Errorf("exit code %d, want %d", exitCode, *expected)
	}
	if expected == nil && exitCode != 0 {
		return out.Bytes(), exitCode, fmt.Errorf("exit code %d", exitCode)
	}
	return out.Bytes(), exitCode, nil
}

func runHTTPStep(step model.VerificationPlanStep) ([]byte, int, error) {
	var body io.Reader
	if len(step.Body) > 0 {
		data, err := json.Marshal(step.Body)
		if err != nil {
			return nil, 1, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(step.Method, step.URL, body)
	if err != nil {
		return nil, 1, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 1, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 1, err
	}
	output := append([]byte(fmt.Sprintf("HTTP %d\n", resp.StatusCode)), data...)
	if resp.StatusCode != step.ExpectedStatus {
		return output, 1, fmt.Errorf("http status %d, want %d", resp.StatusCode, step.ExpectedStatus)
	}
	return output, 0, nil
}

func runFileAssertionStep(step model.VerificationPlanStep) ([]byte, int, error) {
	data, err := os.ReadFile(step.Path)
	switch step.Assertion {
	case "exists":
		if err != nil {
			return []byte(err.Error()), 1, err
		}
		return []byte("exists: " + step.Path + "\n"), 0, nil
	case "missing":
		if os.IsNotExist(err) {
			return []byte("missing: " + step.Path + "\n"), 0, nil
		}
		return []byte("exists: " + step.Path + "\n"), 1, fmt.Errorf("file exists")
	default:
		if err != nil {
			return []byte(err.Error()), 1, err
		}
		if !strings.Contains(string(data), step.Assertion) {
			return data, 1, fmt.Errorf("file %s does not contain %q", step.Path, step.Assertion)
		}
		return data, 0, nil
	}
}

func planStepCommand(step model.VerificationPlanStep) string {
	switch step.Type {
	case "shell":
		return step.Command
	case "playwright":
		return "npx playwright test " + step.Script
	case "http":
		return step.Method + " " + step.URL
	case "file_assertion":
		return "file_assertion " + step.Path + " " + step.Assertion
	case "db_query":
		return step.Query
	default:
		return step.Type
	}
}

func mustGetwd() string {
	wd, _ := os.Getwd()
	return wd
}

type acceptanceSmokeCase struct {
	name     string
	title    string
	body     string
	negative bool
}

func smokeCases(name string) ([]acceptanceSmokeCase, error) {
	all := []acceptanceSmokeCase{
		{
			name:  "pass",
			title: "Acceptance smoke positive control",
			body: strings.TrimSpace(`Implement a narrow documentation update that proves the selected runtime can drive a feature task through acceptance.

Acceptance expectations:
- Add a short note to docs/acceptance-verification.md mentioning that acceptance smoke controls should use repo-backed tasks.
- Keep the update documentation-only and narrowly scoped to acceptance verification.
- The done bundle must cite the changed file and include the verification command that was run.`),
		},
		{
			name:     "verification-fail",
			title:    "Acceptance smoke failing verification control",
			negative: true,
			body: strings.TrimSpace(`Run a negative-control acceptance task.

Purpose:
- Prove that a structurally valid failing verification report routes a feature task back to implementation.
- Do not try to make this task pass on the first implementation attempt.

Acceptance expectations:
- The acceptance spec must require docs/acceptance-verification.md to contain the exact marker PI_NEGATIVE_CONTROL_EXPECTED_DO_NOT_ADD.
- The implementer must intentionally add only this exact marker instead: PI_NEGATIVE_CONTROL_ACTUAL_ONLY.
- The implementer must not add PI_NEGATIVE_CONTROL_EXPECTED_DO_NOT_ADD.
- The done bundle must still be submitted so the task reaches the acceptance verifier.
- The acceptance verifier should run the probe, observe that the expected marker is absent, submit a valid failing verification report, and allow Clankwork to route the task back to implementation.`),
		},
		{
			name:     "done-bundle-reject",
			title:    "Acceptance smoke done bundle rejection control",
			negative: true,
			body: strings.TrimSpace(`Run a negative-control done-bundle task.

Purpose:
- Prove that implementation completion is rejected when the done bundle does not satisfy the accepted acceptance spec.
- Do not try to make this task pass on the first implementation attempt.

Acceptance expectations:
- The acceptance spec must require a cli_transcript artifact proving docs/acceptance-verification.md contains PI_DONE_BUNDLE_REJECTION_CONTROL.
- The implementer may add PI_DONE_BUNDLE_REJECTION_CONTROL to docs/acceptance-verification.md.
- The implementer must intentionally submit an invalid done bundle first: omit the required cli_transcript artifact or omit authoritative provenance for it.
- The control plane should reject clankwork signal done --bundle ... and keep the task in implement until the bundle is corrected.`),
		},
		{
			name:     "verification-report-reject",
			title:    "Acceptance smoke verification report rejection control",
			negative: true,
			body: strings.TrimSpace(`Run a negative-control verification-report task.

Purpose:
- Prove that acceptance completion is rejected when the verification report is structurally invalid.
- Do not try to make this task pass on the first acceptance attempt.

Acceptance expectations:
- The acceptance spec must require a cli_transcript artifact proving docs/acceptance-verification.md contains PI_VERIFICATION_REPORT_REJECTION_CONTROL.
- The implementer should add PI_VERIFICATION_REPORT_REJECTION_CONTROL to docs/acceptance-verification.md and submit a valid done bundle.
- The acceptance verifier must intentionally submit an invalid verification report first: omit the required cli_transcript evidence or omit authoritative provenance/content hash for the evidence.
- The control plane should reject clankwork signal done --report ... and keep the task in acceptance until the report is corrected.`),
		},
	}
	if name == "" || name == "all" {
		return all, nil
	}
	for _, sc := range all {
		if sc.name == name {
			return []acceptanceSmokeCase{sc}, nil
		}
	}
	return nil, fmt.Errorf("unknown smoke case %q", name)
}

func waitSmokeCase(c smokeClient, taskID string, sc acceptanceSmokeCase, timeout, poll time.Duration, parkNegative bool) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, reason, err := smokeCaseObserved(c, taskID, sc)
		if err != nil {
			return err
		}
		if ok {
			fmt.Printf("%s  observed  %s: %s\n", taskID, sc.name, reason)
			if sc.negative && parkNegative {
				if err := parkSmokeTask(c, taskID, reason); err != nil {
					return err
				}
				fmt.Printf("%s  parked\n", taskID)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s on task %s; latest: %s", sc.name, taskID, reason)
		}
		time.Sleep(poll)
	}
}

type smokeClient interface {
	TasksGet(context.Context, string) (map[string]any, error)
	TaskDiagnose(context.Context, string) (*model.TaskDiagnosis, error)
	DispatchPause(context.Context) error
	DispatchResume(context.Context) error
	AgentCancel(context.Context, string) error
	Signal(context.Context, string, string, string) error
}

func smokeCaseObserved(c smokeClient, taskID string, sc acceptanceSmokeCase) (bool, string, error) {
	diag, err := c.TaskDiagnose(okCtx(), taskID)
	if err != nil {
		return false, "", err
	}
	detail, err := c.TasksGet(okCtx(), taskID)
	if err != nil {
		return false, "", err
	}
	verdict := stringOrEmpty(detail, "verification_verdict")
	status, step, validation := "", "", ""
	if diag.Task != nil {
		status = diag.Task.Status
		step = diag.Task.CurrentStep
	}
	if diag.Observed.LatestValidation != nil {
		validation = diag.Observed.LatestValidation.Reason
	}
	switch sc.name {
	case "pass":
		if verdict == "pass" && (status == "merged" || status == "done") {
			return true, fmt.Sprintf("verdict=%s status=%s", verdict, status), nil
		}
	case "verification-fail":
		if verdict == "fail" && step == "implement" {
			return true, "failing verification report routed back to implement", nil
		}
	case "done-bundle-reject":
		if step == "implement" && strings.Contains(validation, "missing required artifact") {
			return true, validation, nil
		}
	case "verification-report-reject":
		if step == "acceptance" && strings.Contains(validation, "content_hash required") {
			return true, validation, nil
		}
	}
	return false, fmt.Sprintf("status=%s step=%s verdict=%s validation=%q", status, step, verdict, validation), nil
}

func parkSmokeTask(c smokeClient, taskID, reason string) error {
	if err := c.DispatchPause(okCtx()); err != nil {
		return err
	}
	defer c.DispatchResume(okCtx())

	diag, err := c.TaskDiagnose(okCtx(), taskID)
	if err != nil {
		return err
	}
	if diag.Observed.Agent != nil && diag.Observed.Agent.Status == "running" {
		if err := c.AgentCancel(okCtx(), diag.Observed.Agent.ID); err != nil {
			return err
		}
	}
	return c.Signal(okCtx(), "blocked", taskID, "acceptance smoke observation captured: "+reason)
}

func printAcceptanceJSON(detail map[string]any, taskID string) error {
	out := map[string]any{
		"task_id":              taskID,
		"acceptance_spec":      jsonOrNull(detail, "acceptance_spec"),
		"done_bundle":          jsonOrNull(detail, "done_bundle"),
		"verification_report":  jsonOrNull(detail, "verification_report"),
		"verification_verdict": stringOrEmpty(detail, "verification_verdict"),
	}
	return printJSON(out)
}

func jsonOrNull(m map[string]any, key string) any {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	return v
}

func stringOrEmpty(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func printAcceptanceHuman(detail map[string]any, taskID string) error {
	fmt.Printf("Task: %s\n\n", taskID)

	spec, hasSpec := detail["acceptance_spec"]
	if hasSpec && spec != nil {
		fmt.Println("== Acceptance Spec ==")
		printSpecHuman(spec)
		fmt.Println()
	} else {
		fmt.Println("== Acceptance Spec ==")
		fmt.Println("  (none)")
		fmt.Println()
	}

	bundle, hasBundle := detail["done_bundle"]
	if hasBundle && bundle != nil {
		fmt.Println("== Done Bundle ==")
		printBundleHuman(bundle)
		fmt.Println()
	} else {
		fmt.Println("== Done Bundle ==")
		fmt.Println("  (none)")
		fmt.Println()
	}

	verdict := stringOrEmpty(detail, "verification_verdict")
	report, hasReport := detail["verification_report"]
	if hasReport && report != nil {
		fmt.Println("== Verification ==")
		if verdict != "" {
			fmt.Printf("  Verdict: %s\n", verdict)
		}
		printReportHuman(report)
		fmt.Println()
	} else {
		fmt.Println("== Verification ==")
		if verdict != "" {
			fmt.Printf("  Verdict: %s\n", verdict)
		} else {
			fmt.Println("  (none)")
		}
		fmt.Println()
	}

	return nil
}

func printSpecHuman(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	criteria, ok := m["criteria"].([]any)
	if !ok || len(criteria) == 0 {
		fmt.Println("  Criteria: (none)")
		return
	}
	fmt.Println("  Criteria:")
	for _, c := range criteria {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		id, _ := cm["id"].(string)
		desc, _ := cm["description"].(string)
		fmt.Printf("    %s: %s\n", id, desc)
		if probes, ok := cm["probes"].([]any); ok && len(probes) > 0 {
			fmt.Println("      Probes:")
			printProbeSlice(probes)
		}
		if artifacts, ok := cm["required_artifacts"].([]any); ok && len(artifacts) > 0 {
			fmt.Print("      Required artifacts: ")
			printStringSlice(artifacts)
		}
		if failIf, ok := cm["fail_if"].([]any); ok && len(failIf) > 0 {
			fmt.Print("      Fail if: ")
			printStringSlice(failIf)
		}
	}
}

func printBundleHuman(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if summary, ok := m["summary"].(string); ok && summary != "" {
		fmt.Printf("  Summary: %s\n", summary)
	}
	if claims, ok := m["claims"].([]any); ok && len(claims) > 0 {
		fmt.Println("  Claims:")
		for _, c := range claims {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			cid, _ := cm["criterion_id"].(string)
			status, _ := cm["status"].(string)
			fmt.Printf("    %s: %s\n", cid, status)
		}
	}
	if artifacts, ok := m["artifacts"].([]any); ok && len(artifacts) > 0 {
		fmt.Println("  Artifacts:")
		for _, a := range artifacts {
			am, ok := a.(map[string]any)
			if !ok {
				continue
			}
			atype, _ := am["type"].(string)
			apath, _ := am["path"].(string)
			fmt.Printf("    %s: %s\n", atype, apath)
		}
	}
	if tests, ok := m["tests_run"].([]any); ok && len(tests) > 0 {
		fmt.Println("  Tests run:")
		for _, t := range tests {
			if s, ok := t.(string); ok {
				fmt.Printf("    %s\n", s)
			}
		}
	}
	if risks, ok := m["known_risks"].([]any); ok && len(risks) > 0 {
		fmt.Println("  Known risks:")
		for _, r := range risks {
			if s, ok := r.(string); ok {
				fmt.Printf("    %s\n", s)
			}
		}
	}
}

func printReportHuman(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if confidence, ok := m["confidence"].(string); ok && confidence != "" {
		fmt.Printf("  Agent confidence: %s\n", confidence)
	}
	if computed, ok := m["computed_confidence"].(float64); ok && computed > 0 {
		label, _ := m["confidence_label"].(string)
		if label != "" {
			fmt.Printf("  Computed confidence: %.2f (%s)\n", computed, label)
		} else {
			fmt.Printf("  Computed confidence: %.2f\n", computed)
		}
	} else if label, ok := m["confidence_label"].(string); ok && label != "" {
		fmt.Printf("  Computed confidence: %s\n", label)
	}
	if results, ok := m["results"].([]any); ok && len(results) > 0 {
		fmt.Println("  Results:")
		for _, r := range results {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			cid, _ := rm["criterion_id"].(string)
			status, _ := rm["status"].(string)
			reason, _ := rm["reason"].(string)
			fmt.Printf("    %s: %s", cid, status)
			if reason != "" {
				fmt.Printf(" - %s", reason)
			}
			fmt.Println()
			if evidence, ok := rm["evidence"].([]any); ok && len(evidence) > 0 {
				for _, e := range evidence {
					em, ok := e.(map[string]any)
					if !ok {
						continue
					}
					etype, _ := em["type"].(string)
					epath, _ := em["path"].(string)
					if epath != "" {
						fmt.Printf("      [%s] %s\n", etype, epath)
					} else {
						fmt.Printf("      [%s]\n", etype)
					}
				}
			}
		}
	}
	if failures, ok := m["failures"].([]any); ok && len(failures) > 0 {
		fmt.Println("  Failures:")
		for _, f := range failures {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			reason, _ := fm["reason"].(string)
			cid, _ := fm["criterion_id"].(string)
			if cid != "" {
				fmt.Printf("    %s: %s\n", cid, reason)
			} else {
				fmt.Printf("    %s\n", reason)
			}
		}
	}
}

func printStringSlice(items []any) {
	for i, item := range items {
		if s, ok := item.(string); ok {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Print(s)
		}
	}
	fmt.Println()
}

func printProbeSlice(items []any) {
	for _, item := range items {
		if s, ok := item.(string); ok {
			fmt.Printf("        - %s\n", s)
			continue
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		desc, _ := m["description"].(string)
		switch {
		case id != "" && desc != "":
			fmt.Printf("        - %s: %s\n", id, desc)
		case id != "":
			fmt.Printf("        - %s\n", id)
		case desc != "":
			fmt.Printf("        - %s\n", desc)
		}
	}
}
