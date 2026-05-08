package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHomeResolution(t *testing.T) {
	userHome, _ := os.UserHomeDir()

	tests := []struct {
		name      string
		flagValue string
		envValue  string
		want      string
	}{
		{
			name: "default is ~/.clankwork",
			want: filepath.Join(userHome, ".clankwork"),
		},
		{
			name:     "env takes precedence over default",
			envValue: "/tmp/testenv",
			want:     "/tmp/testenv",
		},
		{
			name:      "flag takes precedence over env",
			flagValue: "/tmp/testflag",
			envValue:  "/tmp/testenv",
			want:      "/tmp/testflag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Unsetenv("CLANKWORK_HOME")
			if tt.envValue != "" {
				os.Setenv("CLANKWORK_HOME", tt.envValue)
				defer os.Unsetenv("CLANKWORK_HOME")
			}
			got, err := Home(tt.flagValue)
			if err != nil {
				t.Fatalf("Home() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("Home() = %q, want %q", got, tt.want)
			}
		})
	}
}
