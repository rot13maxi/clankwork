package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
)

type renderedAgentEvent struct {
	Kind         string
	Text         string
	MessageChunk bool
	Skip         bool
}

type agentEventPrinter struct {
	out           io.Writer
	inMessage     bool
	messagePrefix string
	lastMessage   rune
	lastStatus    string
}

func newAgentEventPrinter(out io.Writer) *agentEventPrinter {
	return &agentEventPrinter{out: out}
}

func (p *agentEventPrinter) Print(ev *model.AgentEvent) {
	rendered := renderAgentEvent(ev)
	if rendered.Skip {
		return
	}
	if rendered.MessageChunk {
		if !p.inMessage {
			p.messagePrefix = eventPrefix(ev, rendered.Kind)
			fmt.Fprint(p.out, p.messagePrefix)
			p.inMessage = true
		} else if shouldInsertChunkSpace(p.lastMessage, rendered.Text) {
			fmt.Fprint(p.out, " ")
		}
		fmt.Fprint(p.out, rendered.Text)
		p.lastMessage = lastRune(rendered.Text, p.lastMessage)
		return
	}
	p.Flush()
	if rendered.Kind == "status" {
		if rendered.Text == p.lastStatus {
			return
		}
		p.lastStatus = rendered.Text
	} else {
		p.lastStatus = ""
	}
	fmt.Fprintf(p.out, "%s%s\n", eventPrefix(ev, rendered.Kind), rendered.Text)
}

func (p *agentEventPrinter) Flush() {
	if p.inMessage {
		fmt.Fprintln(p.out)
		p.inMessage = false
		p.messagePrefix = ""
		p.lastMessage = 0
	}
}

func renderAgentEventLine(ev *model.AgentEvent) string {
	rendered := renderAgentEvent(ev)
	switch rendered.Kind {
	case "", "raw":
		return rendered.Text
	case "message":
		return "message: " + rendered.Text
	case "thought":
		return "thought: " + rendered.Text
	case "stderr":
		return "stderr: " + rendered.Text
	case "turn":
		return "turn: " + rendered.Text
	case "status":
		return "status: " + rendered.Text
	default:
		if rendered.Text == "" {
			return rendered.Kind
		}
		return rendered.Kind + ": " + rendered.Text
	}
}

func renderAgentEvent(ev *model.AgentEvent) renderedAgentEvent {
	if ev == nil {
		return renderedAgentEvent{}
	}
	if ev.Stream == "acp.stderr" {
		if strings.TrimSpace(ev.Payload) != "" {
			return renderedAgentEvent{Kind: "stderr", Text: ev.Payload}
		}
		return renderedAgentEvent{Kind: "stderr", Text: "stderr"}
	}
	if ev.Stream == "acp.permission.request" {
		return renderACPPermissionEvent("permission", ev.Payload, "pending")
	}
	if ev.Stream == "acp.permission.decision" {
		return renderACPPermissionEvent("permission", ev.Payload, "decision")
	}

	if line, ok := renderACPResponseLine(ev.Payload); ok {
		return line
	}
	if line, ok := renderACPUpdateLine(ev.Payload); ok {
		return line
	}

	return renderedAgentEvent{Kind: "raw", Text: ev.Payload}
}

func renderACPPermissionEvent(kind, payload, fallback string) renderedAgentEvent {
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		return renderedAgentEvent{Kind: kind, Text: payload}
	}
	id := getJSONString(event, "id")
	command := getJSONString(event, "command")
	decision := getJSONString(event, "decision")
	reason := getJSONString(event, "reason")
	if decision == "" {
		decision = fallback
	}
	parts := []string{decision}
	if id != "" {
		parts = append(parts, id)
	}
	if command != "" {
		parts = append(parts, command)
	}
	if reason != "" {
		parts = append(parts, "("+reason+")")
	}
	return renderedAgentEvent{Kind: kind, Text: strings.Join(parts, " ")}
}

func eventPrefix(ev *model.AgentEvent, kind string) string {
	if kind == "" {
		kind = ev.Stream
	}
	return fmt.Sprintf("%s #%d %-8s ", ev.CreatedAt.Format("15:04:05"), ev.Seq, "["+kind+"]")
}

