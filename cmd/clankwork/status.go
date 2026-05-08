package main

import (
	"context"
	"fmt"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func statusCmd() *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "System overview: tasks, agents, plans",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			s, err := c.Status(okCtx())
			if err != nil {
				return err
			}
			fmt.Printf("tasks:   %d total  (%d pending, %d running, %d done, %d failed, %d blocked)\n",
				s.Tasks.Total, s.Tasks.Pending, s.Tasks.Running, s.Tasks.Done, s.Tasks.Failed, s.Tasks.Blocked)
			fmt.Printf("agents:  %d running\n", s.Agents.Running)
			fmt.Printf("plans:   %d total  (%d active)\n", s.Plans.Total, s.Plans.Active)
			fmt.Printf("queue:   %d queued  %d active  pressure=%s",
				s.MergeQueue.Queued, s.MergeQueue.InProgress, s.QueuePressure.Level)
			if s.QueuePressure.Reason != "" {
				fmt.Printf(" (%s)", s.QueuePressure.Reason)
			}
			fmt.Println()

			blocked, err := c.TasksList(okCtx(), "", "", []string{"blocked"})
			if err == nil && len(blocked) > 0 {
				// Collect escalations for all blocked tasks.
				escList, escErr := c.EscalationsList(okCtx(), "", "open")
				escByTask := make(map[string][]*model.Escalation)
				if escErr == nil {
					for _, e := range escList {
						if e.TaskID != "" {
							escByTask[e.TaskID] = append(escByTask[e.TaskID], e)
						}
					}
				}
				fmt.Println()
				fmt.Println("blocked tasks:")
				for _, t := range blocked {
					name := t.Name
					if name == "" {
						name = t.ID
					}
					escs := escByTask[t.ID]
					escNote := ""
					if len(escs) > 0 {
						escNote = fmt.Sprintf(" [%d escalation(s) open]", len(escs))
					}
					fmt.Printf("  %-20s  %-14s  %s%s\n", formatTaskName(name), t.CurrentStep, t.Title, escNote)
					for _, esc := range escs {
						fmt.Printf("    ! %s  sig=%s\n", esc.Reason, esc.FailureSignature)
					}
				}
			}

			// Show running agents with task names.
			agents, err := c.AgentsList(okCtx(), "running")
			if err == nil && len(agents) > 0 {
				fmt.Println()
				for _, agent := range agents {
					taskName := "?"
					// Look up task to get its name.
					tasks, err := c.TasksList(okCtx(), "", "", nil)
					if err == nil {
						for _, t := range tasks {
							if t.ID == agent.TaskID {
								taskName = t.Name
								if taskName == "" {
									taskName = t.ID
								}
								break
							}
						}
					}
					fmt.Printf("  %-20s  %-12s  %-8s  (%s)\n",
						formatTaskName(taskName),
						agent.Model,
						agent.Status,
						agent.TaskID,
					)
				}
			}
			return nil
		},
	}
}

// formatTaskName returns the task name or "-" if empty.
func formatTaskName(name string) string {
	if name == "" {
		return "-"
	}
	return name
}

// Ensure model types are used to avoid import error.
var _ = []model.Agent{}
