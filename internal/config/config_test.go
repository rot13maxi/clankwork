package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Scheduler.MaxSlots != 4 {
		t.Errorf("max_slots = %d, want 4", cfg.Scheduler.MaxSlots)
	}
	if _, ok := cfg.Runtimes["default"]; !ok {
		t.Error("default runtime missing")
	}
	if got := RuntimeTransport(cfg.Runtimes["default"]); got != TransportACP {
		t.Errorf("default transport = %q, want %q", got, TransportACP)
	}
	if got := cfg.Runtimes["default"].ACPPermissionPolicy; got != "worktree" {
		t.Errorf("default acp_permission_policy = %q, want worktree", got)
	}
	if !cfg.Acceptance.Adversarial.Enabled || cfg.Acceptance.Adversarial.SampleRate != 0.10 || !cfg.Acceptance.Adversarial.AlwaysForHighRisk {
		t.Fatalf("default adversarial config = %+v", cfg.Acceptance.Adversarial)
	}
	if len(cfg.Acceptance.Risk.HighRiskLabels) == 0 || len(cfg.Acceptance.Risk.HighRiskPaths) == 0 {
		t.Fatalf("default risk config = %+v", cfg.Acceptance.Risk)
	}
}

func TestAcceptanceConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	toml := `
[acceptance.adversarial]
enabled = true
sample_rate = 0.25
always_for_high_risk = false

[acceptance.risk]
high_risk_labels = ["security"]
high_risk_paths = ["pkg/security/**"]
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Acceptance.Adversarial.Enabled {
		t.Fatal("adversarial config should be enabled")
	}
	if cfg.Acceptance.Adversarial.SampleRate != 0.25 {
		t.Fatalf("sample_rate = %.2f, want 0.25", cfg.Acceptance.Adversarial.SampleRate)
	}
	if cfg.Acceptance.Adversarial.AlwaysForHighRisk {
		t.Fatal("always_for_high_risk should be false")
	}
	if len(cfg.Acceptance.Risk.HighRiskLabels) != 1 || cfg.Acceptance.Risk.HighRiskLabels[0] != "security" {
		t.Fatalf("high_risk_labels = %+v, want [security]", cfg.Acceptance.Risk.HighRiskLabels)
	}
	if len(cfg.Acceptance.Risk.HighRiskPaths) != 1 || cfg.Acceptance.Risk.HighRiskPaths[0] != "pkg/security/**" {
		t.Fatalf("high_risk_paths = %+v, want [pkg/security/**]", cfg.Acceptance.Risk.HighRiskPaths)
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scheduler.MaxSlots != 4 {
		t.Errorf("max_slots = %d, want 4", cfg.Scheduler.MaxSlots)
	}
}

func TestRuntimeEscalationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	toml := `
[runtimes.sonnet]
command        = "claude"
args           = ["--model", "claude-sonnet-4-5"]
escalate_after = 2
escalate_to    = "opus"

[runtimes.opus]
command = "claude"
args    = ["--model", "claude-opus-4-5"]
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	sonnet, ok := cfg.Runtimes["sonnet"]
	if !ok {
		t.Fatal("sonnet runtime missing")
	}
	if sonnet.EscalateAfter != 2 {
		t.Errorf("EscalateAfter = %d, want 2", sonnet.EscalateAfter)
	}
	if sonnet.EscalateTo != "opus" {
		t.Errorf("EscalateTo = %q, want \"opus\"", sonnet.EscalateTo)
	}

	opus, ok := cfg.Runtimes["opus"]
	if !ok {
		t.Fatal("opus runtime missing")
	}
	// Zero values mean escalation is disabled for the escalated runtime itself.
	if opus.EscalateAfter != 0 {
		t.Errorf("opus EscalateAfter = %d, want 0 (disabled)", opus.EscalateAfter)
	}
	if opus.EscalateTo != "" {
		t.Errorf("opus EscalateTo = %q, want empty (disabled)", opus.EscalateTo)
	}
}

