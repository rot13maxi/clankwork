// ACP session lifecycle, JSON-RPC transport, and runtime management.
// Permission policy is in acp_permissions.go; path checks in acp_paths.go.
package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type ACPRuntime struct {
	LogDir  string
	OnEvent func(agentID, taskID, sessionName, stream, payload string)

	mu       sync.Mutex
	sessions map[string]*acpSession
	bindings map[string]acpBinding
	pending  map[string][]acpRuntimeEvent
}

func NewACPRuntime(logDir string) *ACPRuntime {
	return &ACPRuntime{
		LogDir:   logDir,
		sessions: make(map[string]*acpSession),
		bindings: make(map[string]acpBinding),
		pending:  make(map[string][]acpRuntimeEvent),
	}
}

type acpBinding struct {
	agentID string
	taskID  string
}

type acpRuntimeEvent struct {
	sessionName string
	stream      string
	payload     string
}

type acpSession struct {
	runtime   *ACPRuntime
	name      string
	workdir   string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	log       *os.File
	sessionID string

	writeMu      sync.Mutex
	mu           sync.Mutex
	nextID       int64
	pendingCalls map[int64]chan acpResponse
	lines        []string
	events       chan string
	policy       ACPPermissionPolicy

	permissionSeq     int64
	permissionPending map[string]*acpPendingPermission

	lastActivity time.Time
	exited       bool
}

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type acpResponse struct {
	result json.RawMessage
	err    error
}

func (a *ACPRuntime) Spawn(sessionName, workdir, command string, args []string, env map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return a.SpawnWithContext(ctx, sessionName, workdir, command, args, env)
}

func (a *ACPRuntime) SpawnWithContext(ctx context.Context, sessionName, workdir, command string, args []string, env map[string]string) error {
	if command == "" {
		return fmt.Errorf("acp command is empty")
	}
	commandPath, err := ResolveCommand(command, env)
	if err != nil {
		return err
	}
	cmd := exec.Command(commandPath, args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), flattenEnv(env)...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("acp stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("acp stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("acp stderr: %w", err)
	}

	s := &acpSession{
		runtime:           a,
		name:              sessionName,
		workdir:           workdir,
		cmd:               cmd,
		stdin:             stdin,
		pendingCalls:      make(map[int64]chan acpResponse),
		events:            make(chan string, 200),
		policy:            permissionPolicyFromEnv(env),
		permissionPending: make(map[string]*acpPendingPermission),
		lastActivity:      time.Now(),
	}
	if a.LogDir != "" {
		if err := os.MkdirAll(a.LogDir, 0700); err != nil {
			return fmt.Errorf("create acp log dir: %w", err)
		}
		logPath := filepath.Join(a.LogDir, sessionName+".log")
		logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open acp log: %w", err)
		}
		s.log = logFile
	}

	if err := cmd.Start(); err != nil {
		if s.log != nil {
			s.log.Close()
		}
		return fmt.Errorf("start acp agent: %w", err)
	}

	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = make(map[string]*acpSession)
	}
	a.sessions[sessionName] = s
	a.mu.Unlock()

	go s.readLoop(stdout)
	go s.stderrLoop(stderr)
	go s.wait()

	if err := s.initialize(ctx); err != nil {
		a.Kill(sessionName)
		return err
	}
	sessionID, err := s.newSession(ctx, workdir)
	if err != nil {
		a.Kill(sessionName)
		return err
	}
	s.sessionID = sessionID
	return nil
}

func ResolveCommand(command string, env map[string]string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("command is empty")
	}
	if strings.ContainsRune(command, rune(os.PathSeparator)) {
		if err := findExecutable(command); err != nil {
			return "", fmt.Errorf("command %q is not executable: %w", command, err)
		}
		return command, nil
	}
	path := env["PATH"]
	if path == "" {
		path = os.Getenv("PATH")
	}
	if resolved, err := lookPathInPath(command, path); err == nil {
		return resolved, nil
	}
	hint := ""
	if command == "acp-adapter" {
		hint = "; install it with `make install-acp-adapter` or set the ACP runtime command to an absolute adapter path"
	}
	return "", fmt.Errorf("command %q not found on runtime PATH%s", command, hint)
}

