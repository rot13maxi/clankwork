package runtimeenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/rot13maxi/clankwork/internal/config"
)

// Build returns the environment for a runtime, including any transport-specific
// overrides Clankwork needs to keep the runtime deterministic and writable.
func Build(homeDir, runtimeName string, rt config.RuntimeConfig, base map[string]string) (map[string]string, error) {
	env := clone(base)
	for k, v := range rt.Env {
		env[k] = v
	}
	if config.RuntimeTransport(rt) != config.TransportACP {
		return env, nil
	}
	prependPath(env, filepath.Join(homeDir, "bin"))
	if acpAdapter(rt) != "pi" {
		return env, nil
	}

	agentDir := filepath.Join(homeDir, "runtime-state", sanitize(runtimeName), "pi-agent")
	sessionDir := filepath.Join(agentDir, "sessions")
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return nil, err
	}

	// Preserve existing Pi login state when possible, but keep mutable runtime
	// state inside CLANKWORK_HOME so the adapter does not need to lock ~/.pi.
	if home := env["HOME"]; home != "" {
		copyIfMissing(filepath.Join(home, ".pi", "agent", "auth.json"), filepath.Join(agentDir, "auth.json"))
		copyIfMissing(filepath.Join(home, ".pi", "agent", "settings.json"), filepath.Join(agentDir, "settings.json"))
		copyIfMissing(filepath.Join(home, ".pi", "agent", "models.json"), filepath.Join(agentDir, "models.json"))
	}

	env["PI_CODING_AGENT_DIR"] = agentDir
	env["PI_SESSION_DIR"] = sessionDir
	return env, nil
}

type Summary struct {
	Provider       string
	ProviderSource string
	AuthMode       string
	AuthSource     string
	AgentDir       string
	SessionDir     string
	Adapter        string
}

func Describe(homeDir, runtimeName string, rt config.RuntimeConfig, env map[string]string) Summary {
	s := Summary{Adapter: acpAdapter(rt)}
	if s.Adapter != "pi" {
		return s
	}
	if provider, src := piProvider(rt.Args, env); provider != "" {
		s.Provider = provider
		s.ProviderSource = src
	} else if provider, src := piProviderFromSettings(env); provider != "" {
		s.Provider = provider
		s.ProviderSource = src
	}
	if mode, src := piAuth(env); mode != "" {
		s.AuthMode = mode
		s.AuthSource = src
	}
	agentDir := env["PI_CODING_AGENT_DIR"]
	if agentDir == "" {
		agentDir = filepath.Join(homeDir, "runtime-state", sanitize(runtimeName), "pi-agent")
	}
	s.AgentDir = agentDir
	sessionDir := env["PI_SESSION_DIR"]
	if sessionDir == "" {
		sessionDir = filepath.Join(agentDir, "sessions")
	}
	s.SessionDir = sessionDir
	return s
}

func prependPath(env map[string]string, dir string) {
	if dir == "" {
		return
	}
	if env["PATH"] == "" {
		env["PATH"] = dir
		return
	}
	for _, part := range filepath.SplitList(env["PATH"]) {
		if part == dir {
			return
		}
	}
	env["PATH"] = dir + string(os.PathListSeparator) + env["PATH"]
}

func acpAdapter(rt config.RuntimeConfig) string {
	for i := 0; i < len(rt.Args)-1; i++ {
		if rt.Args[i] == "--adapter" {
			return strings.TrimSpace(rt.Args[i+1])
		}
	}
	return ""
}

func clone(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func sanitize(name string) string {
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, " ", "-")
	if name == "" {
		return "default"
	}
	return name
}

func copyIfMissing(src, dst string) {
	if _, err := os.Stat(dst); err == nil {
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	_ = os.WriteFile(dst, data, 0600)
}

func piProvider(args []string, env map[string]string) (string, string) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--pi-provider" || args[i] == "--provider" {
			return args[i+1], "args"
		}
	}
	if v := env["PI_PROVIDER"]; v != "" {
		return v, "env"
	}
	return "", ""
}

func piAuth(env map[string]string) (string, string) {
	keys := []string{
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"GROQ_API_KEY",
		"CEREBRAS_API_KEY",
		"XAI_API_KEY",
		"OPENROUTER_API_KEY",
		"AI_GATEWAY_API_KEY",
		"ZAI_API_KEY",
		"MISTRAL_API_KEY",
		"MINIMAX_API_KEY",
		"OPENCODE_API_KEY",
		"KIMI_API_KEY",
		"AWS_BEARER_TOKEN_BEDROCK",
		"AWS_ACCESS_KEY_ID",
	}
	for _, key := range keys {
		if env[key] != "" {
			return "api-key-env", key
		}
	}
	if path := env["PI_CODING_AGENT_DIR"]; path != "" {
		if _, err := os.Stat(filepath.Join(path, "auth.json")); err == nil {
			return "login-state", filepath.Join(path, "auth.json")
		}
	}
	return "unknown", ""
}

func piProviderFromSettings(env map[string]string) (string, string) {
	path := env["PI_CODING_AGENT_DIR"]
	if path == "" {
		return "", ""
	}
	data, err := os.ReadFile(filepath.Join(path, "settings.json"))
	if err != nil {
		return "", ""
	}
	var cfg struct {
		DefaultProvider string `json:"defaultProvider"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", ""
	}
	if strings.TrimSpace(cfg.DefaultProvider) == "" {
		return "", ""
	}
	return cfg.DefaultProvider, "settings"
}
