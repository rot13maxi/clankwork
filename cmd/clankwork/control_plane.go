package main

import (
	"context"
	"fmt"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func reconcileCmd() *cli.Command {
	return &cli.Command{
		Name:  "reconcile",
		Usage: "Run control-plane reconciliation",
		Commands: []*cli.Command{
			{
				Name:      "task",
				Usage:     "Reconcile one task",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "format", Value: "table", Usage: "Output format: table (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork reconcile task <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					diag, err := c.ReconcileTask(okCtx(), args[0])
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
				Name:  "all",
				Usage: "Reconcile all eligible control-plane work",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.ReconcileAll(okCtx()); err != nil {
						return err
					}
					fmt.Println("reconciled")
					return nil
				},
			},
		},
	}
}

func refreshCmd() *cli.Command {
	return &cli.Command{
		Name:  "refresh",
		Usage: "Refresh measured control-plane state",
		Commands: []*cli.Command{
			{
				Name:      "task",
				Usage:     "Refresh task state",
				ArgsUsage: "<id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork refresh task <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					diag, err := c.RefreshTask(okCtx(), args[0])
					if err != nil {
						return err
					}
					printDiagnosis(diag)
					return nil
				},
			},
			{
				Name:      "agent",
				Usage:     "Refresh agent observed state",
				ArgsUsage: "<id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork refresh agent <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.RefreshAgent(okCtx(), args[0]); err != nil {
						return err
					}
					fmt.Printf("%s refreshed\n", args[0])
					return nil
				},
			},
			{
				Name:      "worktree",
				Usage:     "Refresh task worktree observed state",
				ArgsUsage: "<task-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork refresh worktree <task-id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.RefreshWorktree(okCtx(), args[0]); err != nil {
						return err
					}
					fmt.Printf("%s worktree refreshed\n", args[0])
					return nil
				},
			},
		},
	}
}

func eventsCmd() *cli.Command {
	return &cli.Command{
		Name:      "events",
		Usage:     "Show control-plane audit events",
		ArgsUsage: "[target]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "task", Usage: "Filter by task ID"},
			&cli.IntFlag{Name: "limit", Value: 50, Usage: "Maximum events"},
			&cli.StringFlag{Name: "format", Value: "table", Usage: "Output format: table (default) or json"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			target := ""
			if args := cmd.Args().Slice(); len(args) > 0 {
				target = args[0]
			}
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			events, err := c.EventsList(okCtx(), target, cmd.String("task"), cmd.Int("limit"))
			if err != nil {
				return err
			}
			if cmd.String("format") == "json" {
				return printJSON(events)
			}
			for _, ev := range events {
				fmt.Printf("%s  %-10s  %-14s  %s\n", ev.CreatedAt.Format(time.RFC3339), ev.Source, ev.Type, ev.Summary)
			}
			return nil
		},
	}
}

func escalationCmd() *cli.Command {
	return &cli.Command{
		Name:  "escalation",
		Usage: "Manage task escalations",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List escalations",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "task", Usage: "Filter by task ID"},
					&cli.StringFlag{Name: "status", Usage: "Filter by status"},
					&cli.StringFlag{Name: "format", Value: "table", Usage: "Output format: table (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					escalations, err := c.EscalationsList(okCtx(), cmd.String("task"), cmd.String("status"))
					if err != nil {
						return err
					}
					if cmd.String("format") == "json" {
						return printJSON(escalations)
					}
					if len(escalations) == 0 {
						fmt.Println("no escalations")
						return nil
					}
					fmt.Printf("%-28s  %-10s  %-18s  %-28s  %-16s  %s\n", "ID", "STATUS", "TARGET", "TASK", "SIG", "REASON")
					for _, esc := range escalations {
						sig := esc.FailureSignature
						if sig == "" {
							sig = "-"
						}
						fmt.Printf("%-28s  %-10s  %-18s  %-28s  %-16s  %s\n", esc.ID, esc.Status, esc.TargetType, esc.TaskID, sig, esc.Reason)
					}
					return nil
				},
			},
			{
				Name:      "show",
				Usage:     "Show escalation details",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "format", Value: "table", Usage: "Output format: table (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork escalation show <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					escalations, err := c.EscalationsList(okCtx(), "", "")
					if err != nil {
						return err
					}
					var found *model.Escalation
					for _, esc := range escalations {
						if esc.ID == args[0] {
							found = esc
							break
						}
					}
					if found == nil {
						return fmt.Errorf("escalation %s not found", args[0])
					}
					if cmd.String("format") == "json" {
						return printJSON(found)
					}
					printField("ID", found.ID)
					printField("Status", found.Status)
					printField("Task", found.TaskID)
					printField("Step", found.StepName)
					printField("Target", found.TargetType)
					printField("Target Ref", found.TargetRef)
					printField("Action", found.RequestedAction)
					printField("Failure Sig", found.FailureSignature)
					printField("Reason", found.Reason)
					if len(found.SuggestedCommands) > 0 {
						fmt.Println()
						fmt.Println("Suggested Commands:")
						for _, cmd := range found.SuggestedCommands {
							fmt.Printf("  %s\n", cmd)
						}
					}
					if found.CreatedByType != "" {
						fmt.Printf("  Created by: %s (%s) at %s\n", found.CreatedByType, found.CreatedByID, found.CreatedAt.Format(time.RFC3339))
					}
					if found.ResolvedAt != nil {
						printField("Resolved At", found.ResolvedAt.Format(time.RFC3339))
						printField("Resolved By", fmt.Sprintf("%s (%s)", found.ResolvedByType, found.ResolvedByID))
						printField("Outcome", found.Outcome)
					}
					return nil
				},
			},
			{
				Name:      "resolve",
				Usage:     "Resolve an escalation",
				ArgsUsage: "<id>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "outcome", Required: true, Usage: "Resolution outcome"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork escalation resolve <id> --outcome <outcome>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.EscalationResolve(okCtx(), args[0], cmd.String("outcome")); err != nil {
						return err
					}
					fmt.Printf("%s resolved\n", args[0])
					return nil
				},
			},
		},
	}
}