func lookPathInPath(file, path string) (string, error) {
	if path == "" {
		return "", exec.ErrNotFound
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, file)
		if err := findExecutable(candidate); err == nil {
			if dir == "." {
				return "." + string(os.PathSeparator) + file, nil
			}
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func findExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&0111 == 0 {
		return os.ErrPermission
	}
	return nil
}

func (a *ACPRuntime) IsAlive(sessionName string) (bool, error) {
	s := a.get(sessionName)
	if s == nil {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return false, nil
	}
	if s.cmd == nil || s.cmd.Process == nil {
		return false, nil
	}
	err := s.cmd.Process.Signal(syscall.Signal(0))
	return err == nil, nil
}

func (a *ACPRuntime) Kill(sessionName string) error {
	s := a.get(sessionName)
	if s == nil {
		return nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	a.delete(sessionName)
	return nil
}

func (a *ACPRuntime) GracefulKill(sessionName string, gracePeriod time.Duration) error {
	s := a.get(sessionName)
	if s == nil {
		return nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = s.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(gracePeriod):
			_ = s.cmd.Process.Kill()
		}
	}
	a.delete(sessionName)
	return nil
}

func (a *ACPRuntime) PaneLastActivity(sessionName string) (time.Time, error) {
	s := a.get(sessionName)
	if s == nil {
		return time.Time{}, fmt.Errorf("acp session %q not found", sessionName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastActivity, nil
}

func (a *ACPRuntime) CapturePane(sessionName string, lines int) (string, error) {
	s := a.get(sessionName)
	if s == nil {
		return "", fmt.Errorf("acp session %q not found", sessionName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	if lines > 0 && lines < len(s.lines) {
		start = len(s.lines) - lines
	}
	return strings.Join(s.lines[start:], "\n"), nil
}

func (a *ACPRuntime) SendInitialPrompt(sessionName, msg string) error {
	_, err := a.PromptWithContext(context.Background(), sessionName, msg)
	return err
}

func (a *ACPRuntime) SendNudge(sessionName, msg string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := a.PromptWithContext(ctx, sessionName, msg)
	return err
}

func (a *ACPRuntime) PromptWithContext(ctx context.Context, sessionName, msg string) (string, error) {
	s := a.get(sessionName)
	if s == nil {
		return "", fmt.Errorf("acp session %q not found", sessionName)
	}
	return s.prompt(ctx, msg)
}

func (a *ACPRuntime) Cancel(sessionName string) error {
	s := a.get(sessionName)
	if s == nil {
		return fmt.Errorf("acp session %q not found", sessionName)
	}
	return s.cancel()
}

func (a *ACPRuntime) Status(sessionName string) (ACPSessionStatus, error) {
	s := a.get(sessionName)
	if s == nil {
		return ACPSessionStatus{}, fmt.Errorf("acp session %q not found", sessionName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pid := 0
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	return ACPSessionStatus{
		Name:          s.name,
		RuntimeID:     s.sessionID,
		Workdir:       s.workdir,
		PID:           pid,
		Exited:        s.exited,
		LastActivity:  s.lastActivity,
		PendingCalls:  len(s.pendingCalls),
		BufferedLines: len(s.lines),
	}, nil
}

func (a *ACPRuntime) PIDForSession(sessionName string) int {
	status, err := a.Status(sessionName)
	if err != nil {
		return 0
	}
	return status.PID
}

func (a *ACPRuntime) RuntimeSessionIDForSession(sessionName string) string {
	status, err := a.Status(sessionName)
	if err != nil || status.RuntimeID == "" {
		return sessionName
	}
	return status.RuntimeID
}

func (a *ACPRuntime) Events(sessionName string) (<-chan string, error) {
	s := a.get(sessionName)
	if s == nil {
		return nil, fmt.Errorf("acp session %q not found", sessionName)
	}
	return s.events, nil
}

type ACPSessionStatus struct {
	Name          string    `json:"name"`
	RuntimeID     string    `json:"runtime_session_id"`
	Workdir       string    `json:"workdir"`
	PID           int       `json:"pid"`
	Exited        bool      `json:"exited"`
	LastActivity  time.Time `json:"last_activity"`
	PendingCalls  int       `json:"pending_calls"`
	BufferedLines int       `json:"buffered_lines"`
}

func (a *ACPRuntime) get(sessionName string) *acpSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[sessionName]
}

func (a *ACPRuntime) delete(sessionName string) {
	a.mu.Lock()
	delete(a.sessions, sessionName)
	delete(a.bindings, sessionName)
	delete(a.pending, sessionName)
	a.mu.Unlock()
}

func (s *acpSession) initialize(ctx context.Context) error {
	var out struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]any{
			"name":    "clankwork",
			"version": "dev",
		},
		"clientCapabilities": map[string]any{
			"fs": map[string]bool{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
	}, &out)
	if err != nil {
		return fmt.Errorf("acp initialize: %w", err)
	}
	return nil
}

func (s *acpSession) newSession(ctx context.Context, cwd string) (string, error) {
	var out struct {
		SessionID string `json:"sessionId"`
	}
	err := s.call(ctx, "session/new", map[string]any{
		"cwd":        cwd,
		"mcpServers": []any{},
	}, &out)
	if err != nil {
		return "", fmt.Errorf("acp session/new: %w", err)
	}
	if out.SessionID == "" {
		return "", fmt.Errorf("acp session/new returned empty sessionId")
	}
	return out.SessionID, nil
}

func (s *acpSession) prompt(ctx context.Context, msg string) (string, error) {
	var out struct {
		StopReason string `json:"stopReason"`
	}
	err := s.call(ctx, "session/prompt", map[string]any{
		"sessionId": s.sessionID,
		"prompt": []map[string]any{
			{
				"type": "text",
				"text": msg,
			},
		},
	}, &out)
	if err != nil {
		return "", fmt.Errorf("acp session/prompt: %w", err)
	}
	return out.StopReason, nil
}

func (s *acpSession) cancel() error {
	return s.notify("session/cancel", map[string]any{"sessionId": s.sessionID})
}

func (s *acpSession) call(ctx context.Context, method string, params any, out any) error {
	id := atomic.AddInt64(&s.nextID, 1)
	ch := make(chan acpResponse, 1)
	s.mu.Lock()
	s.pendingCalls[id] = ch
	s.mu.Unlock()

	msg := acpMessage{JSONRPC: "2.0", ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		msg.Params = b
	}
	if err := s.write(msg); err != nil {
		s.mu.Lock()
		delete(s.pendingCalls, id)
		s.mu.Unlock()
		return err
	}

	select {
	case resp := <-ch:
		if resp.err != nil {
			return resp.err
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(resp.result, out)
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pendingCalls, id)
		s.mu.Unlock()
		return ctx.Err()
	}
}

func (s *acpSession) notify(method string, params any) error {
	msg := acpMessage{JSONRPC: "2.0", Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		msg.Params = b
	}
	return s.write(msg)
}

func (s *acpSession) write(msg acpMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write acp message: %w", err)
	}
	return nil
}

func (s *acpSession) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		s.record("acp.recv " + line)
		var msg acpMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			s.record("acp.decode_error " + err.Error())
			continue
		}
		s.handleMessage(msg)
	}
	if err := sc.Err(); err != nil {
		s.record("acp.read_error " + err.Error())
	}
}

func (s *acpSession) handleMessage(msg acpMessage) {
	if len(msg.ID) > 0 && msg.Method == "" {
		if id, ok := numericJSONRPCID(msg.ID); ok {
			s.mu.Lock()
			ch := s.pendingCalls[id]
			delete(s.pendingCalls, id)
			s.mu.Unlock()
			if ch != nil {
				if msg.Error != nil {
					ch <- acpResponse{err: msg.Error.toError()}
				} else {
					ch <- acpResponse{result: msg.Result}
				}
			}
		}
		return
	}
	if len(msg.ID) == 0 || msg.Method == "" {
		return
	}
	var err error
	switch msg.Method {
	case "session/request_permission":
		err = s.respondPermission(msg.ID, msg.Params)
	default:
		err = s.respondMethodNotFound(msg.ID, msg.Method)
	}
	if err != nil {
		s.record("acp.respond_error " + err.Error())
	}
}

func (e *acpError) toError() error {
	if e == nil {
		return nil
	}
	msg := fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
	if detail := e.detail(); detail != "" && detail != e.Message {
		msg += ": " + detail
	}
	return fmt.Errorf("%s", msg)
}

func (e *acpError) detail() string {
	if e == nil || len(e.Data) == 0 {
		return ""
	}
	var fields map[string]any
	if err := json.Unmarshal(e.Data, &fields); err != nil {
		return strings.TrimSpace(string(e.Data))
	}
	for _, key := range []string{"error", "message", "detail", "details"} {
		if v, ok := fields[key]; ok {
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
				return s
			}
		}
	}
	return strings.TrimSpace(string(e.Data))
}

