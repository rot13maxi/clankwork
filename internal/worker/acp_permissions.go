// ACP permission policy decisions and request bookkeeping.
package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type ACPPermissionPolicy struct {
	Mode       string
	AllowPaths []string
	DenyPaths  []string
	Timeout    time.Duration
}

type ACPPermissionRequest struct {
	ID          string    `json:"id"`
	SessionName string    `json:"session_name"`
	SessionID   string    `json:"runtime_session_id,omitempty"`
	Command     string    `json:"command"`
	Policy      string    `json:"policy"`
	Options     []string  `json:"options"`
	CreatedAt   time.Time `json:"created_at"`
}

type acpPendingPermission struct {
	request ACPPermissionRequest
	options map[string]bool
	ch      chan string
}

func (p ACPPermissionPolicy) withDefaults() ACPPermissionPolicy {
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = "worktree"
	}
	if p.Mode != "trusted" && p.Mode != "manual" && p.Mode != "deny" && p.Mode != "worktree" && p.Mode != "acceptance-spec" {
		p.Mode = "worktree"
	}
	if p.Timeout <= 0 {
		p.Timeout = 5 * time.Minute
	}
	return p
}

func permissionPolicyFromEnv(env map[string]string) ACPPermissionPolicy {
	p := ACPPermissionPolicy{
		Mode:       env["CLANKWORK_ACP_PERMISSION_POLICY"],
		AllowPaths: splitPathList(env["CLANKWORK_ACP_PERMISSION_ALLOW_PATHS"]),
		DenyPaths:  splitPathList(env["CLANKWORK_ACP_PERMISSION_DENY_PATHS"]),
	}
	if raw := strings.TrimSpace(env["CLANKWORK_ACP_PERMISSION_TIMEOUT_SEC"]); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			p.Timeout = time.Duration(n) * time.Second
		}
	}
	return p.withDefaults()
}

func splitPathList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, string(os.PathListSeparator))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (s *acpSession) respondPermission(id json.RawMessage, params json.RawMessage) error {
	optionID := s.permissionOption(params)
	return s.respondResult(id, permissionResult(optionID))
}

func permissionResult(optionID string) map[string]any {
	if optionID == "" {
		return map[string]any{"outcome": "cancelled"}
	}
	switch optionID {
	case "decline":
		return map[string]any{"outcome": "declined", "optionId": optionID}
	default:
		return map[string]any{"outcome": "approved", "optionId": optionID}
	}
}

func (s *acpSession) permissionOption(params json.RawMessage) string {
	var req map[string]any
	_ = json.Unmarshal(params, &req)
	command := permissionCommand(req)
	options := permissionOptions(req)
	requestID := s.nextPermissionRequestID()
	policy := s.policy.withDefaults()
	s.recordPermissionRequest(requestID, command, policy, options)

	var optionID string
	reason := ""
	switch policy.Mode {
	case "trusted":
		optionID = selectPermissionOption(req, "acceptForSession", "accept")
		reason = "trusted policy"
	case "manual":
		switch {
		case isClankworkCommand(command):
			optionID = selectPermissionOption(req, "acceptForSession", "accept")
			reason = "clankwork control command"
		case isWorktreePermissionRequest(command, s.workdir, policy):
			optionID = selectPermissionOption(req, "accept", "acceptForSession")
			reason = "within allowed worktree paths"
		default:
			var timedOut bool
			optionID, timedOut = s.awaitManualPermission(requestID, command, policy, options)
			if timedOut {
				reason = "manual approval timed out"
			} else {
				reason = "manual decision"
			}
		}
	case "deny":
		optionID = selectPermissionOption(req, "decline")
		reason = "deny policy"
	case "acceptance-spec":
		switch {
		case isClankworkCommand(command):
			optionID = selectPermissionOption(req, "acceptForSession", "accept")
			reason = "clankwork control command"
		case isAcceptanceSpecPermissionRequest(command, s.workdir, policy):
			optionID = selectPermissionOption(req, "accept", "acceptForSession")
			reason = "acceptance spec artifact or read-only command"
		default:
			optionID = selectPermissionOption(req, "decline")
			reason = "acceptance_spec step may not edit source files"
		}
	default:
		switch {
		case isClankworkCommand(command):
			optionID = selectPermissionOption(req, "acceptForSession", "accept")
			reason = "clankwork control command"
		case isWorktreePermissionRequest(command, s.workdir, policy):
			optionID = selectPermissionOption(req, "accept", "acceptForSession")
			reason = "within allowed worktree paths"
		default:
			optionID = selectPermissionOption(req, "decline")
			reason = "outside policy"
		}
	}
	if optionID == "" && hasPermissionOption(req, "decline") {
		optionID = "decline"
	}
	s.recordPermissionDecision(requestID, command, policy, optionID, reason)
	return optionID
}

func (s *acpSession) nextPermissionRequestID() string {
	n := atomic.AddInt64(&s.permissionSeq, 1)
	return fmt.Sprintf("perm-%d", n)
}

