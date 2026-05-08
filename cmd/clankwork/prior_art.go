package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func priorArtCmd() *cli.Command {
	return &cli.Command{
		Name:  "prior-art",
		Usage: "Search task-history prior art for planner use",
		Commands: []*cli.Command{
			{
				Name:      "search",
				Usage:     "Search prior task histories",
				ArgsUsage: "<query>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "repo", Usage: "Repo ID filter"},
					&cli.StringFlag{Name: "template", Usage: "Template filter"},
					&cli.StringFlag{Name: "status", Usage: "Status filter"},
					&cli.FloatFlag{Name: "min-rework-score", Usage: "Minimum rework score"},
					&cli.FloatFlag{Name: "min-risk-score", Usage: "Minimum risk score"},
					&cli.IntFlag{Name: "limit", Value: 10, Usage: "Maximum results"},
					&cli.StringFlag{Name: "format", Usage: "Output format: human (default) or json"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					resp, err := c.PriorArtSearch(okCtx(), model.PriorArtSearchRequest{
						Query:          strings.Join(cmd.Args().Slice(), " "),
						RepoID:         cmd.String("repo"),
						Template:       cmd.String("template"),
						Status:         cmd.String("status"),
						MinReworkScore: cmd.Float("min-rework-score"),
						MinRiskScore:   cmd.Float("min-risk-score"),
						Limit:          cmd.Int("limit"),
					})
					if err != nil {
						return err
					}
					if cmd.String("format") == "json" {
						return printJSON(resp)
					}
					if len(resp.Results) == 0 {
						fmt.Println("no prior art found")
						return nil
					}
					for _, r := range resp.Results {
						fmt.Printf("%s  status=%s  rework=%.0f  risk=%.0f\n", r.TaskID, r.Status, r.ReworkScore, r.RiskScore)
						fmt.Printf("  %s\n", r.Title)
						fmt.Printf("  why: %s\n", r.MatchedReason)
						for _, lesson := range r.KeyLessons {
							fmt.Printf("  lesson: %s\n", lesson)
						}
						if r.Summary != "" {
							fmt.Printf("  summary: %s\n", r.Summary)
						}
						fmt.Println()
					}
					return nil
				},
			},
			{
				Name:      "show",
				Usage:     "Show indexed history for a task",
				ArgsUsage: "<task-id>",
				Flags:     []cli.Flag{&cli.StringFlag{Name: "format", Usage: "Output format: human (default) or json"}},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if cmd.Args().Len() == 0 {
						return fmt.Errorf("usage: clankwork prior-art show <task-id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					h, err := c.PriorArtShow(okCtx(), cmd.Args().First())
					if err != nil {
						return err
					}
					if cmd.String("format") == "json" {
						return printJSON(h)
					}
					printPriorArtHistory(h)
					return nil
				},
			},
			{
				Name:  "rebuild",
				Usage: "Rebuild the prior-art index from existing tasks, traces, and artifacts",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					count, err := c.PriorArtRebuild(okCtx())
					if err != nil {
						return err
					}
					fmt.Printf("%d task histories indexed\n", count)
					return nil
				},
			},
		},
	}
}

func printPriorArtHistory(h *model.PriorArtHistory) {
	fmt.Printf("# %s\n\n", h.Title)
	fmt.Printf("Task: `%s`\nStatus: `%s`\nRework score: %.0f\nRisk score: %.0f\n\n", h.TaskID, h.Status, h.ReworkScore, h.RiskScore)
	if h.Summary != "" {
		fmt.Printf("## Summary\n\n%s\n\n", h.Summary)
	}
	if len(h.Tags) > 0 {
		fmt.Printf("Tags: `%s`\n\n", strings.Join(h.Tags, "`, `"))
	}
	if h.Metadata != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(h.Metadata), &meta); err == nil {
			for _, section := range []struct{ key, title string }{
				{"acceptance", "Original Acceptance Spec"},
				{"trace_outcomes", "Failure/Retry History"},
				{"verification", "Final Verification Report"},
				{"merge_outcome", "Merge Outcome"},
				{"artifacts", "Useful Evidence Artifacts"},
			} {
				if val, ok := meta[section.key]; ok && val != nil {
					b, _ := json.MarshalIndent(val, "", "  ")
					fmt.Printf("## %s\n\n```json\n%s\n```\n\n", section.title, string(b))
				}
			}
		}
	}
}
