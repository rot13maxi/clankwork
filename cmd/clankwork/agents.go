package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/rot13maxi/clankwork/internal/client"
	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func agentsCmd() *cli.Command {
	return &cli.Command{
		Name:  "agents",
		Usage: "List and manage running agents",
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List agents",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "status", Usage: "Filter by status"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					agents, err := c.AgentsList(okCtx(), cmd.String("status"))
					if err != nil {
						return err
					}
					if len(agents) == 0 {
						fmt.Println("no agents")
						return nil
					}
					// Build task name map for display.
					tasks, _ := c.TasksList(okCtx(), "", "", nil)
					nameByTaskID := make(map[string]string)
					for _, t := range tasks {
						if t.Name != "" {
							nameByTaskID[t.ID] = t.Name
						}
					}
					fmt.Printf("%-18s  %-26s  %-8s  %s\n", "TASK_NAME", "TASK_ID", "STATUS", "STARTED_AT")
					for _, a := range agents {
						name := nameByTaskID[a.TaskID]
						if name == "" {
							name = "-"
						}
						fmt.Printf("%-18s  %-26s  %-8s  %s\n", name, a.TaskID, a.Status, a.StartedAt.Format("2006-01-02 15:04:05"))
					}
					return nil
				},
			},
			{
				Name:      "attach",
				Usage:     "Attach to an agent's tmux session",
				ArgsUsage: "<agent-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork agents attach <agent-id>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}

					agent, err := resolveAgent(c, args[0])
					if err != nil {
						return err
					}

					return attachOrWatchAgent(ctx, cmd, c, agent)
				},
			},
			{
				Name:      "events",
				Usage:     "Print persisted output events for an agent or task",
				ArgsUsage: "<task-id|agent-id>",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "limit", Value: 100, Usage: "Maximum events to print"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					agent, err := resolveAgent(c, cmd.Args().First())
					if err != nil {
						return err
					}
					events, err := c.AgentEvents(okCtx(), agent.ID, "", 0, cmd.Int("limit"))
					if err != nil {
						return err
					}
					printer := newAgentEventPrinter(os.Stdout)
					for _, ev := range events {
						printer.Print(ev)
					}
					printer.Flush()
					return nil
				},
			},
			{
				Name:      "watch",
				Usage:     "Follow persisted output events for an agent or task",
				ArgsUsage: "<task-id|agent-id>",
				Flags: []cli.Flag{
					&cli.DurationFlag{Name: "interval", Value: time.Second, Usage: "Polling interval"},
					&cli.IntFlag{Name: "tail", Value: 100, Usage: "Initial events to print"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					agent, err := resolveAgent(c, cmd.Args().First())
					if err != nil {
						return err
					}
					var lastSeq int64
					if cmd.Int("tail") > 0 {
						lastSeq, err = printAgentEvents(c, agent.ID, 0, cmd.Int("tail"))
						if err != nil {
							return err
						}
					}
					return followAgentEvents(ctx, c, agent.ID, lastSeq, cmd.Duration("interval"))
				},
			},
			{
				Name:      "send",
				Usage:     "Send a follow-up message to a running agent",
				ArgsUsage: "<task-id|agent-id> <message>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) < 2 {
						return fmt.Errorf("usage: clankwork agents send <task-id|agent-id> <message>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					agent, err := resolveAgent(c, args[0])
					if err != nil {
						return err
					}
					msg := strings.Join(args[1:], " ")
					if err := c.AgentSend(okCtx(), agent.ID, msg); err != nil {
						return err
					}
					fmt.Printf("sent to %s\n", agent.ID)
					return nil
				},
			},
			{
				Name:      "cancel",
				Usage:     "Cancel the current turn for a running agent",
				ArgsUsage: "<task-id|agent-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					agent, err := resolveAgent(c, cmd.Args().First())
					if err != nil {
						return err
					}
					if err := c.AgentCancel(okCtx(), agent.ID); err != nil {
						return err
					}
					fmt.Printf("cancelled %s\n", agent.ID)
					return nil
				},
			},
			{
				Name:      "permissions",
				Usage:     "List pending ACP permission requests for an agent",
				ArgsUsage: "<task-id|agent-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					agent, err := resolveAgent(c, cmd.Args().First())
					if err != nil {
						return err
					}
					pending, err := c.AgentPermissions(okCtx(), agent.ID)
					if err != nil {
						return err
					}
					if len(pending) == 0 {
						fmt.Println("no pending permissions")
						return nil
					}
					fmt.Printf("%-10s  %-8s  %s\n", "REQUEST", "POLICY", "COMMAND")
					for _, p := range pending {
						fmt.Printf("%-10s  %-8s  %s\n", p.ID, p.Policy, p.Command)
					}
					return nil
				},
			},
			{
				Name:      "approve",
				Usage:     "Approve a pending ACP permission request once",
				ArgsUsage: "<task-id|agent-id> <request-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return resolveAgentPermission(cmd, "accept")
				},
			},
			{
				Name:      "approve-session",
				Usage:     "Approve a pending ACP permission request for the session",
				ArgsUsage: "<task-id|agent-id> <request-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return resolveAgentPermission(cmd, "acceptForSession")
				},
			},
			{
				Name:      "deny",
				Usage:     "Deny a pending ACP permission request",
				ArgsUsage: "<task-id|agent-id> <request-id>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					return resolveAgentPermission(cmd, "decline")
				},
			},
		},
	}
}

