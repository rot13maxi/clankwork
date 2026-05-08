package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/rot13maxi/clankwork/internal/model"
)

type Client struct {
	http    *http.Client
	baseURL string
	homeDir string
}

func New(homeDir string) *Client {
	socketPath := filepath.Join(homeDir, "clankwork.sock")
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	return &Client{
		http:    &http.Client{Transport: transport},
		baseURL: "http://unix",
		homeDir: homeDir,
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Translate connection errors into a friendly message.
		return fmt.Errorf("daemon request failed — start it with `clankwork daemon` if it is stopped: %w", err)
	}
	defer resp.Body.Close()

	var envelope model.APIResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return fmt.Errorf("[%s] %s", envelope.Error.Code, envelope.Error.Message)
		}
		return fmt.Errorf("request failed (status %d)", resp.StatusCode)
	}
	if out != nil && envelope.Data != nil {
		data, err := json.Marshal(envelope.Data)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, "GET", "/v1/health", nil, nil)
}

// PlansCreate registers a new plan.
func (c *Client) PlansCreate(ctx context.Context, title, body string) (*model.Plan, error) {
	return c.PlansCreateWithOptions(ctx, title, body, false)
}

func (c *Client) PlansCreateWithOptions(ctx context.Context, title, body string, withPriorArt bool) (*model.Plan, error) {
	var plan model.Plan
	err := c.do(ctx, "POST", "/v1/plans.create", model.CreatePlanRequest{Title: title, Body: body, WithPriorArt: withPriorArt}, &plan)
	return &plan, err
}

func (c *Client) PlansList(ctx context.Context) ([]*model.Plan, error) {
	var plans []*model.Plan
	err := c.do(ctx, "GET", "/v1/plans.list", nil, &plans)
	return plans, err
}

func (c *Client) PlansGet(ctx context.Context, id string) (map[string]any, error) {
	var detail map[string]any
	err := c.do(ctx, "GET", "/v1/plans.get?id="+id, nil, &detail)
	return detail, err
}

// ReposCreate registers a git repository.
func (c *Client) ReposCreate(ctx context.Context, name, path, targetBranch, verifyCommand, lintCommand, typecheckCommand string, autoPush bool) (*model.Repo, error) {
	var repo model.Repo
	err := c.do(ctx, "POST", "/v1/repos.create",
		model.CreateRepoRequest{Name: name, Path: path, TargetBranch: targetBranch, VerifyCommand: verifyCommand, LintCommand: lintCommand, TypecheckCommand: typecheckCommand, AutoPush: autoPush}, &repo)
	return &repo, err
}

// RepoGet fetches a single repo by ID.
func (c *Client) RepoGet(ctx context.Context, id string) (*model.Repo, error) {
	var repo model.Repo
	err := c.do(ctx, "GET", "/v1/repos.get?id="+id, nil, &repo)
	return &repo, err
}

func (c *Client) ReposList(ctx context.Context) ([]*model.Repo, error) {
	var repos []*model.Repo
	err := c.do(ctx, "GET", "/v1/repos.list", nil, &repos)
	return repos, err
}

// TasksCreate creates a new task.
func (c *Client) TasksCreate(ctx context.Context, req model.CreateTaskRequest) (*model.Task, error) {
	var task model.Task
	err := c.do(ctx, "POST", "/v1/tasks.create", req, &task)
	return &task, err
}

func (c *Client) TasksList(ctx context.Context, planID, repoID string, statuses []string) ([]*model.Task, error) {
	q := "/v1/tasks.list?"
	if planID != "" {
		q += "plan_id=" + planID + "&"
	}
	if repoID != "" {
		q += "repo_id=" + repoID + "&"
	}
	if len(statuses) > 0 {
		q += "status=" + strings.Join(statuses, ",") + "&"
	}
	var tasks []*model.Task
	err := c.do(ctx, "GET", q, nil, &tasks)
	return tasks, err
}

func (c *Client) TasksGet(ctx context.Context, id string) (map[string]any, error) {
	var detail map[string]any
	err := c.do(ctx, "GET", "/v1/tasks.get?id="+id, nil, &detail)
	return detail, err
}

func (c *Client) TaskDiagnose(ctx context.Context, id string) (*model.TaskDiagnosis, error) {
	var diag model.TaskDiagnosis
	err := c.do(ctx, "GET", "/v1/tasks.diagnose?id="+url.QueryEscape(id), nil, &diag)
	return &diag, err
}

// TaskGetByName returns a task by name prefix.
func (c *Client) TaskGetByName(ctx context.Context, name string) (*model.Task, error) {
	var task model.Task
	err := c.do(ctx, "GET", "/v1/tasks.getByName?name="+name, nil, &task)
	return &task, err
}

