package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/urfave/cli/v3"
)

func logsCmd() *cli.Command {
	return &cli.Command{
		Name:      "logs",
		Usage:     "Print or stream the log file for a task",
		ArgsUsage: "<task-id>",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "follow",
				Aliases: []string{"f"},
				Usage:   "Keep streaming (like tail -f)",
			},
			&cli.IntFlag{
				Name:    "lines",
				Aliases: []string{"n"},
				Usage:   "Number of lines to show from the end (static mode only)",
				Value:   50,
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) == 0 {
				return fmt.Errorf("usage: clankwork logs <task-id>")
			}
			taskID := args[0]

			c, err := newClient(cmd)
			if err != nil {
				return err
			}

			agent, err := c.AgentGetByTask(okCtx(), taskID)
			if err != nil || agent == nil {
				return fmt.Errorf("no agent found for task %q", taskID)
			}
			if agent.LogfilePath == "" {
				return fmt.Errorf("agent %s has no log file", agent.ID)
			}

			path := agent.LogfilePath

			if cmd.Bool("follow") {
				tail, err := exec.LookPath("tail")
				if err != nil {
					return fmt.Errorf("tail not found: %w", err)
				}
				tailCmd := exec.Command(tail, "-f", path)
				tailCmd.Stdout = os.Stdout
				tailCmd.Stderr = os.Stderr
				return tailCmd.Run()
			}

			return printLastN(path, cmd.Int("lines"))
		},
	}
}

func printLastN(path string, n int) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open log file: %w", err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading log file: %w", err)
	}

	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}
