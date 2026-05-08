package worker

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AgentRuntime is the control-plane surface for a long-lived agent session.
// The current implementation is tmux-backed, but the scheduler and reconciler
// should depend on these lifecycle operations rather than tmux details.
type AgentRuntime interface {
	Spawn(sessionName, workdir, command string, args []string, env map[string]string) error
	IsAlive(sessionName string) (bool, error)
	Kill(sessionName string) error
	GracefulKill(sessionName string, gracePeriod time.Duration) error
	PaneLastActivity(sessionName string) (time.Time, error)
	CapturePane(sessionName string, lines int) (string, error)
	SendInitialPrompt(sessionName, msg string) error
	SendNudge(sessionName, msg string) error
}

// AgentSpawner is kept as a compatibility alias while callers migrate to the
// transport-neutral AgentRuntime name.
type AgentSpawner = AgentRuntime

type TmuxSpawner struct {
	LogDir string
}

func (t *TmuxSpawner) Spawn(sessionName, workdir, command string, args []string, env map[string]string) error {
	// Note: we intentionally do not use -t <group> here. In tmux 3.x, -t creates
	// a grouped session that shares windows with the target, which is mutually
	// exclusive with passing a shell command. Sessions are named with the
	// "clankwork-worker-" prefix so they are easy to identify in `tmux ls`.
	cmdArgs := []string{"new-session", "-d", "-s", sessionName, "-c", workdir}
	for k, v := range env {
		cmdArgs = append(cmdArgs, "-e", k+"="+v)
	}
	cmdArgs = append(cmdArgs, command)
	cmdArgs = append(cmdArgs, args...)

	if out, err := exec.Command("tmux", cmdArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %w\n%s", err, out)
	}

	logPath := filepath.Join(t.LogDir, sessionName+".log")
	pipeCmd := fmt.Sprintf("cat >> %s", logPath)
	if out, err := exec.Command("tmux", "pipe-pane", "-o", "-t", sessionName, pipeCmd).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux pipe-pane: %w\n%s", err, out)
	}
	return nil
}

func (t *TmuxSpawner) IsAlive(sessionName string) (bool, error) {
	err := exec.Command("tmux", "has-session", "-t", sessionName).Run()
	return err == nil, nil
}

func (t *TmuxSpawner) Kill(sessionName string) error {
	out, err := exec.Command("tmux", "kill-session", "-t", sessionName).CombinedOutput()
	if err != nil {
		msg := string(out)
		if strings.Contains(msg, "no server running") || strings.Contains(msg, "session not found") || strings.Contains(msg, "can't find session") {
			return nil
		}
		return fmt.Errorf("tmux kill-session: %w\n%s", err, out)
	}
	return nil
}

// PaneLastActivity returns the time of last output to the session's pane.
func (t *TmuxSpawner) PaneLastActivity(sessionName string) (time.Time, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", sessionName, "#{pane_last_activity}").Output()
	if err != nil {
		return time.Time{}, fmt.Errorf("tmux display-message: %w", err)
	}
	ts := strings.TrimSpace(string(out))
	sec, err := parseInt64(ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse pane_last_activity %q: %w", ts, err)
	}
	return time.Unix(sec, 0), nil
}

// CapturePane returns the last N lines of visible output from the session's pane.
func (t *TmuxSpawner) CapturePane(sessionName string, lines int) (string, error) {
	startLine := fmt.Sprintf("-%d", lines)
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", sessionName, "-S", startLine).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return string(out), nil
}

// SendInitialPrompt waits for the Claude Code TUI to render when present, then
// delivers the bootstrap message. For other REPL-style agents the deadline just
// expires and the message is still sent via the same tmux send-keys path.
func (t *TmuxSpawner) SendInitialPrompt(sessionName, msg string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "capture-pane", "-p", "-t", sessionName).Output()
		if err == nil && strings.Contains(string(out), "Claude Code") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return t.SendNudge(sessionName, msg)
}

