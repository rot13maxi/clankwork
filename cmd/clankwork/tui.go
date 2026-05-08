package main

import (
	"context"
	"fmt"

	"github.com/rot13maxi/clankwork/internal/config"
	clanktui "github.com/rot13maxi/clankwork/internal/tui"
	"github.com/urfave/cli/v3"
)

func tuiCmd() *cli.Command {
	return &cli.Command{
		Name:  "tui",
		Usage: "Open the terminal control-plane dashboard",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "tasks", Usage: "Show only the tasks pane"},
			&cli.BoolFlag{Name: "escalations", Usage: "Show only the escalations pane"},
			&cli.BoolFlag{Name: "merge-queue", Usage: "Show only the merge queue pane"},
			&cli.BoolFlag{Name: "health", Usage: "Show only the health pane"},
			&cli.BoolFlag{Name: "events", Usage: "Show only the recent events pane"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			home, err := config.Home(cmd.Root().String("home"))
			if err != nil {
				return err
			}
			mode, err := tuiModeFromFlags(cmd)
			if err != nil {
				return err
			}
			return clanktui.Run(clanktui.Config{Home: home, Mode: mode})
		},
	}
}

func tuiModeFromFlags(cmd *cli.Command) (clanktui.Mode, error) {
	selected := clanktui.ModeDashboard
	count := 0
	for _, option := range []struct {
		flag string
		mode clanktui.Mode
	}{
		{flag: "tasks", mode: clanktui.ModeTasks},
		{flag: "escalations", mode: clanktui.ModeEscalations},
		{flag: "merge-queue", mode: clanktui.ModeMergeQueue},
		{flag: "health", mode: clanktui.ModeHealth},
		{flag: "events", mode: clanktui.ModeEvents},
	} {
		if cmd.Bool(option.flag) {
			selected = option.mode
			count++
		}
	}
	if count > 1 {
		return "", fmt.Errorf("choose only one focused TUI pane")
	}
	return selected, nil
}