func renderACPResponseLine(payload string) (renderedAgentEvent, bool) {
	var message map[string]any
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		return renderedAgentEvent{}, false
	}
	if getJSONString(message, "method") == "session/request_permission" {
		params, _ := message["params"].(map[string]any)
		command := firstJSONString(params, "command", "message")
		if toolCall, _ := params["toolCall"].(map[string]any); toolCall != nil {
			command = firstJSONString(toolCall, "title", "command", "message")
			if rawInput, _ := toolCall["rawInput"].(map[string]any); rawInput != nil {
				if rawCommand := firstJSONString(rawInput, "command", "message"); rawCommand != "" {
					command = rawCommand
				}
			}
		}
		if command == "" {
			command = "requested"
		}
		return renderedAgentEvent{Kind: "permission", Text: command}, true
	}
	if errMsg, ok := message["error"].(map[string]any); ok {
		msg := strings.TrimSpace(firstJSONString(errMsg, "message", "error"))
		if data, _ := errMsg["data"].(map[string]any); data != nil {
			if detail := firstJSONString(data, "error", "message"); detail != "" {
				msg += ": " + strings.TrimSpace(detail)
			}
		}
		return renderedAgentEvent{Kind: "error", Text: msg}, msg != ""
	}
	result, _ := message["result"].(map[string]any)
	if result == nil {
		return renderedAgentEvent{}, false
	}
	if protocolVersion, ok := jsonNumber(result, "protocolVersion"); ok {
		name := "ACP adapter"
		if info, _ := result["agentInfo"].(map[string]any); info != nil {
			name = firstJSONString(info, "title", "name")
		}
		return renderedAgentEvent{Kind: "handshake", Text: fmt.Sprintf("%s protocol %s", name, protocolVersion)}, true
	}
	if sessionID := getJSONString(result, "sessionId"); sessionID != "" {
		text := sessionID
		if model := configOptionValue(result, "model"); model != "" {
			text += " model " + model
		}
		if effort := configOptionValue(result, "thought_level"); effort != "" {
			text += " effort " + effort
		}
		return renderedAgentEvent{Kind: "session", Text: text}, true
	}
	if stopReason := getJSONString(result, "stopReason"); stopReason != "" {
		return renderedAgentEvent{Kind: "turn", Text: "stopped: " + stopReason}, true
	}
	return renderedAgentEvent{}, false
}

