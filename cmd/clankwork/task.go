package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func taskCmd() *cli.Command {
	return &cli.Command{
		Name:  "task",
		Usage: "Manage tasks",
		Commands: []*cli.Command{
			{
				Name:  "create",
				Usage: "Create a task",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "plan", Usage: "Plan ID"},
					&cli.StringFlag{Name: "repo", Usage: "Repo ID"},
					&cli.StringFlag{Name: "title", Required: true, Usage: "Task title"},
					&cli.StringFlag{Name: "body", Usage: "Path to body file, or - for stdin"},
					&cli.StringFlag{Name: "template", Usage: "Workflow template name"},
					&cli.StringFlag{Name: "role", Usage: "Role name for the agent"},
					&cli.StringFlag{Name: "runtime", Usage: "Runtime name (e.g. default, claude)"},
					&cli.IntFlag{Name: "priority", Value: 0, Usage: "Priority (higher = sooner)"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					var body string
					if bodyFile := cmd.String("body"); bodyFile != "" {
						if bodyFile == "-" {
							data, err := io.ReadAll(os.Stdin)
							if err != nil {
								return err
							}
							body = string(data)
						} else {
							data, err := os.ReadFile(bodyFile)
							if err != nil {
								return err
							}
							body = string(data)
						}
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					task, err := c.TasksCreate(okCtx(), model.CreateTaskRequest{
						PlanID:   cmd.String("plan"),
						RepoID:   cmd.String("repo"),
						Title:    cmd.String("title"),
						Body:     body,
						Template: cmd.String("template"),
						Role:     cmd.String("role"),
						Runtime:  cmd.String("runtime"),
						Priority: cmd.Int("priority"),
					})
					if err != nil {
						return err
					}
					fmt.Printf("%s  created\n", task.ID)
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "List tasks",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "plan", Usage: "Filter by plan ID"},
					&cli.StringFlag{Name: "repo", Usage: "Filter by repo ID"},
					&cli.StringFlag{Name: "status", Usage: "Filter by status (comma-separated: pending,running,done,failed,blocked,merged)"},
					&cli.StringFlag{Name: "format", Value: "table", Usage: "Output format: table (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					var statuses []string
					if statusParam := cmd.String("status"); statusParam != "" {
						statuses = strings.Split(statusParam, ",")
					}
					tasks, err := c.TasksList(okCtx(), cmd.String("plan"), cmd.String("repo"), statuses)
					if err != nil {
						return err
					}
					format := cmd.String("format")
					if format == "json" {
						if tasks == nil {
							tasks = []*model.Task{}
						}
						return printJSON(tasks)
					}
					if len(tasks) == 0 {
						fmt.Println("no tasks")
						return nil
					}
					fmt.Printf("%-20s  %-28s  %-10s  %-4s  %s\n", "NAME", "ID", "STATUS", "PRI", "TITLE")
					for _, t := range tasks {
						name := t.Name
						if name == "" {
							name = "-"
						}
						fmt.Printf("%-20s  %-28s  %-10s  %-4d  %s\n", name, t.ID, t.Status, t.Priority, t.Title)
					}
					return nil
				},
			},
			{
				Name:      "show",
				Usage:     "Show task details",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "format", Value: "table", Usage: "Output format: table (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork task show <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					detail, err := c.TasksGet(okCtx(), args[0])
					if err != nil {
						return err
					}
					format := cmd.String("format")
					if format == "json" {
						return printJSON(detail)
					}
					return printTaskTable(detail)
				},
			},
			{
				Name:      "retry",
				Usage:     "Retry a failed task",
				ArgsUsage: "<id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork task retry <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					taskID := args[0]
					if err := c.TasksRetry(okCtx(), taskID); err != nil {
						return err
					}
					fmt.Printf("retrying task %s\n", taskID)
					return nil
				},
			},
			{
				Name:      "close",
				Usage:     "Close/archive a task that is no longer actionable",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "outcome", Required: true, Usage: "obsolete|superseded|expected_failure|manual_abandon"},
					&cli.StringFlag{Name: "reason", Required: true, Usage: "Reason for closing the task"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork task close <id> --outcome <outcome> --reason <reason>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.TaskClose(okCtx(), model.CloseTaskRequest{
						TaskID:  args[0],
						Outcome: cmd.String("outcome"),
						Reason:  cmd.String("reason"),
					}); err != nil {
						return err
					}
					fmt.Printf("%s closed as %s\n", args[0], cmd.String("outcome"))
					return nil
				},
			},
			{
				Name:      "unblock",
				Usage:     "Unblock a blocked task and clear oscillation metadata",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "step", Usage: "Optional step to continue from"},
					&cli.StringFlag{Name: "reason", Required: true, Usage: "Reason for manual unblock"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork task unblock <id> --reason <reason>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					taskID := args[0]
					if err := c.TaskUnblock(okCtx(), taskID, cmd.String("step"), cmd.String("reason")); err != nil {
						return err
					}
					if cmd.String("step") != "" {
						fmt.Printf("%s unblocked with step %s\n", taskID, cmd.String("step"))
					} else {
						fmt.Printf("%s unblocked\n", taskID)
					}
					return nil
				},
			},
			{
				Name:      "diagnose",
				Usage:     "Explain why a task is or is not progressing",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "format", Value: "table", Usage: "Output format: table (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork task diagnose <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					diag, err := c.TaskDiagnose(okCtx(), args[0])
					if err != nil {
						return err
					}
					if cmd.String("format") == "json" {
						return printJSON(diag)
					}
					printDiagnosis(diag)
					return nil
				},
			},
			{
				Name:      "retry-step",
				Usage:     "Retry the current or specified task step",
				ArgsUsage: "<id> [step]",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork task retry-step <id> [step]")
					}
					step := ""
					if len(args) > 1 {
						step = args[1]
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.TaskRetryStep(okCtx(), args[0], step); err != nil {
						return err
					}
					fmt.Printf("%s step queued for retry\n", args[0])
					return nil
				},
			},
			{
				Name:      "reset-step",
				Usage:     "Reset a task to a workflow step",
				ArgsUsage: "<id> <step>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) < 2 {
						return fmt.Errorf("usage: clankwork task reset-step <id> <step>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.TaskResetStep(okCtx(), args[0], args[1]); err != nil {
						return err
					}
					fmt.Printf("%s reset to step %s\n", args[0], args[1])
					return nil
				},
			},
			{
				Name:      "escalate",
				Usage:     "Escalate a task to a target system",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "target-type", Required: true, Usage: "human|ticketing|parent_controller|runtime_operator|policy_engine|external_system"},
					&cli.StringFlag{Name: "target-ref", Usage: "Optional target reference"},
					&cli.StringFlag{Name: "requested-action", Value: "investigate", Usage: "Requested action"},
					&cli.StringFlag{Name: "step", Usage: "Workflow step related to escalation"},
					&cli.StringFlag{Name: "reason", Required: true, Usage: "Escalation reason"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork task escalate <id> --target-type <type> --reason <reason>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					esc, err := c.TaskEscalate(okCtx(), model.TaskEscalateRequest{
						TaskID:          args[0],
						StepName:        cmd.String("step"),
						TargetType:      cmd.String("target-type"),
						TargetRef:       cmd.String("target-ref"),
						RequestedAction: cmd.String("requested-action"),
						Reason:          cmd.String("reason"),
					})
					if err != nil {
						return err
					}
					fmt.Printf("%s escalated to %s (%s)\n", args[0], esc.TargetType, esc.ID)
					return nil
				},
			},
			{
				Name:      "add-dep",
				Usage:     "Declare a dependency between tasks",
				ArgsUsage: "<task-id> <depends-on-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) < 2 {
						return fmt.Errorf("usage: clankwork task add-dep <task-id> <depends-on-id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.TasksAddDep(okCtx(), args[0], args[1]); err != nil {
						return err
					}
					fmt.Printf("%s depends on %s\n", args[0], args[1])
					return nil
				},
			},
			{
				Name:      "set-priority",
				Usage:     "Set task priority",
				ArgsUsage: "<task-id> <priority>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) < 2 {
						return fmt.Errorf("usage: clankwork task set-priority <task-id> <n>")
					}
					priority, err := strconv.Atoi(args[1])
					if err != nil {
						return fmt.Errorf("priority must be an integer")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.TasksSetPriority(okCtx(), args[0], priority); err != nil {
						return err
					}
					fmt.Printf("%s priority set to %d\n", args[0], priority)
					return nil
				},
			},
		},
	}
}

