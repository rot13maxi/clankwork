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

func TestM2Integration(t *testing.T) {
	// Require tmux.
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found; skipping integration test")
	}

	homeDir := t.TempDir()
	logDir := filepath.Join(homeDir, "logs")
	os.MkdirAll(logDir, 0700)
	os.MkdirAll(filepath.Join(homeDir, "worktrees"), 0700)

	// Initialize a temp git repo with one commit.
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

	// Open store and boot server.
	st, err := store.Open(filepath.Join(homeDir, "clankwork.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Build clankwork binary so the stub agent can call it.
	binPath := filepath.Join(homeDir, "clankwork")
	if out, err := exec.Command("go", "build", "-o", binPath, "github.com/rot13maxi/clankwork/cmd/clankwork").CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}

	cfg := config.DefaultConfig()
	cfg.Scheduler.TickSec = 1
	// Stub agent: call the binary we just built.
	cfg.Runtimes["default"] = config.RuntimeConfig{
		Command: binPath,
		Args:    []string{"signal", "done"},
	}

	spawner := &worker.TmuxSpawner{LogDir: logDir}
	wtCreator := &worker.GitWorktreeCreator{HomeDir: homeDir}
	disp := scheduler.New(context.Background(), st, spawner, wtCreator, homeDir, cfg)
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

	// Register repo.
	repo, err := c.ReposCreate(context.Background(), "test-repo", repoDir, "main", "", "", "", false)
	if err != nil {
		t.Fatalf("ReposCreate: %v", err)
	}

	// Create task.
	task, err := c.TasksCreate(context.Background(), model.CreateTaskRequest{
		RepoID:  repo.ID,
		Title:   "Stub task",
		Runtime: "default",
	})
	if err != nil {
		t.Fatalf("TasksCreate: %v", err)
	}

	// Start scheduler and reconciler goroutines.
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

	// Wait up to 15s for task to reach done.
	deadline = time.Now().Add(15 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		detail, err := c.TasksGet(context.Background(), task.ID)
		if err == nil {
			finalStatus, _ = detail["status"].(string)
			if finalStatus == "done" {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	if finalStatus != "done" {
		t.Errorf("task status = %q, want done (after 15s)", finalStatus)
	}
}
