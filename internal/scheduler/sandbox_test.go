package scheduler

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/config"
)

func TestWrapForSandboxDisabled(t *testing.T) {
	rt := config.RuntimeConfig{
		Command: "acp-adapter",
		Args:    []string{"--adapter", "claude"},
	}
	cmd, args := wrapForSandbox(rt, "/work", "/home", rt.Command, rt.Args)
	if cmd != "acp-adapter" {
		t.Fatalf("cmd = %q, want acp-adapter", cmd)
	}
	if !reflect.DeepEqual(args, []string{"--adapter", "claude"}) {
		t.Fatalf("args = %#v, want unchanged", args)
	}
}

func TestWrapForSandboxBasic(t *testing.T) {
	rt := config.RuntimeConfig{
		Sandbox: config.SandboxConfig{
			Enabled: true,
		},
	}
	cmd, args := wrapForSandbox(rt, "/work/wt-123", "/home/alex/.clankwork", "acp-adapter", []string{"--adapter", "claude"})
	if cmd != "nono" {
		t.Fatalf("cmd = %q, want nono", cmd)
	}
	want := []string{
		"run",
		"--allow", "/work/wt-123",
		"--allow", "/home/alex/.clankwork",
		"--", "acp-adapter", "--adapter", "claude",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v\nwant   %#v", args, want)
	}
}

func TestWrapForSandboxAllOptions(t *testing.T) {
	rt := config.RuntimeConfig{
		Sandbox: config.SandboxConfig{
			Enabled:         true,
			Command:         "/usr/local/bin/nono",
			Profile:         "claude-code",
			ExtraReadPaths:  []string{"/etc/ssl/certs"},
			ExtraWritePaths: []string{"/tmp/cache"},
			AllowDomains:    []string{"api.anthropic.com", "github.com"},
		},
	}
	cmd, args := wrapForSandbox(rt, "/work", "/home", "agent", []string{"-x"})
	if cmd != "/usr/local/bin/nono" {
		t.Fatalf("cmd = %q, want /usr/local/bin/nono", cmd)
	}
	got := strings.Join(args, " ")
	for _, sub := range []string{
		"run",
		"--allow /work",
		"--allow /home",
		"--read /etc/ssl/certs",
		"--allow /tmp/cache",
		"--profile claude-code",
		"--allow-domain api.anthropic.com",
		"--allow-domain github.com",
		"-- agent -x",
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("args missing %q: %q", sub, got)
		}
	}
	if strings.Contains(got, "--block-net") {
		t.Errorf("unexpected --block-net in: %q", got)
	}
}

func TestWrapForSandboxBlockNet(t *testing.T) {
	rt := config.RuntimeConfig{
		Sandbox: config.SandboxConfig{
			Enabled:  true,
			BlockNet: true,
		},
	}
	_, args := wrapForSandbox(rt, "/work", "/home", "agent", nil)
	got := strings.Join(args, " ")
	if !strings.Contains(got, "--block-net") {
		t.Errorf("expected --block-net in: %q", got)
	}
}

func TestWrapForSandboxArgsTerminator(t *testing.T) {
	// Ensure the agent command is always after `--` so flags meant for the
	// agent never get parsed by nono.
	rt := config.RuntimeConfig{
		Sandbox: config.SandboxConfig{Enabled: true},
	}
	_, args := wrapForSandbox(rt, "/w", "/h", "agent", []string{"--profile", "shouldnotparseasnonoflag"})
	dashIdx := -1
	for i, a := range args {
		if a == "--" {
			dashIdx = i
			break
		}
	}
	if dashIdx < 0 {
		t.Fatalf("missing -- terminator: %#v", args)
	}
	rest := args[dashIdx+1:]
	want := []string{"agent", "--profile", "shouldnotparseasnonoflag"}
	if !reflect.DeepEqual(rest, want) {
		t.Fatalf("after --: %#v, want %#v", rest, want)
	}
}

func TestWrapForSandboxSkipsEmptyPaths(t *testing.T) {
	rt := config.RuntimeConfig{
		Sandbox: config.SandboxConfig{
			Enabled:         true,
			ExtraReadPaths:  []string{"", "/etc"},
			ExtraWritePaths: []string{""},
			AllowDomains:    []string{""},
		},
	}
	_, args := wrapForSandbox(rt, "/w", "/h", "agent", nil)
	got := strings.Join(args, " ")
	if strings.Contains(got, "--read  ") || strings.Contains(got, "--allow  ") || strings.Contains(got, "--allow-domain  ") {
		t.Fatalf("empty entries leaked into args: %q", got)
	}
	if !strings.Contains(got, "--read /etc") {
		t.Fatalf("expected --read /etc, got %q", got)
	}
}

func TestPreflightSandboxDisabled(t *testing.T) {
	if err := preflightSandbox(config.RuntimeConfig{}); err != nil {
		t.Fatalf("disabled sandbox should pass preflight: %v", err)
	}
}

func TestPreflightSandboxMissingBinary(t *testing.T) {
	rt := config.RuntimeConfig{
		Transport: config.TransportACP,
		Sandbox: config.SandboxConfig{
			Enabled: true,
			Command: "this-binary-definitely-does-not-exist-nono-xyz",
		},
	}
	err := preflightSandbox(rt)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "this-binary-definitely-does-not-exist-nono-xyz") {
		t.Errorf("error should mention binary name: %v", err)
	}
}

func TestPreflightSandboxRejectsTmuxTransport(t *testing.T) {
	rt := config.RuntimeConfig{
		Transport: config.TransportTmux,
		Sandbox: config.SandboxConfig{
			Enabled: true,
		},
	}
	err := preflightSandbox(rt)
	if err == nil {
		t.Fatal("expected error for non-acp transport")
	}
	if !strings.Contains(err.Error(), "acp transport") {
		t.Errorf("error should mention transport: %v", err)
	}
}
