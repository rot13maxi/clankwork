package main

import (
	"context"
	"fmt"
	"os"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func learningCmd() *cli.Command {
	return &cli.Command{
		Name:  "learning",
		Usage: "Deprecated. Use 'clankwork prior-art ...' for planner retrieval; legacy learnings tables remain only for compatibility.",
		Commands: []*cli.Command{
			{
				Name:      "add",
				Usage:     "Deprecated: submit a legacy learning from a markdown file. Prefer the prior-art index.",
				ArgsUsage: "<file>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "category", Value: "general", Usage: "Category tag"},
					&cli.StringFlag{Name: "title", Usage: "Override title (default: filename)"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork learning add <file>")
					}
					body, err := os.ReadFile(args[0])
					if err != nil {
						return err
					}
					title := cmd.String("title")
					if title == "" {
						title = args[0]
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					l, err := c.LearningsAdd(okCtx(), cmd.String("category"), title, string(body))
					if err != nil {
						return err
					}
					fmt.Printf("%s  created\n", l.ID)
					return nil
				},
			},
			{
				Name:      "candidate-add",
				Usage:     "Deprecated: submit a candidate legacy learning. Prefer the prior-art index.",
				ArgsUsage: "<file>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "source-trace", Required: true, Usage: "Source trace ID"},
					&cli.StringFlag{Name: "reason", Required: true, Usage: "Why this learning is candidate-only"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork learning candidate-add <file>")
					}
					body, err := os.ReadFile(args[0])
					if err != nil {
						return err
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					candidate, err := c.CandidateLearningAdd(okCtx(), model.AddCandidateLearningRequest{
						SourceTraceID:    cmd.String("source-trace"),
						ProposedLearning: string(body),
						Reason:           cmd.String("reason"),
					})
					if err != nil {
						return err
					}
					fmt.Printf("%s  candidate\n", candidate.ID)
					return nil
				},
			},
			{
				Name:  "candidate-list",
				Usage: "Deprecated: list candidate legacy learnings.",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "status", Value: "candidate", Usage: "Candidate status filter"},
					&cli.StringFlag{Name: "format", Usage: "Output format: human (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					candidates, err := c.CandidateLearningList(okCtx(), cmd.String("status"))
					if err != nil {
						return err
					}
					if cmd.String("format") == "json" {
						return printJSON(candidates)
					}
					for _, candidate := range candidates {
						fmt.Printf("%s  %s  %s\n", candidate.ID, candidate.Status, candidate.Reason)
					}
					return nil
				},
			},
		},
	}
}
