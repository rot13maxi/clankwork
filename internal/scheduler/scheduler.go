package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/rot13maxi/clankwork/internal/config"
	"github.com/rot13maxi/clankwork/internal/model"
	"github.com/rot13maxi/clankwork/internal/runtimeenv"
	"github.com/rot13maxi/clankwork/internal/store"
	tmplpkg "github.com/rot13maxi/clankwork/internal/template"
	"github.com/rot13maxi/clankwork/internal/worker"
	"github.com/rot13maxi/clankwork/internal/workflow"
)

// TaskCompletedHook is called whenever a task reaches "done" status via RouteStep.
// Used by the merge queue to enqueue auto-merge tasks without a circular import.
type TaskCompletedHook func(ctx context.Context, taskID string)

// TaskFailedHook is called whenever a task reaches "failed" status.
// Used by the merge queue to handle conflict-resolver failures.
type TaskFailedHook func(ctx context.Context, taskID string)

type Dispatcher struct {
	ctx            context.Context
	store          *store.Store
	spawner        worker.AgentRuntime
	worktree       worker.WorktreeCreator
	homeDir        string
	cfg            *config.Config
	mu             sync.Mutex
	userPaused     bool
	queuePressured bool
	queuePressure  model.QueuePressureDecision

	onTaskCompleted TaskCompletedHook
	onTaskFailed    TaskFailedHook
}

func New(ctx context.Context, st *store.Store, spawner worker.AgentRuntime, wt worker.WorktreeCreator, homeDir string, cfg *config.Config) *Dispatcher {
	return &Dispatcher{
		ctx:      ctx,
		store:    st,
		spawner:  spawner,
		worktree: wt,
		homeDir:  homeDir,
		cfg:      cfg,
	}
}

// SetTaskCompletedHook registers a callback fired when any task reaches "done" via RouteStep.
func (d *Dispatcher) SetTaskCompletedHook(h TaskCompletedHook) {
	d.onTaskCompleted = h
}

// SetTaskFailedHook registers a callback fired when any task reaches "failed".
func (d *Dispatcher) SetTaskFailedHook(h TaskFailedHook) {
	d.onTaskFailed = h
}

func (d *Dispatcher) Tick(ctx context.Context) error {
	d.mu.Lock()
	pressure := d.queuePressure
	if d.userPaused || d.queuePressured || pressure.ShouldPause {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()

	running, err := d.store.AgentRunningCount(ctx)
	if err != nil {
		return fmt.Errorf("agent count: %w", err)
	}
	slots := d.cfg.Scheduler.MaxSlots - running
	if pressure.Level == model.QueuePressureReduced && pressure.MaxDispatch > 0 && slots > pressure.MaxDispatch {
		slots = pressure.MaxDispatch
	}
	if slots <= 0 {
		return nil
	}

	tasks, err := d.store.TasksReady(ctx, slots)
	if err != nil {
		return fmt.Errorf("tasks ready: %w", err)
	}

	for _, task := range tasks {
		if err := d.dispatch(ctx, task); err != nil {
			slog.Error("dispatch failed", "task", task.ID, "err", err)
			d.recordDispatchFailure(ctx, task, err)
		}
	}
	return nil
}

func (d *Dispatcher) recordDispatchFailure(ctx context.Context, task *model.Task, dispatchErr error) {
	if task == nil || dispatchErr == nil {
		return
	}
	reason := dispatchErr.Error()
	_ = d.store.ControlObservationPut(ctx, &model.ControlObservation{
		TargetType: "task",
		TargetID:   task.ID,
		TaskID:     task.ID,
		Kind:       "dispatch",
		Status:     "failed",
		Reason:     reason,
	})
	_ = d.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "dispatch_controller",
		TaskID:       task.ID,
		StepName:     task.CurrentStep,
		TargetType:   "task",
		TargetID:     task.ID,
		DecisionKind: "dispatch_failure",
		Action:       "inspect_runtime",
		Reason:       reason,
		Retryable:    true,
	})
	_ = d.store.ControllerActuationAppend(ctx, &model.ControllerActuation{
		RequestedOperation: "task.dispatch",
		ActorType:          "controller",
		ActorID:            "dispatch_controller",
		TargetType:         "task",
		TargetID:           task.ID,
		TaskID:             task.ID,
		StepName:           task.CurrentStep,
		PreviousState:      task.Status,
		NewState:           task.Status,
		Outcome:            "failed",
		Error:              reason,
		Reason:             "dispatcher failed to start runtime",
	})
}

func (d *Dispatcher) dispatch(ctx context.Context, task *model.Task) error {
	// Auto-classify tasks that have no template set.
	if task.Template == "" {
		template := TriageTask(task)
		task.Template = template
		if err := d.store.TaskSetTemplate(ctx, task.ID, template); err != nil {
			slog.Warn("dispatch: failed to persist triage template", "task", task.ID, "template", template, "err", err)
		}
	}

	if task.Template != "" {
		return d.dispatchTemplate(ctx, task)
	}
	return d.dispatchSimple(ctx, task)
}

// dispatchSimple is the M2 path: no template, spawn agent directly.
func (d *Dispatcher) dispatchSimple(ctx context.Context, task *model.Task) error {
	runtimeName := task.Runtime
	if runtimeName == "" {
		runtimeName = "default"
	}
	rt, ok := d.cfg.Runtimes[runtimeName]
	if !ok {
		return fmt.Errorf("unknown runtime %q", runtimeName)
	}

	var worktreePath string
	if task.RepoID != "" {
		repo, err := d.store.RepoGet(ctx, task.RepoID)
		if err != nil {
			return fmt.Errorf("get repo: %w", err)
		}
		targetBranch := repo.TargetBranch
		if targetBranch == "" {
			targetBranch = "main"
		}
		worktreePath, err = d.worktree.Create(repo.Path, task.ID, targetBranch)
		if err != nil {
			return fmt.Errorf("create worktree: %w", err)
		}
		d.installPreCommitHook(ctx, worktreePath, task.ID)
		if config.RuntimeTransport(rt) != config.TransportACP {
			d.writeAgentInstructions(worktreePath, task.ID, "implement")
		}
	}

	sessionName := "clankwork-worker-" + task.ID
	logDir := filepath.Join(d.homeDir, "logs")
	logfilePath := filepath.Join(logDir, sessionName+".log")
	transport := config.RuntimeTransport(rt)
	d.killExistingSession(sessionName)
	if err := d.configureTransport(sessionName, transport); err != nil {
		return fmt.Errorf("configure runtime transport: %w", err)
	}

	env := map[string]string{
		"CLANKWORK_TASK_ID":     task.ID,
		"CLANKWORK_ROLE":        task.Role,
		"CLANKWORK_REPO_ID":     task.RepoID,
		"CLANKWORK_HOME":        d.homeDir,
		"HOME":                  os.Getenv("HOME"),
		"PATH":                  agentPath(),
		"CLAUDE_CODE_SANDBOXED": "1",
	}
	builtEnv, err := runtimeenv.Build(d.homeDir, runtimeName, rt, env)
	if err != nil {
		if worktreePath != "" {
			d.worktree.Remove(worktreePath)
		}
		return fmt.Errorf("build runtime env: %w", err)
	}
	env = builtEnv
	addACPPermissionEnv(env, rt)

	workdir := worktreePath
	if workdir == "" {
		workdir = d.homeDir
	}

	args := rt.Args
	if rt.NonInteractive && transport != config.TransportACP {
		args = append(append([]string{}, rt.Args...), bootstrapMessage("implement"))
	}

	if err := d.spawner.Spawn(sessionName, workdir, rt.Command, args, env); err != nil {
		if worktreePath != "" {
			d.worktree.Remove(worktreePath)
		}
		return fmt.Errorf("spawn agent: %w", err)
	}

	agentID := ulid.Make().String()
	pid := d.runtimePID(sessionName)
	runtimeSessionID := d.runtimeSessionID(sessionName)
	if _, err := d.store.AgentCreateWithRuntime(ctx, agentID, task.ID, 0, sessionName, transport, runtimeSessionID, pid, logfilePath, worktreePath, runtimeName, rt.Model); err != nil {
		d.spawner.Kill(sessionName)
		if worktreePath != "" {
			d.worktree.Remove(worktreePath)
		}
		return fmt.Errorf("create agent record: %w", err)
	}
	d.bindAgentEvents(sessionName, agentID, task.ID)

	if err := d.store.TaskSetStatus(ctx, task.ID, "running"); err != nil {
		return fmt.Errorf("set task running: %w", err)
	}

	if !rt.NonInteractive || transport == config.TransportACP {
		go d.sendInitialPrompt(sessionName, "implement")
	}

	slog.Info("dispatched", "task", task.ID, "agent", agentID, "session", sessionName, "runtime", runtimeName)
	return nil
}