func (c *Client) TasksAddDep(ctx context.Context, taskID, dependsOnID string) error {
	return c.do(ctx, "POST", "/v1/tasks.addDep",
		model.AddDepRequest{TaskID: taskID, DependsOnID: dependsOnID}, nil)
}

func (c *Client) TasksSetPriority(ctx context.Context, taskID string, priority int) error {
	return c.do(ctx, "POST", "/v1/tasks.setPriority",
		model.SetPriorityRequest{TaskID: taskID, Priority: priority}, nil)
}

// TasksRetry resets a failed task to pending.
func (c *Client) TasksRetry(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/v1/tasks.retry?id="+id, nil, nil)
}

func (c *Client) TaskClose(ctx context.Context, req model.CloseTaskRequest) error {
	return c.do(ctx, "POST", "/v1/tasks.close", req, nil)
}

func (c *Client) TaskRetryStep(ctx context.Context, taskID, step string) error {
	return c.do(ctx, "POST", "/v1/tasks.retryStep", model.TaskResetStepRequest{TaskID: taskID, Step: step}, nil)
}

func (c *Client) TaskResetStep(ctx context.Context, taskID, step string) error {
	return c.do(ctx, "POST", "/v1/tasks.resetStep", model.TaskResetStepRequest{TaskID: taskID, Step: step}, nil)
}

func (c *Client) TaskEscalate(ctx context.Context, req model.TaskEscalateRequest) (*model.Escalation, error) {
	var esc model.Escalation
	err := c.do(ctx, "POST", "/v1/tasks.escalate", req, &esc)
	return &esc, err
}

// Signal emits a lifecycle signal for a task.
func (c *Client) Signal(ctx context.Context, eventName, taskID, message string) error {
	return c.do(ctx, "POST", "/v1/signals."+eventName,
		model.SignalRequest{TaskID: taskID, Message: message}, nil)
}

func (c *Client) SignalWithPayload(ctx context.Context, eventName string, req model.SignalRequest) error {
	return c.do(ctx, "POST", "/v1/signals."+eventName, req, nil)
}

func (c *Client) AcceptanceSpecGet(ctx context.Context, taskID string) (*model.AcceptanceSpec, error) {
	var spec model.AcceptanceSpec
	err := c.do(ctx, "GET", "/v1/acceptance.spec?task_id="+taskID, nil, &spec)
	return &spec, err
}

func (c *Client) AcceptanceSpecPut(ctx context.Context, spec *model.AcceptanceSpec) (*model.AcceptanceSpec, error) {
	var out model.AcceptanceSpec
	err := c.do(ctx, "POST", "/v1/acceptance.spec", spec, &out)
	return &out, err
}

func (c *Client) ArtifactAdd(ctx context.Context, req model.AddArtifactRequest) (*model.Artifact, error) {
	var artifact model.Artifact
	err := c.do(ctx, "POST", "/v1/artifacts.add", req, &artifact)
	return &artifact, err
}

func (c *Client) ArtifactsList(ctx context.Context, taskID string) ([]*model.Artifact, error) {
	var artifacts []*model.Artifact
	err := c.do(ctx, "GET", "/v1/artifacts.list?task_id="+url.QueryEscape(taskID), nil, &artifacts)
	return artifacts, err
}

// Bootstrap fetches the agent startup bundle.
func (c *Client) Bootstrap(ctx context.Context, taskID, role, repoID string) (*model.BootstrapResponse, error) {
	var resp model.BootstrapResponse
	err := c.do(ctx, "POST", "/v1/bootstrap",
		model.BootstrapRequest{TaskID: taskID, Role: role, RepoID: repoID}, &resp)
	return &resp, err
}

// ContextGet fetches task/plan/learnings context.
func (c *Client) ContextGet(ctx context.Context, taskID string) (map[string]any, error) {
	var ctx2 map[string]any
	err := c.do(ctx, "POST", "/v1/context.get", map[string]string{"task_id": taskID}, &ctx2)
	return ctx2, err
}

// LearningsAdd appends a new learning.
func (c *Client) LearningsAdd(ctx context.Context, category, title, body string) (*model.Learning, error) {
	var l model.Learning
	err := c.do(ctx, "POST", "/v1/learnings.add",
		model.AddLearningRequest{Category: category, Title: title, Body: body}, &l)
	return &l, err
}

func (c *Client) CandidateLearningAdd(ctx context.Context, req model.AddCandidateLearningRequest) (*model.CandidateLearning, error) {
	var candidate model.CandidateLearning
	err := c.do(ctx, "POST", "/v1/learnings.candidateAdd", req, &candidate)
	return &candidate, err
}

func (c *Client) CandidateLearningList(ctx context.Context, status string) ([]*model.CandidateLearning, error) {
	var candidates []*model.CandidateLearning
	path := "/v1/learnings.candidateList"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	err := c.do(ctx, "GET", path, nil, &candidates)
	return candidates, err
}

