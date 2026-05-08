package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/rot13maxi/clankwork/internal/client"
	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/urfave/cli/v3"
)

// newClient resolves home and returns a configured HTTP client.
func newClient(cmd *cli.Command) (*client.Client, error) {
	home, err := config.Home(cmd.Root().String("home"))
	if err != nil {
		return nil, err
	}
	return client.New(home), nil
}

// printJSON prints v as formatted JSON.
func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// okCtx returns a background context for CLI use.
func okCtx() context.Context {
	return context.Background()
}

// fatalf prints to stderr and exits 1.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
