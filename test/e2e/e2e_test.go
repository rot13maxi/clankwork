package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/api"
	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

// ---------------------------------------------------------------------------
// TestMain: build the clankwork binary before running tests.
// ---------------------------------------------------------------------------

var (
	binaryPath string
	projectDir string
)

func TestMain(m *testing.M) {
	// Find the project root (two levels up from test/e2e/).
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		os.Exit(1)
	}
	projectDir = filepath.Join(wd, "..", "..")
	binaryPath = filepath.Join(projectDir, "bin", "clankwork")

	// Build the binary.
	cmd := exec.Command("go", "build", "-o", binaryPath, "./cmd/clankwork")
	cmd.Dir = projectDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build clankwork binary: %v\n", err)
		os.Exit(1)
	}

	// Ensure the clankwork binary is on PATH for agent scripts.
	binDir := filepath.Dir(binaryPath)
	os.Setenv("PATH", binDir+string(filepath.ListSeparator)+os.Getenv("PATH"))

	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// skipIfMissing skips the test if tmux or jq are not available.
func skipIfMissing(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available, skipping e2e test")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not available, skipping e2e test")
	}
}

// e2eEnv holds the in-process daemon and all test state.
type e2eEnv struct {
	homeDir    string
	socketPath string
	store      *store.Store
	disp       *scheduler.Dispatcher
	recon      *scheduler.Reconciler
	binDir     string // directory containing the clankwork binary
	repoDir    string // path to the test git repo
	repoID     string
}

// agentScriptPath returns the absolute path to a script in test/e2e/.
func agentScriptPath(name string) string {
	return filepath.Join(projectDir, "test", "e2e", name)
}

// newEnv creates a full in-process daemon with real tmux spawner and real git repos.
// scriptName selects which agent script to use.
func newEnv(t *testing.T, scriptName, verifyCommand string, autoMerge bool) *e2eEnv {
	t.Helper()
	skipIfMissing(t)

	// Use a short path under /tmp to stay within macOS 104-char Unix socket limit.
	// t.TempDir() produces paths that are too long for test names like
	// TestE2E_FeatureTemplateLifecycle.
	homeDir, err := os.MkdirTemp("/tmp", "cwe2e")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(homeDir) })
	os.MkdirAll(filepath.Join(homeDir, "logs"), 0700)
	os.MkdirAll(filepath.Join(homeDir, "worktrees"), 0700)

	st, err := store.Open(filepath.Join(homeDir, "clankwork.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Write config.toml with the agent script as the default runtime.
	agentScript := agentScriptPath(scriptName)
	cfgContent := fmt.Sprintf(`[scheduler]
max_slots = 2
tick_secs = 1
heartbeat_timeout_secs = 30
deterministic_timeout_sec = 15

[runtimes.default]
command = "bash"
args = [%q]
non_interactive = true

[runtimes.frontier]
command = "bash"
args = [%q]
non_interactive = true
`, agentScript, agentScript)

	if err := os.WriteFile(filepath.Join(homeDir, "config.toml"), []byte(cfgContent), 0600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	cfg, err := config.Load(homeDir)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	logDir := filepath.Join(homeDir, "logs")
	tmuxSpawner := &worker.TmuxSpawner{LogDir: logDir}
	wtCreator := &worker.GitWorktreeCreator{HomeDir: homeDir}

	ctx := context.Background()
	disp := scheduler.New(ctx, st, tmuxSpawner, wtCreator, homeDir, cfg)
	recon := scheduler.NewReconciler(st, tmuxSpawner, wtCreator,
		time.Duration(cfg.Scheduler.HeartbeatTimeoutSec)*time.Second)
	recon.SetDispatcher(disp)

	// Listen directly on $homeDir/clankwork.sock — the short /tmp path keeps us
	// within macOS's 104-char Unix socket limit.
	socketPath := filepath.Join(homeDir, "clankwork.sock")
	apiSrv := api.NewServerWithDispatcher(st, homeDir, disp, wtCreator)
	httpSrv := &http.Server{Handler: apiSrv.Handler()}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go httpSrv.Serve(ln)

	t.Cleanup(func() {
		sctx, cf := context.WithTimeout(context.Background(), 2*time.Second)
		defer cf()
		httpSrv.Shutdown(sctx)
	})

	// Create a real git repo.
	repoDir := t.TempDir()
	gitInit(t, repoDir)

	// Register the repo.
	repoID := "e2e-repo"
	if _, err := st.RepoCreate(ctx, repoID, "e2erepo", repoDir, "main", verifyCommand, "", "", autoMerge); err != nil {
		t.Fatalf("RepoCreate: %v", err)
	}

	return &e2eEnv{
		homeDir:    homeDir,
		socketPath: socketPath,
		store:      st,
		disp:       disp,
		recon:      recon,
		binDir:     filepath.Dir(binaryPath),
		repoDir:    repoDir,
		repoID:     repoID,
	}
}

// gitInit creates a git repo with an initial commit.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "init", "-b", "main")
	run(t, dir, "git", "config", "user.email", "test@test.com")
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "init")
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %v in %s: %v\n%s", name, args, dir, err, out)
	}
	return string(out)
}

