package worker

import (
	"fmt"
	"sync"
	"time"
)

const (
	TransportTmux = "tmux"
	TransportACP  = "acp"
)

type TransportSelector interface {
	UseTransport(sessionName, transport string) error
}

type AgentEventBinder interface {
	BindAgentSession(sessionName, agentID, taskID string)
}

type TransportReporter interface {
	TransportForSession(sessionName string) string
}

type RuntimePIDReporter interface {
	PIDForSession(sessionName string) int
}

type RuntimeSessionIDReporter interface {
	RuntimeSessionIDForSession(sessionName string) string
}

type AgentCanceller interface {
	Cancel(sessionName string) error
}

type ACPPermissionApprover interface {
	PendingPermissions(sessionName string) []ACPPermissionRequest
	ResolvePermission(sessionName, requestID, decision string) error
}

type RuntimeMux struct {
	mu         sync.Mutex
	tmux       AgentRuntime
	acp        AgentRuntime
	transports map[string]string
}

func NewRuntimeMux(tmux, acp AgentRuntime) *RuntimeMux {
	return &RuntimeMux{
		tmux:       tmux,
		acp:        acp,
		transports: make(map[string]string),
	}
}

func (m *RuntimeMux) UseTransport(sessionName, transport string) error {
	switch transport {
	case "", TransportTmux:
		transport = TransportTmux
	case TransportACP:
		if m.acp == nil {
			return fmt.Errorf("acp runtime is not configured")
		}
	default:
		return fmt.Errorf("unknown agent transport %q", transport)
	}
	m.mu.Lock()
	m.transports[sessionName] = transport
	m.mu.Unlock()
	return nil
}

func (m *RuntimeMux) runtimeFor(sessionName string) AgentRuntime {
	m.mu.Lock()
	transport := m.transports[sessionName]
	m.mu.Unlock()
	if transport == TransportACP && m.acp != nil {
		return m.acp
	}
	return m.tmux
}

func (m *RuntimeMux) TransportForSession(sessionName string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	transport := m.transports[sessionName]
	if transport == "" {
		return TransportTmux
	}
	return transport
}

func (m *RuntimeMux) Spawn(sessionName, workdir, command string, args []string, env map[string]string) error {
	return m.runtimeFor(sessionName).Spawn(sessionName, workdir, command, args, env)
}

func (m *RuntimeMux) IsAlive(sessionName string) (bool, error) {
	return m.runtimeFor(sessionName).IsAlive(sessionName)
}

func (m *RuntimeMux) Kill(sessionName string) error {
	err := m.runtimeFor(sessionName).Kill(sessionName)
	m.mu.Lock()
	delete(m.transports, sessionName)
	m.mu.Unlock()
	return err
}

func (m *RuntimeMux) GracefulKill(sessionName string, gracePeriod time.Duration) error {
	err := m.runtimeFor(sessionName).GracefulKill(sessionName, gracePeriod)
	m.mu.Lock()
	delete(m.transports, sessionName)
	m.mu.Unlock()
	return err
}

func (m *RuntimeMux) PaneLastActivity(sessionName string) (time.Time, error) {
	return m.runtimeFor(sessionName).PaneLastActivity(sessionName)
}

func (m *RuntimeMux) CapturePane(sessionName string, lines int) (string, error) {
	return m.runtimeFor(sessionName).CapturePane(sessionName, lines)
}

func (m *RuntimeMux) SendInitialPrompt(sessionName, msg string) error {
	return m.runtimeFor(sessionName).SendInitialPrompt(sessionName, msg)
}

func (m *RuntimeMux) SendNudge(sessionName, msg string) error {
	return m.runtimeFor(sessionName).SendNudge(sessionName, msg)
}

func (m *RuntimeMux) BindAgentSession(sessionName, agentID, taskID string) {
	if binder, ok := m.runtimeFor(sessionName).(AgentEventBinder); ok {
		binder.BindAgentSession(sessionName, agentID, taskID)
	}
}

func (m *RuntimeMux) Cancel(sessionName string) error {
	if canceller, ok := m.runtimeFor(sessionName).(AgentCanceller); ok {
		return canceller.Cancel(sessionName)
	}
	return m.GracefulKill(sessionName, 5*time.Second)
}

func (m *RuntimeMux) PendingPermissions(sessionName string) []ACPPermissionRequest {
	if approver, ok := m.runtimeFor(sessionName).(ACPPermissionApprover); ok {
		return approver.PendingPermissions(sessionName)
	}
	return nil
}

func (m *RuntimeMux) ResolvePermission(sessionName, requestID, decision string) error {
	if approver, ok := m.runtimeFor(sessionName).(ACPPermissionApprover); ok {
		return approver.ResolvePermission(sessionName, requestID, decision)
	}
	return fmt.Errorf("runtime does not support ACP permission approvals")
}

func (m *RuntimeMux) PIDForSession(sessionName string) int {
	if reporter, ok := m.runtimeFor(sessionName).(RuntimePIDReporter); ok {
		return reporter.PIDForSession(sessionName)
	}
	return 0
}

func (m *RuntimeMux) RuntimeSessionIDForSession(sessionName string) string {
	if reporter, ok := m.runtimeFor(sessionName).(RuntimeSessionIDReporter); ok {
		return reporter.RuntimeSessionIDForSession(sessionName)
	}
	return sessionName
}
