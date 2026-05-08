package main

import (
	"context"
	"fmt"
	"os"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func bootstrapCmd() *cli.Command {
	return &cli.Command{
		Name:  "bootstrap",
		Usage: "Load agent context (auto-detects planning vs worker from CLANKWORK_TASK_ID)",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:  "format",
				Value: "text",
				Usage: "Output format: text (default, rich markdown) or json (worker mode only)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}

			taskID := os.Getenv("CLANKWORK_TASK_ID")
			if taskID == "" {
				// No task in environment — planning mode.
				return printPlanningBootstrap(c)
			}

			role := os.Getenv("CLANKWORK_ROLE")
			repoID := os.Getenv("CLANKWORK_REPO_ID")

			resp, err := c.Bootstrap(okCtx(), taskID, role, repoID)
			if err != nil {
				return err
			}
			if cmd.String("format") == "json" {
				return printJSON(resp)
			}
			printWorkerBootstrap(resp)
			return nil
		},
	}
}

func printPlanningBootstrap(c interface {
	Status(context.Context) (*model.StatusResponse, error)
	ReposList(context.Context) ([]*model.Repo, error)
	PlansList(context.Context) ([]*model.Plan, error)
	TasksList(context.Context, string, string, []string) ([]*model.Task, error)
}) error {
	ctx := okCtx()
	status, err := c.Status(ctx)
	if err != nil {
		return err
	}
	repos, _ := c.ReposList(ctx)
	plans, _ := c.PlansList(ctx)
	pending, _ := c.TasksList(ctx, "", "", []string{"pending"})
	running, _ := c.TasksList(ctx, "", "", []string{"running"})
	blocked, _ := c.TasksList(ctx, "", "", []string{"blocked"})

	fmt.Print("# Clankwork Planning Agent\n\n")
	fmt.Print("You are a planning agent for Clankwork — an automated software factory.\n")
	fmt.Print("Your job: understand what the user wants to build, decompose it into tasks, and dispatch them.\n")
	fmt.Print("You do not implement code. You create plans and tasks; the system dispatches worker agents to execute them.\n\n")

	fmt.Print("---\n\n## System State\n\n")
	fmt.Printf("**Tasks**: %d total — %d pending, %d running, %d done, %d failed, %d blocked\n",
		status.Tasks.Total, status.Tasks.Pending, status.Tasks.Running,
		status.Tasks.Done, status.Tasks.Failed, status.Tasks.Blocked)
	fmt.Printf("**Agents**: %d running\n", status.Agents.Running)
	fmt.Printf("**Merge queue**: %d queued\n\n", status.MergeQueue.Queued)

	if len(repos) > 0 {
		fmt.Print("### Registered Repos\n\n")
		for _, r := range repos {
			fmt.Printf("- `%s` — **%s** (`%s`) target: `%s`\n", r.ID, r.Name, r.Path, r.TargetBranch)
		}
		fmt.Println()
	}

	if len(plans) > 0 {
		fmt.Print("### Active Plans\n\n")
		for _, p := range plans {
			if p.Status == "active" {
				fmt.Printf("- `%s` — %s\n", p.ID, p.Title)
			}
		}
		fmt.Println()
	}

	if len(pending) > 0 {
		fmt.Print("### Pending Tasks\n\n")
		for _, t := range pending {
			fmt.Printf("- `%s` — %s\n", t.ID, t.Title)
		}
		fmt.Println()
	}

	if len(running) > 0 {
		fmt.Print("### Running Tasks\n\n")
		for _, t := range running {
			fmt.Printf("- `%s` — %s (step: %s, retries: %d)\n", t.ID, t.Title, t.CurrentStep, t.RetryCount)
		}
		fmt.Println()
	}

	if len(blocked) > 0 {
		fmt.Print("### Blocked Tasks (need your attention)\n\n")
		for _, t := range blocked {
			fmt.Printf("- `%s` — %s\n", t.ID, t.Title)
		}
		fmt.Println()
	}

	fmt.Print("---\n\n## How To Dispatch Work\n\n")
	fmt.Print("### 1. Create a plan (optional, groups related tasks)\n\n")
	fmt.Print("```\nclankwork plan create --title \"My plan\" /path/to/plan.md\n```\n\n")
	fmt.Print("### 2. Create tasks\n\n")
	fmt.Print("```\nclankwork task create \\\n  --title \"Add feature X\" \\\n  --body /path/to/task-body.md \\\n  --repo <repo-id> \\\n  --plan <plan-id> \\\n  --template feature\n```\n\n")
	fmt.Print("Available templates: `feature` (implement→test→acceptance), `bugfix` (implement→test), `refactor` (implement→test), `simple` (implement only)\n\n")
	fmt.Print("### 3. Monitor\n\n```\n")
	fmt.Print("clankwork status                        -- system overview\n")
	fmt.Print("clankwork task list                     -- all tasks\n")
	fmt.Print("clankwork task list --status blocked    -- tasks waiting on you\n")
	fmt.Print("clankwork task show <task-id>           -- task details + traces\n")
	fmt.Print("clankwork traces list --task <task-id>  -- execution trace\n```\n\n")

	fmt.Print("---\n\n## Key CLI Commands\n\n```\n")
	fmt.Print("clankwork plan create [--title T] <file>                  -- register a plan\n")
	fmt.Print("clankwork plan list                                        -- list plans\n")
	fmt.Print("clankwork task create [options]                            -- create a task\n")
	fmt.Print("clankwork task list [--status S] [--plan P] [--repo R]    -- list tasks\n")
	fmt.Print("clankwork task show <id>                                   -- task details\n")
	fmt.Print("clankwork task add-dep <task-id> <depends-on-id>          -- declare dependency\n")
	fmt.Print("clankwork task set-priority <task-id> <n>                 -- reprioritize\n")
	fmt.Print("clankwork status                                           -- system overview\n")
	fmt.Print("clankwork repo list                                        -- list repos\n")
	fmt.Print("clankwork agents list                                      -- list agents\n")
	fmt.Print("clankwork traces list [--task T] [--type E] [--outcome O] -- execution traces\n")
	fmt.Print("clankwork prior-art search <query>                         -- search task-history prior art\n")
	fmt.Print("clankwork dispatch pause / resume                          -- pause/resume dispatch\n")
	fmt.Print("clankwork queue list                                       -- merge queue state\n```\n\n")

	fmt.Print("---\n\n## Principles\n\n")
	fmt.Print("- **Decompose aggressively.** Small, independent tasks parallelize better than large monolithic ones.\n")
	fmt.Print("- **Use the right template.** `simple` for one-shot changes; `bugfix`/`refactor` for changes needing tests; `feature` for new functionality needing acceptance testing.\n")
	fmt.Print("- **Dependencies matter.** Use `task add-dep` when task B needs task A's output.\n")
	fmt.Print("- **Check blocked tasks first.** They're waiting on you.\n")
	fmt.Print("- **Use prior art deliberately.** Search task histories before decomposition, then encode useful lessons as fresh acceptance criteria or probes.\n")
	return nil
}