// getOrCompileGraph loads the persisted compiled workflow graph for a task.
// If no persisted graph exists, it compiles the template and persists the result.
// If compilation produces diagnostics, it records controller observations and returns an error.
func (d *Dispatcher) getOrCompileGraph(ctx context.Context, task *model.Task) (*workflow.Graph, error) {
	// Try to load persisted graph first.
	wf, err := d.store.CompiledWorkflowGetByTask(ctx, task.ID)
	if err == nil {
		g, parseErr := workflow.UnmarshalGraphString(wf.GraphJSON)
		if parseErr == nil {
			slog.Debug("loaded persisted compiled workflow graph", "task", task.ID, "source", wf.SourceName)
			return g, nil
		}
		slog.Warn("failed to parse persisted graph, recompiling", "task", task.ID, "err", parseErr)
	}

	// No persisted graph or parse failed — compile from template.
	tmpl, err := tmplpkg.Load(task.Template, d.repoPathFor(ctx, task), d.homeDir)
	if err != nil {
		return nil, fmt.Errorf("load template %q: %w", task.Template, err)
	}

	g, diags := workflow.Compile(tmpl)

	// If compilation produced diagnostics, record them and reject dispatch.
	if len(diags) > 0 {
		var diagMsgs []string
		for _, diag := range diags {
			diagMsgs = append(diagMsgs, fmt.Sprintf("%s: %s", diag.Kind, diag.Message))
		}
		reason := fmt.Sprintf("template %q compilation failed: %s", task.Template, strings.Join(diagMsgs, "; "))
		_ = d.store.ControlObservationPut(ctx, &model.ControlObservation{
			TargetType: "task",
			TargetID:   task.ID,
			TaskID:     task.ID,
			Kind:       "graph_compilation",
			Status:     "failed",
			Reason:     reason,
		})
		_ = d.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
			Controller:   "dispatch_controller",
			TaskID:       task.ID,
			StepName:     task.CurrentStep,
			TargetType:   "task",
			TargetID:     task.ID,
			DecisionKind: "graph_compilation_failure",
			Action:       "block_dispatch",
			Reason:       reason,
			Retryable:    false,
		})
		return nil, fmt.Errorf("%s", reason)
	}

	// Marshal and persist the graph.
	graphJSON, err := workflow.MarshalGraphString(g)
	if err != nil {
		return nil, fmt.Errorf("marshal graph: %w", err)
	}

	wfID := "wf-" + task.ID
	if err := d.store.CompiledWorkflowCreate(ctx, &model.CompiledWorkflow{
		ID:            wfID,
		TaskID:        task.ID,
		SourceType:    "template",
		SourceName:    task.Template,
		SourceRef:     "builtin",
		PolicyVersion: "1",
		GraphJSON:     graphJSON,
	}); err != nil {
		return nil, fmt.Errorf("persist compiled workflow graph: %w", err)
	}

	slog.Info("compiled and persisted workflow graph", "task", task.ID, "template", task.Template)
	return g, nil
}

// dispatchTemplate dispatches a task that has a workflow template.
func (d *Dispatcher) dispatchTemplate(ctx context.Context, task *model.Task) error {
	// Get the compiled graph — compile and persist on first dispatch, load from store on subsequent dispatches.
	graph, err := d.getOrCompileGraph(ctx, task)
	if err != nil {
		return err
	}

	stepName := task.CurrentStep
	if stepName == "" {
		stepName = graph.Entry
		if err := d.store.TaskSetStepFromPending(ctx, task.ID, stepName); err != nil {
			return fmt.Errorf("set entry step: %w", err)
		}
	}

	node, ok := graph.Nodes[stepName]
	if !ok {
		return fmt.Errorf("template %q: current step %q not found in compiled graph", task.Template, stepName)
	}

	worktreePath, err := d.resolveWorktree(ctx, task)
	if err != nil {
		return err
	}

	// Install pre-commit hook for lint/typecheck (idempotent, overwrites on each dispatch).
	d.installPreCommitHook(ctx, worktreePath, task.ID)

	switch node.Kind {
	case workflow.KindAgent:
		return d.dispatchAgent(ctx, task, stepName, node, worktreePath)
	case workflow.KindDeterministic:
		return d.dispatchDeterministic(ctx, task, stepName, node, worktreePath)
	default:
		return fmt.Errorf("unknown node kind %q in template %q step %q", node.Kind, task.Template, stepName)
	}
}