func TestLoadOverride(t *testing.T) {
	dir := t.TempDir()
	toml := `
[scheduler]
max_slots = 8

[runtimes.claude]
command = "claude"
args    = []
transport = "acp"
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Scheduler.MaxSlots != 8 {
		t.Errorf("max_slots = %d, want 8", cfg.Scheduler.MaxSlots)
	}
	if _, ok := cfg.Runtimes["claude"]; !ok {
		t.Error("claude runtime missing")
	}
	if got := RuntimeTransport(cfg.Runtimes["claude"]); got != TransportACP {
		t.Errorf("claude transport = %q, want %q", got, TransportACP)
	}
	if got := cfg.Runtimes["claude"].ACPPermissionPolicy; got != "worktree" {
		t.Errorf("claude acp_permission_policy = %q, want worktree", got)
	}
}

func TestACPPermissionConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	toml := `
[runtimes.pi-acp]
command = "acp-adapter"
args = ["--adapter", "pi"]
transport = "acp"
env = { PI_PROVIDER = "openai", OPENAI_API_KEY = "test-key" }
acp_permission_policy = "manual"
acp_permission_allow_paths = ["/tmp/allowed"]
acp_permission_deny_paths = ["~/.aws"]
acp_permission_timeout_sec = 12
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rt := cfg.Runtimes["pi-acp"]
	if rt.ACPPermissionPolicy != "manual" || rt.ACPPermissionTimeoutSec != 12 {
		t.Fatalf("permission config = policy %q timeout %d, want manual/12", rt.ACPPermissionPolicy, rt.ACPPermissionTimeoutSec)
	}
	if got := rt.Env["PI_PROVIDER"]; got != "openai" {
		t.Fatalf("PI_PROVIDER = %q, want openai", got)
	}
	if got := rt.Env["OPENAI_API_KEY"]; got != "test-key" {
		t.Fatalf("OPENAI_API_KEY = %q, want test-key", got)
	}
	if len(rt.ACPPermissionAllowPaths) != 1 || rt.ACPPermissionAllowPaths[0] != "/tmp/allowed" {
		t.Fatalf("allow paths = %#v", rt.ACPPermissionAllowPaths)
	}
	if len(rt.ACPPermissionDenyPaths) != 1 || rt.ACPPermissionDenyPaths[0] != "~/.aws" {
		t.Fatalf("deny paths = %#v", rt.ACPPermissionDenyPaths)
	}
}

func TestSandboxConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	toml := `
[runtimes.claude-acp]
command = "acp-adapter"
args = ["--adapter", "claude"]
transport = "acp"

  [runtimes.claude-acp.sandbox]
  enabled = true
  profile = "claude-code"
  command = "/usr/local/bin/nono"
  extra_read_paths = ["/etc/ssl/certs"]
  extra_write_paths = ["/tmp/agent-cache"]
  allow_domains = ["api.anthropic.com", "github.com"]
  block_net = false
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rt, ok := cfg.Runtimes["claude-acp"]
	if !ok {
		t.Fatal("claude-acp runtime missing")
	}
	sb := rt.Sandbox
	if !sb.Enabled {
		t.Fatal("sandbox should be enabled")
	}
	if sb.Profile != "claude-code" {
		t.Errorf("profile = %q, want claude-code", sb.Profile)
	}
	if sb.Command != "/usr/local/bin/nono" {
		t.Errorf("command = %q, want /usr/local/bin/nono", sb.Command)
	}
	if len(sb.ExtraReadPaths) != 1 || sb.ExtraReadPaths[0] != "/etc/ssl/certs" {
		t.Errorf("extra_read_paths = %#v", sb.ExtraReadPaths)
	}
	if len(sb.ExtraWritePaths) != 1 || sb.ExtraWritePaths[0] != "/tmp/agent-cache" {
		t.Errorf("extra_write_paths = %#v", sb.ExtraWritePaths)
	}
	if len(sb.AllowDomains) != 2 {
		t.Errorf("allow_domains = %#v", sb.AllowDomains)
	}
}

func TestSandboxDisabledByDefault(t *testing.T) {
	cfg := DefaultConfig()
	for name, rt := range cfg.Runtimes {
		if rt.Sandbox.Enabled {
			t.Errorf("runtime %q has sandbox enabled by default", name)
		}
	}
}

func TestLoadNormalizesRuntimeTransport(t *testing.T) {
	dir := t.TempDir()
	toml := `
[runtimes.custom]
command = "pi"
args    = []
`
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Runtimes["custom"].Transport; got != TransportTmux {
		t.Errorf("custom transport = %q, want %q", got, TransportTmux)
	}
}
