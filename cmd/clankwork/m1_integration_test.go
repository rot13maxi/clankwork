package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/api"
	"github.com/rot13maxi/clankwork/internal/client"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/store"
)

// startDaemon boots the API server in-process against a temp home dir.
// Returns a client connected to it and a cancel func that shuts it down.
func startDaemon(t *testing.T) (*client.Client, func()) {
	t.Helper()
	homeDir := t.TempDir()

	dbPath := filepath.Join(homeDir, "clankwork.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	socketPath := filepath.Join(homeDir, "clankwork.sock")
	apiSrv := api.NewServer(st, homeDir)
	httpSrv := &http.Server{Handler: apiSrv.Handler()}

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go httpSrv.Serve(ln)

	c := client.New(homeDir)

	// Wait for daemon to be ready.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := c.Health(context.Background()); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel := func() {
		ctx, cf := context.WithTimeout(context.Background(), 2*time.Second)
		defer cf()
		httpSrv.Shutdown(ctx)
		st.Close()
		os.Remove(socketPath)
	}
	return c, cancel
}

func TestM1EndToEnd(t *testing.T) {
	c, shutdown := startDaemon(t)
	defer shutdown()

	ctx := context.Background()

	// -- repo add
	repo, err := c.ReposCreate(ctx, "testrepo", "/tmp/testrepo", "main", "", "", "", false)
	if err != nil {
		t.Fatalf("ReposCreate: %v", err)
	}
	if repo.Name != "testrepo" {
		t.Errorf("repo name = %q", repo.Name)
	}

	repos, err := c.ReposList(ctx)
	if err != nil {
		t.Fatalf("ReposList: %v", err)
	}
	if len(repos) != 1 {
		t.Errorf("repo count = %d, want 1", len(repos))
	}

	// -- plan create
	plan, err := c.PlansCreate(ctx, "Test Plan", "# Test\n\nImplement the thing.")
	if err != nil {
		t.Fatalf("PlansCreate: %v", err)
	}
	if plan.Status != "active" {
		t.Errorf("plan status = %q", plan.Status)
	}

	plans, err := c.PlansList(ctx)
	if err != nil {
		t.Fatalf("PlansList: %v", err)
	}
	if len(plans) != 1 {
		t.Errorf("plan count = %d, want 1", len(plans))
	}

	// -- task create x2
	task1, err := c.TasksCreate(ctx, model.CreateTaskRequest{
		PlanID:   plan.ID,
		RepoID:   repo.ID,
		Title:    "Do the thing",
		Priority: 5,
	})
	if err != nil {
		t.Fatalf("TasksCreate task1: %v", err)
	}
	if task1.Status != "pending" {
		t.Errorf("task1 status = %q", task1.Status)
	}

	task2, err := c.TasksCreate(ctx, model.CreateTaskRequest{
		PlanID: plan.ID,
		RepoID: repo.ID,
		Title:  "Then this",
	})
	if err != nil {
		t.Fatalf("TasksCreate task2: %v", err)
	}

	// -- task add-dep
	if err := c.TasksAddDep(ctx, task2.ID, task1.ID); err != nil {
		t.Fatalf("TasksAddDep: %v", err)
	}

	// -- task set-priority
	if err := c.TasksSetPriority(ctx, task2.ID, 10); err != nil {
		t.Fatalf("TasksSetPriority: %v", err)
	}

	// Verify task list
	tasks, err := c.TasksList(ctx, plan.ID, "", nil)
	if err != nil {
		t.Fatalf("TasksList: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("task count = %d, want 2", len(tasks))
	}

	// -- bootstrap (simulating an agent)
	boot, err := c.Bootstrap(ctx, task1.ID, "implementer", repo.ID)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if boot.Task == nil || boot.Task.ID != task1.ID {
		t.Errorf("bootstrap task mismatch")
	}
	if boot.Repo == nil || boot.Repo.ID != repo.ID {
		t.Errorf("bootstrap repo mismatch")
	}
	if len(boot.CLIReference) == 0 {
		t.Errorf("bootstrap cli_reference empty")
	}

	// -- signals
	if err := c.Signal(ctx, "started", task1.ID, ""); err != nil {
		t.Fatalf("signal started: %v", err)
	}
	if err := c.Signal(ctx, "progress", task1.ID, "halfway there"); err != nil {
		t.Fatalf("signal progress: %v", err)
	}
	if err := c.SignalWithPayload(ctx, "done", model.SignalRequest{TaskID: task1.ID, DoneBundle: testDoneBundle(task1.ID)}); err != nil {
		t.Fatalf("signal done: %v", err)
	}

	// Verify task status after signals.
	detail, err := c.TasksGet(ctx, task1.ID)
	if err != nil {
		t.Fatalf("TasksGet: %v", err)
	}
	status, _ := detail["status"].(string)
	if status != "done" {
		t.Errorf("task1 status = %q, want done", status)
	}

	// Verify trace count (started + progress + done = 3 rows).
	traces, ok := detail["traces"].([]any)
	if !ok || len(traces) != 3 {
		t.Errorf("trace count = %d, want 3", len(traces))
	}

	// -- context
	ctxData, err := c.ContextGet(ctx, task1.ID)
	if err != nil {
		t.Fatalf("ContextGet: %v", err)
	}
	if ctxData["task"] == nil {
		t.Errorf("context missing task")
	}

	// -- learning add
	l, err := c.LearningsAdd(ctx, "testing", "Test patterns", "Always test with real DB.")
	if err != nil {
		t.Fatalf("LearningsAdd: %v", err)
	}
	if l.Category != "testing" {
		t.Errorf("learning category = %q", l.Category)
	}

	// -- status reflects reality
	s, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.Tasks.Total != 2 {
		t.Errorf("status tasks.total = %d, want 2", s.Tasks.Total)
	}
	if s.Tasks.Done != 1 {
		t.Errorf("status tasks.done = %d, want 1", s.Tasks.Done)
	}
	if s.Plans.Active != 1 {
		t.Errorf("status plans.active = %d, want 1", s.Plans.Active)
	}
	if s.Agents.Running != 0 {
		t.Errorf("status agents.running = %d, want 0", s.Agents.Running)
	}

	fmt.Println("M1 integration test passed")
}

func testDoneBundle(taskID string) *model.DoneBundle {
	return &model.DoneBundle{
		TaskID:  taskID,
		Summary: "test completion",
		Claims:  []model.CompletionClaim{{CriterionID: "C1", Status: "satisfied"}},
		Artifacts: []model.CompletionArtifact{{
			Type:          "test_output",
			Path:          "artifacts/test-output.txt",
			ProbeID:       "C1-test",
			ProducerStep:  "implement",
			ProducerRole:  "worker",
			Timestamp:     "2026-05-04T20:00:00Z",
			ContentHash:   "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			Authoritative: true,
		}},
	}
}

func TestTaskListJSONFormat(t *testing.T) {
	c, shutdown := startDaemon(t)
	defer shutdown()

	ctx := context.Background()

	// Create a task.
	_, err := c.TasksCreate(ctx, model.CreateTaskRequest{Title: "JSON test", Priority: 5})
	if err != nil {
		t.Fatalf("TasksCreate: %v", err)
	}

	// Test JSON format via the API (the client returns parsed JSON).
	tasks, err := c.TasksList(ctx, "", "", nil)
	if err != nil {
		t.Fatalf("TasksList: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("task count = %d, want 1", len(tasks))
	}
	if tasks[0].Title != "JSON test" {
		t.Errorf("title = %q, want JSON test", tasks[0].Title)
	}
	if tasks[0].Status != "pending" {
		t.Errorf("status = %q, want pending", tasks[0].Status)
	}
	if tasks[0].Priority != 5 {
		t.Errorf("priority = %d, want 5", tasks[0].Priority)
	}
}

func TestTaskListStatusFilter(t *testing.T) {
	c, shutdown := startDaemon(t)
	defer shutdown()

	ctx := context.Background()

	// Create tasks with different statuses.
	t1, err := c.TasksCreate(ctx, model.CreateTaskRequest{Title: "Pending task"})
	if err != nil {
		t.Fatalf("TasksCreate t1: %v", err)
	}
	t2, err := c.TasksCreate(ctx, model.CreateTaskRequest{Title: "Running task"})
	if err != nil {
		t.Fatalf("TasksCreate t2: %v", err)
	}
	c.Signal(ctx, "started", t2.ID, "")

	// Filter by single status.
	pending, err := c.TasksList(ctx, "", "", []string{"pending"})
	if err != nil {
		t.Fatalf("TasksList pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != t1.ID {
		t.Errorf("pending tasks = %d, want 1 (t1)", len(pending))
	}

	running, err := c.TasksList(ctx, "", "", []string{"running"})
	if err != nil {
		t.Fatalf("TasksList running: %v", err)
	}
	if len(running) != 1 || running[0].ID != t2.ID {
		t.Errorf("running tasks = %d, want 1 (t2)", len(running))
	}

	// Filter by multiple statuses.
	multi, err := c.TasksList(ctx, "", "", []string{"pending", "running"})
	if err != nil {
		t.Fatalf("TasksList multi: %v", err)
	}
	if len(multi) != 2 {
		t.Errorf("multi status tasks = %d, want 2", len(multi))
	}
}

func TestTaskRetry(t *testing.T) {
	c, shutdown := startDaemon(t)
	defer shutdown()

	ctx := context.Background()

	// Create and fail a task.
	task, err := c.TasksCreate(ctx, model.CreateTaskRequest{Title: "Retry me"})
	if err != nil {
		t.Fatalf("TasksCreate: %v", err)
	}
	c.Signal(ctx, "started", task.ID, "")
	c.Signal(ctx, "failed", task.ID, "something broke")

	// Verify task is failed.
	detail, err := c.TasksGet(ctx, task.ID)
	if err != nil {
		t.Fatalf("TasksGet: %v", err)
	}
	if detail["status"] != "failed" {
		t.Fatalf("status = %v, want failed", detail["status"])
	}

	// Retry the task.
	err = c.TasksRetry(ctx, task.ID)
	if err != nil {
		t.Fatalf("TasksRetry: %v", err)
	}

	// Verify task is back to pending.
	detail, err = c.TasksGet(ctx, task.ID)
	if err != nil {
		t.Fatalf("TasksGet: %v", err)
	}
	if detail["status"] != "pending" {
		t.Errorf("status = %v, want pending", detail["status"])
	}

	// Retry a non-failed task should fail.
	task2, err := c.TasksCreate(ctx, model.CreateTaskRequest{Title: "Don't retry"})
	if err != nil {
		t.Fatalf("TasksCreate task2: %v", err)
	}
	err = c.TasksRetry(ctx, task2.ID)
	if err == nil {
		t.Error("expected error retrying non-failed task")
	}
}
