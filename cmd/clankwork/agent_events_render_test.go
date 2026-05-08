package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/rot13maxi/clankwork/internal/model"
)

func TestRenderAgentEventLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ev   *model.AgentEvent
		want string
	}{
		{
			name: "message chunk text",
			ev: &model.AgentEvent{
				Stream:  "acp.recv",
				Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","type":"text","text":"received"}}}`,
			},
			want: "message: received",
		},
		{
			name: "message chunk delta",
			ev: &model.AgentEvent{
				Stream:  "acp.recv",
				Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","delta":"hel"}}}`,
			},
			want: "message: hel",
		},
		{
			name: "message chunk nested content",
			ev: &model.AgentEvent{
				Stream:  "acp.recv",
				Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"nested"}}}}`,
			},
			want: "message: nested",
		},
		{
			name: "turn status update",
			ev: &model.AgentEvent{
				Stream:  "acp.recv",
				Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"turn_started","status":"running"}}}`,
			},
			want: "turn: running",
		},
		{
			name: "thought chunk",
			ev: &model.AgentEvent{
				Stream:  "acp.recv",
				Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"thinking"}}}}`,
			},
			want: "thought: thinking",
		},
		{
			name: "permission request",
			ev: &model.AgentEvent{
				Stream:  "acp.recv",
				Payload: `{"jsonrpc":"2.0","id":"server-1","method":"session/request_permission","params":{"toolCall":{"rawInput":{"command":"clankwork bootstrap"}}}}`,
			},
			want: "permission: clankwork bootstrap",
		},
		{
			name: "persisted permission request",
			ev: &model.AgentEvent{
				Stream:  "acp.permission.request",
				Payload: `{"id":"perm-1","decision":"pending","command":"cat README.md","policy":"manual"}`,
			},
			want: "permission: pending perm-1 cat README.md",
		},
		{
			name: "persisted permission decision",
			ev: &model.AgentEvent{
				Stream:  "acp.permission.decision",
				Payload: `{"id":"perm-1","decision":"allow","command":"cat README.md","reason":"manual decision"}`,
			},
			want: "permission: allow perm-1 cat README.md (manual decision)",
		},
		{
			name: "tool call update",
			ev: &model.AgentEvent{
				Stream:  "acp.recv",
				Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"type":"tool_call_update","status":"in_progress","message":"pwd","update":{"sessionUpdate":"tool_call_update","title":"pwd","status":"in_progress"}}}`,
			},
			want: "tool: pwd - in progress",
		},
		{
			name: "stderr stream",
			ev: &model.AgentEvent{
				Stream:  "acp.stderr",
				Payload: "warn: something happened",
			},
			want: "stderr: warn: something happened",
		},
		{
			name: "fallback raw payload",
			ev: &model.AgentEvent{
				Stream:  "acp.recv",
				Payload: "plain payload",
			},
			want: "plain payload",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := renderAgentEventLine(tt.ev); got != tt.want {
				t.Fatalf("renderAgentEventLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAgentEventPrinterCoalescesMessageChunks(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 4, 22, 1, 2, 3, 0, time.UTC)
	events := []*model.AgentEvent{
		{Seq: 1, Stream: "acp.recv", CreatedAt: ts, Payload: `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentInfo":{"title":"ACP Adapter"}}}`},
		{Seq: 2, Stream: "acp.recv", CreatedAt: ts, Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","text":"Hello"}}}`},
		{Seq: 3, Stream: "acp.recv", CreatedAt: ts, Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","text":" world."}}}`},
		{Seq: 4, Stream: "acp.recv", CreatedAt: ts, Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","text":" Next sentence."}}}`},
		{Seq: 5, Stream: "acp.recv", CreatedAt: ts, Payload: `{"jsonrpc":"2.0","id":4,"error":{"message":"begin turn failed","data":{"error":"active turn already exists"}}}`},
	}

	var out bytes.Buffer
	printer := newAgentEventPrinter(&out)
	for _, ev := range events {
		printer.Print(ev)
	}
	printer.Flush()

	want := "01:02:03 #1 [handshake] ACP Adapter protocol 1\n" +
		"01:02:03 #2 [message] Hello world. Next sentence.\n" +
		"01:02:03 #5 [error]  begin turn failed: active turn already exists\n"
	if got := out.String(); got != want {
		t.Fatalf("printed transcript:\n%s\nwant:\n%s", got, want)
	}
}

func TestAgentEventPrinterCoalescesThoughtChunks(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 4, 22, 1, 2, 3, 0, time.UTC)
	events := []*model.AgentEvent{
		{Seq: 1, Stream: "acp.recv", CreatedAt: ts, Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_thought_chunk","content":{"text":"Let","type":"text"}}}}`},
		{Seq: 2, Stream: "acp.recv", CreatedAt: ts, Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_thought_chunk","content":{"text":" me think.","type":"text"}}}}`},
		{Seq: 3, Stream: "acp.recv", CreatedAt: ts, Payload: `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_thought_chunk","content":{"text":" Next step.","type":"text"}}}}`},
	}

	var out bytes.Buffer
	printer := newAgentEventPrinter(&out)
	for _, ev := range events {
		printer.Print(ev)
	}
	printer.Flush()

	want := "01:02:03 #1 [thought] Let me think. Next step.\n"
	if got := out.String(); got != want {
		t.Fatalf("printed transcript:\n%s\nwant:\n%s", got, want)
	}
}