func (c *Client) PriorArtSearch(ctx context.Context, req model.PriorArtSearchRequest) (*model.PriorArtSearchResponse, error) {
	q := "/v1/prior-art.search?"
	if req.Query != "" {
		q += "q=" + url.QueryEscape(req.Query) + "&"
	}
	if req.RepoID != "" {
		q += "repo_id=" + url.QueryEscape(req.RepoID) + "&"
	}
	if req.Template != "" {
		q += "template=" + url.QueryEscape(req.Template) + "&"
	}
	if req.Status != "" {
		q += "status=" + url.QueryEscape(req.Status) + "&"
	}
	if req.MinReworkScore > 0 {
		q += "min_rework_score=" + fmt.Sprintf("%.3f", req.MinReworkScore) + "&"
	}
	if req.MinRiskScore > 0 {
		q += "min_risk_score=" + fmt.Sprintf("%.3f", req.MinRiskScore) + "&"
	}
	if req.Limit > 0 {
		q += "limit=" + fmt.Sprintf("%d", req.Limit) + "&"
	}
	var resp model.PriorArtSearchResponse
	err := c.do(ctx, "GET", q, nil, &resp)
	return &resp, err
}

func (c *Client) PriorArtShow(ctx context.Context, taskID string) (*model.PriorArtHistory, error) {
	var history model.PriorArtHistory
	err := c.do(ctx, "GET", "/v1/prior-art.show?task_id="+url.QueryEscape(taskID), nil, &history)
	return &history, err
}

func (c *Client) PriorArtRebuild(ctx context.Context) (int, error) {
	var resp struct {
		Indexed int `json:"indexed"`
	}
	err := c.do(ctx, "POST", "/v1/prior-art.rebuild", nil, &resp)
	return resp.Indexed, err
}

// TracesList returns traces with optional filters.
func (c *Client) TracesList(ctx context.Context, taskID, eventType, since string, limit int, template, retries, outcome, pathGlob string) ([]*model.Trace, error) {
	q := "/v1/traces.list?"
	if taskID != "" {
		q += "task_id=" + taskID + "&"
	}
	if eventType != "" {
		q += "type=" + eventType + "&"
	}
	if since != "" {
		q += "since=" + since + "&"
	}
	if limit > 0 {
		q += "limit=" + fmt.Sprintf("%d", limit) + "&"
	}
	if template != "" {
		q += "template=" + template + "&"
	}
	if retries != "" {
		q += "retries=" + retries + "&"
	}
	if outcome != "" {
		q += "outcome=" + outcome + "&"
	}
	if pathGlob != "" {
		q += "path=" + pathGlob + "&"
	}
	var traces []*model.Trace
	err := c.do(ctx, "GET", q, nil, &traces)
	return traces, err
}

