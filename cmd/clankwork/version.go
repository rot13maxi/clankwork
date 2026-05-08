package main

import (
	"context"
	"fmt"
	"runtime"

	"github.com/urfave/cli/v3"
)

func versionCmd() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print version information",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			fmt.Printf("clankwork %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