func numericJSONRPCID(raw json.RawMessage) (int64, bool) {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	return 0, false
}

func (s *acpSession) respondMethodNotFound(id json.RawMessage, method string) error {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    -32601,
			"message": "method not found: " + method,
		},
	}
	return s.writeRawResponse(msg)
}

func (s *acpSession) respondResult(id json.RawMessage, result any) error {
	msg := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	}
	return s.writeRawResponse(msg)
}

func (s *acpSession) writeRawResponse(msg map[string]any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

func (s *acpSession) stderrLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		s.record("acp.stderr " + sc.Text())
	}
}

func (s *acpSession) wait() {
	err := s.cmd.Wait()
	if err != nil {
		s.record("acp.exit " + err.Error())
	} else {
		s.record("acp.exit 0")
	}
	s.mu.Lock()
	s.exited = true
	for id, ch := range s.pendingCalls {
		delete(s.pendingCalls, id)
		ch <- acpResponse{err: fmt.Errorf("acp process exited")}
	}
	for id, pending := range s.permissionPending {
		delete(s.permissionPending, id)
		pending.ch <- "decline"
	}
	if s.log != nil {
		_ = s.log.Close()
	}
	if s.events != nil {
		close(s.events)
		s.events = nil
	}
	s.mu.Unlock()
}