// AgentGetByTask returns the most recent agent for a task.
func (c *Client) AgentGetByTask(ctx context.Context, taskID string) (*model.Agent, error) {
	var agent model.Agent
	err := c.do(ctx, "GET", "/v1/agents.getByTask?task_id="+taskID, nil, &agent)
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

// AgentsGet returns a single agent by ID.
func (c *Client) AgentsGet(ctx context.Context, id string) (*model.Agent, error) {
	var agent model.Agent
	err := c.do(ctx, "GET", "/v1/agents.get?id="+id, nil, &agent)
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

// AgentsList returns agents, optionally filtered by status.
func (c *Client) AgentsList(ctx context.Context, status string) ([]*model.Agent, error) {
	var agents []*model.Agent
	q := "/v1/agents.list"
	if status != "" {
		q += "?status=" + status
	}
	err := c.do(ctx, "GET", q, nil, &agents)
	return agents, err
}

func (c *Client) AgentEvents(ctx context.Context, agentID, taskID string, afterSeq int64, limit int) ([]*model.AgentEvent, error) {
	q := "/v1/agents.events?"
	if agentID != "" {
		q += "agent_id=" + agentID + "&"
	}
	if taskID != "" {
		q += "task_id=" + taskID + "&"
	}
	if afterSeq > 0 {
		q += "after_seq=" + fmt.Sprintf("%d", afterSeq) + "&"
	}
	if limit > 0 {
		q += "limit=" + fmt.Sprintf("%d", limit) + "&"
	}
	var events []*model.AgentEvent
	err := c.do(ctx, "GET", q, nil, &events)
	return events, err
}

func (c *Client) AgentPermissions(ctx context.Context, agentID string) ([]*model.ACPPermissionRequest, error) {
	var pending []*model.ACPPermissionRequest
	err := c.do(ctx, "GET", "/v1/agents.permissions?agent_id="+agentID, nil, &pending)
	return pending, err
}

func (c *Client) AgentPermissionDecision(ctx context.Context, agentID, requestID, decision string) error {
	return c.do(ctx, "POST", "/v1/agents.permissionDecision", model.AgentPermissionDecisionRequest{
		AgentID:   agentID,
		RequestID: requestID,
		Decision:  decision,
	}, nil)
}

func (c *Client) AgentSend(ctx context.Context, agentID, message string) error {
	return c.do(ctx, "POST", "/v1/agents.send", model.AgentSendRequest{AgentID: agentID, Message: message}, nil)
}

func (c *Client) AgentCancel(ctx context.Context, agentID string) error {
	return c.do(ctx, "POST", "/v1/agents.cancel", model.AgentSendRequest{AgentID: agentID}, nil)
}

func (c *Client) ReconcileTask(ctx context.Context, taskID string) (*model.TaskDiagnosis, error) {
	var diag model.TaskDiagnosis
	err := c.do(ctx, "POST", "/v1/reconcile.task", model.ReconcileRequest{TaskID: taskID}, &diag)
	return &diag, err
}

func (c *Client) ReconcileAll(ctx context.Context) error {
	return c.do(ctx, "POST", "/v1/reconcile.all", model.ReconcileRequest{}, nil)
}

func (c *Client) RefreshTask(ctx context.Context, taskID string) (*model.TaskDiagnosis, error) {
	var diag model.TaskDiagnosis
	err := c.do(ctx, "POST", "/v1/refresh.task", model.RefreshRequest{TaskID: taskID}, &diag)
	return &diag, err
}

func (c *Client) RefreshAgent(ctx context.Context, agentID string) error {
	return c.do(ctx, "POST", "/v1/refresh.agent", model.RefreshRequest{AgentID: agentID}, nil)
}

func (c *Client) RefreshWorktree(ctx context.Context, taskID string) error {
	return c.do(ctx, "POST", "/v1/refresh.worktree", model.RefreshRequest{TaskID: taskID}, nil)
}

func (c *Client) EventsList(ctx context.Context, target, taskID string, limit int) ([]*model.ControlPlaneEvent, error) {
	q := "/v1/events.list?"
	if target != "" {
		q += "target=" + url.QueryEscape(target) + "&"
	}
	if taskID != "" {
		q += "task_id=" + url.QueryEscape(taskID) + "&"
	}
	if limit > 0 {
		q += "limit=" + fmt.Sprintf("%d", limit)
	}
	var events []*model.ControlPlaneEvent
	err := c.do(ctx, "GET", q, nil, &events)
	return events, err
}

func (c *Client) EscalationsList(ctx context.Context, taskID, status string) ([]*model.Escalation, error) {
	q := "/v1/escalations.list?"
	if taskID != "" {
		q += "task_id=" + url.QueryEscape(taskID) + "&"
	}
	if status != "" {
		q += "status=" + url.QueryEscape(status)
	}
	var escalations []*model.Escalation
	err := c.do(ctx, "GET", q, nil, &escalations)
	return escalations, err
}

func (c *Client) EscalationResolve(ctx context.Context, id, outcome string) error {
	return c.do(ctx, "POST", "/v1/escalations.resolve", model.EscalationResolveRequest{EscalationID: id, Outcome: outcome}, nil)
}

// DispatchPause pauses the scheduler dispatcher.
func (c *Client) DispatchPause(ctx context.Context) error {
	return c.do(ctx, "POST", "/v1/dispatch.pause", nil, nil)
}

// DispatchResume resumes the scheduler dispatcher.
func (c *Client) DispatchResume(ctx context.Context) error {
	return c.do(ctx, "POST", "/v1/dispatch.resume", nil, nil)
}

// DispatchStatus returns whether dispatch is paused.
func (c *Client) DispatchStatus(ctx context.Context) (bool, error) {
	var result map[string]bool
	err := c.do(ctx, "GET", "/v1/dispatch.status", nil, &result)
	return result["paused"], err
}

// Status returns the system overview.
func (c *Client) Status(ctx context.Context) (*model.StatusResponse, error) {
	var s model.StatusResponse
	err := c.do(ctx, "GET", "/v1/status", nil, &s)
	return &s, err
}

func (c *Client) QueueList(ctx context.Context) ([]*model.MergeQueueItem, error) {
	var items []*model.MergeQueueItem
	err := c.do(ctx, "GET", "/v1/queue.list", nil, &items)
	return items, err
}

func (c *Client) QueueSkip(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/v1/queue.skip", model.QueueSkipRequest{ItemID: id}, nil)
}

func (c *Client) QueueRetry(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/v1/queue.retry", model.QueueSkipRequest{ItemID: id}, nil)
}

// ConfigGet fetches the current daemon configuration.
func (c *Client) ConfigGet(ctx context.Context, out any) error {
	return c.do(ctx, "GET", "/v1/config", nil, out)
}