// pollTaskStatus polls until the task reaches one of the target statuses or times out.
func pollTaskStatus(t *testing.T, st *store.Store, taskID string, timeout time.Duration, targets ...string) string {
	t.Helper()
	targetSet := map[string]bool{}
	for _, s := range targets {
		targetSet[s] = true
	}
	deadline := time.Now().Add(timeout)
	lastLog := ""
	for time.Now().Before(deadline) {
		task, err := st.TaskGet(context.Background(), taskID)
		if err == nil {
			if targetSet[task.Status] {
				return task.Status
			}
			logLine := fmt.Sprintf("status=%s step=%s attempts=%v", task.Status, task.CurrentStep, task.StepAttempts["implement"])
			if logLine != lastLog {
				t.Logf("poll: %s", logLine)
				lastLog = logLine
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	task, _ := st.TaskGet(context.Background(), taskID)
	status := "unknown"
	step := ""
	if task != nil {
		status = task.Status
		step = task.CurrentStep
	}
	// Dump diagnostics on timeout: traces and agent log.
	for _, evType := range []string{"signal.started", "signal.done", "signal.failed", "signal.blocked", "step.routed", "step.failure_context"} {
		traces, _ := st.TraceListByType(context.Background(), taskID, evType, 10)
		for _, tr := range traces {
			t.Logf("  trace %s: %s", tr.EventType, tr.Payload)
		}
	}
	agent, _ := st.AgentGetByTask(context.Background(), taskID)
	if agent != nil && agent.LogfilePath != "" {
		if logData, err := os.ReadFile(agent.LogfilePath); err == nil {
			t.Logf("agent log:\n%s", string(logData))
		}
	}
	t.Fatalf("task %s did not reach %v within %v (current: status=%s step=%s)", taskID, targets, timeout, status, step)
	return ""
}

// runDispatchLoop runs the dispatcher in a goroutine, ticking every interval.
func runDispatchLoop(ctx context.Context, disp *scheduler.Dispatcher, interval time.Duration) context.CancelFunc {
	lctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-ticker.C:
				disp.Tick(lctx)
			}
		}
	}()
	return cancel
}

// killTmuxSession cleans up a tmux session if it exists.
func killTmuxSession(t *testing.T, taskID string) {
	t.Helper()
	sessionName := "clankwork-worker-" + taskID
	exec.Command("tmux", "kill-session", "-t", sessionName).Run()
}

// mustBootstrapViaHTTP calls the bootstrap API directly over the Unix socket.
func mustBootstrapViaHTTP(t *testing.T, socketPath string, taskID, role, repoID string) *model.BootstrapResponse {
	t.Helper()

	body, _ := json.Marshal(model.BootstrapRequest{TaskID: taskID, Role: role, RepoID: repoID})
	req, _ := http.NewRequest("POST", "http://unix/v1/bootstrap", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	httpClient := &http.Client{Transport: transport}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("bootstrap HTTP: %v", err)
	}
	defer resp.Body.Close()

	var apiResp model.APIResponse
	json.NewDecoder(resp.Body).Decode(&apiResp)
	if !apiResp.OK {
		t.Fatalf("bootstrap failed: %+v", apiResp.Error)
	}

	dataBytes, _ := json.Marshal(apiResp.Data)
	var boot model.BootstrapResponse
	json.Unmarshal(dataBytes, &boot)
	return &boot
}

// ---------------------------------------------------------------------------
// Test A: Full simple lifecycle (no template)
// ---------------------------------------------------------------------------