// dispatchAgent spawns a tmux agent for an agent step.
func (d *Dispatcher) dispatchAgent(ctx context.Context, task *model.Task, stepName string, node *workflow.Node, worktreePath string) error {
	runtimeName := node.Runtime
	if runtimeName == "" {
		runtimeName = task.Runtime
	}
	if runtimeName == "" {
		runtimeName = "default"
	}
	rt, ok := d.cfg.Runtimes[runtimeName]
	if !ok {
		return fmt.Errorf("unknown runtime %q for step %q", runtimeName, stepName)
	}

	// Escalate to a higher-capability runtime if the step has been retried enough times.
	if rt.EscalateAfter > 0 && task.StepAttempts[stepName] >= rt.EscalateAfter && rt.EscalateTo != "" {
		if escalated, ok := d.cfg.Runtimes[rt.EscalateTo]; ok {
			slog.Info("escalating runtime", "task", task.ID, "step", stepName,
				"from", runtimeName, "to", rt.EscalateTo, "retries", task.StepAttempts[stepName])
			runtimeName = rt.EscalateTo
			rt = escalated
		}
	}

	sessionName := "clankwork-worker-" + task.ID
	logDir := filepath.Join(d.homeDir, "logs")
	logfilePath := filepath.Join(logDir, sessionName+".log")
	transport := config.RuntimeTransport(rt)

	// Wait for any lingering session from a previous attempt to finish dying.
	// A bare Kill returns before cmd.Wait reaps the process; spawning a fresh
	// adapter while the old one still holds CLI auth state, file locks, or
	// sub-process IDs causes the new adapter to die within seconds.
	d.killExistingSession(sessionName)
	if err := d.configureTransport(sessionName, transport); err != nil {
		return fmt.Errorf("configure runtime transport: %w", err)
	}

	workdir := worktreePath
	if workdir == "" {
		workdir = d.homeDir
	}

	env := map[string]string{
		"CLANKWORK_TASK_ID":     task.ID,
		"CLANKWORK_ROLE":        node.Role,
		"CLANKWORK_REPO_ID":     task.RepoID,
		"CLANKWORK_HOME":        d.homeDir,
		"CLANKWORK_STEP":        stepName,
		"HOME":                  os.Getenv("HOME"),
		"PATH":                  agentPath(),
		"CLAUDE_CODE_SANDBOXED": "1", // bypass the Claude workspace trust dialog
	}
	builtEnv, err := runtimeenv.Build(d.homeDir, runtimeName, rt, env)
	if err != nil {
		if worktreePath != "" {
			d.worktree.Remove(worktreePath)
		}
		return fmt.Errorf("build runtime env: %w", err)
	}
	env = builtEnv
	addACPPermissionEnv(env, rt)
	if transport == config.TransportACP && stepName == "acceptance_spec" {
		env["CLANKWORK_ACP_PERMISSION_POLICY"] = "acceptance-spec"
	}

	// Write agent instructions into the worktree so the agent knows what to do.
	if worktreePath != "" && transport != config.TransportACP {
		d.writeAgentInstructions(worktreePath, task.ID, stepName)
	}

	args := rt.Args
	if rt.NonInteractive && transport != config.TransportACP {
		// Runtime accepts the initial prompt as a CLI arg (e.g. pi, claude --print).
		// Append bootstrap message so the agent knows what to do without needing CLAUDE.md.
		args = append(append([]string{}, rt.Args...), bootstrapMessage(stepName))
	}

	if err := d.spawner.Spawn(sessionName, workdir, rt.Command, args, env); err != nil {
		if worktreePath != "" {
			d.worktree.Remove(worktreePath)
		}
		return fmt.Errorf("spawn agent: %w", err)
	}

	agentID := ulid.Make().String()
	pid := d.runtimePID(sessionName)
	runtimeSessionID := d.runtimeSessionID(sessionName)
	if _, err := d.store.AgentCreateWithRuntime(ctx, agentID, task.ID, 0, sessionName, transport, runtimeSessionID, pid, logfilePath, worktreePath, runtimeName, rt.Model); err != nil {
		d.spawner.Kill(sessionName)
		if worktreePath != "" {
			d.worktree.Remove(worktreePath)
		}
		return fmt.Errorf("create agent: %w", err)
	}
	d.bindAgentEvents(sessionName, agentID, task.ID)

	if err := d.store.TaskSetStatus(ctx, task.ID, "running"); err != nil {
		return fmt.Errorf("set task running: %w", err)
	}

	if !rt.NonInteractive || transport == config.TransportACP {
		// Interactive REPL runtime (e.g. claude TUI): CLAUDE.md instructs the agent on startup.
		// For runtimes without CLAUDE.md support, send a nudge as fallback.
		go d.sendInitialPrompt(sessionName, stepName)
	}

	slog.Info("dispatched agent step", "task", task.ID, "step", stepName, "runtime", runtimeName, "session", sessionName)
	return nil
}

// resolveVerifyCommand returns the effective command and args for a deterministic step.
// When the step uses the sentinel "clankwork verify", the repo's VerifyCommand is used instead.
// "clankwork verify lint" and "clankwork verify typecheck" use the corresponding repo commands.
// Falls back to "go test ./..." if no verify command is configured.
func (d *Dispatcher) resolveVerifyCommand(ctx context.Context, task *model.Task, node *workflow.Node) (string, []string) {
	if node.Command == "clankwork" && len(node.Args) >= 1 && node.Args[0] == "verify" {
		// Determine which subcommand (if any) is being requested.
		subcommand := ""
		if len(node.Args) >= 2 {
			subcommand = node.Args[1]
		}

		if task.RepoID != "" {
			if repo, err := d.store.RepoGet(ctx, task.RepoID); err == nil {
				var command string
				switch subcommand {
				case "lint":
					command = repo.LintCommand
				case "typecheck":
					command = repo.TypecheckCommand
				default:
					command = repo.VerifyCommand
				}
				if command != "" {
					return "sh", []string{"-c", command}
				}
			}
		}

		// Fallback for "clankwork verify" (no subcommand): run test suite.
		if subcommand == "" {
			return "go", []string{"test", "./..."}
		}

		// Lint/typecheck are cheap gates when a repo declares them. Repos that
		// have not configured those commands skip the gate instead of making the
		// built-in workflow unusable.
		return "sh", []string{"-c", fmt.Sprintf("echo 'clankwork verify %s: command not configured in repo; skipping' && exit 0", subcommand)}
	}
	return node.Command, node.Args
}

// dispatchDeterministic creates an agent row, marks the task running, then runs the
// command in a goroutine, calling RouteStep when done.
func (d *Dispatcher) dispatchDeterministic(ctx context.Context, task *model.Task, stepName string, node *workflow.Node, worktreePath string) error {
	agentID := ulid.Make().String()
	logDir := filepath.Join(d.homeDir, "logs")
	// Step-specific log so concurrent agent pipe-pane output doesn't corrupt test output.
	logfilePath := filepath.Join(logDir, "clankwork-worker-"+task.ID+"-"+stepName+".log")

	agent, err := d.store.AgentCreate(ctx, agentID, task.ID, 0, "", logfilePath, worktreePath, "deterministic", "")
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	if err := d.store.TaskSetStatus(ctx, task.ID, "running"); err != nil {
		return fmt.Errorf("set task running: %w", err)
	}

	command, args := d.resolveVerifyCommand(ctx, task, node)
	slog.Info("dispatched deterministic step", "task", task.ID, "step", stepName, "command", command, "args", args)
	go d.runDeterministic(agent, task.ID, stepName, command, args, worktreePath)
	return nil
}