func (s *acpSession) awaitManualPermission(requestID, command string, policy ACPPermissionPolicy, options []string) (string, bool) {
	pending := &acpPendingPermission{
		request: ACPPermissionRequest{
			ID:          requestID,
			SessionName: s.name,
			SessionID:   s.sessionID,
			Command:     command,
			Policy:      policy.Mode,
			Options:     append([]string(nil), options...),
			CreatedAt:   time.Now().UTC(),
		},
		options: optionSet(options),
		ch:      make(chan string, 1),
	}
	s.mu.Lock()
	if s.permissionPending == nil {
		s.permissionPending = make(map[string]*acpPendingPermission)
	}
	s.permissionPending[requestID] = pending
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.permissionPending, requestID)
		s.mu.Unlock()
	}()

	select {
	case optionID := <-pending.ch:
		return optionID, false
	case <-time.After(policy.Timeout):
		if pending.options["decline"] {
			return "decline", true
		}
		return "", true
	}
}

func (s *acpSession) recordPermissionRequest(requestID, command string, policy ACPPermissionPolicy, options []string) {
	s.recordPermissionEvent("acp.permission.request", map[string]any{
		"id":       requestID,
		"command":  command,
		"policy":   policy.Mode,
		"options":  options,
		"workdir":  s.workdir,
		"session":  s.name,
		"runtime":  s.sessionID,
		"manual":   policy.Mode == "manual",
		"timeout":  int(policy.Timeout.Seconds()),
		"allow":    policy.AllowPaths,
		"deny":     policy.DenyPaths,
		"event":    "request",
		"decision": "pending",
	})
}

func (s *acpSession) recordPermissionDecision(requestID, command string, policy ACPPermissionPolicy, optionID, reason string) {
	decision := "deny"
	if optionID == "accept" || optionID == "acceptForSession" {
		decision = "allow"
	} else if optionID == "" {
		decision = "cancel"
	}
	s.recordPermissionEvent("acp.permission.decision", map[string]any{
		"id":       requestID,
		"command":  command,
		"policy":   policy.Mode,
		"option":   optionID,
		"decision": decision,
		"reason":   reason,
		"event":    "decision",
	})
}

func (s *acpSession) recordPermissionEvent(stream string, payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		s.record(stream + " " + err.Error())
		return
	}
	s.record(stream + " " + string(b))
}

func permissionCommand(req map[string]any) string {
	candidates := []string{
		getACPMapString(req, "command"),
		getACPMapString(req, "message"),
	}
	if toolCall, _ := req["toolCall"].(map[string]any); toolCall != nil {
		candidates = append(candidates,
			getACPMapString(toolCall, "title"),
			getACPMapString(toolCall, "command"),
			getACPMapString(toolCall, "message"),
		)
		if rawInput, _ := toolCall["rawInput"].(map[string]any); rawInput != nil {
			candidates = append(candidates,
				getACPMapString(rawInput, "command"),
				getACPMapString(rawInput, "message"),
			)
		}
	}
	for _, candidate := range candidates {
		command := strings.TrimSpace(candidate)
		if command != "" {
			return command
		}
	}
	return ""
}

func extractPermissionCommand(params json.RawMessage) string {
	var req map[string]any
	_ = json.Unmarshal(params, &req)
	return permissionCommand(req)
}

func permissionOptions(req map[string]any) []string {
	options, _ := req["options"].([]any)
	out := make([]string, 0, len(options))
	for _, option := range options {
		m, _ := option.(map[string]any)
		if optionID := getACPMapString(m, "optionId"); optionID != "" {
			out = append(out, optionID)
		}
	}
	return out
}

func optionSet(options []string) map[string]bool {
	out := make(map[string]bool, len(options))
	for _, option := range options {
		out[option] = true
	}
	return out
}

func selectPermissionOption(req map[string]any, preferred ...string) string {
	options, _ := req["options"].([]any)
	if len(options) == 0 && len(preferred) > 0 {
		return preferred[0]
	}
	for _, optionID := range preferred {
		if hasPermissionOption(req, optionID) {
			return optionID
		}
	}
	if hasPermissionOption(req, "decline") {
		return "decline"
	}
	return ""
}

func hasPermissionOption(req map[string]any, optionID string) bool {
	options, _ := req["options"].([]any)
	for _, option := range options {
		m, _ := option.(map[string]any)
		if getACPMapString(m, "optionId") == optionID {
			return true
		}
	}
	return false
}

func getACPMapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// PendingPermissions returns the list of pending permission requests for a session.
func (a *ACPRuntime) PendingPermissions(sessionName string) []ACPPermissionRequest {
	s := a.get(sessionName)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ACPPermissionRequest, 0, len(s.permissionPending))
	for _, pending := range s.permissionPending {
		out = append(out, pending.request)
	}
	return out
}

// ResolvePermission resolves a pending permission request with the given decision.
func (a *ACPRuntime) ResolvePermission(sessionName, requestID, decision string) error {
	s := a.get(sessionName)
	if s == nil {
		return fmt.Errorf("acp session %q not found", sessionName)
	}
	decision = strings.ToLower(strings.TrimSpace(decision))
	optionID := ""
	switch decision {
	case "allow", "approve", "accept":
		optionID = "accept"
	case "allow-session", "approve-session", "accept-session", "acceptforsession":
		optionID = "acceptForSession"
	case "deny", "decline", "reject":
		optionID = "decline"
	default:
		return fmt.Errorf("unknown permission decision %q", decision)
	}
	s.mu.Lock()
	pending := s.permissionPending[requestID]
	s.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("permission request %q not pending", requestID)
	}
	if !pending.options[optionID] {
		if optionID == "acceptForSession" && pending.options["accept"] {
			optionID = "accept"
		} else if optionID != "decline" || !pending.options["decline"] {
			return fmt.Errorf("permission option %q is not available for request %q", optionID, requestID)
		}
	}
	pending.ch <- optionID
	return nil
}
