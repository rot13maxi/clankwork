package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/urfave/cli/v3"
)

type tmuxCommand struct {
	args []string
}

func workspaceCmd() *cli.Command {
	return &cli.Command{
		Name:  "workspace",
		Usage: "Open an opinionated tmux workspace with agent and Clankwork panes",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "session", Value: "clankwork", Usage: "tmux session name"},
			&cli.StringFlag{Name: "agent", Usage: "Command to run in the main agent pane; defaults to $SHELL"},
			&cli.BoolFlag{Name: "replace", Usage: "Kill and recreate an existing workspace session"},
			&cli.BoolFlag{Name: "intro", Usage: "Show the workspace controls intro even if it was already seen"},
			&cli.BoolFlag{Name: "no-intro", Usage: "Skip the first-run workspace controls intro"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			home, err := config.Home(cmd.Root().String("home"))
			if err != nil {
				return err
			}
			agent := cmd.String("agent")
			if agent == "" {
				agent = os.Getenv("SHELL")
			}
			if agent == "" {
				agent = "bash"
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			return runWorkspace(workspaceConfig{
				Session: cmd.String("session"),
				Agent:   agent,
				Cwd:     cwd,
				Exe:     exe,
				Home:    home,
				Replace: cmd.Bool("replace"),
				Intro:   cmd.Bool("intro"),
				NoIntro: cmd.Bool("no-intro"),
				In:      os.Stdin,
				Out:     os.Stdout,
			})
		},
	}
}

type workspaceConfig struct {
	Session string
	Agent   string
	Cwd     string
	Exe     string
	Home    string
	Replace bool
	Intro   bool
	NoIntro bool
	In      *os.File
	Out     io.Writer
}

func runWorkspace(cfg workspaceConfig) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not found: %w", err)
	}
	if sessionExists(cfg.Session) && !cfg.Replace {
		if err := maybeShowWorkspaceIntro(cfg); err != nil {
			return err
		}
		return attachWorkspace(cfg.Session)
	}
	if sessionExists(cfg.Session) && cfg.Replace {
		if out, err := exec.Command("tmux", "kill-session", "-t", cfg.Session).CombinedOutput(); err != nil {
			return fmt.Errorf("tmux kill-session -t %s: %w\n%s", cfg.Session, err, out)
		}
	}
	created := false
	for i, command := range workspaceCommands(cfg) {
		if out, err := exec.Command("tmux", command.args...).CombinedOutput(); err != nil {
			if created {
				_ = exec.Command("tmux", "kill-session", "-t", cfg.Session).Run()
			}
			return fmt.Errorf("tmux %s: %w\n%s", strings.Join(command.args, " "), err, out)
		}
		if i == 0 {
			created = true
		}
	}
	if err := maybeShowWorkspaceIntro(cfg); err != nil {
		return err
	}
	return attachWorkspace(cfg.Session)
}

func maybeShowWorkspaceIntro(cfg workspaceConfig) error {
	if cfg.NoIntro {
		return nil
	}
	if !cfg.Intro && workspaceIntroSeen(cfg.Home) {
		return nil
	}
	out := cfg.Out
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprint(out, workspaceIntroText())
	if err := markWorkspaceIntroSeen(cfg.Home); err != nil {
		return err
	}
	if cfg.In != nil && isTerminal(cfg.In) {
		fmt.Fprint(out, "\nPress Enter to open the workspace...")
		var buf [1]byte
		_, _ = cfg.In.Read(buf[:])
		fmt.Fprintln(out)
	}
	return nil
}

func workspaceIntroText() string {
	return strings.TrimSpace(`
Clankwork workspace controls

tmux:
  Ctrl-b then arrows  move between panes
  Ctrl-b then z       zoom or unzoom the current pane
  Ctrl-b then d       detach from the workspace

Clankwork TUI panes:
  up/down             select a row
  Enter               inspect selected item
  r                   refresh now
  q                   quit the pane

Escalations:
  x                   resolve selected escalation
  t                   retry the selected escalation's task step

Merge queue:
  y                   retry selected queue item
  s                   skip selected queue item

Run with --no-intro to skip this message, or --intro to show it again.
`) + "\n"
}

func workspaceIntroSeen(home string) bool {
	if home == "" {
		return false
	}
	_, err := os.Stat(workspaceIntroPath(home))
	return err == nil
}

func markWorkspaceIntroSeen(home string) error {
	if home == "" {
		return nil
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return fmt.Errorf("create clankwork home: %w", err)
	}
	if err := os.WriteFile(workspaceIntroPath(home), []byte("seen\n"), 0600); err != nil {
		return fmt.Errorf("write workspace intro marker: %w", err)
	}
	return nil
}

func workspaceIntroPath(home string) string {
	return filepath.Join(home, "workspace-intro-seen")
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func workspaceCommands(cfg workspaceConfig) []tmuxCommand {
	shell := shellForWorkspace()
	target := cfg.Session + ":0"
	return []tmuxCommand{
		{args: []string{"new-session", "-d", "-s", cfg.Session, "-c", cfg.Cwd, shell, "-lc", cfg.Agent}},
		{args: []string{"split-window", "-h", "-l", "34%", "-t", target + ".0", "-c", cfg.Cwd, shell, "-lc", tuiCommand(cfg, "--escalations")}},
		{args: []string{"split-window", "-v", "-l", "66%", "-t", target + ".1", "-c", cfg.Cwd, shell, "-lc", tuiCommand(cfg, "--tasks")}},
		{args: []string{"split-window", "-v", "-l", "50%", "-t", target + ".2", "-c", cfg.Cwd, shell, "-lc", tuiCommand(cfg, "--merge-queue")}},
		{args: []string{"split-window", "-v", "-l", "50%", "-t", target + ".3", "-c", cfg.Cwd, shell, "-lc", tuiCommand(cfg, "--health")}},
		{args: []string{"split-window", "-v", "-l", "28%", "-t", target + ".0", "-c", cfg.Cwd, shell, "-lc", tuiCommand(cfg, "--events")}},
		{args: []string{"select-pane", "-t", target + ".0"}},
	}
}

func tuiCommand(cfg workspaceConfig, mode string) string {
	parts := []string{shellQuote(cfg.Exe)}
	if cfg.Home != "" {
		parts = append(parts, "--home", shellQuote(cfg.Home))
	}
	parts = append(parts, "tui", mode)
	return strings.Join(parts, " ")
}

func shellForWorkspace() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "bash"
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func sessionExists(session string) bool {
	return exec.Command("tmux", "has-session", "-t", session).Run() == nil
}

func attachWorkspace(session string) error {
	if os.Getenv("TMUX") != "" {
		return runInteractiveTmux("switch-client", "-t", session)
	}
	return runInteractiveTmux("attach-session", "-t", session)
}

func runInteractiveTmux(args ...string) error {
	cmd := exec.Command("tmux", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
