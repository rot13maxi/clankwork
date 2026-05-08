package main

import (
	"context"
	"fmt"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func attachCmd() *cli.Command {
	return &cli.Command{
		Name:      "attach",
		Usage:     "Attach to the tmux session for a task or agent",
		ArgsUsage: "<task-id|agent-id>",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			args := cmd.Args().Slice()
			if len(args) == 0 {
				return fmt.Errorf("usage: clankwork attach <task-id|agent-id>")
			}
			id := args[0]

			c, err := newClient(cmd)
			if err != nil {
				return err
			}

			// Try name prefix first (most user-friendly identifier).
			var agent *model.Agent
			task, err := c.TaskGetByName(okCtx(), id)
			if err == nil {
				// Found by name, get agent for this task.
				agent, err = c.AgentGetByTask(okCtx(), task.ID)
				if err != nil {
					return fmt.Errorf("no agent found for task %q (%s)", task.Name, task.ID)
				}
			} else {
				// Not a name, try as task ID.
				agent, err = c.AgentGetByTask(okCtx(), id)
				if err != nil {
					// Fall back to agent ID lookup.
					agent, err = c.AgentsGet(okCtx(), id)
					if err != nil {
						return fmt.Errorf("no agent found for %q", id)
					}
				}
			}

			return attachOrWatchAgent(ctx, cmd, c, agent)
		},
	}
}
