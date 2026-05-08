package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rot13maxi/clankwork/internal/config"
)

func TestACPDoctorTargets(t *testing.T) {
	cfg := &config.Config{
		Runtimes: map[string]config.RuntimeConfig{
			"tmux":  {Command: "bash", Transport: config.TransportTmux},
			"alpha": {Command: "alpha-adapter", Args: []string{"--adapter", "alpha"}, Transport: config.TransportACP},
			"beta":  {Command: "beta-adapter", Args: []string{"--adapter", "beta"}, Transport: config.TransportACP},
		},
	}

	targets, err := acpDoctorTargets(cfg, "")
	if err != nil {
		t.Fatalf("acpDoctorTargets: %v", err)
	}
	if got, want := len(targets), 2; got != want {
		t.Fatalf("target count = %d, want %d", got, want)
	}
	if targets[0].Name != "alpha" || targets[1].Name != "beta" {
		t.Fatalf("target order = %q, %q; want alpha, beta", targets[0].Name, targets[1].Name)
	}

	filtered, err := acpDoctorTargets(cfg, "beta")
	if err != nil {
		t.Fatalf("filtered acpDoctorTargets: %v", err)
	}
	if got, want := len(filtered), 1; got != want || filtered[0].Name != "beta" {
		t.Fatalf("filtered targets = %+v, want beta", filtered)
	}

	if _, err := acpDoctorTargets(cfg, "tmux"); err == nil || !strings.Contains(err.Error(), "uses transport") {
		t.Fatalf("acpDoctorTargets(tmux) error = %v, want transport mismatch", err)
	}
}

func TestDiagnoseACPDoctorTargetWithoutHandshake(t *testing.T) {
	bin := os.Args[0]
	target := acpDoctorTarget{
		Name: "helper",
		Runtime: config.RuntimeConfig{
			Command:   bin,
			Transport: config.TransportACP,
		},
	}

	var out bytes.Buffer
	if err := diagnoseACPDoctorTarget(context.Background(), t.TempDir(), target, false, &out); err != nil {
		t.Fatalf("diagnoseACPDoctorTarget: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		`runtime "helper"`,
		`transport: acp`,
		`command: ` + bin,
		`args: []`,
		`handshake: skipped`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestDiagnoseACPDoctorTargetFindsAdapterInHomeBin(t *testing.T) {
	t.Setenv("PATH", "")
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	adapter := filepath.Join(binDir, "acp-adapter")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	target := acpDoctorTarget{
		Name: "helper",
		Runtime: config.RuntimeConfig{
			Command:   "acp-adapter",
			Transport: config.TransportACP,
		},
	}

	var out bytes.Buffer
	if err := diagnoseACPDoctorTarget(context.Background(), home, target, false, &out); err != nil {
		t.Fatalf("diagnoseACPDoctorTarget: %v", err)
	}
	if text := out.String(); !strings.Contains(text, "command_path: "+adapter) {
		t.Fatalf("output missing home bin adapter path:\n%s", text)
	}
}

func TestDiagnoseACPDoctorTargetMissingAdapterHasInstallHint(t *testing.T) {
	t.Setenv("PATH", "")
	target := acpDoctorTarget{
		Name: "helper",
		Runtime: config.RuntimeConfig{
			Command:   "acp-adapter",
			Transport: config.TransportACP,
		},
	}

	err := diagnoseACPDoctorTarget(context.Background(), t.TempDir(), target, false, &bytes.Buffer{})
	if err == nil {
		t.Fatal("diagnoseACPDoctorTarget succeeded, want missing adapter error")
	}
	if !strings.Contains(err.Error(), "make install-acp-adapter") {
		t.Fatalf("error missing install hint: %v", err)
	}
}

func TestRunACPDoctorHandshake(t *testing.T) {
	home := t.TempDir()
	helper := filepath.Join(home, "acp-helper.sh")
	helperScript := "#!/bin/sh\nCLANKWORK_ACP_DOCTOR_HELPER=1 \"" + os.Args[0] + "\" -test.run=TestACPDoctorHelperProcess\n"
	if err := os.WriteFile(helper, []byte(helperScript), 0700); err != nil {
		t.Fatalf("WriteFile helper: %v", err)
	}
	configToml := `
[runtimes.helper]
command = "` + helper + `"
transport = "acp"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configToml), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	var out bytes.Buffer
	if err := runACPDoctor(context.Background(), home, cfg, "helper", true, &out); err != nil {
		t.Fatalf("runACPDoctor: %v\n%s", err, out.String())
	}

	text := out.String()
	for _, want := range []string{
		`clankwork acp doctor`,
		`runtime "helper"`,
		`transport: acp`,
		`handshake: ok`,
		`prompt: ok`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestACPDoctorHelperProcess(t *testing.T) {
	if os.Getenv("CLANKWORK_ACP_DOCTOR_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			writeACPDoctorHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"promptCapabilities": map[string]bool{},
					},
				},
			})
		case "session/new":
			writeACPDoctorHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"sessionId": "doctor-session-1"},
			})
		case "session/prompt":
			writeACPDoctorHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"stopReason": "end_turn"},
			})
		default:
			writeACPDoctorHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32601, "message": "not found"},
			})
		}
	}
	os.Exit(0)
}

func writeACPDoctorHelper(v any) {
	b, _ := json.Marshal(v)
	_, _ = os.Stdout.Write(append(b, '\n'))
}
