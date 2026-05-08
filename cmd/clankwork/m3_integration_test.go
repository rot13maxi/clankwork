//go:build integration

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/api"
	"github.com/rot13maxi/clankwork/internal/client"
	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/scheduler"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

// TestM3Integration verifies end-to-end template dispatch:
//   - agent step (implement): spawned via tmux, calls `signal done`
//   - deterministic step (test): runs `true` (always succeeds)
//   - task reaches status=done after two steps
func TestM3Integration(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found; skipping M3 integration test")
	}

	homeDir := t.TempDir()
	logDir := filepath.Join(homeDir, "logs")
	os.MkdirAll(logDir, 0700)
	os.MkdirAll(filepath.Join(homeDir, "worktrees"), 0700)

	// Write a custom template: agent step → deterministic step → done.
	tmplDir := filepath.Join(homeDir, "templates")
	os.MkdirAll(tmplDir, 0700)
	customTOML := `
name        = "m3test"
description = "M3 integration test template"
entry       = "implement"

[steps.implement]
type        = "agent"
runtime     = "default"
on_success  = "verify"
on_failure  = "implement"
max_retries = 3

[steps.verify]
type       = "deterministic"
command    = "true"
on_success = "complete"
on_failure = "implement"
`
	os.WriteFile(filepath.Join(tmplDir, "m3test.toml"), []byte(customTOML), 0600)

	// Initialize a temp git repo.
	repoDir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main", repoDir},
		{"-C", repoDir, "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	st, err := store.Open(filepath.Join(homeDir, "clankwork.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Build clankwork binary so the stub agent can call `signal done`.
	binPath := filepath.Join(homeDir, "clankwork")
	if out, err := exec.Command("go", "build", "-o", binPath, "github.com/rot13maxi/clankwork/cmd/clankwork").CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}

	cfg := config.DefaultConfig()
	cfg.Scheduler.TickSec = 1
	cfg.Runtimes["default"] = config.RuntimeConfig{
		Command: binPath,
		Args:    []string{"signal", "done"},
	}

	daemonCtx, daemonCancel := context.WithCancel(context.Background())
	defer daemonCancel()

	spawner := &worker.TmuxSpawner{LogDir: logDir}
	wtCreator := &worker.GitWorktreeCreator{HomeDir: homeDir}
	disp := scheduler.New(daemonCtx, st, spawner, wtCreator, homeDir, cfg)
	recon := scheduler.NewReconciler(st, spawner, wtCreator, 10*time.Minute)

	socketPath := filepath.Join(homeDir, "clankwork.sock")
	apiSrv := api.NewServerWithDispatcher(st, homeDir, disp, wtCreator)
	httpSrv := &http.Server{Handler: apiSrv.Handler()}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	go httpSrv.Serve(ln)
	defer httpSrv.Close()

	c := client.New(homeDir)

	// Wait for socket.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Health(context.Background()); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	repo, err := c.ReposCreate(context.Background(), "test-repo", repoDir, "main", "", "", "", false)
	if err != nil {
		t.Fatalf("ReposCreate: %v", err)
	}

	task, err := c.TasksCreate(context.Background(), model.CreateTaskRequest{
		RepoID:   repo.ID,
		Title:    "M3 template task",
		Template: "m3test",
	})
	if err != nil {
		t.Fatalf("TasksCreate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		tick := time.NewTicker(time.Duration(cfg.Scheduler.TickSec) * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				disp.Tick(ctx)
				recon.Tick(ctx)
			}
		}
	}()

	// Wait up to 20s for task to complete (two steps).
	deadline = time.Now().Add(20 * time.Second)
	var finalStatus string
	var currentStep string
	for time.Now().Before(deadline) {
		detail, err := c.TasksGet(context.Background(), task.ID)
		if err == nil {
			finalStatus, _ = detail["status"].(string)
			if t2, ok := detail["task"].(map[string]any); ok {
				currentStep, _ = t2["current_step"].(string)
			}
			if finalStatus == "done" {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	if finalStatus != "done" {
		t.Errorf("task status = %q (current_step=%q), want done after 20s", finalStatus, currentStep)
	}

	// Verify traces include step routing.
	detail, _ := c.TasksGet(context.Background(), task.ID)
	traces, _ := detail["traces"].([]any)
	hasRouted := false
	for _, tr := range traces {
		if m, ok := tr.(map[string]any); ok {
			if m["event_type"] == "step.routed" {
				hasRouted = true
				break
			}
		}
	}
	if !hasRouted {
		t.Error("no step.routed trace found — template routing may not have run")
	}
}
