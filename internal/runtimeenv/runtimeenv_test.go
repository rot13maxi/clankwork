package runtimeenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rot13maxi/clankwork/internal/config"
)

func TestBuildPiACPEnvRedirectsWritableStateAndCopiesLoginState(t *testing.T) {
	homeDir := t.TempDir()
	userHome := filepath.Join(t.TempDir(), "user-home")
	srcAgentDir := filepath.Join(userHome, ".pi", "agent")
	if err := os.MkdirAll(srcAgentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcAgentDir, "auth.json"), []byte(`{"ok":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcAgentDir, "settings.json"), []byte(`{"provider":"google"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcAgentDir, "models.json"), []byte(`{"providers":{"Forge":{"apiKey":"secret"}}}`), 0600); err != nil {
		t.Fatal(err)
	}

	env, err := Build(homeDir, "pi-acp", config.RuntimeConfig{
		Command:   "acp-adapter",
		Args:      []string{"--adapter", "pi"},
		Transport: config.TransportACP,
	}, map[string]string{
		"HOME": userHome,
		"PATH": "/usr/bin",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	agentDir := filepath.Join(homeDir, "runtime-state", "pi-acp", "pi-agent")
	if got := env["PI_CODING_AGENT_DIR"]; got != agentDir {
		t.Fatalf("PI_CODING_AGENT_DIR = %q, want %q", got, agentDir)
	}
	if got := env["PI_SESSION_DIR"]; got != filepath.Join(agentDir, "sessions") {
		t.Fatalf("PI_SESSION_DIR = %q", got)
	}
	if got := env["HOME"]; got != userHome {
		t.Fatalf("HOME = %q, want %q", got, userHome)
	}
	if got, want := env["PATH"], filepath.Join(homeDir, "bin")+string(os.PathListSeparator)+"/usr/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(filepath.Join(agentDir, "auth.json")); err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("copied auth.json = %q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(agentDir, "settings.json")); err != nil || string(data) != `{"provider":"google"}` {
		t.Fatalf("copied settings.json = %q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(filepath.Join(agentDir, "models.json")); err != nil || string(data) != `{"providers":{"Forge":{"apiKey":"secret"}}}` {
		t.Fatalf("copied models.json = %q err=%v", string(data), err)
	}
}

func TestBuildLeavesNonPiRuntimeUnchanged(t *testing.T) {
	homeDir := t.TempDir()
	base := map[string]string{"HOME": "/tmp/home", "PATH": "/usr/bin"}
	env, err := Build(homeDir, "claude-acp", config.RuntimeConfig{
		Command:   "acp-adapter",
		Args:      []string{"--adapter", "claude"},
		Transport: config.TransportACP,
	}, base)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := env["PATH"], filepath.Join(homeDir, "bin")+string(os.PathListSeparator)+"/usr/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
	if _, ok := env["PI_CODING_AGENT_DIR"]; ok {
		t.Fatal("PI_CODING_AGENT_DIR unexpectedly set")
	}
}

func TestBuildPrependsHomeBinForACPRuntime(t *testing.T) {
	homeDir := t.TempDir()
	env, err := Build(homeDir, "claude-acp", config.RuntimeConfig{
		Command:   "acp-adapter",
		Args:      []string{"--adapter", "claude"},
		Transport: config.TransportACP,
	}, map[string]string{
		"HOME": "/tmp/home",
		"PATH": "/usr/bin",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := env["PATH"], filepath.Join(homeDir, "bin")+string(os.PathListSeparator)+"/usr/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestBuildMergesRuntimeEnvOverrides(t *testing.T) {
	env, err := Build(t.TempDir(), "pi-acp", config.RuntimeConfig{
		Command:   "acp-adapter",
		Args:      []string{"--adapter", "pi"},
		Env:       map[string]string{"PI_PROVIDER": "openai", "OPENAI_API_KEY": "secret"},
		Transport: config.TransportACP,
	}, map[string]string{
		"HOME": "/tmp/home",
		"PATH": "/usr/bin",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := env["PI_PROVIDER"]; got != "openai" {
		t.Fatalf("PI_PROVIDER = %q, want openai", got)
	}
	if got := env["OPENAI_API_KEY"]; got != "secret" {
		t.Fatalf("OPENAI_API_KEY = %q, want secret", got)
	}
}

func TestDescribePiRuntimeReportsProviderAndAuthMode(t *testing.T) {
	homeDir := t.TempDir()
	agentDir := filepath.Join(homeDir, "runtime-state", "pi-acp", "pi-agent")
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "auth.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"defaultProvider":"Forge"}`), 0600); err != nil {
		t.Fatal(err)
	}
	summary := Describe(homeDir, "pi-acp", config.RuntimeConfig{
		Command:   "acp-adapter",
		Args:      []string{"--adapter", "pi"},
		Transport: config.TransportACP,
	}, map[string]string{
		"PI_CODING_AGENT_DIR": agentDir,
		"PI_SESSION_DIR":      filepath.Join(agentDir, "sessions"),
	})
	if summary.Provider != "Forge" || summary.ProviderSource != "settings" {
		t.Fatalf("provider = %q/%q, want Forge/settings", summary.Provider, summary.ProviderSource)
	}
	if summary.AuthMode != "login-state" {
		t.Fatalf("auth mode = %q, want login-state", summary.AuthMode)
	}
}
