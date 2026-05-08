package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

func queueCmd() *cli.Command {
	return &cli.Command{
		Name:  "queue",
		Usage: "Inspect the merge queue",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List merge queue items",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					items, err := c.QueueList(okCtx())
					if err != nil {
						return err
					}
					if len(items) == 0 {
						fmt.Println("merge queue is empty")
						return nil
					}
					fmt.Printf("%-26s  %-10s  %-26s  %-20s  %s\n", "ID", "STATUS", "TASK", "BRANCH", "TARGET")
					for _, item := range items {
						fmt.Printf("%-26s  %-10s  %-26s  %-20s  %s\n",
							item.ID, item.Status, item.TaskID, item.Branch, item.Target)
					}
					return nil
				},
			},
			{
				Name:      "skip",
				Usage:     "Reject (skip) a queued merge queue item",
				ArgsUsage: "<id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork queue skip <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.QueueSkip(okCtx(), args[0]); err != nil {
						return err
					}
					fmt.Printf("item %s rejected\n", args[0])
					return nil
				},
			},
			{
				Name:      "retry",
				Usage:     "Re-queue a failed merge queue item",
				ArgsUsage: "<id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork queue retry <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.QueueRetry(okCtx(), args[0]); err != nil {
						return err
					}
					fmt.Printf("item %s queued\n", args[0])
					return nil
				},
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			// Default action: list
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			items, err := c.QueueList(okCtx())
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Println("merge queue is empty")
				return nil
			}
			fmt.Printf("%-26s  %-10s  %-26s  %-20s  %s\n", "ID", "STATUS", "TASK", "BRANCH", "TARGET")
			for _, item := range items {
				fmt.Printf("%-26s  %-10s  %-26s  %-20s  %s\n",
					item.ID, item.Status, item.TaskID, item.Branch, item.Target)
			}
			return nil
		},
	}
}