func printDiagnosis(diag *model.TaskDiagnosis) {
	if diag == nil || diag.Task == nil {
		fmt.Println("no diagnosis")
		return
	}
	printField("Task", diag.Task.ID)
	printField("Name", diag.Task.Name)
	printField("Status", diag.Task.Status)
	printField("Current Step", diag.Task.CurrentStep)
	printField("Target", diag.Desired.TargetStatus)
	printField("Next Action", diag.NextAction)
	printField("Reason", diag.Reason)
	if diag.Observed.Agent != nil {
		fmt.Println()
		fmt.Println("Agent:")
		printField("  ID", diag.Observed.Agent.ID)
		printField("  Status", diag.Observed.Agent.Status)
		printField("  Runtime", diag.Observed.Agent.Runtime)
		printField("  Worktree", diag.Observed.Agent.WorktreePath)
	}
	if diag.Observed.LatestValidation != nil {
		fmt.Println()
		fmt.Println("Latest Validation:")
		printField("  Status", diag.Observed.LatestValidation.Status)
		printField("  Reason", diag.Observed.LatestValidation.Reason)
	}
	if diag.Observed.LatestObservation != nil && (diag.Observed.LatestValidation == nil || diag.Observed.LatestObservation.ID != diag.Observed.LatestValidation.ID) {
		fmt.Println()
		fmt.Println("Latest Observation:")
		printField("  Kind", diag.Observed.LatestObservation.Kind)
		printField("  Status", diag.Observed.LatestObservation.Status)
		printField("  Reason", diag.Observed.LatestObservation.Reason)
	}
	if diag.LatestDecision != nil {
		fmt.Println()
		fmt.Println("Latest Decision:")
		printField("  Controller", diag.LatestDecision.Controller)
		printField("  Action", diag.LatestDecision.Action)
		printField("  Reason", diag.LatestDecision.Reason)
	}
	if len(diag.Observed.OpenEscalations) > 0 {
		fmt.Println()
		fmt.Println("Open Escalations:")
		for _, esc := range diag.Observed.OpenEscalations {
			sig := esc.FailureSignature
			if sig == "" {
				sig = "-"
			}
			fmt.Printf("  %-28s  sig=%-16s  %s\n", esc.ID, sig, esc.Reason)
			if len(esc.SuggestedCommands) > 0 {
				for _, cmd := range esc.SuggestedCommands {
					fmt.Printf("    > %s\n", cmd)
				}
			}
		}
	}
}

// printTaskTable renders a task detail map in a human-readable table.
func printTaskTable(detail map[string]any) error {
	printField("ID", toString(detail["id"]))
	printField("Name", toString(detail["name"]))
	printField("Title", toString(detail["title"]))
	printField("Status", toString(detail["status"]))
	printField("Priority", fmt.Sprintf("%v", getField(detail, "priority")))
	printField("Template", toString(detail["template"]))
	printField("Role", toString(detail["role"]))
	printField("Runtime", toString(detail["runtime"]))
	printField("Current Step", toString(detail["current_step"]))
	printField("Retry Count", fmt.Sprintf("%v", getField(detail, "retry_count")))
	if body, ok := detail["body"]; ok && body != nil {
		if bodyStr, ok := body.(string); ok && bodyStr != "" {
			fmt.Printf("\nBody:\n%s\n", bodyStr)
		}
	}
	return nil
}

func printField(label, value string) {
	if value != "" {
		fmt.Printf("%-14s %s\n", label+":", value)
	}
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func getField(m map[string]any, key string) any {
	return m[key]
}