func printWorkerBootstrap(b *model.BootstrapResponse) {
	fmt.Print("# Clankwork Worker Agent\n\n")

	if b.Task != nil {
		fmt.Printf("You are a worker agent tasked with: **%s**\n\n", b.Task.Title)
		if b.Task.Template != "" {
			step := b.Task.CurrentStep
			if step == "" {
				step = "entry"
			}
			fmt.Printf("**Workflow**: `%s` template, current step: `%s`\n", b.Task.Template, step)
		}
		if b.Task.RetryCount > 0 {
			fmt.Printf("**Retry**: attempt %d\n", b.Task.RetryCount+1)
		}
	}
	if b.Repo != nil {
		fmt.Printf("**Repo**: %s at `%s` (target branch: `%s`)\n", b.Repo.Name, b.Repo.Path, b.Repo.TargetBranch)
	}
	if cwd, err := os.Getwd(); err == nil {
		fmt.Printf("**Task worktree**: `%s`\n", cwd)
		fmt.Print("Use paths relative to this task worktree for all reads and writes. Do not edit the registered repo checkout path directly.\n")
	}
	fmt.Println()

	if b.Role != "" && b.RoleBody != "" {
		fmt.Printf("---\n\n## Your Role: %s\n\n%s\n\n", b.Role, b.RoleBody)
	}

	if b.Task != nil && b.Task.Body != "" {
		fmt.Printf("---\n\n## Task\n\n%s\n\n", b.Task.Body)
	}

	if b.FailureContext != "" {
		fmt.Printf("---\n\n## Previous Attempt — What Went Wrong\n\n%s\n\n", b.FailureContext)
	}

	if b.AcceptanceSpec != nil {
		fmt.Print("---\n\n## Accepted Acceptance Spec\n\n")
		fmt.Print("Use these exact criterion IDs and probe IDs in completion claims and verification evidence. Do not invent alternate IDs.\n\n")
		for _, c := range b.AcceptanceSpec.Criteria {
			fmt.Printf("- `%s`: %s\n", c.ID, c.Description)
			if len(c.Probes) > 0 {
				fmt.Print("  Probes:")
				for _, p := range c.Probes {
					fmt.Printf(" `%s`", p.ID)
				}
				fmt.Println()
				for _, p := range c.Probes {
					if len(p.RequiredEvidence) == 0 {
						continue
					}
					fmt.Printf("    `%s` required evidence:", p.ID)
					for _, ev := range p.RequiredEvidence {
						fmt.Printf(" `%s`", ev)
					}
					fmt.Println()
				}
			}
			if len(c.RequiredArtifacts) > 0 {
				fmt.Print("  Required artifact types:")
				for _, a := range c.RequiredArtifacts {
					fmt.Printf(" `%s`", a)
				}
				fmt.Println()
			}
		}
		fmt.Println()
	}

	fmt.Print("---\n\n## Workflow\n\n")
	fmt.Print("1. Do the work described in the task above.\n")
	fmt.Print("2. Commit your changes to the current branch (never to the target branch directly).\n")
	if b.LintCommand != "" || b.TypecheckCommand != "" {
		fmt.Print("3. Before signalling done, run:\n")
		if b.LintCommand != "" {
			fmt.Printf("   - `clankwork verify lint` (%s)\n", b.LintCommand)
		}
		if b.TypecheckCommand != "" {
			fmt.Printf("   - `clankwork verify typecheck` (%s)\n", b.TypecheckCommand)
		}
		fmt.Print("   - `clankwork verify` (full test suite)\n")
		fmt.Print("4. Signal when complete.\n")
	} else {
		fmt.Print("3. Run `clankwork verify` to check your work.\n")
		fmt.Print("4. Signal when complete.\n")
	}
	fmt.Println()

	fmt.Print("---\n\n## Signals\n\n```\n")
	fmt.Print("clankwork signal started              -- call this first\n")
	fmt.Print("clankwork signal progress \"<msg>\"    -- heartbeat (call periodically)\n")
	fmt.Print("clankwork signal done                 -- work complete, tests pass\n")
	fmt.Print("clankwork signal failed \"<reason>\"   -- you're stuck, explain why\n")
	fmt.Print("clankwork signal blocked \"<question>\"-- you need human input\n")
	fmt.Print("```\n\n")

	if len(b.CLIReference) > 0 {
		fmt.Print("---\n\n## Other Useful Commands\n\n```\n")
		for _, line := range b.CLIReference {
			fmt.Printf("%s\n", line)
		}
		fmt.Print("```\n\n")
	}

	fmt.Print("---\nWork autonomously. Signal `started` now, then get to work. Signal `done` when tests pass.\n")
}

func contextCmd() *cli.Command {
	return &cli.Command{
		Name:      "context",
		Usage:     "Get task/plan context",
		ArgsUsage: "<task-id>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) == 0 {
				return fmt.Errorf("usage: clankwork context <task-id>")
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			detail, err := c.ContextGet(okCtx(), args[0])
			if err != nil {
				return err
			}
			return printJSON(detail)
		},
	}
}