func renderACPUpdateLine(payload string) (renderedAgentEvent, bool) {
	var message map[string]any
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		return renderedAgentEvent{}, false
	}
	if getJSONString(message, "method") != "session/update" {
		return renderedAgentEvent{}, false
	}

	params, _ := message["params"].(map[string]any)
	if params == nil {
		return renderedAgentEvent{}, false
	}
	update, _ := params["update"].(map[string]any)
	if update == nil {
		update = params
	}

	updateType := firstJSONString(update, "sessionUpdate", "type")
	if updateType == "tool_call_update" || getJSONString(params, "type") == "tool_call_update" {
		return renderACPToolCallLine(update, params)
	}
	switch updateType {
	case "agent_message_chunk":
		if chunk := firstJSONString(update, "delta", "text", "message"); chunk != "" {
			return renderedAgentEvent{Kind: "message", Text: chunk, MessageChunk: true}, true
		}
		if chunk := nestedJSONString(update, "content", "text"); chunk != "" {
			return renderedAgentEvent{Kind: "message", Text: chunk, MessageChunk: true}, true
		}
		return renderedAgentEvent{Kind: "message", Text: ""}, true
	case "agent_thought_chunk":
		if chunk := firstJSONString(update, "delta", "text", "message"); chunk != "" {
			if isACPStatusToken(chunk) {
				return renderedAgentEvent{Kind: "status", Text: strings.ReplaceAll(chunk, "_", " ")}, true
			}
			return renderedAgentEvent{Kind: "thought", Text: chunk, MessageChunk: true}, true
		}
		if chunk := nestedJSONString(update, "content", "text"); chunk != "" {
			if isACPStatusToken(chunk) {
				return renderedAgentEvent{Kind: "status", Text: strings.ReplaceAll(chunk, "_", " ")}, true
			}
			return renderedAgentEvent{Kind: "thought", Text: chunk, MessageChunk: true}, true
		}
		if detail := firstJSONString(update, "status", "state"); detail != "" {
			return renderedAgentEvent{Kind: "status", Text: strings.ReplaceAll(detail, "_", " ")}, true
		}
		return renderedAgentEvent{Kind: "thought", Text: "thinking"}, true
	case "available_commands_update":
		names := commandNames(update)
		if len(names) == 0 {
			names = commandNames(params)
		}
		if len(names) == 0 {
			return renderedAgentEvent{Kind: "commands", Text: "updated"}, true
		}
		return renderedAgentEvent{Kind: "commands", Text: strings.Join(names, ", ")}, true
	default:
		if updateType == "" {
			return renderedAgentEvent{}, false
		}
		label := strings.ReplaceAll(updateType, "_", " ")
		switch {
		case strings.Contains(updateType, "turn"):
			if detail := firstJSONString(update, "turn", "status", "state", "text", "delta"); detail != "" {
				return renderedAgentEvent{Kind: "turn", Text: detail}, true
			}
			return renderedAgentEvent{Kind: "turn", Text: label}, true
		case strings.Contains(updateType, "status"):
			if detail := firstJSONString(update, "status", "state", "turn", "text", "delta"); detail != "" {
				return renderedAgentEvent{Kind: "status", Text: detail}, true
			}
			return renderedAgentEvent{Kind: "status", Text: label}, true
		default:
			if detail := firstJSONString(update, "status", "turn", "text", "delta"); detail != "" {
				return renderedAgentEvent{Kind: "update", Text: label + ": " + detail}, true
			}
			return renderedAgentEvent{Kind: "update", Text: label}, true
		}
	}
}

func renderACPToolCallLine(update, params map[string]any) (renderedAgentEvent, bool) {
	status := firstJSONString(update, "status")
	if status == "" {
		status = firstJSONString(params, "status", "phase")
	}
	title := firstJSONString(update, "title", "message")
	if title == "" {
		title = firstJSONString(params, "message", "delta")
	}
	if title == "" {
		title = "tool call"
	}
	if status == "" {
		return renderedAgentEvent{Kind: "tool", Text: title}, true
	}
	status = strings.ReplaceAll(status, "_", " ")
	if decision := firstJSONString(params, "permissionDecision"); decision != "" {
		return renderedAgentEvent{Kind: "tool", Text: title + " - permission " + decision}, true
	}
	return renderedAgentEvent{Kind: "tool", Text: title + " - " + status}, true
}

func getJSONString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

func firstJSONString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s := getJSONString(m, key); strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func nestedJSONString(m map[string]any, key, nestedKey string) string {
	if m == nil {
		return ""
	}
	child, _ := m[key].(map[string]any)
	return getJSONString(child, nestedKey)
}

func jsonNumber(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	switch n := v.(type) {
	case float64:
		return fmt.Sprintf("%.0f", n), true
	case int:
		return fmt.Sprint(n), true
	case string:
		return n, true
	default:
		return "", false
	}
}

func configOptionValue(result map[string]any, id string) string {
	options, _ := result["configOptions"].([]any)
	for _, option := range options {
		m, _ := option.(map[string]any)
		if getJSONString(m, "id") == id {
			return firstJSONString(m, "currentValue", "value")
		}
	}
	return ""
}

func commandNames(m map[string]any) []string {
	commands, _ := m["availableCommands"].([]any)
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		cmd, _ := command.(map[string]any)
		if name := getJSONString(cmd, "name"); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func isACPStatusToken(s string) bool {
	switch strings.TrimSpace(s) {
	case "turn_started", "turn_completed", "item_started", "item_completed":
		return true
	default:
		return false
	}
}

func shouldInsertChunkSpace(prev rune, next string) bool {
	if prev != '.' && prev != '!' && prev != '?' {
		return false
	}
	for _, r := range next {
		return r != ' ' && r != '\n' && r != '\t'
	}
	return false
}

func lastRune(s string, fallback rune) rune {
	last := fallback
	for _, r := range s {
		last = r
	}
	return last
}
