package mergequeue

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFetchAndRebase_LocalAheadOfOrigin reproduces the merge queue spin-loop
// bug: when local <target> has unpushed commits, the merge queue must still
// rebase onto and CAS against LOCAL <target> rather than origin/<target>.
// Otherwise the CAS in advanceTarget always fails and the item re-queues
// forever.
func TestFetchAndRebase_LocalAheadOfOrigin(t *testing.T) {
	repoPath, _ := setupRepoWithRemote(t)

	// Local master is now ahead of origin/master by one extra commit.
	commitFile(t, repoPath, "extra.txt", "local-only", "extra commit not pushed")
	localMasterBefore := revParse(t, repoPath, "refs/heads/master")
	originMaster := revParse(t, repoPath, "refs/remotes/origin/master")
	if localMasterBefore == originMaster {
		t.Fatalf("setup failed: local master == origin/master (%s); test requires divergence", localMasterBefore)
	}

	// Create a task branch from local master with one new commit.
	branch := "clankwork/task-aheadtest"
	runGit(t, repoPath, "branch", branch, "refs/heads/master")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoPath, "worktree", "add", wt, branch)
	commitFile(t, wt, "feature.txt", "feature", "task work")

	// Rebase onto local master and grab the SHA used for CAS.
	res, err := fetchAndRebase(wt, repoPath, "master", 30*time.Second)
	if err != nil {
		t.Fatalf("fetchAndRebase: %v", err)
	}
	if res.ConflictLog != "" {
		t.Fatalf("unexpected conflict: %s", res.ConflictLog)
	}
	if res.TargetSHA != localMasterBefore {
		t.Fatalf("TargetSHA = %s, want local master %s (not origin/master %s)", res.TargetSHA, localMasterBefore, originMaster)
	}

	// CAS must succeed against the recorded local target SHA.
	if err := advanceTarget(repoPath, "master", res.RebasedSHA, res.TargetSHA); err != nil {
		t.Fatalf("advanceTarget: %v", err)
	}
	got := revParse(t, repoPath, "refs/heads/master")
	if got != res.RebasedSHA {
		t.Fatalf("master = %s, want rebased SHA %s", got, res.RebasedSHA)
	}
	if status := gitStatus(t, repoPath); status != "" {
		t.Fatalf("status after advanceTarget = %q, want clean", status)
	}
	if contents := readFile(t, filepath.Join(repoPath, "feature.txt")); contents != "feature" {
		t.Fatalf("feature.txt = %q, want feature", contents)
	}
}

// TestFetchAndRebase_NoRemote covers the local-only repo path.
func TestFetchAndRebase_NoRemote(t *testing.T) {
	repoPath := setupBareLocalRepo(t)
	branch := "clankwork/task-localonly"
	runGit(t, repoPath, "branch", branch, "refs/heads/master")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoPath, "worktree", "add", wt, branch)
	commitFile(t, wt, "feature.txt", "feature", "task work")

	master := revParse(t, repoPath, "refs/heads/master")
	res, err := fetchAndRebase(wt, repoPath, "master", 30*time.Second)
	if err != nil {
		t.Fatalf("fetchAndRebase: %v", err)
	}
	if res.TargetSHA != master {
		t.Fatalf("TargetSHA = %s, want %s", res.TargetSHA, master)
	}
	if err := advanceTarget(repoPath, "master", res.RebasedSHA, res.TargetSHA); err != nil {
		t.Fatalf("advanceTarget: %v", err)
	}
}

func TestAdvanceTargetIgnoresUntrackedFilesInCheckedOutTarget(t *testing.T) {
	repoPath := setupBareLocalRepo(t)
	branch := "clankwork/task-untracked"
	runGit(t, repoPath, "branch", branch, "refs/heads/master")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, repoPath, "worktree", "add", wt, branch)
	commitFile(t, wt, "feature.txt", "feature", "task work")

	master := revParse(t, repoPath, "refs/heads/master")
	res, err := fetchAndRebase(wt, repoPath, "master", 30*time.Second)
	if err != nil {
		t.Fatalf("fetchAndRebase: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "scratch.txt"), []byte("local scratch"), 0600); err != nil {
		t.Fatalf("write untracked scratch: %v", err)
	}

	if err := advanceTarget(repoPath, "master", res.RebasedSHA, master); err != nil {
		t.Fatalf("advanceTarget with untracked file: %v", err)
	}
	if got := revParse(t, repoPath, "refs/heads/master"); got != res.RebasedSHA {
		t.Fatalf("master = %s, want rebased SHA %s", got, res.RebasedSHA)
	}
	if contents := readFile(t, filepath.Join(repoPath, "scratch.txt")); contents != "local scratch" {
		t.Fatalf("scratch.txt = %q, want preserved untracked file", contents)
	}
}

// setupRepoWithRemote creates a non-bare local repo, an "origin" bare repo,
// pushes an initial commit, and returns (localPath, originPath).
func setupRepoWithRemote(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	local := filepath.Join(root, "local")

	runGit(t, "", "init", "--bare", "--initial-branch=master", origin)
	runGit(t, "", "init", "--initial-branch=master", local)
	runGit(t, local, "config", "user.email", "test@example.com")
	runGit(t, local, "config", "user.name", "test")
	runGit(t, local, "remote", "add", "origin", origin)
	commitFile(t, local, "README.md", "init", "initial")
	runGit(t, local, "push", "-u", "origin", "master")
	return local, origin
}

func setupBareLocalRepo(t *testing.T) string {
	t.Helper()
	local := filepath.Join(t.TempDir(), "local")
	runGit(t, "", "init", "--initial-branch=master", local)
	runGit(t, local, "config", "user.email", "test@example.com")
	runGit(t, local, "config", "user.name", "test")
	commitFile(t, local, "README.md", "init", "initial")
	return local
}

func commitFile(t *testing.T, repoPath, name, contents, msg string) {
	t.Helper()
	path := filepath.Join(repoPath, name)
	if err := writeFile(path, contents); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	runGit(t, repoPath, "add", name)
	runGit(t, repoPath, "-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-m", msg)
}

func writeFile(path, contents string) error {
	return exec.Command("sh", "-c", "printf %s '"+contents+"' > "+path).Run()
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func revParse(t *testing.T, repoPath, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

func gitStatus(t *testing.T, repoPath string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("cat", path).Output()
	if err != nil {
		t.Fatalf("cat %s: %v", path, err)
	}
	return string(out)
}
