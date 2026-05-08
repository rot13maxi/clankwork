package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"
)

func repoCmd() *cli.Command {
	return &cli.Command{
		Name:  "repo",
		Usage: "Manage git repositories",
		Commands: []*cli.Command{
			{
				Name:      "add",
				Usage:     "Register a git repository",
				ArgsUsage: "<path>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "name", Usage: "Short name for this repo (default: directory name)"},
					&cli.StringFlag{Name: "branch", Value: "main", Usage: "Target branch"},
					&cli.StringFlag{Name: "verify-command", Usage: "Shell command to run after rebase to verify correctness"},
					&cli.StringFlag{Name: "lint-command", Usage: "Shell command to run linter (e.g. 'golangci-lint run ./...')"},
					&cli.StringFlag{Name: "typecheck-command", Usage: "Shell command to run type checker (e.g. 'go build ./...')"},
					&cli.BoolFlag{Name: "auto-push", Usage: "Push target branch to origin after merge"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork repo add <path>")
					}
					absPath, err := filepath.Abs(args[0])
					if err != nil {
						return err
					}
					name := cmd.String("name")
					if name == "" {
						name = filepath.Base(absPath)
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					repo, err := c.ReposCreate(okCtx(), name, absPath, cmd.String("branch"),
						cmd.String("verify-command"), cmd.String("lint-command"),
						cmd.String("typecheck-command"), cmd.Bool("auto-push"))
					if err != nil {
						return err
					}
					fmt.Printf("%s  %s  (%s)\n", repo.ID, repo.Name, repo.Path)
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "List registered repositories",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					repos, err := c.ReposList(okCtx())
					if err != nil {
						return err
					}
					if len(repos) == 0 {
						fmt.Println("no repos registered")
						return nil
					}
					fmt.Printf("%-28s  %-16s  %-8s  %s\n", "ID", "NAME", "BRANCH", "PATH")
					for _, r := range repos {
						fmt.Printf("%-28s  %-16s  %-8s  %s\n", r.ID, r.Name, r.TargetBranch, r.Path)
					}
					return nil
				},
			},
			{
				Name:      "prune",
				Usage:     "Delete merged clankwork/* branches from a registered repo",
				ArgsUsage: "[repo-id]",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "dry-run", Usage: "Print what would be deleted without deleting"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}

					var repoPath, targetBranch string

					args := cmd.Args().Slice()
					if len(args) > 0 {
						repo, err := c.RepoGet(okCtx(), args[0])
						if err != nil {
							return err
						}
						repoPath = repo.Path
						targetBranch = repo.TargetBranch
					} else {
						repos, err := c.ReposList(okCtx())
						if err != nil {
							return err
						}
						if len(repos) == 0 {
							return fmt.Errorf("no repos registered")
						}
						if len(repos) > 1 {
							return fmt.Errorf("multiple repos registered; specify a repo-id")
						}
						repoPath = repos[0].Path
						targetBranch = repos[0].TargetBranch
					}

					out, err := exec.Command("git", "-C", repoPath, "branch", "--merged", targetBranch).Output()
					if err != nil {
						return fmt.Errorf("git branch --merged: %w", err)
					}

					var merged []string
					scanner := bufio.NewScanner(bytes.NewReader(out))
					for scanner.Scan() {
						branch := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "*"))
						if strings.HasPrefix(branch, "clankwork/") {
							merged = append(merged, branch)
						}
					}

					dryRun := cmd.Bool("dry-run")
					pruned := 0
					for _, branch := range merged {
						if dryRun {
							fmt.Printf("would delete %s\n", branch)
						} else {
							if err := exec.Command("git", "-C", repoPath, "branch", "-d", branch).Run(); err != nil {
								fmt.Printf("failed to delete %s: %v\n", branch, err)
								continue
							}
							fmt.Printf("deleted %s\n", branch)
						}
						pruned++
					}

					if dryRun {
						fmt.Printf("would prune %d branch(es)\n", pruned)
					} else {
						fmt.Printf("pruned %d branch(es)\n", pruned)
					}
					return nil
				},
			},
		},
	}
}
