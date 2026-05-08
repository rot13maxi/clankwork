package template

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed builtin/*.toml
var builtinFS embed.FS

// Template is decoded from a TOML file.
// Top-level TOML keys map to top-level struct fields (no [template] section).
type Template struct {
	Name        string          `toml:"name"`
	Description string          `toml:"description"`
	Entry       string          `toml:"entry"`
	AutoMerge   bool            `toml:"auto_merge"`
	Steps       map[string]Step `toml:"steps"`
}

// Step is one node in the workflow DAG.
type Step struct {
	Type       string   `toml:"type"`        // "agent" or "deterministic"
	Role       string   `toml:"role"`
	Runtime    string   `toml:"runtime"`
	Command    string   `toml:"command"`     // binary name for exec.Command (not a shell string)
	Args       []string `toml:"args"`
	MaxRetries int      `toml:"max_retries"` // 0 = unlimited; N > 0 = fail after N attempts
	OnSuccess  string   `toml:"on_success"`
	OnFailure  string   `toml:"on_failure"`
}

// Validate checks a parsed template for structural correctness.
func Validate(t *Template) error {
	if t.Name == "" {
		return fmt.Errorf("template missing name")
	}
	if t.Entry == "" {
		return fmt.Errorf("template %q: missing entry", t.Name)
	}
	if _, ok := t.Steps[t.Entry]; !ok {
		return fmt.Errorf("template %q: entry step %q not found in steps", t.Name, t.Entry)
	}
	for name, step := range t.Steps {
		switch step.Type {
		case "agent":
			// runtime can be empty (falls back to task.Runtime or "default")
		case "deterministic":
			if step.Command == "" {
				return fmt.Errorf("template %q step %q: deterministic step requires command", t.Name, name)
			}
		default:
			return fmt.Errorf("template %q step %q: unknown type %q (want agent or deterministic)", t.Name, name, step.Type)
		}
		if step.OnSuccess != "complete" && step.OnSuccess != "" {
			if _, ok := t.Steps[step.OnSuccess]; !ok {
				return fmt.Errorf("template %q step %q: on_success %q not found", t.Name, name, step.OnSuccess)
			}
		}
		if step.OnFailure != "complete" && step.OnFailure != "" {
			if _, ok := t.Steps[step.OnFailure]; !ok {
				return fmt.Errorf("template %q step %q: on_failure %q not found", t.Name, name, step.OnFailure)
			}
		}
	}
	return nil
}

// Load finds and parses a template by name.
// Search order: repoPath/templates/<name>.toml → homeDir/templates/<name>.toml → embedded built-ins.
func Load(name, repoPath, homeDir string) (*Template, error) {
	candidates := []string{}
	if repoPath != "" {
		candidates = append(candidates, filepath.Join(repoPath, "templates", name+".toml"))
	}
	if homeDir != "" {
		candidates = append(candidates, filepath.Join(homeDir, "templates", name+".toml"))
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read template %q: %w", path, err)
		}
		return parse(data, path)
	}

	// Fall back to embedded built-ins.
	data, err := builtinFS.ReadFile("builtin/" + name + ".toml")
	if err != nil {
		return nil, fmt.Errorf("template %q not found (checked repo, home, and built-ins)", name)
	}
	return parse(data, "builtin:"+name)
}

func parse(data []byte, source string) (*Template, error) {
	var t Template
	if _, err := toml.Decode(string(data), &t); err != nil {
		return nil, fmt.Errorf("parse template %q: %w", source, err)
	}
	if err := Validate(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// TemplateInfo describes a discovered template.
type TemplateInfo struct {
	Name   string
	Source string // "repo", "home", "builtin"
}

// List returns all available templates from all search locations.
// Later entries in the list are overridden by earlier ones (repo > home > builtin).
func List(repoPath, homeDir string) ([]TemplateInfo, error) {
	seen := map[string]bool{}
	var result []TemplateInfo

	search := func(dir, source string) error {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".toml")
			if !seen[name] {
				seen[name] = true
				result = append(result, TemplateInfo{Name: name, Source: source})
			}
		}
		return nil
	}

	if repoPath != "" {
		if err := search(filepath.Join(repoPath, "templates"), "repo"); err != nil {
			return nil, err
		}
	}
	if homeDir != "" {
		if err := search(filepath.Join(homeDir, "templates"), "home"); err != nil {
			return nil, err
		}
	}

	// Built-ins.
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		if !seen[name] {
			seen[name] = true
			result = append(result, TemplateInfo{Name: name, Source: "builtin"})
		}
	}

	return result, nil
}
