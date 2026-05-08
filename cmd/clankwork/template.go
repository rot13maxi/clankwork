package main

import (
	"context"
	"fmt"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/template"
	"github.com/urfave/cli/v3"
)

func templateCmd() *cli.Command {
	return &cli.Command{
		Name:  "template",
		Usage: "Manage workflow templates",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List available templates",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "repo-path", Usage: "Path to repo with local templates"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					homeDir, err := config.Home(cmd.Root().String("home"))
					if err != nil {
						return err
					}
					templates, err := template.List(cmd.String("repo-path"), homeDir)
					if err != nil {
						return err
					}
					if len(templates) == 0 {
						fmt.Println("no templates found")
						return nil
					}
					fmt.Printf("%-20s  %s\n", "NAME", "SOURCE")
					for _, t := range templates {
						fmt.Printf("%-20s  %s\n", t.Name, t.Source)
					}
					return nil
				},
			},
		},
	}
}
