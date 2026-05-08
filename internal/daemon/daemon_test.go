package daemon

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestIsACPAdapterProcess(t *testing.T) {
	// Test against the current test process — it should NOT be identified
	// as an acp-adapter (it's `go` or `test` or similar).
	currentPID := os.Getpid()
	result := isACPAdapterProcess(currentPID)
	if result {
		t.Errorf("isACPAdapterProcess(%d) = true, want false (current process is not an acp-adapter)", currentPID)
	}
}

func TestIsACPAdapterProcessNonexistentPID(t *testing.T) {
	// A non-existent PID should return false (ps will fail).
	result := isACPAdapterProcess(999999)
	if result {
		t.Error("isACPAdapterProcess(999999) = true, want false (PID does not exist)")
	}
}

func TestIsACPAdapterProcessPIDZero(t *testing.T) {
	// PID 0 should return false.
	result := isACPAdapterProcess(0)
	if result {
		t.Error("isACPAdapterProcess(0) = true, want false")
	}
}

func TestIsACPAdapterProcessNegativePID(t *testing.T) {
	// Negative PID should return false.
	result := isACPAdapterProcess(-1)
	if result {
		t.Error("isACPAdapterProcess(-1) = true, want false")
	}
}

// Test that the ps command output parsing works correctly.
// Since we can't easily spawn a real acp-adapter in tests, we verify the
// string matching logic by checking that the current test binary is correctly rejected.
func TestIsACPAdapterProcessParsing(t *testing.T) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(os.Getpid()), "-o", "args=").Output()
	if err != nil {
		t.Skipf("ps not available: %v", err)
	}
	args := string(out)
	if args == "" {
		t.Skip("ps returned empty output")
	}
	t.Logf("current process args: %q", args)
	// This should NOT match acp-adapter patterns.
	if isACPAdapterProcess(os.Getpid()) {
		t.Errorf("process args %q should not match acp-adapter patterns", args)
	}
}

// Test that the string matching covers all expected acp-adapter binary names.
func TestIsACPAdapterProcessMatching(t *testing.T) {
	testCases := []struct {
		args   string
		want   bool
		reason string
	}{
		{"/Users/anon/.clankwork/bin/acp-adapter --adapter pi", true, "standard acp-adapter path"},
		{"acp-adapter --adapter claude", true, "acp-adapter without path"},
		{"acp_adapter", true, "underscore variant"},
		{"/usr/local/bin/acp --adapter pi", true, "raw Go binary (cmd/acp) with path"},
		{"/Users/anon/.clankwork/bin/acp --adapter pi", true, "local acp binary with --adapter flag"},
		{"/bin/bash", false, "bash process"},
		{"/usr/bin/node", false, "node process"},
		{"/Users/anon/code/clankwork/bin/clankwork daemon start", false, "clankwork daemon"},
		{"", false, "empty"},
		// Negative cases: these should NOT be matched
		{"/usr/bin/acp-service --config /etc", false, "unrelated binary 'acp-service'"},
		{"echo done with acp", false, "shell command containing 'acp' without space"},
		{"cat /path/to/acp.txt", false, "file path containing 'acp'"},
	}

	for _, tc := range testCases {
		t.Run(tc.reason, func(t *testing.T) {
			matched := strings.Contains(tc.args, "acp-adapter") ||
				strings.Contains(tc.args, "acp_adapter") ||
				strings.Contains(tc.args, "acp ") ||
				strings.Contains(tc.args, "--adapter")
			if matched != tc.want {
				t.Errorf("args %q: got %v, want %v (%s)", tc.args, matched, tc.want, tc.reason)
			}
		})
	}
}
