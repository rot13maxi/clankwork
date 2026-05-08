package mergequeue

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// rebaseResult holds the output of a rebase attempt.
type rebaseResult struct {
	ConflictLog string // non-empty if rebase conflicted
	RebasedSHA  string // tip SHA after successful rebase
	TargetSHA   string // SHA of refs/heads/<target> at time of rebase (used for CAS)
}

// fetchAndRebase fetches origin/<target> (if a remote exists) and rebases the
// worktree branch onto LOCAL refs/heads/<target>. The fetch is informational
// only — the merge queue always advances LOCAL <target>, and an optional
// auto_push step publishes it. Pinning the rebase to local lets the queue
// make progress when local is ahead of origin (e.g., commits made before
// push); pinning it to origin would loop forever in that state because the
// CAS in advanceTarget targets local.
//
// On success, returns the rebased tip SHA and the local target SHA used for
// compare-and-swap. On conflict, returns a non-empty ConflictLog (the rebase
// is aborted before return).
func fetchAndRebase(worktreePath, repoPath, target string, timeout time.Duration) (rebaseResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if hasOriginRemote(ctx, repoPath) {
		fetchCmd := exec.CommandContext(ctx, "git", "-C", repoPath, "fetch", "origin", target)
		if out, err := fetchCmd.CombinedOutput(); err != nil {
			return rebaseResult{}, fmt.Errorf("git fetch: %w\n%s", err, out)
		}
	}

	rebaseTarget := "refs/heads/" + target

	// Record the local target SHA for compare-and-swap in advanceTarget.
	targetSHA, err := gitRevParse(ctx, repoPath, rebaseTarget)
	if err != nil {
		return rebaseResult{}, fmt.Errorf("rev-parse %s: %w", rebaseTarget, err)
	}

	// Rebase the worktree branch onto the target ref.
	rebaseCmd := exec.CommandContext(ctx, "git", "rebase", rebaseTarget)
	rebaseCmd.Dir = worktreePath
	out, rebaseErr := rebaseCmd.CombinedOutput()
	if rebaseErr != nil {
		// Capture conflict info before aborting.
		statusOut, _ := exec.Command("git", "-C", worktreePath, "status", "--short").Output()
		conflictLog := strings.TrimSpace(string(out)) + "\n" + strings.TrimSpace(string(statusOut))
		// Abort the rebase so the branch is restored to pre-rebase state.
		exec.Command("git", "-C", worktreePath, "rebase", "--abort").Run() //nolint:errcheck
		return rebaseResult{ConflictLog: conflictLog, TargetSHA: targetSHA}, nil
	}

	// Get the rebased tip SHA.
	rebasedSHA, err := gitRevParse(ctx, worktreePath, "HEAD")
	if err != nil {
		return rebaseResult{}, fmt.Errorf("rev-parse HEAD: %w", err)
	}

	return rebaseResult{RebasedSHA: rebasedSHA, TargetSHA: targetSHA}, nil
}

// runVerify executes the verify command inside the worktree with a timeout.
// Returns the combined output (for failure_log) and any error.
func runVerify(worktreePath, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("verify failed: %w", err)
	}
	return string(out), nil
}

// advanceTarget fast-forwards the target branch to newSHA using compare-and-swap.
// expectedOldSHA must match the current tip of refs/heads/<target>; if it has
// advanced (e.g. from an external push), the update fails and the item is re-queued.
func advanceTarget(repoPath, target, newSHA, expectedOldSHA string) error {
	targetCheckedOut := checkedOutBranch(repoPath) == target
	if targetCheckedOut {
		clean, err := gitWorktreeClean(repoPath)
		if err != nil {
			return err
		}
		if !clean {
			return fmt.Errorf("target worktree %s is dirty; refusing to advance checked-out branch", repoPath)
		}
	}
	cmd := exec.Command("git", "-C", repoPath,
		"update-ref", "refs/heads/"+target, newSHA, expectedOldSHA)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("update-ref: %w\n%s", err, out)
	}
	if targetCheckedOut {
		if err := syncCheckedOutTarget(repoPath, target); err != nil {
			return err
		}
	}
	return nil
}

func checkedOutBranch(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitWorktreeClean(repoPath string) (bool, error) {
	out, err := exec.Command("git", "-C", repoPath, "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w\n%s", err, out)
	}
	return strings.TrimSpace(string(out)) == "", nil
}

func syncCheckedOutTarget(repoPath, target string) error {
	cmd := exec.Command("git", "-C", repoPath, "reset", "--hard", "refs/heads/"+target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git reset --hard %s: %w\n%s", target, err, out)
	}
	return nil
}

// pushTarget pushes the local target branch to origin.
func pushTarget(repoPath, target string) error {
	cmd := exec.Command("git", "-C", repoPath, "push", "origin", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push: %w\n%s", err, out)
	}
	return nil
}

// createMergeWorktree creates a temporary worktree from the task branch.
func createMergeWorktree(repoPath, taskID, branch, homeDir string) (string, error) {
	// Remove any existing scheduler worktree for this task, then prune stale metadata.
	// The scheduler creates worktrees at <homeDir>/worktrees/<taskID> and cleans them up
	// asynchronously, so they may still be registered when the merge queue starts.
	schedulerWorktree := filepath.Join(homeDir, "worktrees", taskID)
	exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", schedulerWorktree).Run() //nolint:errcheck
	exec.Command("git", "-C", repoPath, "worktree", "prune").Run()                                //nolint:errcheck

	worktreePath := filepath.Join(homeDir, "merging", taskID)
	cmd := exec.Command("git", "-C", repoPath, "worktree", "add", worktreePath, branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("worktree add: %w\n%s", err, out)
	}
	return worktreePath, nil
}

// removeMergeWorktree removes the temporary merge worktree.
func removeMergeWorktree(repoPath, worktreePath string) error {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("worktree remove: %w\n%s", err, out)
	}
	return nil
}

// deleteBranch force-deletes the task branch after a successful merge.
func deleteBranch(repoPath, branch string) error {
	cmd := exec.Command("git", "-C", repoPath, "branch", "-D", branch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("branch -D: %w\n%s", err, out)
	}
	return nil
}

// pruneWorktrees runs git worktree prune on the repo to clean up stale metadata.
func pruneWorktrees(repoPath string) {
	exec.Command("git", "-C", repoPath, "worktree", "prune").Run() //nolint:errcheck
}

// isMergedInto checks whether branch is already an ancestor of target (merge-base check).
// Used during startup recovery to detect items where update-ref ran but status wasn't set.
func isMergedInto(repoPath, branch, target string) bool {
	cmd := exec.Command("git", "-C", repoPath, "merge-base", "--is-ancestor", branch, target)
	return cmd.Run() == nil
}

// hasOriginRemote returns true if the repo has an 'origin' remote configured.
func hasOriginRemote(ctx context.Context, repoPath string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "remote", "get-url", "origin")
	return cmd.Run() == nil
}

func gitRevParse(ctx context.Context, dir, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", ref)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}
