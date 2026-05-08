package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rot13maxi/clankwork/internal/model"
)

func TestDisplayTask(t *testing.T) {
	tests := []struct {
		name string
		task *model.Task
		want string
	}{
		{name: "nil", task: nil, want: "-"},
		{name: "task name", task: &model.Task{ID: "task-1", Name: "friendly", Title: "Title"}, want: "friendly"},
		{name: "title fallback", task: &model.Task{ID: "task-1", Title: "Title"}, want: "Title"},
		{name: "id fallback", task: &model.Task{ID: "task-1"}, want: "task-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayTask(tt.task); got != tt.want {
				t.Fatalf("displayTask() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSnapshotLookups(t *testing.T) {
	snap := Snapshot{
		Tasks:  []*model.Task{{ID: "task-1", Title: "Build TUI"}},
		Agents: []*model.Agent{{ID: "agent-1", TaskID: "task-1"}},
	}

	if got := snap.TaskByID("task-1"); got == nil || got.Title != "Build TUI" {
		t.Fatalf("TaskByID() = %#v, want task", got)
	}
	if got := snap.AgentByTaskID("task-1"); got == nil || got.ID != "agent-1" {
		t.Fatalf("AgentByTaskID() = %#v, want agent", got)
	}
	if got := snap.TaskByID("missing"); got != nil {
		t.Fatalf("TaskByID(missing) = %#v, want nil", got)
	}
}

func TestLimitLines(t *testing.T) {
	got := limitLines("a\nb\nc", 2)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[1], "...") {
		t.Fatalf("last line = %q, want ellipsis marker", lines[1])
	}
}

func TestTruncate(t *testing.T) {
	got := truncate("abcdef", 5)
	if got != "ab..." {
		t.Fatalf("truncate() = %q, want %q", got, "ab...")
	}
	got = truncate("abc\ndef", 20)
	if got != "abc def" {
		t.Fatalf("truncate() = %q, want newline collapsed", got)
	}
}

func TestResolveSelectedEscalationAction(t *testing.T) {
	backend := &fakeBackend{}
	model := appModel{
		loader:   backend,
		mode:     ModeEscalations,
		selected: map[Mode]int{ModeEscalations: 0},
		snapshot: Snapshot{Escalations: []*model.Escalation{{ID: "esc-1", TaskID: "task-1", Status: "open"}}},
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("expected escalation resolve command")
	}
	model = updated.(appModel)
	if !model.acting {
		t.Fatal("model should show action in progress")
	}

	msg := cmd()
	updated, _ = model.Update(msg)
	model = updated.(appModel)
	if backend.resolvedID != "esc-1" {
		t.Fatalf("resolvedID = %q, want esc-1", backend.resolvedID)
	}
	if backend.resolvedOutcome != "resolved_from_tui" {
		t.Fatalf("resolvedOutcome = %q, want resolved_from_tui", backend.resolvedOutcome)
	}
	if model.notice == "" || model.acting {
		t.Fatalf("notice = %q acting = %v, want completed action notice", model.notice, model.acting)
	}
}

type fakeBackend struct {
	resolvedID      string
	resolvedOutcome string
}

func (f *fakeBackend) Snapshot(ctx context.Context) Snapshot {
	return Snapshot{}
}

func (f *fakeBackend) ResolveEscalation(ctx context.Context, id, outcome string) error {
	f.resolvedID = id
	f.resolvedOutcome = outcome
	return nil
}

func (f *fakeBackend) RetryTaskStep(ctx context.Context, taskID, step string) error {
	return nil
}

func (f *fakeBackend) RetryQueueItem(ctx context.Context, id string) error {
	return nil
}

func (f *fakeBackend) SkipQueueItem(ctx context.Context, id string) error {
	return nil
}
