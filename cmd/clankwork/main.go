package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
)

var version = "dev"

func main() {
	app := &cli.Command{
		Name:    "clankwork",
		Usage:   "AI agent orchestration control plane",
		Version: version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "home",
				Usage:   "Override $CLANKWORK_HOME",
				Sources: cli.EnvVars("CLANKWORK_HOME"),
			},
		},
		Commands: []*cli.Command{
			daemonCmd(),
			versionCmd(),
			statusCmd(),
			doctorCmd(),
			planCmd(),
			taskCmd(),
			repoCmd(),
			attachCmd(),
			logsCmd(),
			agentsCmd(),
			dispatchCmd(),
			reconcileCmd(),
			refreshCmd(),
			eventsCmd(),
			escalationCmd(),
			templateCmd(),
			signalCmd(),
			bootstrapCmd(),
			contextCmd(),
			learningCmd(),
			priorArtCmd(),
			queueCmd(),
			tracesCmd(),
			verifyCmd(),
			configCmd(),
			acceptanceCmd(),
			artifactCmd(),
			acpCmd(),
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