func (s *acpSession) record(line string) {
	stream := "acp"
	payload := line
	if before, after, ok := strings.Cut(line, " "); ok && strings.HasPrefix(before, "acp.") {
		stream = before
		payload = after
	}
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.lines = append(s.lines, line)
	if len(s.lines) > 500 {
		s.lines = append([]string(nil), s.lines[len(s.lines)-500:]...)
	}
	if s.log != nil {
		_, _ = s.log.WriteString(line + "\n")
	}
	if s.events != nil {
		select {
		case s.events <- line:
		default:
		}
	}
	s.mu.Unlock()
	if s.runtime != nil && s.runtime.OnEvent != nil {
		s.runtime.emit(s.name, stream, payload)
	}
}

func (a *ACPRuntime) SetEventSink(fn func(agentID, taskID, sessionName, stream, payload string)) {
	a.OnEvent = fn
}

func (a *ACPRuntime) BindAgentSession(sessionName, agentID, taskID string) {
	a.mu.Lock()
	if a.bindings == nil {
		a.bindings = make(map[string]acpBinding)
	}
	if a.pending == nil {
		a.pending = make(map[string][]acpRuntimeEvent)
	}
	a.bindings[sessionName] = acpBinding{agentID: agentID, taskID: taskID}
	pending := append([]acpRuntimeEvent(nil), a.pending[sessionName]...)
	delete(a.pending, sessionName)
	sink := a.OnEvent
	a.mu.Unlock()

	for _, ev := range pending {
		if sink != nil {
			sink(agentID, taskID, ev.sessionName, ev.stream, ev.payload)
		}
	}
}

func (a *ACPRuntime) emit(sessionName, stream, payload string) {
	a.mu.Lock()
	binding, ok := a.bindings[sessionName]
	sink := a.OnEvent
	if !ok {
		if a.pending == nil {
			a.pending = make(map[string][]acpRuntimeEvent)
		}
		a.pending[sessionName] = append(a.pending[sessionName], acpRuntimeEvent{
			sessionName: sessionName,
			stream:      stream,
			payload:     payload,
		})
		if len(a.pending[sessionName]) > 200 {
			a.pending[sessionName] = append([]acpRuntimeEvent(nil), a.pending[sessionName][len(a.pending[sessionName])-200:]...)
		}
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	if sink != nil {
		sink(binding.agentID, binding.taskID, sessionName, stream, payload)
	}
}

func flattenEnv(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}
