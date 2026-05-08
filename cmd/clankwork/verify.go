package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func verifyCmd() *cli.Command {
	return &cli.Command{
		Name:  "verify",
		Usage: "Run repo verification commands (test, lint, typecheck)",
		Description: `Looks up the current repo's configured commands and executes them.
The repo is identified by CLANKWORK_REPO_ID (set by the control plane when
spawning agents) or by matching the current working directory against
registered repos.

Subcommands:
  clankwork verify           — run the repo's verify (test) command
  clankwork verify test      — alias for clankwork verify (runs verify command)
  clankwork verify lint      — run the repo's lint command
  clankwork verify typecheck — run the repo's typecheck command`,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runRepoCommand(cmd, "verify")
		},
		Commands: []*cli.Command{
			{
				Name:  "test",
				Usage: "Run the repo's configured verify (test) command",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return runRepoCommand(cmd, "verify")
				},
			},
			{
				Name:  "lint",
				Usage: "Run the repo's configured lint command",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return runRepoCommand(cmd, "lint")
				},
			},
			{
				Name:  "typecheck",
				Usage: "Run the repo's configured typecheck command",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return runRepoCommand(cmd, "typecheck")
				},
			},
		},
	}
}

// runRepoCommand resolves the repo and runs the specified command type.
// commandType is one of "verify", "lint", or "typecheck".
func runRepoCommand(cmd *cli.Command, commandType string) error {
	c, err := newClient(cmd)
	if err != nil {
		return err
	}

	repo, err := resolveRepo(c)
	if err != nil {
		return err
	}

	var command string
	var label string
	switch commandType {
	case "verify":
		command = repo.VerifyCommand
		label = "verify"
	case "lint":
		command = repo.LintCommand
		label = "lint"
	case "typecheck":
		command = repo.TypecheckCommand
		label = "typecheck"
	default:
		return fmt.Errorf("unknown command type %q", commandType)
	}

	if command == "" {
		if commandType == "verify" {
			fmt.Fprintf(os.Stderr, "warning: repo %q has no verify command configured, falling back to `go test ./...`\n", repo.Name)
			command = "go test ./..."
		} else {
			fmt.Fprintf(os.Stderr, "error: repo %q has no %s command configured (set lint_command in repo config)\n", repo.Name, label)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "verify %s: running %q\n", label, command)

	shellCmd := exec.Command("sh", "-c", command)
	shellCmd.Stdout = os.Stdout
	shellCmd.Stderr = os.Stderr
	shellCmd.Stdin = os.Stdin

	err = shellCmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				os.Exit(status.ExitStatus())
			}
		}
		os.Exit(1)
	}
	return nil
}

// resolveRepo finds the repo by CLANKWORK_REPO_ID or by matching the cwd.
func resolveRepo(c interface {
	ReposList(ctx context.Context) ([]*model.Repo, error)
}) (*model.Repo, error) {
	repoID := os.Getenv("CLANKWORK_REPO_ID")
	repos, err := c.ReposList(okCtx())
	if err != nil {
		return nil, fmt.Errorf("failed to list repos: %w", err)
	}

	if repoID != "" {
		for _, r := range repos {
			if r.ID == repoID {
				return r, nil
			}
		}
		return nil, fmt.Errorf("repo %s not found", repoID)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot determine working directory: %w", err)
	}
	cwd, _ = filepath.EvalSymlinks(cwd)

	for _, r := range repos {
		repoPath, _ := filepath.EvalSymlinks(r.Path)
		if repoPath == "" {
			repoPath = r.Path
		}
		if cwd == repoPath || strings.HasPrefix(cwd, repoPath+string(os.PathSeparator)) {
			return r, nil
		}
	}
	return nil, fmt.Errorf("current directory %s does not match any registered repo (set CLANKWORK_REPO_ID or cd into a repo)", cwd)
}
