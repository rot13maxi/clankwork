package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
)

func planCmd() *cli.Command {
	return &cli.Command{
		Name:  "plan",
		Usage: "Manage plans",
		Commands: []*cli.Command{
			{
				Name:      "create",
				Usage:     "Register a plan from a markdown file",
				ArgsUsage: "<file>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "title", Usage: "Override plan title (default: filename)"},
					&cli.BoolFlag{Name: "with-prior-art", Usage: "Append relevant prior-art context for planner use"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork plan create <file>")
					}
					path := args[0]
					body, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					title := cmd.String("title")
					if title == "" {
						title = path
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					plan, err := c.PlansCreateWithOptions(okCtx(), title, string(body), cmd.Bool("with-prior-art"))
					if err != nil {
						return err
					}
					fmt.Printf("%s  created\n", plan.ID)
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "List all plans",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					plans, err := c.PlansList(okCtx())
					if err != nil {
						return err
					}
					if len(plans) == 0 {
						fmt.Println("no plans")
						return nil
					}
					fmt.Printf("%-28s  %-10s  %s\n", "ID", "STATUS", "TITLE")
					for _, p := range plans {
						fmt.Printf("%-28s  %-10s  %s\n", p.ID, p.Status, p.Title)
					}
					return nil
				},
			},
			{
				Name:      "show",
				Usage:     "Show plan details",
				ArgsUsage: "<id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork plan show <id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					detail, err := c.PlansGet(okCtx(), args[0])
					if err != nil {
						return err
					}
					return printJSON(detail)
				},
			},
		},
	}
}