func (d *Dispatcher) runDeterministic(agent *model.Agent, taskID, stepName, command string, args []string, worktreePath string) {
	timeout := time.Duration(d.cfg.Scheduler.DeterministicTimeoutSec) * time.Second
	ctx, cancel := context.WithTimeout(d.ctx, timeout)
	defer cancel()

	logFile, err := os.OpenFile(agent.LogfilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		slog.Error("deterministic: open log", "task", taskID, "err", err)
	}

	workdir := worktreePath
	if workdir == "" {
		workdir = d.homeDir
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = workdir
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	outcome := "success"
	if err := cmd.Run(); err != nil {
		outcome = "failure"
		slog.Info("deterministic step failed", "task", taskID, "step", stepName, "err", err)
	}
	if logFile != nil {
		logFile.Close()
	}

	logSnippet := readLastN(agent.LogfilePath, 4096)

	if outcome == "failure" {
		payload, _ := json.Marshal(map[string]string{"step": stepName, "log": logSnippet})
		d.store.TraceAppend(context.Background(), taskID, agent.ID, "step.failure_context", string(payload))
	}

	payload, _ := json.Marshal(map[string]string{"step": stepName, "outcome": outcome, "log": logSnippet})
	d.store.TraceAppend(context.Background(), taskID, agent.ID, "step.deterministic_result", string(payload))

	if err := d.RouteStep(context.Background(), taskID, stepName, outcome); err != nil {
		slog.Error("deterministic: route step", "task", taskID, "step", stepName, "err", err)
	}

	d.store.AgentSetEnded(context.Background(), agent.ID)
}

// RouteStep advances a template task to its next step based on the outcome of the current step.
// Called by signal handlers (for agent steps) and runDeterministic (for deterministic steps).
func (d *Dispatcher) RouteStep(ctx context.Context, taskID, currentStep, outcome string) error {
	task, err := d.store.TaskGet(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task: %w", err)
	}
	if task.Template == "" {
		return nil
	}

	// Use the compiled graph if available; fall back to template load for backward compat.
	wf, graphErr := d.store.CompiledWorkflowGetByTask(ctx, taskID)
	var graph *workflow.Graph
	if graphErr == nil {
		graph, graphErr = workflow.UnmarshalGraphString(wf.GraphJSON)
	}
	if graphErr != nil {
		// Fall back to template-based routing for tasks without a persisted graph
		// (backward compatibility with tasks created before compiled workflow support).
		tmpl, err := tmplpkg.Load(task.Template, d.repoPathFor(ctx, task), d.homeDir)
		if err != nil {
			return fmt.Errorf("load template: %w", err)
		}
		return d.routeStepWithTemplate(ctx, task, tmpl, currentStep, outcome)
	}

	return d.routeStepWithGraph(ctx, task, graph, currentStep, outcome)
}

func (d *Dispatcher) routeStepWithGraph(ctx context.Context, task *model.Task, graph *workflow.Graph, currentStep, outcome string) error {
	edges, ok := graph.Edges[currentStep]
	if !ok {
		return fmt.Errorf("step %q not found in compiled graph %q", currentStep, task.Template)
	}

	var nextStep string
	if outcome == "success" {
		nextStep = edges.Success
		if nextStep == "" {
			nextStep = "complete"
		}
	} else {
		nextStep = edges.Failure
		if nextStep == "" {
			nextStep = "complete"
		}
		if nextStep != "complete" {
			count, sig := d.repeatedFailureSignature(ctx, task.ID, currentStep)
			if count >= model.DefaultOscillationThreshold && sig != nil {
				reason := fmt.Sprintf("repeated identical failure signature %s at step %s (%d occurrences)", sig.NormalizedHash, currentStep, count)
				_ = d.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
					Controller:   "task_controller",
					TaskID:       task.ID,
					StepName:     currentStep,
					TargetType:   "task",
					TargetID:     task.ID,
					DecisionKind: "route_oscillation",
					Action:       model.ControllerActionBlock,
					Reason:       reason,
					Retryable:    false,
					Payload:      model.MarshalPayload(sig),
				})
				_ = d.store.ControllerActuationAppend(ctx, &model.ControllerActuation{
					RequestedOperation: "task.block",
					ActorType:          "controller",
					ActorID:            "task_controller",
					TargetType:         "task",
					TargetID:           task.ID,
					TaskID:             task.ID,
					StepName:           currentStep,
					PreviousState:      task.Status,
					NewState:           "blocked",
					Outcome:            model.ActuationOutcomeSuccess,
					Reason:             reason,
				})
				d.store.TaskSetStatusIfRunning(ctx, task.ID, "blocked")
				return nil
			}
		}
		// Check max retries for the destination step (works for same-step loops AND cross-step loops).
		if nextStep != "complete" {
			nextNode, ok := graph.Nodes[nextStep]
			if ok && nextNode.MaxRetries > 0 {
				attempts := task.StepAttempts[nextStep]
				if attempts >= nextNode.MaxRetries {
					slog.Warn("max retries exceeded for destination step, failing task", "task", task.ID, "from", currentStep, "next", nextStep, "retries", attempts)
					d.store.TaskSetStatusIfRunning(ctx, task.ID, "failed")
					d.cleanupWorktree(ctx, task.ID)
					return nil
				}
			}
		}
	}

	if nextStep == "complete" {
		payload, _ := json.Marshal(map[string]string{"from": currentStep, "to": nextStep, "outcome": outcome})
		d.store.TraceAppend(ctx, task.ID, "", "step.routed", string(payload))
		d.store.TaskSetStatusIfRunning(ctx, task.ID, "done")
		d.cleanupWorktree(ctx, task.ID)
		slog.Info("task complete", "task", task.ID)
		if d.onTaskCompleted != nil {
			go d.onTaskCompleted(context.Background(), task.ID)
		}
		return nil
	}

	// Increment attempt count for the next step we're entering.
	newAttempts := make(map[string]int)
	for k, v := range task.StepAttempts {
		newAttempts[k] = v
	}
	newAttempts[nextStep]++

	advanced, err := d.store.TaskSetStepIfRunning(ctx, task.ID, currentStep, nextStep, newAttempts)
	if err != nil {
		return fmt.Errorf("set step: %w", err)
	}
	if !advanced {
		slog.Info("RouteStep: idempotent (already advanced)", "task", task.ID)
		return nil
	}

	payload, _ := json.Marshal(map[string]string{"from": currentStep, "to": nextStep, "outcome": outcome})
	d.store.TraceAppend(ctx, task.ID, "", "step.routed", string(payload))
	slog.Info("step routed", "task", task.ID, "from", currentStep, "to", nextStep, "outcome", outcome)
	return nil
}

// routeStepWithTemplate provides backward-compatible routing using the template
// for tasks that don't have a persisted compiled graph.
func (d *Dispatcher) routeStepWithTemplate(ctx context.Context, task *model.Task, tmpl *tmplpkg.Template, currentStep, outcome string) error {
	step, ok := tmpl.Steps[currentStep]
	if !ok {
		return fmt.Errorf("step %q not found in template %q", currentStep, task.Template)
	}

	var nextStep string
	if outcome == "success" {
		nextStep = step.OnSuccess
		if nextStep == "" {
			nextStep = "complete"
		}
	} else {
		nextStep = step.OnFailure
		if nextStep == "" {
			nextStep = "complete"
		}
		if nextStep != "complete" {
			count, sig := d.repeatedFailureSignature(ctx, task.ID, currentStep)
			if count >= model.DefaultOscillationThreshold && sig != nil {
				reason := fmt.Sprintf("repeated identical failure signature %s at step %s (%d occurrences)", sig.NormalizedHash, currentStep, count)
				_ = d.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
					Controller:   "task_controller",
					TaskID:       task.ID,
					StepName:     currentStep,
					TargetType:   "task",
					TargetID:     task.ID,
					DecisionKind: "route_oscillation",
					Action:       model.ControllerActionBlock,
					Reason:       reason,
					Retryable:    false,
					Payload:      model.MarshalPayload(sig),
				})
				_ = d.store.ControllerActuationAppend(ctx, &model.ControllerActuation{
					RequestedOperation: "task.block",
					ActorType:          "controller",
					ActorID:            "task_controller",
					TargetType:         "task",
					TargetID:           task.ID,
					TaskID:             task.ID,
					StepName:           currentStep,
					PreviousState:      task.Status,
					NewState:           "blocked",
					Outcome:            model.ActuationOutcomeSuccess,
					Reason:             reason,
				})
				d.store.TaskSetStatusIfRunning(ctx, task.ID, "blocked")
				return nil
			}
		}
		// Check max retries for the destination step (works for same-step loops AND cross-step loops).
		if nextStep != "complete" {
			nextStepDef, ok := tmpl.Steps[nextStep]
			if ok && nextStepDef.MaxRetries > 0 {
				attempts := task.StepAttempts[nextStep]
				if attempts >= nextStepDef.MaxRetries {
					slog.Warn("max retries exceeded for destination step, failing task", "task", task.ID, "from", currentStep, "next", nextStep, "retries", attempts)
					d.store.TaskSetStatusIfRunning(ctx, task.ID, "failed")
					d.cleanupWorktree(ctx, task.ID)
					return nil
				}
			}
		}
	}

	if nextStep == "complete" {
		payload, _ := json.Marshal(map[string]string{"from": currentStep, "to": nextStep, "outcome": outcome})
		d.store.TraceAppend(ctx, task.ID, "", "step.routed", string(payload))
		d.store.TaskSetStatusIfRunning(ctx, task.ID, "done")
		d.cleanupWorktree(ctx, task.ID)
		slog.Info("task complete", "task", task.ID)
		if d.onTaskCompleted != nil {
			go d.onTaskCompleted(context.Background(), task.ID)
		}
		return nil
	}

	// Increment attempt count for the next step we're entering.
	newAttempts := make(map[string]int)
	for k, v := range task.StepAttempts {
		newAttempts[k] = v
	}
	newAttempts[nextStep]++

	advanced, err := d.store.TaskSetStepIfRunning(ctx, task.ID, currentStep, nextStep, newAttempts)
	if err != nil {
		return fmt.Errorf("set step: %w", err)
	}
	if !advanced {
		slog.Info("RouteStep: idempotent (already advanced)", "task", task.ID)
		return nil
	}

	payload, _ := json.Marshal(map[string]string{"from": currentStep, "to": nextStep, "outcome": outcome})
	d.store.TraceAppend(ctx, task.ID, "", "step.routed", string(payload))
	slog.Info("step routed", "task", task.ID, "from", currentStep, "to", nextStep, "outcome", outcome)
	return nil
}