func resolveAgentPermission(cmd *cli.Command, decision string) error {
	args := cmd.Args().Slice()
	if len(args) < 2 {
		return fmt.Errorf("usage: clankwork agents <approve|approve-session|deny> <task-id|agent-id> <request-id>")
	}
	c, err := newClient(cmd)
	if err != nil {
		return err
	}
	agent, err := resolveAgent(c, args[0])
	if err != nil {
		return err
	}
	if err := c.AgentPermissionDecision(okCtx(), agent.ID, args[1], decision); err != nil {
		return err
	}
	fmt.Printf("%s %s for %s\n", decision, args[1], agent.ID)
	return nil
}

func resolveAgent(c *client.Client, id string) (*model.Agent, error) {
	if id == "" {
		return nil, fmt.Errorf("usage: clankwork agents <events|watch> <task-id|agent-id>")
	}
	if agent, err := c.AgentsGet(okCtx(), id); err == nil {
		return agent, nil
	}
	if agent, err := c.AgentGetByTask(okCtx(), id); err == nil {
		return agent, nil
	}
	agents, err := c.AgentsList(okCtx(), "")
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		if strings.HasPrefix(a.ID, id) || strings.HasPrefix(a.TaskID, id) {
			return a, nil
		}
	}
	return nil, fmt.Errorf("no agent found for %q", id)
}

func printAgentEvent(ev *model.AgentEvent) {
	p := newAgentEventPrinter(os.Stdout)
	p.Print(ev)
	p.Flush()
}

func attachOrWatchAgent(ctx context.Context, cmd *cli.Command, c *client.Client, agent *model.Agent) error {
	if agentTransport(cmd, agent) == config.TransportACP {
		printAgentAttachHeader(agent)
		lastSeq, err := printAgentEvents(c, agent.ID, 0, 100)
		if err != nil {
			return err
		}
		if agent.Status != "running" {
			return nil
		}
		return followAgentEvents(ctx, c, agent.ID, lastSeq, time.Second)
	}
	if agent.TmuxSession == "" {
		return fmt.Errorf("agent %s has no live session (status: %s)", agent.ID, agent.Status)
	}

	if err := exec.Command("tmux", "has-session", "-t", agent.TmuxSession).Run(); err != nil {
		return fmt.Errorf("tmux session %q no longer exists (agent status: %s)", agent.TmuxSession, agent.Status)
	}
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}
	if os.Getenv("TMUX") != "" {
		return syscall.Exec(tmuxBin, []string{"tmux", "switch-client", "-t", agent.TmuxSession}, os.Environ())
	}
	return syscall.Exec(tmuxBin, []string{"tmux", "attach-session", "-t", agent.TmuxSession}, os.Environ())
}

func printAgentAttachHeader(agent *model.Agent) {
	fmt.Printf("ACP agent %s\n", agent.ID)
	fmt.Printf("task: %s  status: %s  runtime: %s\n", agent.TaskID, agent.Status, agent.Runtime)
	if agent.RuntimeSessionID != "" || agent.PID != 0 {
		fmt.Printf("session: %s", agent.RuntimeSessionID)
		if agent.PID != 0 {
			fmt.Printf("  pid: %d", agent.PID)
		}
		fmt.Println()
	}
	fmt.Println("events:")
}

func agentTransport(cmd *cli.Command, agent *model.Agent) string {
	if agent.Transport != "" {
		return agent.Transport
	}
	home, err := config.Home(cmd.Root().String("home"))
	if err != nil {
		return config.TransportTmux
	}
	cfg, err := config.Load(home)
	if err != nil {
		return config.TransportTmux
	}
	if rt, ok := cfg.Runtimes[agent.Runtime]; ok {
		return config.RuntimeTransport(rt)
	}
	if strings.Contains(agent.Runtime, "acp") {
		return config.TransportACP
	}
	return config.TransportTmux
}

func printAgentEvents(c *client.Client, agentID string, afterSeq int64, limit int) (int64, error) {
	events, err := c.AgentEvents(okCtx(), agentID, "", afterSeq, limit)
	if err != nil {
		return afterSeq, err
	}
	lastSeq := afterSeq
	printer := newAgentEventPrinter(os.Stdout)
	for _, ev := range events {
		printer.Print(ev)
		if ev.Seq > lastSeq {
			lastSeq = ev.Seq
		}
	}
	printer.Flush()
	return lastSeq, nil
}

func followAgentEvents(ctx context.Context, c *client.Client, agentID string, lastSeq int64, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			var err error
			lastSeq, err = printAgentEvents(c, agentID, lastSeq, 200)
			if err != nil {
				return err
			}
		}
	}
}

func dispatchCmd() *cli.Command {
	return &cli.Command{
		Name:  "dispatch",
		Usage: "Control the scheduler dispatcher",
		Commands: []*cli.Command{
			{
				Name:  "pause",
				Usage: "Pause task dispatching",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.DispatchPause(okCtx()); err != nil {
						return err
					}
					fmt.Println("dispatch paused")
					return nil
				},
			},
			{
				Name:  "resume",
				Usage: "Resume task dispatching",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					if err := c.DispatchResume(okCtx()); err != nil {
						return err
					}
					fmt.Println("dispatch resumed")
					return nil
				},
			},
			{
				Name:  "status",
				Usage: "Show dispatch status",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					paused, err := c.DispatchStatus(okCtx())
					if err != nil {
						return err
					}
					if paused {
						fmt.Println("dispatch: paused")
					} else {
						fmt.Println("dispatch: running")
					}
					return nil
				},
			},
		},
	}
}
