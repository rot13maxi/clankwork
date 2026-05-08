package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTUICommandQuotesHomeAndMode(t *testing.T) {
	got := tuiCommand(workspaceConfig{
		Exe:  "/tmp/clank work/bin/clankwork",
		Home: "/tmp/clank'home",
	}, "--escalations")

	if !strings.Contains(got, "'/tmp/clank work/bin/clankwork'") {
		t.Fatalf("command = %q, missing quoted executable", got)
	}
	if !strings.Contains(got, "--home '/tmp/clank'\"'\"'home'") {
		t.Fatalf("command = %q, missing quoted home", got)
	}
	if !strings.HasSuffix(got, "tui --escalations") {
		t.Fatalf("command = %q, missing tui mode", got)
	}
}

func TestWorkspaceCommandsUseFocusedTUIPanes(t *testing.T) {
	cmds := workspaceCommands(workspaceConfig{
		Session: "cw",
		Agent:   "codex",
		Cwd:     "/repo",
		Exe:     "/repo/bin/clankwork",
		Home:    "/home/user/.clankwork",
	})
	joined := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		joined = append(joined, strings.Join(cmd.args, " "))
	}
	all := strings.Join(joined, "\n")

	for _, want := range []string{"new-session", "tui --escalations", "tui --tasks", "tui --merge-queue", "tui --health", "tui --events", "select-pane"} {
		if !strings.Contains(all, want) {
			t.Fatalf("workspace commands missing %q:\n%s", want, all)
		}
	}
	if strings.Contains(all, " -p ") {
		t.Fatalf("workspace commands should use portable -l percent sizes, got:\n%s", all)
	}
	if !strings.Contains(all, " -l 34% ") {
		t.Fatalf("workspace commands missing percent split size:\n%s", all)
	}
}

func TestWorkspaceIntroSeenMarker(t *testing.T) {
	home := t.TempDir()
	if workspaceIntroSeen(home) {
		t.Fatal("intro should not be marked seen before marker exists")
	}
	if err := markWorkspaceIntroSeen(home); err != nil {
		t.Fatalf("markWorkspaceIntroSeen: %v", err)
	}
	if !workspaceIntroSeen(home) {
		t.Fatal("intro should be marked seen after marker write")
	}
	if _, err := os.Stat(filepath.Join(home, "workspace-intro-seen")); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
}

func TestMaybeShowWorkspaceIntro(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer

	if err := maybeShowWorkspaceIntro(workspaceConfig{Home: home, Out: &out}); err != nil {
		t.Fatalf("maybeShowWorkspaceIntro: %v", err)
	}
	if !strings.Contains(out.String(), "Clankwork workspace controls") {
		t.Fatalf("intro output missing controls:\n%s", out.String())
	}

	out.Reset()
	if err := maybeShowWorkspaceIntro(workspaceConfig{Home: home, Out: &out}); err != nil {
		t.Fatalf("maybeShowWorkspaceIntro second call: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("intro should not repeat without --intro, got:\n%s", out.String())
	}

	if err := maybeShowWorkspaceIntro(workspaceConfig{Home: home, Out: &out, Intro: true}); err != nil {
		t.Fatalf("forced maybeShowWorkspaceIntro: %v", err)
	}
	if !strings.Contains(out.String(), "Clankwork workspace controls") {
		t.Fatalf("forced intro output missing controls:\n%s", out.String())
	}
}
