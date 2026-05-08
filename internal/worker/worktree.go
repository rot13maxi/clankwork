package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type WorktreeCreator interface {
	Create(repoPath, taskID, targetBranch string) (worktreePath string, err error)
	Remove(worktreePath string) error
}

type GitWorktreeCreator struct {
	HomeDir string
}

func (g *GitWorktreeCreator) Create(repoPath, taskID, targetBranch string) (string, error) {
	// Guard: repo must have at least one commit.
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("repo %q has no commits (git worktree requires at least one commit)", repoPath)
	}

	worktreePath := filepath.Join(g.HomeDir, "worktrees", taskID)
	branch := "clankwork/" + taskID

	// Use -B (create or reset) so re-dispatch works after worktree cleanup leaves the branch intact.
	cmd := exec.Command("git", "-C", repoPath, "worktree", "add", "-B", branch, worktreePath, targetBranch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %w\n%s", err, out)
	}
	return worktreePath, nil
}

func (g *GitWorktreeCreator) Remove(worktreePath string) error {
	out, err := exec.Command("git", "worktree", "remove", "--force", worktreePath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, out)
	}
	return nil
}

// FakeWorktreeCreator records calls for use in unit tests.
type FakeWorktreeCreator struct {
	Created []string
	Removed []string
	Err     error
}

func (f *FakeWorktreeCreator) Create(repoPath, taskID, targetBranch string) (string, error) {
	if f.Err != nil {
		return "", f.Err
	}
	path := filepath.Join(os.TempDir(), "fake-worktree-"+taskID)
	f.Created = append(f.Created, path)
	return path, nil
}

func (f *FakeWorktreeCreator) Remove(worktreePath string) error {
	f.Removed = append(f.Removed, worktreePath)
	return nil
}
