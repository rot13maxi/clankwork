package tui

import (
	"context"
	"time"

	"github.com/rot13maxi/clankwork/internal/client"
	"github.com/rot13maxi/clankwork/internal/model"
)

type Snapshot struct {
	LoadedAt    time.Time
	HealthError string

	Status      *model.StatusResponse
	Tasks       []*model.Task
	Agents      []*model.Agent
	Escalations []*model.Escalation
	Queue       []*model.MergeQueueItem
	Events      []*model.ControlPlaneEvent
}

type Loader struct {
	client *client.Client
}

type Backend interface {
	Snapshot(ctx context.Context) Snapshot
	ResolveEscalation(ctx context.Context, id, outcome string) error
	RetryTaskStep(ctx context.Context, taskID, step string) error
	RetryQueueItem(ctx context.Context, id string) error
	SkipQueueItem(ctx context.Context, id string) error
}

func NewLoader(home string) *Loader {
	return &Loader{client: client.New(home)}
}

func (l *Loader) Snapshot(ctx context.Context) Snapshot {
	snap := Snapshot{LoadedAt: time.Now()}
	if err := l.client.Health(ctx); err != nil {
		snap.HealthError = err.Error()
	}
	if status, err := l.client.Status(ctx); err == nil {
		snap.Status = status
	} else if snap.HealthError == "" {
		snap.HealthError = err.Error()
	}
	if tasks, err := l.client.TasksList(ctx, "", "", nil); err == nil {
		snap.Tasks = tasks
	}
	if agents, err := l.client.AgentsList(ctx, "running"); err == nil {
		snap.Agents = agents
	}
	if escalations, err := l.client.EscalationsList(ctx, "", "open"); err == nil {
		snap.Escalations = escalations
	}
	if queue, err := l.client.QueueList(ctx); err == nil {
		snap.Queue = queue
	}
	if events, err := l.client.EventsList(ctx, "", "", 20); err == nil {
		snap.Events = events
	}
	return snap
}

func (s Snapshot) TaskByID(id string) *model.Task {
	for _, task := range s.Tasks {
		if task.ID == id {
			return task
		}
	}
	return nil
}

func (s Snapshot) AgentByTaskID(taskID string) *model.Agent {
	for _, agent := range s.Agents {
		if agent.TaskID == taskID {
			return agent
		}
	}
	return nil
}

func (l *Loader) ResolveEscalation(ctx context.Context, id, outcome string) error {
	return l.client.EscalationResolve(ctx, id, outcome)
}

func (l *Loader) RetryTaskStep(ctx context.Context, taskID, step string) error {
	return l.client.TaskRetryStep(ctx, taskID, step)
}

func (l *Loader) RetryQueueItem(ctx context.Context, id string) error {
	return l.client.QueueRetry(ctx, id)
}

func (l *Loader) SkipQueueItem(ctx context.Context, id string) error {
	return l.client.QueueSkip(ctx, id)
}