func (d *Dispatcher) repeatedFailureSignature(ctx context.Context, taskID, stepName string) (int, *model.FailureSignature) {
	traces, err := d.store.TraceListByType(ctx, taskID, "step.failure_context", model.DefaultOscillationThreshold)
	if err != nil || len(traces) < model.DefaultOscillationThreshold {
		return 0, nil
	}
	counts := make(map[string]int)
	signatures := make(map[string]*model.FailureSignature)
	for _, tr := range traces {
		var info model.StepFailureContextPayload
		if err := json.Unmarshal([]byte(tr.Payload), &info); err != nil {
			continue
		}
		if info.Step != "" && info.Step != stepName {
			continue
		}
		raw := info.Log
		if raw == "" {
			raw = info.Message
		}
		if isLifecycleCleanupFailure(raw) {
			continue
		}
		sig := model.NewFailureSignature("step_failure", stepName, "", "route_failure", raw, nil)
		if sig == nil {
			continue
		}
		counts[sig.NormalizedHash]++
		signatures[sig.NormalizedHash] = sig
	}
	for hash, count := range counts {
		if count >= model.DefaultOscillationThreshold {
			return count, signatures[hash]
		}
	}
	return 0, nil
}

func isLifecycleCleanupFailure(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "pid no longer an acp-adapter") ||
		strings.Contains(message, "agent terminal, runtime cleaned")
}

// resolveWorktree returns the worktree path for a task, reusing it if it already exists.
func (d *Dispatcher) resolveWorktree(ctx context.Context, task *model.Task) (string, error) {
	if task.RepoID == "" {
		return "", nil
	}
	expectedPath := filepath.Join(d.homeDir, "worktrees", task.ID)
	if _, err := os.Stat(expectedPath); err == nil {
		if err := d.ensureWorktreeFresh(ctx, task, expectedPath); err != nil {
			return "", err
		}
		return expectedPath, nil
	}
	repo, err := d.store.RepoGet(ctx, task.RepoID)
	if err != nil {
		return "", fmt.Errorf("get repo: %w", err)
	}
	targetBranch := repo.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}
	return d.worktree.Create(repo.Path, task.ID, targetBranch)
}

func (d *Dispatcher) ensureWorktreeFresh(ctx context.Context, task *model.Task, worktreePath string) error {
	repo, err := d.store.RepoGet(ctx, task.RepoID)
	if err != nil {
		return fmt.Errorf("get repo: %w", err)
	}
	targetBranch := repo.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}
	targetSHA := strings.TrimSpace(gitOutput(worktreePath, "rev-parse", targetBranch))
	headSHA := strings.TrimSpace(gitOutput(worktreePath, "rev-parse", "HEAD"))
	dirty := strings.TrimSpace(gitOutput(worktreePath, "status", "--porcelain", "--untracked-files=no")) != ""
	payload := model.MarshalPayload(map[string]string{
		"worktree_path": worktreePath,
		"target_branch": targetBranch,
		"target_sha":    targetSHA,
		"head_sha":      headSHA,
		"dirty":         fmt.Sprintf("%t", dirty),
	})

	if targetSHA == "" || headSHA == "" {
		_ = d.store.ControlObservationPut(ctx, &model.ControlObservation{
			TargetType:   "worktree",
			TargetID:     task.ID,
			TaskID:       task.ID,
			WorktreePath: worktreePath,
			Kind:         "worktree_state",
			Status:       "unknown",
			Reason:       "could not measure worktree target or head sha",
			Payload:      payload,
		})
		return fmt.Errorf("could not measure worktree state for task %s", task.ID)
	}

	if exec.Command("git", "-C", worktreePath, "merge-base", "--is-ancestor", targetBranch, "HEAD").Run() == nil {
		_ = d.store.ControlObservationPut(ctx, &model.ControlObservation{
			TargetType:   "worktree",
			TargetID:     task.ID,
			TaskID:       task.ID,
			WorktreePath: worktreePath,
			Kind:         "worktree_state",
			Status:       "fresh",
			Reason:       "worktree contains target branch",
			Payload:      payload,
		})
		return nil
	}

	if dirty {
		reason := "worktree is stale relative to target branch and has tracked changes"
		_ = d.store.ControlObservationPut(ctx, &model.ControlObservation{
			TargetType:   "worktree",
			TargetID:     task.ID,
			TaskID:       task.ID,
			WorktreePath: worktreePath,
			Kind:         "worktree_state",
			Status:       "stale_dirty",
			Reason:       reason,
			Payload:      payload,
		})
		_ = d.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
			Controller:   "worktree_controller",
			TaskID:       task.ID,
			StepName:     task.CurrentStep,
			TargetType:   "worktree",
			TargetID:     task.ID,
			DecisionKind: "unsafe_repair",
			Action:       "block_dispatch",
			Reason:       reason,
			Retryable:    false,
			Payload:      payload,
		})
		_ = d.store.TaskSetStatus(ctx, task.ID, "blocked")
		return fmt.Errorf("%s", reason)
	}

	_ = d.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "worktree_controller",
		TaskID:       task.ID,
		StepName:     task.CurrentStep,
		TargetType:   "worktree",
		TargetID:     task.ID,
		DecisionKind: "safe_repair",
		Action:       "rebase_worktree",
		Reason:       "worktree is stale relative to target branch and clean",
		Retryable:    true,
		Payload:      payload,
	})
	out, rebaseErr := exec.Command("git", "-C", worktreePath, "rebase", targetBranch).CombinedOutput()
	outcome := "success"
	errMsg := ""
	if rebaseErr != nil {
		outcome = "failed"
		errMsg = string(out)
	}
	_ = d.store.ControllerActuationAppend(ctx, &model.ControllerActuation{
		RequestedOperation: "worktree.rebase",
		ActorType:          "controller",
		ActorID:            "worktree_controller",
		TargetType:         "worktree",
		TargetID:           task.ID,
		TaskID:             task.ID,
		StepName:           task.CurrentStep,
		PreviousState:      headSHA,
		NewState:           strings.TrimSpace(gitOutput(worktreePath, "rev-parse", "HEAD")),
		Outcome:            outcome,
		Error:              errMsg,
		Reason:             "repair stale clean worktree before dispatch",
		Payload:            payload,
	})
	if rebaseErr != nil {
		_ = d.store.ControlObservationPut(ctx, &model.ControlObservation{
			TargetType:   "worktree",
			TargetID:     task.ID,
			TaskID:       task.ID,
			WorktreePath: worktreePath,
			Kind:         "worktree_state",
			Status:       "repair_failed",
			Reason:       strings.TrimSpace(string(out)),
			Payload:      payload,
		})
		_ = d.store.TaskSetStatus(ctx, task.ID, "blocked")
		return fmt.Errorf("rebase worktree: %w\n%s", rebaseErr, out)
	}
	_ = d.store.ControlObservationPut(ctx, &model.ControlObservation{
		TargetType:   "worktree",
		TargetID:     task.ID,
		TaskID:       task.ID,
		WorktreePath: worktreePath,
		Kind:         "worktree_state",
		Status:       "repaired",
		Reason:       "rebased stale clean worktree onto target branch",
		Payload:      payload,
	})
	return nil
}