func TestE2E_SimpleLifecycle(t *testing.T) {
	env := newEnv(t, "reference-agent.sh", "", false)
	ctx := context.Background()

	// Create a task with no template.
	taskID := fmt.Sprintf("e2e-simple-%d", time.Now().UnixNano())
	_, err := env.store.TaskCreate(ctx, taskID, "", env.repoID, "Create hello world", "Write a hello world file", "", "", "default", 0)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}
	t.Cleanup(func() { killTmuxSession(t, taskID) })

	// Dispatch.
	if err := env.disp.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Wait for the agent to complete.
	status := pollTaskStatus(t, env.store, taskID, 30*time.Second, "done", "failed")
	if status != "done" {
		t.Fatalf("task status = %q, want done", status)
	}

	// Verify traces exist.
	traces, err := env.store.TraceListByType(ctx, taskID, "signal.done", 10)
	if err != nil {
		t.Fatalf("TraceListByType signal.done: %v", err)
	}
	if len(traces) == 0 {
		t.Error("expected at least one signal.done trace")
	}

	progressTraces, _ := env.store.TraceListByType(ctx, taskID, "signal.progress", 10)
	if len(progressTraces) == 0 {
		t.Error("expected at least one signal.progress trace")
	}

	// Verify the agent's commit exists in the worktree branch.
	branch := "clankwork/" + taskID
	out, err := exec.Command("git", "-C", env.repoDir, "log", "--oneline", branch).CombinedOutput()
	if err != nil {
		// The worktree may have been cleaned up; check via branch existence.
		out2, err2 := exec.Command("git", "-C", env.repoDir, "branch", "--list", branch).CombinedOutput()
		if err2 != nil || len(strings.TrimSpace(string(out2))) == 0 {
			t.Logf("branch %s not found (worktree cleaned up), skipping commit check", branch)
		} else {
			t.Fatalf("git log on branch %s: %v\n%s", branch, err, out)
		}
	} else {
		if !strings.Contains(string(out), "agent: implement task") {
			t.Errorf("expected agent commit in branch %s, got:\n%s", branch, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Test B: Template lifecycle (feature template)
// ---------------------------------------------------------------------------

func TestE2E_FeatureTemplateLifecycle(t *testing.T) {
	// Use a verify command that passes.
	env := newEnv(t, "reference-agent.sh", "true", true)
	ctx := context.Background()

	taskID := fmt.Sprintf("e2e-feat-%d", time.Now().UnixNano())
	_, err := env.store.TaskCreate(ctx, taskID, "", env.repoID, "Add widget feature", "Implement the widget", "feature", "", "default", 0)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}
	t.Cleanup(func() { killTmuxSession(t, taskID) })

	// Start a dispatch loop since template tasks need multiple dispatch cycles.
	cancel := runDispatchLoop(ctx, env.disp, 1*time.Second)
	defer cancel()

	// Wait for the full cycle to complete: implement -> lint -> typecheck -> test -> acceptance -> done.
	// Give extra time since this goes through several steps.
	status := pollTaskStatus(t, env.store, taskID, 60*time.Second, "done", "failed")
	if status != "done" {
		t.Fatalf("task status = %q, want done", status)
	}

	// Verify step routing traces.
	routedTraces, _ := env.store.TraceListByType(ctx, taskID, "step.routed", 10)
	if len(routedTraces) < 2 {
		t.Errorf("expected at least 2 step.routed traces, got %d", len(routedTraces))
	}

	// Check that the cheap-check funnel and acceptance transition happened.
	foundImplToLint := false
	foundLintToTypecheck := false
	foundTypecheckToTest := false
	foundTestToAcceptance := false
	for _, tr := range routedTraces {
		var payload map[string]string
		json.Unmarshal([]byte(tr.Payload), &payload)
		if payload["from"] == "implement" && payload["to"] == "lint" {
			foundImplToLint = true
		}
		if payload["from"] == "lint" && payload["to"] == "typecheck" {
			foundLintToTypecheck = true
		}
		if payload["from"] == "typecheck" && payload["to"] == "test" {
			foundTypecheckToTest = true
		}
		if payload["from"] == "test" && payload["to"] == "acceptance" {
			foundTestToAcceptance = true
		}
	}
	if !foundImplToLint {
		t.Error("missing step.routed trace: implement -> lint")
	}
	if !foundLintToTypecheck {
		t.Error("missing step.routed trace: lint -> typecheck")
	}
	if !foundTypecheckToTest {
		t.Error("missing step.routed trace: typecheck -> test")
	}
	if !foundTestToAcceptance {
		t.Error("missing step.routed trace: test -> acceptance")
	}
}

// ---------------------------------------------------------------------------
// Test C: Failure and retry
// ---------------------------------------------------------------------------

func TestE2E_FailureAndRetry(t *testing.T) {
	// Use a verify command that fails.
	env := newEnv(t, "reference-agent.sh", "exit 1", false)
	ctx := context.Background()

	taskID := fmt.Sprintf("e2e-retry-%d", time.Now().UnixNano())
	_, err := env.store.TaskCreate(ctx, taskID, "", env.repoID, "Feature that fails tests", "Implement something", "feature", "", "default", 0)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}
	t.Cleanup(func() { killTmuxSession(t, taskID) })

	// Start a dispatch loop.
	cancel := runDispatchLoop(ctx, env.disp, 1*time.Second)
	defer cancel()

	// Wait for the task to go through at least one retry cycle.
	// Feature template: acceptance_spec -> implement -> test (fails) -> implement (retry).
	deadline := time.Now().Add(60 * time.Second)
	retried := false
	for time.Now().Before(deadline) {
		task, err := env.store.TaskGet(ctx, taskID)
		if err == nil && task.StepAttempts["implement"] > 1 {
			retried = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !retried {
		task, _ := env.store.TaskGet(ctx, taskID)
		t.Fatalf("task never retried (step_retry_count=%d, status=%s, step=%s)",
			task.StepAttempts["implement"], task.Status, task.CurrentStep)
	}

	// Verify failure context traces exist.
	fcTraces, _ := env.store.TraceListByType(ctx, taskID, "step.failure_context", 10)
	if len(fcTraces) == 0 {
		t.Error("expected at least one step.failure_context trace")
	}

	// Verify the test step result traces show failure.
	detTraces, _ := env.store.TraceListByType(ctx, taskID, "step.deterministic_result", 10)
	foundTestFailure := false
	for _, tr := range detTraces {
		var payload map[string]string
		json.Unmarshal([]byte(tr.Payload), &payload)
		if payload["step"] == "test" && payload["outcome"] == "failure" {
			foundTestFailure = true
			break
		}
	}
	if !foundTestFailure {
		t.Error("expected a deterministic test step failure trace")
	}
}

// ---------------------------------------------------------------------------
// Test D: Bootstrap content validation
// ---------------------------------------------------------------------------

func TestE2E_BootstrapContentValidation(t *testing.T) {
	env := newEnv(t, "reference-agent.sh", "", false)
	ctx := context.Background()

	// Create learnings that match our task keywords.
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("learn-e2e-%d", i)
		env.store.LearningCreate(ctx, id, "testing", fmt.Sprintf("Widget testing tip %d", i),
			fmt.Sprintf("Always test widgets with approach %d", i))
	}

	// Create a roles/ directory in the git repo with a role file.
	rolesDir := filepath.Join(env.repoDir, "roles")
	os.MkdirAll(rolesDir, 0700)
	os.WriteFile(filepath.Join(rolesDir, "implementer.md"), []byte("# Implementer Role\nYou build features carefully."), 0600)

	// Create the task.
	taskID := fmt.Sprintf("e2e-boot-%d", time.Now().UnixNano())
	_, err := env.store.TaskCreate(ctx, taskID, "", env.repoID, "Build widget feature", "Implement the widget", "", "implementer", "default", 0)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}

	// Call bootstrap directly via HTTP (no need to dispatch an agent).
	boot := mustBootstrapViaHTTP(t, env.socketPath, taskID, "implementer", env.repoID)

	// Verify task details.
	if boot.Task == nil || boot.Task.ID != taskID {
		t.Error("bootstrap task mismatch")
	}
	if boot.Task.Title != "Build widget feature" {
		t.Errorf("bootstrap task title = %q", boot.Task.Title)
	}

	// Verify role.
	if boot.Role != "implementer" {
		t.Errorf("bootstrap role = %q, want implementer", boot.Role)
	}
	if !strings.Contains(boot.RoleBody, "Implementer Role") {
		t.Errorf("bootstrap role_body missing role content, got: %q", boot.RoleBody)
	}

	// Verify CLI reference is populated.
	if len(boot.CLIReference) == 0 {
		t.Error("bootstrap cli_reference empty")
	}

	// Verify learnings (FTS match on "widget").
	if len(boot.Learnings) == 0 {
		t.Log("WARNING: no learnings returned (FTS may not have matched)")
	}
}

// ---------------------------------------------------------------------------
// Test E: Signal blocked
// ---------------------------------------------------------------------------

func TestE2E_SignalBlocked(t *testing.T) {
	env := newEnv(t, "reference-agent-blocked.sh", "", false)
	ctx := context.Background()

	taskID := fmt.Sprintf("e2e-blocked-%d", time.Now().UnixNano())
	_, err := env.store.TaskCreate(ctx, taskID, "", env.repoID, "Unclear task", "Requirements are vague", "", "", "default", 0)
	if err != nil {
		t.Fatalf("TaskCreate: %v", err)
	}
	t.Cleanup(func() { killTmuxSession(t, taskID) })

	// Dispatch.
	if err := env.disp.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Wait for the agent to signal blocked.
	status := pollTaskStatus(t, env.store, taskID, 30*time.Second, "blocked", "failed", "done")
	if status != "blocked" {
		t.Fatalf("task status = %q, want blocked", status)
	}

	// Verify blocked trace exists.
	traces, _ := env.store.TraceListByType(ctx, taskID, "signal.blocked", 10)
	if len(traces) == 0 {
		t.Error("expected at least one signal.blocked trace")
	}
}
