package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/urfave/cli/v3"
)

func configCmd() *cli.Command {
	return &cli.Command{
		Name:  "config",
		Usage: "Show or modify configuration",
		Commands: []*cli.Command{
			{
				Name:      "get",
				Usage:     "Get a specific config value",
				ArgsUsage: "<key>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					args := cmd.Args().Slice()
					if len(args) == 0 {
						return fmt.Errorf("usage: clankwork config get <key>")
					}
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					var cfg config.Config
					if err := c.ConfigGet(okCtx(), &cfg); err != nil {
						return err
					}
					return printConfigKey(&cfg, args[0])
				},
			},
			{
				Name:      "set",
				Usage:     "Set a config value at runtime",
				ArgsUsage: "<key> <value>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					fmt.Println("clankwork config set: not yet implemented")
					return nil
				},
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			var cfg config.Config
			if err := c.ConfigGet(okCtx(), &cfg); err != nil {
				return err
			}
			printConfig(&cfg)
			return nil
		},
	}
}

func printConfig(cfg *config.Config) {
	fmt.Println("scheduler:")
	fmt.Printf("  max_slots:                %d\n", cfg.Scheduler.MaxSlots)
	fmt.Printf("  heartbeat_timeout_secs:   %d\n", cfg.Scheduler.HeartbeatTimeoutSec)
	fmt.Printf("  tick_secs:                %d\n", cfg.Scheduler.TickSec)
	fmt.Printf("  deterministic_timeout_sec:%d\n", cfg.Scheduler.DeterministicTimeoutSec)
	fmt.Printf("  merge_queue_max_depth:    %d\n", cfg.Scheduler.MergeQueueMaxDepth)
	fmt.Printf("  merge_queue_tick_secs:    %d\n", cfg.Scheduler.MergeQueueTickSec)
	fmt.Printf("  merge_queue_max_attempts: %d\n", cfg.Scheduler.MergeQueueMaxAttempts)
	fmt.Printf("  verify_timeout_secs:      %d\n", cfg.Scheduler.VerifyTimeoutSec)

	if len(cfg.Runtimes) > 0 {
		fmt.Println("\nruntimes:")
		for name, rt := range cfg.Runtimes {
			fmt.Printf("  [%s]\n", name)
			fmt.Printf("    command:         %s\n", rt.Command)
			fmt.Printf("    transport:       %s\n", config.RuntimeTransport(rt))
			if len(rt.Args) > 0 {
				fmt.Printf("    args:            %s\n", strings.Join(rt.Args, " "))
			}
			if rt.EscalateAfter > 0 {
				fmt.Printf("    escalate_after:  %d\n", rt.EscalateAfter)
				fmt.Printf("    escalate_to:     %s\n", rt.EscalateTo)
			}
			if rt.NonInteractive {
				fmt.Printf("    non_interactive: true\n")
			}
			if config.RuntimeTransport(rt) == config.TransportACP {
				fmt.Printf("    acp_permission_policy: %s\n", rt.ACPPermissionPolicy)
				if len(rt.ACPPermissionAllowPaths) > 0 {
					fmt.Printf("    acp_permission_allow_paths: %s\n", strings.Join(rt.ACPPermissionAllowPaths, ", "))
				}
				if len(rt.ACPPermissionDenyPaths) > 0 {
					fmt.Printf("    acp_permission_deny_paths: %s\n", strings.Join(rt.ACPPermissionDenyPaths, ", "))
				}
				if rt.ACPPermissionTimeoutSec > 0 {
					fmt.Printf("    acp_permission_timeout_sec: %d\n", rt.ACPPermissionTimeoutSec)
				}
			}
		}
	}
}

func printConfigKey(cfg *config.Config, key string) error {
	switch key {
	case "scheduler.max_slots":
		fmt.Println(cfg.Scheduler.MaxSlots)
	case "scheduler.heartbeat_timeout_secs":
		fmt.Println(cfg.Scheduler.HeartbeatTimeoutSec)
	case "scheduler.tick_secs":
		fmt.Println(cfg.Scheduler.TickSec)
	case "scheduler.deterministic_timeout_sec":
		fmt.Println(cfg.Scheduler.DeterministicTimeoutSec)
	case "scheduler.merge_queue_max_depth":
		fmt.Println(cfg.Scheduler.MergeQueueMaxDepth)
	case "scheduler.merge_queue_tick_secs":
		fmt.Println(cfg.Scheduler.MergeQueueTickSec)
	case "scheduler.merge_queue_max_attempts":
		fmt.Println(cfg.Scheduler.MergeQueueMaxAttempts)
	case "scheduler.verify_timeout_secs":
		fmt.Println(cfg.Scheduler.VerifyTimeoutSec)
	default:
		return fmt.Errorf("unknown config key: %s\nvalid keys: scheduler.max_slots, scheduler.heartbeat_timeout_secs, scheduler.tick_secs, scheduler.deterministic_timeout_sec, scheduler.merge_queue_max_depth, scheduler.merge_queue_tick_secs, scheduler.merge_queue_max_attempts, scheduler.verify_timeout_secs", key)
	}
	return nil
}
