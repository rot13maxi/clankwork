package worker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestACPRuntimeSpawnAndPrompt(t *testing.T) {
	rt := NewACPRuntime(t.TempDir())
	err := rt.Spawn("acp-test", t.TempDir(), os.Args[0], []string{"-test.run=TestACPHelperProcess"}, map[string]string{
		"CLANKWORK_ACP_HELPER": "1",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = rt.Kill("acp-test") })

	if alive, err := rt.IsAlive("acp-test"); err != nil || !alive {
		t.Fatalf("IsAlive = %v, %v; want alive", alive, err)
	}
	if err := rt.SendInitialPrompt("acp-test", "hello agent"); err != nil {
		t.Fatalf("SendInitialPrompt: %v", err)
	}
	out, err := rt.CapturePane("acp-test", 20)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if !strings.Contains(out, "agent_message_chunk") {
		t.Fatalf("CapturePane missing ACP update:\n%s", out)
	}
	if last, err := rt.PaneLastActivity("acp-test"); err != nil || time.Since(last) > time.Minute {
		t.Fatalf("PaneLastActivity = %v, %v; want recent", last, err)
	}
}

func TestResolveCommandUsesRuntimePATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "acp-adapter")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}

	resolved, err := ResolveCommand("acp-adapter", map[string]string{"PATH": dir})
	if err != nil {
		t.Fatalf("ResolveCommand: %v", err)
	}
	if resolved != bin {
		t.Fatalf("resolved = %q, want %q", resolved, bin)
	}
}

func TestResolveCommandMissingACPAdapterHasInstallHint(t *testing.T) {
	_, err := ResolveCommand("acp-adapter", map[string]string{"PATH": t.TempDir()})
	if err == nil {
		t.Fatal("ResolveCommand succeeded, want missing command error")
	}
	if !strings.Contains(err.Error(), "make install-acp-adapter") {
		t.Fatalf("error missing install hint: %v", err)
	}
}

func TestResolveCommandValidatesExplicitPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "acp-adapter")
	_, err := ResolveCommand(missing, nil)
	if err == nil {
		t.Fatal("ResolveCommand succeeded, want missing explicit command error")
	}
	if !strings.Contains(err.Error(), "is not executable") {
		t.Fatalf("error missing explicit path validation: %v", err)
	}
}

func TestACPErrorIncludesNestedDataError(t *testing.T) {
	err := (&acpError{
		Code:    -32000,
		Message: "thread/start failed",
		Data:    json.RawMessage(`{"error":"start pi rpc process: exec: \"pi\": executable file not found in $PATH"}`),
	}).toError()
	if err == nil {
		t.Fatal("toError returned nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "thread/start failed") {
		t.Fatalf("error missing top-level message: %v", err)
	}
	if !strings.Contains(msg, `exec: "pi": executable file not found in $PATH`) {
		t.Fatalf("error missing nested data error: %v", err)
	}
}

func TestACPRuntimeBuffersEventsUntilBound(t *testing.T) {
	rt := NewACPRuntime(t.TempDir())
	var mu sync.Mutex
	var got []string
	rt.SetEventSink(func(agentID, taskID, sessionName, stream, payload string) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, agentID+" "+stream+" "+payload)
	})
	err := rt.Spawn("acp-buffer-test", t.TempDir(), os.Args[0], []string{"-test.run=TestACPHelperProcess"}, map[string]string{
		"CLANKWORK_ACP_HELPER": "1",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { _ = rt.Kill("acp-buffer-test") })

	mu.Lock()
	beforeBind := len(got)
	mu.Unlock()
	if beforeBind != 0 {
		t.Fatalf("events emitted before binding = %d, want 0", beforeBind)
	}

	rt.BindAgentSession("acp-buffer-test", "agent01", "task01")
	mu.Lock()
	afterBind := append([]string(nil), got...)
	mu.Unlock()
	if len(afterBind) < 2 {
		t.Fatalf("bound events = %d, want initialize and session/new events", len(afterBind))
	}
	if !strings.Contains(afterBind[0], "agent01 acp.recv") {
		t.Fatalf("first bound event = %q, want agent01 acp.recv", afterBind[0])
	}
}