func gitOutput(worktreePath string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", worktreePath}, args...)...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// installPreCommitHook writes a git pre-commit hook into the worktree that runs
// the repo's lint and typecheck commands. This acts as a gate preventing agents
// from committing syntactically or structurally invalid code.
func (d *Dispatcher) installPreCommitHook(ctx context.Context, worktreePath string, taskID string) {
	if worktreePath == "" {
		return
	}

	task, err := d.store.TaskGet(ctx, taskID)
	if err != nil || task.RepoID == "" {
		return
	}
	repo, err := d.store.RepoGet(ctx, task.RepoID)
	if err != nil {
		return
	}

	if repo.LintCommand == "" && repo.TypecheckCommand == "" {
		return
	}

	hookScript := BuildPreCommitHook(repo.LintCommand, repo.TypecheckCommand)

	// In a worktree, .git is a file pointing to the main repo's .git dir.
	// The hooks directory is at .git/hooks in the worktree itself (git creates it).
	hooksDir := filepath.Join(worktreePath, ".git", "hooks")
	// .git might be a file in worktrees; check if hooks dir exists under the gitdir.
	if fi, err := os.Stat(filepath.Join(worktreePath, ".git")); err == nil && !fi.IsDir() {
		// .git is a file — read the gitdir path from it.
		data, err := os.ReadFile(filepath.Join(worktreePath, ".git"))
		if err == nil {
			line := strings.TrimSpace(string(data))
			if strings.HasPrefix(line, "gitdir: ") {
				gitdir := strings.TrimPrefix(line, "gitdir: ")
				if !filepath.IsAbs(gitdir) {
					gitdir = filepath.Join(worktreePath, gitdir)
				}
				hooksDir = filepath.Join(gitdir, "hooks")
			}
		}
	}

	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		slog.Warn("could not create hooks dir", "path", hooksDir, "err", err)
		return
	}

	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		slog.Warn("could not write pre-commit hook", "path", hookPath, "err", err)
		return
	}
	slog.Debug("installed pre-commit hook", "path", hookPath)
}

// BuildPreCommitHook generates a shell script that runs lint and typecheck commands.
// Exported for testing.
func BuildPreCommitHook(lintCommand, typecheckCommand string) string {
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	sb.WriteString("# Pre-commit hook installed by Clankwork\n")
	sb.WriteString("# Runs lint and typecheck commands to prevent invalid commits.\n\n")

	if lintCommand != "" {
		sb.WriteString("echo 'pre-commit: running lint...'\n")
		sb.WriteString(fmt.Sprintf("if ! %s; then\n", lintCommand))
		sb.WriteString("  echo 'pre-commit: lint failed — fix errors before committing'\n")
		sb.WriteString("  exit 1\n")
		sb.WriteString("fi\n\n")
	}

	if typecheckCommand != "" {
		sb.WriteString("echo 'pre-commit: running typecheck...'\n")
		sb.WriteString(fmt.Sprintf("if ! %s; then\n", typecheckCommand))
		sb.WriteString("  echo 'pre-commit: typecheck failed — fix errors before committing'\n")
		sb.WriteString("  exit 1\n")
		sb.WriteString("fi\n\n")
	}

	sb.WriteString("exit 0\n")
	return sb.String()
}

func (d *Dispatcher) cleanupWorktree(ctx context.Context, taskID string) {
	worktreePath := filepath.Join(d.homeDir, "worktrees", taskID)
	if _, err := os.Stat(worktreePath); err == nil {
		go d.worktree.Remove(worktreePath)
	}
}

// KillSession kills a tmux session by name, ignoring "not found" errors.
func (d *Dispatcher) KillSession(sessionName string) {
	if err := d.spawner.Kill(sessionName); err != nil {
		slog.Debug("kill session", "session", sessionName, "err", err)
	}
}

// gracefulKillBudget is the maximum time we wait for a prior session to die
// before falling back to a hard kill. Set to 5s based on observed ACP adapter
// shutdown times (~1-3s typical, occasional 4s outliers).
const gracefulKillBudget = 5 * time.Second

// killExistingSession waits for any prior session at sessionName to fully release its
// resources before returning. Prevents the re-dispatch race where a bare Kill returns
// before cmd.Wait reaps the previous process — spawning a new adapter into the same
// session name while the old one still holds CLI auth state, file locks, or
// sub-process IDs causes the new adapter to die within seconds with
// "acp process exited" errors. If the wait exceeds the grace budget, log a warning;
// that's the signature of a stuck adapter worth surfacing.
func (d *Dispatcher) killExistingSession(sessionName string) {
	alive, _ := d.spawner.IsAlive(sessionName)
	if !alive {
		return
	}
	start := time.Now()
	if err := d.spawner.GracefulKill(sessionName, gracefulKillBudget); err != nil {
		slog.Warn("graceful kill returned error", "session", sessionName, "err", err)
	}
	if elapsed := time.Since(start); elapsed >= gracefulKillBudget {
		slog.Warn("graceful kill exceeded budget; prior agent likely stuck",
			"session", sessionName, "elapsed", elapsed)
	}
}

func (d *Dispatcher) SendToAgent(ctx context.Context, agentID, message string) error {
	agent, err := d.store.AgentGet(ctx, agentID)
	if err != nil {
		return err
	}
	sessionName := agentRuntimeSession(agent)
	if sessionName == "" {
		return fmt.Errorf("agent %s has no live session", agent.ID)
	}
	if message == "" {
		return fmt.Errorf("message is required")
	}
	if err := d.ensureRuntimeTransport(agent); err != nil {
		return err
	}
	return d.spawner.SendNudge(sessionName, message)
}