// SendNudge delivers a message to the session using the Gastown send-keys pattern:
// literal flag to avoid special-char interpretation, then Enter as a separate call.
func (t *TmuxSpawner) SendNudge(sessionName, msg string) error {
	if err := exec.Command("tmux", "send-keys", "-t", sessionName, "-l", msg).Run(); err != nil {
		return fmt.Errorf("send-keys: %w", err)
	}
	time.Sleep(100 * time.Millisecond)
	return exec.Command("tmux", "send-keys", "-t", sessionName, "Enter").Run()
}

// GracefulKill sends C-c to the session, waits for gracePeriod, then hard-kills if still alive.
func (t *TmuxSpawner) GracefulKill(sessionName string, gracePeriod time.Duration) error {
	// Send C-c to let the agent clean up.
	exec.Command("tmux", "send-keys", "-t", sessionName, "C-c", "").Run()

	time.Sleep(gracePeriod)

	alive, _ := t.IsAlive(sessionName)
	if alive {
		return t.Kill(sessionName)
	}
	return nil
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// FakeSpawner implements AgentSpawner for unit tests without requiring tmux.
type FakeSpawner struct {
	mu               sync.Mutex
	sessions         map[string]bool
	SpawnErr         error
	PaneActivityTime time.Time // zero = use time.Now() (active); set to past time to simulate stale pane
	LastEnv          map[string]string

	// GracefulKillDelay simulates a prior process taking time to finish dying.
	// GracefulKill blocks for min(GracefulKillDelay, gracePeriod) before clearing the session.
	// If GracefulKillDelay > gracePeriod, the call returns at gracePeriod with the session still alive,
	// matching the real runtime's behavior of falling back to a hard kill on budget overrun.
	GracefulKillDelay time.Duration

	// calls records the order of spawner methods invoked (for race-ordering assertions).
	calls []FakeSpawnerCall
}

// FakeSpawnerCall records one method invocation against the FakeSpawner.
type FakeSpawnerCall struct {
	Method  string
	Session string
	At      time.Time
}

// MarkAlive forces the named session into the alive set without going through Spawn.
// Useful for simulating a leftover/stuck session from a prior dispatch attempt.
func (f *FakeSpawner) MarkAlive(sessionName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions == nil {
		f.sessions = make(map[string]bool)
	}
	f.sessions[sessionName] = true
}

// CallLog returns a copy of the recorded method-call history.
func (f *FakeSpawner) CallLog() []FakeSpawnerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeSpawnerCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *FakeSpawner) recordCall(method, session string) {
	f.mu.Lock()
	f.calls = append(f.calls, FakeSpawnerCall{Method: method, Session: session, At: time.Now()})
	f.mu.Unlock()
}

func (f *FakeSpawner) Spawn(sessionName, workdir, command string, args []string, env map[string]string) error {
	f.recordCall("Spawn", sessionName)
	if f.SpawnErr != nil {
		return f.SpawnErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LastEnv = make(map[string]string, len(env))
	for k, v := range env {
		f.LastEnv[k] = v
	}
	if f.sessions == nil {
		f.sessions = make(map[string]bool)
	}
	f.sessions[sessionName] = true
	return nil
}

func (f *FakeSpawner) IsAlive(sessionName string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[sessionName], nil
}

func (f *FakeSpawner) Kill(sessionName string) error {
	f.recordCall("Kill", sessionName)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, sessionName)
	return nil
}

func (f *FakeSpawner) GracefulKill(sessionName string, gracePeriod time.Duration) error {
	f.recordCall("GracefulKill", sessionName)

	f.mu.Lock()
	delay := f.GracefulKillDelay
	f.mu.Unlock()

	if delay > gracePeriod {
		// Caller's budget elapses before the simulated process finishes dying;
		// return without clearing the session, matching the budget-exceeded path.
		time.Sleep(gracePeriod)
		return nil
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, sessionName)
	return nil
}

func (f *FakeSpawner) PaneLastActivity(sessionName string) (time.Time, error) {
	if !f.PaneActivityTime.IsZero() {
		return f.PaneActivityTime, nil
	}
	return time.Now(), nil
}

func (f *FakeSpawner) CapturePane(sessionName string, lines int) (string, error) {
	return "", nil
}

func (f *FakeSpawner) SendInitialPrompt(sessionName, msg string) error {
	return nil
}

func (f *FakeSpawner) SendNudge(sessionName, msg string) error {
	return nil
}
