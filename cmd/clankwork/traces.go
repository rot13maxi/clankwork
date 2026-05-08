package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

func tracesCmd() *cli.Command {
	return &cli.Command{
		Name:  "traces",
		Usage: "List execution traces",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "task", Usage: "Filter by task ID"},
			&cli.StringFlag{Name: "type", Usage: "Filter by event type (e.g. signal.done, merge.merged)"},
			&cli.StringFlag{Name: "since", Usage: "Show traces since duration (e.g. 7d, 24h, 30m)"},
			&cli.IntFlag{Name: "limit", Value: 50, Usage: "Max number of results"},
			&cli.StringFlag{Name: "template", Usage: "Filter by task template name (e.g. feature, bugfix)"},
			&cli.StringFlag{Name: "retries", Usage: "Filter by task retry count (e.g. \">2\", \">=5\", \"3\")"},
			&cli.StringFlag{Name: "outcome", Usage: "Filter by task outcome (done, failed, merged, rejected)"},
			&cli.StringFlag{Name: "path", Usage: "Filter traces whose payload matches a file path glob (e.g. \"src/auth/*\")"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			traces, err := c.TracesList(okCtx(),
				cmd.String("task"),
				cmd.String("type"),
				cmd.String("since"),
				cmd.Int("limit"),
				cmd.String("template"),
				cmd.String("retries"),
				cmd.String("outcome"),
				cmd.String("path"),
			)
			if err != nil {
				return err
			}
			if len(traces) == 0 {
				fmt.Println("no traces")
				return nil
			}
			fmt.Printf("%-6s  %-20s  %-28s  %-24s  %s\n", "ID", "EVENT", "TASK", "TIME", "PAYLOAD")
			for _, t := range traces {
				payload := t.Payload
				if len(payload) > 60 {
					payload = payload[:57] + "..."
				}
				fmt.Printf("%-6d  %-20s  %-28s  %-24s  %s\n",
					t.ID,
					t.EventType,
					t.TaskID,
					t.CreatedAt.Format("2006-01-02 15:04:05"),
					payload,
				)
			}
			return nil
		},
	}
}