func (d *Dispatcher) CancelAgent(ctx context.Context, agentID string) error {
	agent, err := d.store.AgentGet(ctx, agentID)
	if err != nil {
		return err
	}
	task, _ := d.store.TaskGet(ctx, agent.TaskID)
	_ = d.store.ReconcilerDecisionAppend(ctx, &model.ReconcilerDecision{
		Controller:   "agent_controller",
		TaskID:       agent.TaskID,
		StepName:     currentStep(task),
		AgentID:      agent.ID,
		TargetType:   "agent",
		TargetID:     agent.ID,
		DecisionKind: "operator_cancel",
		Action:       "cancel_runtime_session",
		Reason:       "operator requested agent cancellation",
		Retryable:    true,
	})
	sessionName := agentRuntimeSession(agent)
	if sessionName == "" {
		return fmt.Errorf("agent %s has no live session", agent.ID)
	}
	if err := d.ensureRuntimeTransport(agent); err != nil {
		return err
	}
	outcome := "success"
	var cancelErr error
	if canceller, ok := d.spawner.(worker.AgentCanceller); ok {
		cancelErr = canceller.Cancel(sessionName)
	} else {
		cancelErr = d.spawner.GracefulKill(sessionName, 5*time.Second)
	}
	errMsg := ""
	if cancelErr != nil {
		outcome = "failed"
		errMsg = cancelErr.Error()
	} else {
		d.store.AgentSetStatus(ctx, agent.ID, "killed")
		if task != nil && task.Status == "running" {
			if task.Template != "" && task.CurrentStep != "" {
				fcPayload, _ := json.Marshal(map[string]string{"step": task.CurrentStep, "message": "agent cancelled by operator"})
				d.store.TraceAppend(ctx, task.ID, agent.ID, "step.failure_context", string(fcPayload))
				if err := d.RouteStep(ctx, task.ID, task.CurrentStep, "failure"); err != nil {
					d.store.TaskSetStatusIfRunning(ctx, task.ID, "blocked")
				}
			} else {
				d.store.TaskSetStatusIfRunning(ctx, task.ID, "blocked")
			}
		}
	}
	_ = d.store.ControllerActuationAppend(ctx, &model.ControllerActuation{
		RequestedOperation: "agents.cancel",
		ActorType:          "user",
		ActorID:            "operator",
		TargetType:         "agent",
		TargetID:           agent.ID,
		TaskID:             agent.TaskID,
		StepName:           currentStep(task),
		AgentID:            agent.ID,
		PreviousState:      agent.Status,
		NewState:           "killed",
		Outcome:            outcome,
		Error:              errMsg,
		Reason:             "operator requested agent cancellation",
	})
	_ = d.store.ControlObservationPut(ctx, &model.ControlObservation{
		TargetType: "agent",
		TargetID:   agent.ID,
		TaskID:     agent.TaskID,
		AgentID:    agent.ID,
		Kind:       "agent_health",
		Status:     "cancelled",
		Reason:     "agent cancelled by operator",
	})
	return cancelErr
}

func currentStep(task *model.Task) string {
	if task == nil {
		return ""
	}
	return task.CurrentStep
}

