package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Scheduler  SchedulerConfig          `toml:"scheduler"`
	Acceptance AcceptanceConfig         `toml:"acceptance"`
	Runtimes   map[string]RuntimeConfig `toml:"runtimes"`
}

type AcceptanceConfig struct {
	Adversarial AdversarialConfig `toml:"adversarial"`
	Risk        RiskConfig        `toml:"risk"`
}

type AdversarialConfig struct {
	Enabled           bool    `toml:"enabled"`
	SampleRate        float64 `toml:"sample_rate"`
	AlwaysForHighRisk bool    `toml:"always_for_high_risk"`
}

type RiskConfig struct {
	HighRiskLabels []string `toml:"high_risk_labels"`
	HighRiskPaths  []string `toml:"high_risk_paths"`
}

type SchedulerConfig struct {
	MaxSlots                int `toml:"max_slots"`
	HeartbeatTimeoutSec     int `toml:"heartbeat_timeout_secs"`
	TickSec                 int `toml:"tick_secs"`
	DeterministicTimeoutSec int `toml:"deterministic_timeout_sec"`
	MergeQueueMaxDepth      int `toml:"merge_queue_max_depth"`
	MergeQueueTickSec       int `toml:"merge_queue_tick_secs"`
	MergeQueueMaxAttempts   int `toml:"merge_queue_max_attempts"`
	VerifyTimeoutSec        int `toml:"verify_timeout_secs"`
	SynthesisIntervalSec    int `toml:"synthesis_interval_secs"`
	SynthesisRetryThreshold int `toml:"synthesis_retry_threshold"`
	LearningMaxAgeDays      int `toml:"learning_max_age_days"`
	LearningMaxCount        int `toml:"learning_max_count"`
	LearningMinAccessCount  int `toml:"learning_min_access_count"`
}

type RuntimeConfig struct {
	Command                 string            `toml:"command"`
	Args                    []string          `toml:"args"`
	Env                     map[string]string `toml:"env"`
	Transport               string            `toml:"transport"`                  // "tmux" or "acp"
	Model                   string            `toml:"model"`                      // model name for tracking (e.g. "claude-sonnet-4-20250514")
	EscalateAfter           int               `toml:"escalate_after"`             // 0 = disabled
	EscalateTo              string            `toml:"escalate_to"`                // name of another runtime entry
	NonInteractive          bool              `toml:"non_interactive"`            // skip tmux send-keys prompt injection; prompt comes via args
	ACPPermissionPolicy     string            `toml:"acp_permission_policy"`      // worktree, trusted, manual, deny
	ACPPermissionAllowPaths []string          `toml:"acp_permission_allow_paths"` // additional absolute path prefixes allowed by ACP policy
	ACPPermissionDenyPaths  []string          `toml:"acp_permission_deny_paths"`  // additional path/token deny markers
	ACPPermissionTimeoutSec int               `toml:"acp_permission_timeout_sec"` // manual approval timeout; 0 = default
	Sandbox                 SandboxConfig     `toml:"sandbox"`                    // OS-level sandbox (nono)
}

// SandboxConfig wraps the agent process in a kernel-enforced sandbox (nono).
// The agent's git worktree is always granted read-write; $CLANKWORK_HOME is
// always granted read-write so the agent can reach the daemon socket. All other
// access is denied unless an extra path or domain is listed below or the
// chosen profile permits it.
type SandboxConfig struct {
	Enabled         bool     `toml:"enabled"`           // off by default; opt-in per runtime
	Command         string   `toml:"command"`           // sandbox binary; defaults to "nono"
	Profile         string   `toml:"profile"`           // optional nono profile name (e.g. "claude-code")
	ExtraReadPaths  []string `toml:"extra_read_paths"`  // additional read-only paths beyond worktree+home
	ExtraWritePaths []string `toml:"extra_write_paths"` // additional read-write paths beyond worktree+home
	AllowDomains    []string `toml:"allow_domains"`     // additive network allowlist
	BlockNet        bool     `toml:"block_net"`         // explicit hard deny of all network
}

const (
	TransportTmux = "tmux"
	TransportACP  = "acp"
)

func RuntimeTransport(rt RuntimeConfig) string {
	if rt.Transport == "" {
		return TransportTmux
	}
	return rt.Transport
}

func DefaultConfig() *Config {
	return &Config{
		Scheduler: SchedulerConfig{
			MaxSlots:                4,
			HeartbeatTimeoutSec:     600,
			TickSec:                 2,
			DeterministicTimeoutSec: 1800,
			MergeQueueMaxDepth:      10,
			MergeQueueTickSec:       5,
			MergeQueueMaxAttempts:   3,
			VerifyTimeoutSec:        600,
			SynthesisIntervalSec:    3600, // 1 hour
			SynthesisRetryThreshold: 2,
			LearningMaxAgeDays:      30,
			LearningMaxCount:        1000,
			LearningMinAccessCount:  0,
		},
		Acceptance: AcceptanceConfig{
			Adversarial: AdversarialConfig{
				Enabled:           true,
				SampleRate:        0.10,
				AlwaysForHighRisk: true,
			},
			Risk: RiskConfig{
				HighRiskLabels: []string{"auth", "payments", "permissions", "data-deletion", "migration", "infra", "iam", "public-api"},
				HighRiskPaths: []string{
					"internal/auth/**",
					"internal/billing/**",
					"migrations/**",
					"infra/**",
				},
			},
		},
		Runtimes: map[string]RuntimeConfig{
			"default": {
				Command:             "acp-adapter",
				Args:                []string{"--adapter", "pi"},
				Transport:           TransportACP,
				Model:               "pi",
				ACPPermissionPolicy: "worktree",
			},
			"claude": {
				Command:        "claude",
				Args:           []string{"--dangerously-skip-permissions"},
				Transport:      TransportTmux,
				Model:          "claude-sonnet-4-6",
				NonInteractive: false,
			},
			"pi": {
				Command:        "pi",
				Args:           []string{},
				Transport:      TransportTmux,
				Model:          "pi",
				NonInteractive: true,
			},
			"pi-acp": {
				Command:             "acp-adapter",
				Args:                []string{"--adapter", "pi"},
				Transport:           TransportACP,
				Model:               "pi",
				ACPPermissionPolicy: "worktree",
			},
			"claude-acp": {
				Command:             "acp-adapter",
				Args:                []string{"--adapter", "claude"},
				Transport:           TransportACP,
				Model:               "claude",
				ACPPermissionPolicy: "worktree",
			},
			"frontier": {
				Command:        "claude",
				Args:           []string{"--dangerously-skip-permissions"},
				Transport:      TransportTmux,
				Model:          "claude-opus-4-7",
				NonInteractive: false,
				EscalateAfter:  0,
			},
		},
	}
}

// Load reads $homeDir/config.toml and merges it over DefaultConfig.
// Missing file is not an error — defaults are returned.
func Load(homeDir string) (*Config, error) {
	cfg := DefaultConfig()
	path := filepath.Join(homeDir, "config.toml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, err
	}
	normalize(cfg)
	return cfg, nil
}

func normalize(cfg *Config) {
	for name, rt := range cfg.Runtimes {
		if rt.Transport == "" {
			rt.Transport = TransportTmux
		}
		if rt.Transport == TransportACP && rt.ACPPermissionPolicy == "" {
			rt.ACPPermissionPolicy = "worktree"
		}
		cfg.Runtimes[name] = rt
	}
}
