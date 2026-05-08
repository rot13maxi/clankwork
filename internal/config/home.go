package config

import (
	"os"
	"path/filepath"
)

// Home resolves $CLANKWORK_HOME with precedence: flag value > env > ~/.clankwork.
// Pass an empty string for flagValue when no --home flag was provided.
func Home(flagValue string) (string, error) {
	if flagValue != "" {
		return filepath.Abs(flagValue)
	}
	if env := os.Getenv("CLANKWORK_HOME"); env != "" {
		return filepath.Abs(env)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clankwork"), nil
}