func (d *Dispatcher) PendingAgentPermissions(ctx context.Context, agentID string) ([]worker.ACPPermissionRequest, error) {
	agent, err := d.store.AgentGet(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if err := d.ensureRuntimeTransport(agent); err != nil {
		return nil, err
	}
	approver, ok := d.spawner.(worker.ACPPermissionApprover)
	if !ok {
		return nil, fmt.Errorf("runtime does not support ACP permission approvals")
	}
	return approver.PendingPermissions(agentRuntimeSession(agent)), nil
}

func (d *Dispatcher) ResolveAgentPermission(ctx context.Context, agentID, requestID, decision string) error {
	agent, err := d.store.AgentGet(ctx, agentID)
	if err != nil {
		return err
	}
	if err := d.ensureRuntimeTransport(agent); err != nil {
		return err
	}
	approver, ok := d.spawner.(worker.ACPPermissionApprover)
	if !ok {
		return fmt.Errorf("runtime does not support ACP permission approvals")
	}
	return approver.ResolvePermission(agentRuntimeSession(agent), requestID, decision)
}

func (d *Dispatcher) runtimePID(sessionName string) int {
	if reporter, ok := d.spawner.(worker.RuntimePIDReporter); ok {
		return reporter.PIDForSession(sessionName)
	}
	return 0
}

func addACPPermissionEnv(env map[string]string, rt config.RuntimeConfig) {
	if rt.ACPPermissionPolicy != "" {
		env["CLANKWORK_ACP_PERMISSION_POLICY"] = rt.ACPPermissionPolicy
	}
	if len(rt.ACPPermissionAllowPaths) > 0 {
		env["CLANKWORK_ACP_PERMISSION_ALLOW_PATHS"] = strings.Join(rt.ACPPermissionAllowPaths, string(os.PathListSeparator))
	}
	if len(rt.ACPPermissionDenyPaths) > 0 {
		env["CLANKWORK_ACP_PERMISSION_DENY_PATHS"] = strings.Join(rt.ACPPermissionDenyPaths, string(os.PathListSeparator))
	}
	if rt.ACPPermissionTimeoutSec > 0 {
		env["CLANKWORK_ACP_PERMISSION_TIMEOUT_SEC"] = fmt.Sprint(rt.ACPPermissionTimeoutSec)
	}
}

func (d *Dispatcher) runtimeSessionID(sessionName string) string {
	if reporter, ok := d.spawner.(worker.RuntimeSessionIDReporter); ok {
		return reporter.RuntimeSessionIDForSession(sessionName)
	}
	return sessionName
}

func agentRuntimeSession(agent *model.Agent) string {
	if agent.TmuxSession != "" {
		return agent.TmuxSession
	}
	return agent.RuntimeSessionID
}

func (d *Dispatcher) ensureRuntimeTransport(agent *model.Agent) error {
	transport := agent.Transport
	if transport == "" {
		return nil
	}
	if transport != config.TransportACP && transport != config.TransportTmux {
		return nil
	}
	selector, ok := d.spawner.(worker.TransportSelector)
	if !ok {
		return nil
	}
	return selector.UseTransport(agentRuntimeSession(agent), transport)
}

// writeAgentInstructions appends Clankwork agent context to CLAUDE.md in the worktree.
// If CLAUDE.md already exists (from the repo), the existing content is preserved.
func (d *Dispatcher) writeAgentInstructions(worktreePath, taskID, stepName string) {
	var gitWorkflow string
	if stepName == "acceptance_spec" {
		gitWorkflow = `## Git workflow

You are authoring the acceptance specification only. Do not edit source files, do not implement the task, and do not commit.
You are running inside the task worktree. Write the spec only at the relative path ` + "`artifacts/acceptance-spec.json`" + ` in this worktree.
Do not write to an absolute checkout path such as ` + "`/Users/...`" + ` or ` + "`/home/...`" + `; that is outside the task worktree and will be denied.
Complete with:

` + "```bash\nclankwork signal done --spec artifacts/acceptance-spec.json\n```" + `
`
	} else {
		gitWorkflow = `## Git workflow

You are working in a git worktree on branch **clankwork/$CLANKWORK_TASK_ID**.
Commit your changes before signaling done — the merge queue will rebase and merge your branch.

` + "```bash\ngit add -A && git commit -m \"<describe what you did>\"\nclankwork signal done\n```" + `

Do not push. Do not merge. Just commit and signal.
`
	}

	instructions := fmt.Sprintf(`
<!-- Clankwork agent instructions injected at dispatch -->
# Clankwork Agent Instructions

You are an autonomous agent dispatched to complete a task (step: **%s**).

## How to start

Run this command immediately to load your task context:

`+"```bash\nclankwork bootstrap\n```"+`

The bootstrap output contains your task title, body, role definition, failure context
from prior attempts, and relevant learnings. Read it carefully before starting work.

## Signaling

When done, signal the outcome — do not exit without signaling:

`+"```bash\nclankwork signal done                     # success\nclankwork signal failed \"reason\"          # unrecoverable failure\nclankwork signal blocked \"what you need\"  # need human input\n```"+`

Send heartbeats every few minutes while working:

`+"```bash\nclankwork signal progress \"brief status\"\n```"+`

%s

## Environment

Your task ID is in **$CLANKWORK_TASK_ID**. The daemon socket is at **$CLANKWORK_HOME/clankwork.sock**.
All clankwork CLI commands read these automatically — you do not need to pass them as flags.
`, stepName, gitWorkflow)

	// Inject lint/typecheck instructions if the repo has them configured.
	instructions += d.buildLintInstructions(taskID)

	path := filepath.Join(worktreePath, "CLAUDE.md")

	// Preserve existing repo CLAUDE.md; append our instructions below it.
	if existing, err := os.ReadFile(path); err == nil {
		instructions = string(existing) + "\n" + instructions
	}

	if err := os.WriteFile(path, []byte(instructions), 0600); err != nil {
		slog.Warn("could not write agent instructions", "path", path, "err", err)
	}

	// Write .claude/settings.json to pre-trust the worktree directory.
	// This bypasses the Claude Code workspace trust dialog so the bootstrap
	// send-keys message isn't consumed by a blocking prompt.
	claudeDir := filepath.Join(worktreePath, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err == nil {
		settingsPath := filepath.Join(claudeDir, "settings.json")
		settings := `{"skipDangerousModePermissionPrompt":true,"permissions":{"allow":["Bash(*)","Read(*)","Write(*)","Edit(*)","Glob(*)","Grep(*)","WebFetch(*)","WebSearch(*)"],"deny":[]}}` + "\n"
		if err := os.WriteFile(settingsPath, []byte(settings), 0600); err != nil {
			slog.Warn("could not write claude settings", "path", settingsPath, "err", err)
		}
	}
}

// buildLintInstructions returns CLAUDE.md instructions for lint/typecheck if configured.
func (d *Dispatcher) buildLintInstructions(taskID string) string {
	// Look up the task to find the repo.
	task, err := d.store.TaskGet(d.ctx, taskID)
	if err != nil || task.RepoID == "" {
		return ""
	}
	repo, err := d.store.RepoGet(d.ctx, task.RepoID)
	if err != nil {
		return ""
	}

	hasLint := repo.LintCommand != ""
	hasTypecheck := repo.TypecheckCommand != ""
	if !hasLint && !hasTypecheck {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## Continuous Verification\n\n")
	sb.WriteString("This repo has lint/typecheck commands configured. Run them **after every file change** to catch errors early.\n")
	sb.WriteString("A pre-commit hook will also enforce these before any commit.\n\n")

	if hasLint {
		sb.WriteString("**Lint** (run after every change):\n")
		sb.WriteString("```bash\nclankwork verify lint\n```\n\n")
	}
	if hasTypecheck {
		sb.WriteString("**Type check** (run after every change):\n")
		sb.WriteString("```bash\nclankwork verify typecheck\n```\n\n")
	}

	sb.WriteString("Fix any issues immediately — do not accumulate lint or type errors.\n")
	return sb.String()
}

// bootstrapMessage returns the initial prompt sent to an agent on startup.
func bootstrapMessage(stepName string) string {
	stepName = strings.TrimSpace(stepName)
	if stepName == "" {
		stepName = "implement"
	}
	if stepName == "acceptance_spec" {
		return "You have been dispatched as a Clankwork acceptance-spec author. " +
			"Run `clankwork bootstrap` to load context, then write only `artifacts/acceptance-spec.json` in the current task worktree. " +
			"Do not edit source files, documentation, tests, or any implementation files; this step defines acceptance only. " +
			"Complete with `clankwork signal done --spec artifacts/acceptance-spec.json` (or failed/blocked)."
	}
	return "You have been dispatched as a Clankwork agent for step `" + stepName + "`. " +
		"Run `clankwork bootstrap` to load your task context and instructions, " +
		"use paths relative to the current task worktree, complete the task, then signal done with `clankwork signal done` (or failed/blocked)."
}

func (d *Dispatcher) sendInitialPrompt(sessionName, stepName string) {
	if err := d.spawner.SendInitialPrompt(sessionName, bootstrapMessage(stepName)); err != nil {
		slog.Warn("send initial prompt", "session", sessionName, "err", err)
	}
}

func (d *Dispatcher) configureTransport(sessionName, transport string) error {
	switch transport {
	case "", config.TransportTmux:
		if selector, ok := d.spawner.(worker.TransportSelector); ok {
			return selector.UseTransport(sessionName, worker.TransportTmux)
		}
		return nil
	case config.TransportACP:
		selector, ok := d.spawner.(worker.TransportSelector)
		if !ok {
			return fmt.Errorf("runtime does not support transport %q", transport)
		}
		return selector.UseTransport(sessionName, worker.TransportACP)
	default:
		return fmt.Errorf("unknown runtime transport %q", transport)
	}
}

func (d *Dispatcher) bindAgentEvents(sessionName, agentID, taskID string) {
	if binder, ok := d.spawner.(worker.AgentEventBinder); ok {
		binder.BindAgentSession(sessionName, agentID, taskID)
	}
}

func (d *Dispatcher) repoPathFor(ctx context.Context, task *model.Task) string {
	if task.RepoID == "" {
		return ""
	}
	repo, err := d.store.RepoGet(ctx, task.RepoID)
	if err != nil {
		return ""
	}
	return repo.Path
}

func readLastN(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	offset := fi.Size() - n
	if offset < 0 {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return string(buf)
}

// agentPath returns PATH with the clankwork binary's directory prepended,
// plus mise shims so agents can use language toolchains installed via mise.
func agentPath() string {
	base := os.Getenv("PATH")

	var extra []string
	if exe, err := os.Executable(); err == nil {
		extra = append(extra, filepath.Dir(exe))
	}
	// Include mise shims so agents can invoke go, node, etc. regardless of how the
	// daemon was started (e.g. from a Bash tool with a limited PATH).
	if home := os.Getenv("HOME"); home != "" {
		miseShims := filepath.Join(home, ".local/share/mise/shims")
		if _, err := os.Stat(miseShims); err == nil {
			extra = append(extra, miseShims)
		}
	}
	if len(extra) == 0 {
		return base
	}
	if base != "" {
		return strings.Join(extra, string(filepath.ListSeparator)) + string(filepath.ListSeparator) + base
	}
	return strings.Join(extra, string(filepath.ListSeparator))
}

func (d *Dispatcher) Pause() {
	d.mu.Lock()
	d.userPaused = true
	d.mu.Unlock()
}

func (d *Dispatcher) Resume() {
	d.mu.Lock()
	d.userPaused = false
	d.mu.Unlock()
}

func (d *Dispatcher) IsPaused() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.userPaused || d.queuePressured
}

func (d *Dispatcher) SetQueuePressure(on bool) {
	d.mu.Lock()
	d.queuePressured = on
	if on {
		d.queuePressure = model.QueuePressureDecision{Level: model.QueuePressureHard, ShouldPause: true, MaxDispatch: 0, Reason: "legacy queue pressure"}
	} else if d.queuePressure.Level == model.QueuePressureHard && d.queuePressure.Reason == "legacy queue pressure" {
		d.queuePressure = model.QueuePressureDecision{}
	}
	d.mu.Unlock()
}

func (d *Dispatcher) SetQueuePressureDecision(decision model.QueuePressureDecision) {
	d.mu.Lock()
	d.queuePressure = decision
	d.queuePressured = decision.ShouldPause
	d.mu.Unlock()
}

func (d *Dispatcher) QueuePressureDecision() model.QueuePressureDecision {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.queuePressure.Level == "" {
		return model.QueuePressureDecision{Level: model.QueuePressureNone, MaxDispatch: d.cfg.Scheduler.MaxSlots, Reason: "merge queue within target"}
	}
	return d.queuePressure
}
