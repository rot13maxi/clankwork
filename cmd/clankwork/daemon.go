package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/daemon"
	"github.com/urfave/cli/v3"
)

func daemonCmd() *cli.Command {
	cmd := &cli.Command{
		Name:  "daemon",
		Usage: "Daemon control plane management",
		Commands: []*cli.Command{
			{
				Name:  "start",
				Usage: "Start the daemon (foreground)",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:    "background",
						Aliases: []string{"b"},
						Usage:   "Run as background daemon",
					},
				},
				Action: func(ctx context.Context, c *cli.Command) error {
					home, err := config.Home(c.Root().String("home"))
					if err != nil {
						return err
					}

					if c.Bool("background") {
						return startBackground(home)
					}

					return daemon.Run(home)
				},
			},
			{
				Name:  "stop",
				Usage: "Stop the running daemon",
				Action: func(ctx context.Context, c *cli.Command) error {
					home, err := config.Home(c.Root().String("home"))
					if err != nil {
						return err
					}
					return stopDaemon(home)
				},
			},
		},
	}

	// Backward compat: if called without subcommand, show a hint
	cmd.Action = func(ctx context.Context, c *cli.Command) error {
		fmt.Fprintln(os.Stderr, "daemon: use 'daemon start' or 'daemon stop'")
		return nil
	}

	return cmd
}

// startBackground re-execs the binary as a detached background process.
func startBackground(home string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}

	if err := os.MkdirAll(home, 0700); err != nil {
		return fmt.Errorf("create home directory: %w", err)
	}

	logPath := home + "/daemon.log"
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	// Intentionally not closing logFile — the child inherits the fd for its
	// stdout/stderr, and closing it from the parent before the child has
	// started writing can truncate log output.

	cmd := exec.Command(exe, "daemon", "start")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Dir = "/"

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start background process: %w", err)
	}

	pid := cmd.Process.Pid
	// Release detaches Go's process handle so the GC finalizer does not send
	// SIGTERM to the child when startBackground returns.
	cmd.Process.Release()
	fmt.Printf("daemon started in background (pid %d)\n", pid)
	return nil
}

// stopDaemon reads the PID file and sends SIGTERM to stop the daemon.
// Before sending SIGTERM, it verifies the process is actually the clankwork
// daemon to avoid killing unrelated processes that may have recycled the PID.
func stopDaemon(home string) error {
	pidPath := home + "/clankwork.pid"
	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("daemon not running")
			return nil
		}
		return fmt.Errorf("read pid file: %w", err)
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return fmt.Errorf("parse pid: %w", err)
	}

	// Verify the process is actually the clankwork daemon before sending SIGTERM.
	// PID reuse can cause the PID file to point to an unrelated process.
	if !isClankworkDaemon(pid) {
		fmt.Printf("process %d is not the clankwork daemon, removing stale PID file\n", pid)
		_ = os.Remove(pidPath)
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if err == syscall.ESRCH {
			fmt.Printf("process %d not found, removing stale PID file\n", pid)
			_ = os.Remove(pidPath)
			return nil
		}
		return fmt.Errorf("send sigterm: %w", err)
	}

	fmt.Println("daemon stopped")
	return nil
}

// isClankworkDaemon checks whether the process with the given PID is the
// clankwork daemon by inspecting its command line via ps.
func isClankworkDaemon(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return false
	}
	args := string(out)
	return strings.Contains(args, "clankwork") && strings.Contains(args, "daemon")
}