func TestACPPermissionOptionAppliesWorktreePolicy(t *testing.T) {
	workdir := t.TempDir()
	session := &acpSession{workdir: workdir}
	clankworkReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"acceptForSession"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"clankwork bootstrap"}}
	}`)
	if got := session.permissionOption(clankworkReq); got != "acceptForSession" {
		t.Fatalf("permissionOption(clankwork) = %q, want acceptForSession", got)
	}

	chainedClankworkReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"acceptForSession"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"clankwork signal started && pwd"}}
	}`)
	if got := session.permissionOption(chainedClankworkReq); got != "accept" {
		t.Fatalf("permissionOption(chained clankwork) = %q, want accept", got)
	}

	insideReq := json.RawMessage(fmt.Sprintf(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"cat %s/README.md"}}
	}`, workdir))
	if got := session.permissionOption(insideReq); got != "accept" {
		t.Fatalf("permissionOption(worktree read) = %q, want accept", got)
	}

	relativeWriteReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"rm -rf build/output"}}
	}`)
	if got := session.permissionOption(relativeWriteReq); got != "accept" {
		t.Fatalf("permissionOption(relative worktree write) = %q, want accept", got)
	}

	outsideReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"rm -rf /tmp/nope"}}
	}`)
	if got := session.permissionOption(outsideReq); got != "decline" {
		t.Fatalf("permissionOption(outside worktree) = %q, want decline", got)
	}

	sensitiveReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"cat ~/.ssh/id_ed25519"}}
	}`)
	if got := session.permissionOption(sensitiveReq); got != "decline" {
		t.Fatalf("permissionOption(sensitive path) = %q, want decline", got)
	}

	quotedInsideReq := json.RawMessage(fmt.Sprintf(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"cat '%s/file with spaces.txt'"}}
	}`, workdir))
	if got := session.permissionOption(quotedInsideReq); got != "accept" {
		t.Fatalf("permissionOption(quoted worktree path) = %q, want accept", got)
	}

	redirectOutsideReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"echo hi > /tmp/outside-policy"}}
	}`)
	if got := session.permissionOption(redirectOutsideReq); got != "decline" {
		t.Fatalf("permissionOption(outside redirect) = %q, want decline", got)
	}

	devNullReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"find . -type f | xargs grep ACP 2>/dev/null | head -20"}}
	}`)
	if got := session.permissionOption(devNullReq); got != "accept" {
		t.Fatalf("permissionOption(/dev/null redirect) = %q, want accept", got)
	}

	stdoutReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"echo hi > /dev/stdout"}}
	}`)
	if got := session.permissionOption(stdoutReq); got != "accept" {
		t.Fatalf("permissionOption(/dev/stdout redirect) = %q, want accept", got)
	}

	substitutionReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"cat $(pwd)/README.md"}}
	}`)
	if got := session.permissionOption(substitutionReq); got != "decline" {
		t.Fatalf("permissionOption(command substitution) = %q, want decline", got)
	}
}

func TestACPPermissionOptionAcceptanceSpecPolicy(t *testing.T) {
	workdir := t.TempDir()
	session := &acpSession{
		workdir: workdir,
		policy:  ACPPermissionPolicy{Mode: "acceptance-spec"},
	}

	bootstrapReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"acceptForSession"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"clankwork bootstrap"}}
	}`)
	if got := session.permissionOption(bootstrapReq); got != "acceptForSession" {
		t.Fatalf("permissionOption(acceptance-spec bootstrap) = %q, want acceptForSession", got)
	}

	readReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"sed -n '1,120p' docs/acceptance-verification.md"}}
	}`)
	if got := session.permissionOption(readReq); got != "accept" {
		t.Fatalf("permissionOption(acceptance-spec read) = %q, want accept", got)
	}

	writeSpecReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"mkdir -p artifacts && tee artifacts/acceptance-spec.json >/dev/null"}}
	}`)
	if got := session.permissionOption(writeSpecReq); got != "accept" {
		t.Fatalf("permissionOption(acceptance-spec artifact write) = %q, want accept", got)
	}

	commitSpecReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"git add artifacts/acceptance-spec.json && git commit -m spec"}}
	}`)
	if got := session.permissionOption(commitSpecReq); got != "decline" {
		t.Fatalf("permissionOption(acceptance-spec git commit) = %q, want decline", got)
	}

	editSourceReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"edit internal/model/acceptance.go (1 replacements)"}}
	}`)
	if got := session.permissionOption(editSourceReq); got != "decline" {
		t.Fatalf("permissionOption(acceptance-spec source edit) = %q, want decline", got)
	}

	testReq := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"go test ./..."}}
	}`)
	if got := session.permissionOption(testReq); got != "decline" {
		t.Fatalf("permissionOption(acceptance-spec go test) = %q, want decline", got)
	}
}

func TestACPPermissionResultUsesProtocolOutcomeShape(t *testing.T) {
	b, err := json.Marshal(permissionResult("acceptForSession"))
	if err != nil {
		t.Fatal(err)
	}
	var selected map[string]any
	if err := json.Unmarshal(b, &selected); err != nil {
		t.Fatal(err)
	}
	if selected["outcome"] != "approved" {
		t.Fatalf("outcome = %#v, want approved", selected["outcome"])
	}
	if selected["optionId"] != "acceptForSession" {
		t.Fatalf("optionId = %#v, want acceptForSession", selected["optionId"])
	}
	b, err = json.Marshal(permissionResult(""))
	if err != nil {
		t.Fatal(err)
	}
	var cancelled map[string]any
	if err := json.Unmarshal(b, &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled["outcome"] != "cancelled" {
		t.Fatalf("outcome = %#v, want cancelled", cancelled["outcome"])
	}
	if _, ok := cancelled["optionId"]; ok {
		t.Fatalf("optionId unexpectedly present in cancelled result: %#v", cancelled)
	}

	b, err = json.Marshal(permissionResult("decline"))
	if err != nil {
		t.Fatal(err)
	}
	var declined map[string]any
	if err := json.Unmarshal(b, &declined); err != nil {
		t.Fatal(err)
	}
	if declined["outcome"] != "declined" {
		t.Fatalf("outcome = %#v, want declined", declined["outcome"])
	}
}

func TestACPPermissionOptionSupportsRequestsWithoutOptions(t *testing.T) {
	session := &acpSession{
		name:              "no-options-session",
		workdir:           t.TempDir(),
		policy:            ACPPermissionPolicy{Mode: "worktree"},
		permissionPending: make(map[string]*acpPendingPermission),
	}

	bootstrapReq := json.RawMessage(`{"command":"clankwork bootstrap"}`)
	if got := session.permissionOption(bootstrapReq); got != "acceptForSession" {
		t.Fatalf("permissionOption(no-options clankwork) = %q, want acceptForSession", got)
	}

	denyReq := json.RawMessage(`{"command":"cat /etc/passwd"}`)
	if got := session.permissionOption(denyReq); got != "decline" {
		t.Fatalf("permissionOption(no-options deny) = %q, want decline", got)
	}
}

func TestACPPermissionOptionManualApproval(t *testing.T) {
	session := &acpSession{
		name:              "manual-session",
		workdir:           t.TempDir(),
		policy:            ACPPermissionPolicy{Mode: "manual", Timeout: time.Second},
		permissionPending: make(map[string]*acpPendingPermission),
	}
	req := json.RawMessage(`{
		"options":[{"optionId":"accept"},{"optionId":"decline"}],
		"toolCall":{"rawInput":{"command":"cat /tmp/outside-policy"}}
	}`)
	result := make(chan string, 1)
	go func() {
		result <- session.permissionOption(req)
	}()

	deadline := time.Now().Add(time.Second)
	var pending []ACPPermissionRequest
	for time.Now().Before(deadline) {
		pending = session.runtimePending()
		if len(pending) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pending) != 1 {
		t.Fatalf("pending permissions = %d, want 1", len(pending))
	}
	if pending[0].Command != "cat /tmp/outside-policy" {
		t.Fatalf("pending command = %q", pending[0].Command)
	}
	session.permissionPending[pending[0].ID].ch <- "accept"
	if got := <-result; got != "accept" {
		t.Fatalf("manual permission result = %q, want accept", got)
	}
}

func TestCommandPathsStayInAllowedRootsCanonicalizesSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if !commandPathsStayInAllowedRoots("cd "+link+" && clankwork bootstrap", []string{target}) {
		t.Fatal("symlinked path inside root was denied")
	}
}

func (s *acpSession) runtimePending() []ACPPermissionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ACPPermissionRequest, 0, len(s.permissionPending))
	for _, pending := range s.permissionPending {
		out = append(out, pending.request)
	}
	return out
}

func TestACPHelperProcess(t *testing.T) {
	if os.Getenv("CLANKWORK_ACP_HELPER") != "1" {
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
			writeHelper(map[string]any{
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
			writeHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"sessionId": "session-1"},
			})
		case "session/prompt":
			writeHelper(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "session-1",
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"type":          "text",
						"text":          "received",
					},
				},
			})
			writeHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"stopReason": "end_turn"},
			})
		default:
			writeHelper(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error":   map[string]any{"code": -32601, "message": "not found"},
			})
		}
	}
	os.Exit(0)
}

func writeHelper(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}
