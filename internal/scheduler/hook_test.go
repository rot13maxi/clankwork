package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/store"
	"github.com/rot13maxi/clankwork/internal/worker"
)

func TestBuildPreCommitHook_BothCommands(t *testing.T) {
	hook := BuildPreCommitHook("golangci-lint run ./...", "go build ./...")

	if !strings.Contains(hook, "#!/bin/sh") {
		t.Error("hook should start with shebang")
	}
	if !strings.Contains(hook, "golangci-lint run ./...") {
		t.Error("hook should contain lint command")
	}
	if !strings.Contains(hook, "go build ./...") {
		t.Error("hook should contain typecheck command")
	}
	if !strings.Contains(hook, "running lint") {
		t.Error("hook should have lint label")
	}
	if !strings.Contains(hook, "running typecheck") {
		t.Error("hook should have typecheck label")
	}
}

func TestBuildPreCommitHook_LintOnly(t *testing.T) {
	hook := BuildPreCommitHook("eslint .", "")

	if !strings.Contains(hook, "eslint .") {
		t.Error("hook should contain lint command")
	}
	if strings.Contains(hook, "running typecheck") {
		t.Error("hook should not contain typecheck section when not configured")
	}
}

func TestBuildPreCommitHook_TypecheckOnly(t *testing.T) {
	hook := BuildPreCommitHook("", "tsc --noEmit")

	if strings.Contains(hook, "running lint") {
		t.Error("hook should not contain lint section when not configured")
	}
	if !strings.Contains(hook, "tsc --noEmit") {
		t.Error("hook should contain typecheck command")
	}
}

func TestBuildPreCommitHook_Neither(t *testing.T) {
	hook := BuildPreCommitHook("", "")

	if !strings.Contains(hook, "#!/bin/sh") {
		t.Error("hook should still have shebang")
	}
	if !strings.Contains(hook, "exit 0") {
		t.Error("hook should exit 0")
	}
}

func TestInstallPreCommitHook(t *testing.T) {
	st := newTestHookStore(t)
	ctx := context.Background()

	// Create a repo with lint/typecheck commands.
	st.RepoCreate(ctx, "repo01", "test-repo", "/tmp/test-repo", "main", "", "golangci-lint run", "go build ./...", false)

	// Create a task linked to the repo.
	st.TaskCreate(ctx, "task01", "", "repo01", "Test task", "", "", "", "default", 0)

	// Create a fake worktree directory with a .git file (simulating a worktree).
	worktreeDir := t.TempDir()
	gitDir := filepath.Join(worktreeDir, ".git-actual")
	os.MkdirAll(gitDir, 0755)
	// Write a .git file pointing to the actual gitdir.
	os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0644)

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := config.DefaultConfig()
	homeDir := t.TempDir()

	d := New(ctx, st, spawner, wt, homeDir, cfg)
	d.installPreCommitHook(ctx, worktreeDir, "task01")

	// Check that the hook was created.
	hookPath := filepath.Join(gitDir, "hooks", "pre-commit")
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("pre-commit hook not found: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "golangci-lint run") {
		t.Error("hook should contain lint command")
	}
	if !strings.Contains(content, "go build ./...") {
		t.Error("hook should contain typecheck command")
	}

	// Check it's executable.
	fi, _ := os.Stat(hookPath)
	if fi.Mode()&0100 == 0 {
		t.Error("hook should be executable")
	}
}

func TestInstallPreCommitHook_NoCommands(t *testing.T) {
	st := newTestHookStore(t)
	ctx := context.Background()

	// Create a repo without lint/typecheck commands.
	st.RepoCreate(ctx, "repo01", "bare-repo", "/tmp/bare-repo", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "Test task", "", "", "", "default", 0)

	worktreeDir := t.TempDir()
	gitDir := filepath.Join(worktreeDir, ".git-actual")
	os.MkdirAll(gitDir, 0755)
	os.WriteFile(filepath.Join(worktreeDir, ".git"), []byte("gitdir: "+gitDir+"\n"), 0644)

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := config.DefaultConfig()
	homeDir := t.TempDir()

	d := New(ctx, st, spawner, wt, homeDir, cfg)
	d.installPreCommitHook(ctx, worktreeDir, "task01")

	// Hook should NOT be installed when no commands are configured.
	hookPath := filepath.Join(gitDir, "hooks", "pre-commit")
	if _, err := os.Stat(hookPath); err == nil {
		t.Error("pre-commit hook should not be installed when no lint/typecheck commands configured")
	}
}

func TestWriteAgentInstructions_WithLintTypecheck(t *testing.T) {
	st := newTestHookStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "test-repo", "/tmp/test-repo", "main", "", "eslint .", "tsc --noEmit", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "Test task", "", "", "", "default", 0)

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := config.DefaultConfig()
	homeDir := t.TempDir()

	worktreeDir := t.TempDir()

	d := New(ctx, st, spawner, wt, homeDir, cfg)
	d.writeAgentInstructions(worktreeDir, "task01", "implement")

	data, err := os.ReadFile(filepath.Join(worktreeDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not found: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "clankwork verify lint") {
		t.Error("CLAUDE.md should contain lint verify instruction")
	}
	if !strings.Contains(content, "clankwork verify typecheck") {
		t.Error("CLAUDE.md should contain typecheck verify instruction")
	}
	if !strings.Contains(content, "Continuous Verification") {
		t.Error("CLAUDE.md should contain Continuous Verification section")
	}
}

func TestWriteAgentInstructions_WithoutLintTypecheck(t *testing.T) {
	st := newTestHookStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "bare-repo", "/tmp/bare-repo", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "Test task", "", "", "", "default", 0)

	spawner := &worker.FakeSpawner{}
	wt := &worker.FakeWorktreeCreator{}
	cfg := config.DefaultConfig()
	homeDir := t.TempDir()

	worktreeDir := t.TempDir()

	d := New(ctx, st, spawner, wt, homeDir, cfg)
	d.writeAgentInstructions(worktreeDir, "task01", "implement")

	data, err := os.ReadFile(filepath.Join(worktreeDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not found: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "Continuous Verification") {
		t.Error("CLAUDE.md should NOT contain Continuous Verification section when no commands configured")
	}
}

func TestWriteAgentInstructions_AcceptanceSpecDoesNotTellAgentToCommit(t *testing.T) {
	st := newTestHookStore(t)
	ctx := context.Background()

	st.RepoCreate(ctx, "repo01", "bare-repo", "/tmp/bare-repo", "main", "", "", "", false)
	st.TaskCreate(ctx, "task01", "", "repo01", "Test task", "", "", "", "default", 0)

	d := New(ctx, st, &worker.FakeSpawner{}, &worker.FakeWorktreeCreator{}, t.TempDir(), config.DefaultConfig())
	worktreeDir := t.TempDir()
	d.writeAgentInstructions(worktreeDir, "task01", "acceptance_spec")

	data, err := os.ReadFile(filepath.Join(worktreeDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not found: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "Do not edit source files") {
		t.Fatalf("acceptance_spec instructions should forbid source edits:\n%s", content)
	}
	if !strings.Contains(content, "clankwork signal done --spec artifacts/acceptance-spec.json") {
		t.Fatalf("acceptance_spec instructions should require --spec signaling:\n%s", content)
	}
	if strings.Contains(content, "git add -A") || strings.Contains(content, "git commit") {
		t.Fatalf("acceptance_spec instructions must not tell the agent to commit:\n%s", content)
	}
}

func newTestHookStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
