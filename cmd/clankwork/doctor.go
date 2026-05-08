package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rot13maxi/clankwork/internal/client"
	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/urfave/cli/v3"
)

func doctorCmd() *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "Self-check: socket, database, schema, repos",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			home, err := config.Home(cmd.Root().String("home"))
			if err != nil {
				return err
			}

			ok := true
			check := func(label, val string, err error) {
				if err != nil {
					fmt.Printf("  %-14s FAIL  %s\n", label+":", err)
					ok = false
				} else {
					fmt.Printf("  %-14s ok    %s\n", label+":", val)
				}
			}

			fmt.Println("clankwork doctor")
			check("home", home, checkDir(home))

			socketPath := filepath.Join(home, "clankwork.sock")
			c := client.New(home)
			socketErr := c.Health(context.Background())
			check("socket", socketPath, socketErr)

			dbPath := filepath.Join(home, "clankwork.db")
			check("database", dbPath, checkFile(dbPath))

			if socketErr == nil {
				repos, err := c.ReposList(context.Background())
				if err == nil {
					check("repos", fmt.Sprintf("%d registered", len(repos)), nil)
				} else {
					check("repos", "", err)
				}
			}

			if !ok {
				os.Exit(1)
			}
			return nil
		},
	}
}

func checkDir(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

func checkFile(path string) error {
	_, err := os.Stat(path)
	return err
}
