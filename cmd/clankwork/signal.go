package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func signalCmd() *cli.Command {
	return &cli.Command{
		Name:  "signal",
		Usage: "Emit lifecycle signals (for worker agents)",
		Commands: []*cli.Command{
			signalAction("started", "Mark task as started"),
			signalAction("progress", "Heartbeat with status message"),
			signalAction("done", "Mark task complete"),
			signalAction("failed", "Mark task failed"),
			signalAction("blocked", "Mark task blocked, request human input"),
		},
	}
}

func signalAction(name, desc string) *cli.Command {
	return &cli.Command{
		Name:      name,
		Usage:     desc,
		ArgsUsage: "[message]",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "spec", Usage: "Path to acceptance spec JSON"},
			&cli.StringFlag{Name: "bundle", Usage: "Path to done bundle JSON"},
			&cli.StringFlag{Name: "report", Usage: "Path to verification report JSON"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			taskID := os.Getenv("CLANKWORK_TASK_ID")
			if taskID == "" {
				return fmt.Errorf("CLANKWORK_TASK_ID not set")
			}
			msg := strings.Join(cmd.Args().Slice(), " ")
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			req := model.SignalRequest{TaskID: taskID, Message: msg}
			if path := cmd.String("spec"); path != "" {
				var spec model.AcceptanceSpec
				if err := readJSONFile(path, &spec); err != nil {
					return err
				}
				hash, err := sha256File(path)
				if err != nil {
					return err
				}
				spec.Path = path
				spec.SHA256 = "sha256:" + hash
				req.AcceptanceSpec = &spec
			}
			if path := cmd.String("bundle"); path != "" {
				var bundle model.DoneBundle
				if err := readJSONFile(path, &bundle); err != nil {
					return err
				}
				req.DoneBundle = &bundle
			}
			if path := cmd.String("report"); path != "" {
				var report model.VerificationReport
				if err := readJSONFile(path, &report); err != nil {
					return err
				}
				hash, err := sha256File(path)
				if err != nil {
					return err
				}
				report.Path = path
				report.SHA256 = "sha256:" + hash
				req.VerificationReport = &report
			}
			if err := c.SignalWithPayload(okCtx(), name, req); err != nil {
				return err
			}
			fmt.Printf("signal %s sent for task %s\n", name, taskID)
			return nil
		},
	}
}

func readJSONFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
