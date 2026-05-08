package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/urfave/cli/v3"
)

func artifactCmd() *cli.Command {
	return &cli.Command{
		Name:  "artifact",
		Usage: "Register acceptance artifacts with provenance",
		Commands: []*cli.Command{
			{
				Name:  "add",
				Usage: "Register an artifact and return its artifact ID",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "type", Required: true, Usage: "Artifact type, such as playwright_trace or cli_transcript"},
					&cli.StringFlag{Name: "path", Required: true, Usage: "Path to artifact file"},
					&cli.StringFlag{Name: "producer", Required: true, Usage: "Producer name, such as acceptance-verifier"},
					&cli.StringFlag{Name: "producer-type", Value: "agent", Usage: "Producer type: agent, deterministic_command, or control_plane"},
					&cli.StringFlag{Name: "step", Value: "acceptance", Usage: "Step that produced the artifact"},
					&cli.StringFlag{Name: "command", Usage: "Command that produced the artifact, when applicable"},
					&cli.IntFlag{Name: "exit-code", Usage: "Command exit code, when applicable"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					taskID := os.Getenv("CLANKWORK_TASK_ID")
					if taskID == "" {
						return fmt.Errorf("CLANKWORK_TASK_ID not set")
					}
					path := cmd.String("path")
					hash, err := sha256File(path)
					if err != nil {
						return err
					}
					wd, _ := os.Getwd()
					c, err := newClient(cmd)
					if err != nil {
						return err
					}
					artifact, err := c.ArtifactAdd(okCtx(), model.AddArtifactRequest{
						TaskID:           taskID,
						StepID:           cmd.String("step"),
						Producer:         cmd.String("producer"),
						ProducerType:     cmd.String("producer-type"),
						Path:             filepath.ToSlash(path),
						ArtifactType:     cmd.String("type"),
						SHA256:           "sha256:" + hash,
						Command:          cmd.String("command"),
						WorkingDirectory: wd,
						ExitCode:         cmd.Int("exit-code"),
					})
					if err != nil {
						return err
					}
					fmt.Println(artifact.ID)
					return nil
				},
			},
		},
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
