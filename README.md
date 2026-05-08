# Clankwork

**Automated software factory for evidence-gated AI delivery.** Clankwork dispatches AI agents to implement, verify, and merge code changes, while a deterministic control plane turns `plausible` output into auditable, reproducible outcomes.

> **Core principle:** Agents generate plausible solutions. The system's job is to progressively constrain the plausibility space until plausible = correct.

Clankwork is not a chatbot that says “done.” It treats agents as disposable workers and the control plane as the verification funnel: work only advances when executable checks, machine-readable evidence, and deterministic controller decisions prove it should.

---

## Contents

1. [Overview](#overview)
2. [Why this exists](#why-this-exists)
3. [Architecture](#architecture)
4. [Quick Start](#quick-start)
5. [CLI Reference](#cli-reference)
6. [Workflow Templates](#workflow-templates)
7. [Configuration](#configuration)
8. [Data Model](#data-model)
9. [Agent Lifecycle](#agent-lifecycle)
10. [Merge Queue](#merge-queue)
11. [Prior-Art Index](#prior-art-index)
12. [Signals and Evidence](#signals-and-evidence)
13. [Merge Queue Auditability](#merge-queue-auditability)
14. [Reference Docs](#reference-docs)

---

## Overview

Clankwork is a Go binary that runs as a long-lived daemon on a single machine (your workstation, a build box, a server). It:

- Maintains a **work DAG** — tasks with dependencies and priorities — stored in SQLite
- **Dispatches** ready tasks to agent workers, spawning each in its own `git worktree`
- Runs **workflow templates** — declarative TOML pipelines of agent and deterministic steps
- **Compiles** templates into persisted workflow graphs before dispatch and rejects non-conforming graphs
- **Routes** each step outcome (success/failure) through compiled graph edges
- **Merges** completed work through a queue that rebases onto the target branch, runs verification, and fast-forward merges
- **Indexes** task histories as planner-only prior art for sharper future plans and acceptance specs
- **Tracks** merge-controller decisions and actuations as durable, queryable audit events
- Communicates with agents via a **Unix socket HTTP API** and runtime transports (`tmux` or ACP)

The control plane has no LLM. Agents are tools (Claude Code, Pi, local models, etc.) invoked through runtime transports. Their role, prompt, and behavior are data (`roles/*.md`, templates, task context), not hard-coded policy.

Clankwork separates three concerns that are often collapsed in agent systems:

- **What to do:** human or planner-authored tasks, acceptance criteria, dependencies, and priorities.
- **How to do it:** role files and workflow templates that route implementation, critique, verification, retry, and merge steps.
- **What runs it:** interchangeable runtimes and models, from frontier planning agents to cheaper or local execution workers.

That split lets the control plane stay deterministic while still taking advantage of heterogeneous model capabilities.

---

## Why this exists

LLMs are fast at producing plausible edits and slow at guaranteeing correctness. Clankwork is designed to escape the "AI slop" cycle by making unsupported claims expensive and verified claims cheap to trust.

Clankwork constrains uncertainty in four layers:

1. **Workflow graph policy:** dependencies, priorities, retries, compiled graph edges, and graph-conformance checks shape how work advances.
2. **Deterministic checks:** lint/type/test gates run on every implementation pass.
3. **Acceptance evidence:** completion requires executable probes, required artifacts, and verifier evidence.
4. **Deterministic controller logic:** stalled agents are nudged, restarted, or escalated based on observed state.

The hard promise is that a task can only advance when each stage can be replayed, verified, and explained.

Three goals define the project:

- **Replace “agent says done” with “system can prove done.”** Workers propose completion; control-plane gates must accept it.
- **Constrain uncertainty early and repeatedly.** Cheap checks (routing, lint, type/test gates) run first, then expensive checks (acceptance verification, conflict resolution) only on survivors.
- **Make integration deterministic by default.** Merge only occurs after structured evidence and deterministic conflict handling, then every decision is persisted for audit.

In practice, this gives a MapReduce-style execution model for software engineering:

- **Map:** centralized plan decomposition into tasks, then many workers execute slices in parallel (potentially with lower-cost/faster models).
- **Reduce:** deterministic gating, reruns, and merge arbitration collapse those slices back into one confident production decision.

In this model, Clankwork's value is not to make each agent smarter; it is to make unsafe assumptions expensive by forcing every claim through structured verification and auditable controller behavior.

---

## Architecture

```text
CLI / planner / API
        |
        v
Control plane daemon (deterministic, no LLM)
        |
        v
tmux, ACP, or deterministic runtime steps
        |
        v
isolated git worktrees, acceptance artifacts, and merge queue
```

See [docs/architecture.md](docs/architecture.md) for the full implemented
architecture reference and [docs/implementation-status.md](docs/implementation-status.md)
for the explicit implemented/partial/future status of reviewed features.

### Components

| Component | Responsibility |
|-----------|---------------|
| **Control Plane Daemon** | Schedules, dispatches, merges, reconciles. No LLM. |
| **CLI** | Human-facing commands + agent-facing signal/bootstrap commands |
| **HTTP API** | Unix socket API for agent communication |
| **Scheduler** | Topo-sorts ready tasks, fills agent slots, dispatches |
| **Reconciler** | Detects stuck agents, heartbeat timeouts; kills and requeues |
| **Merge Queue Processor** | Rebases, verifies, merges completed worktrees |
| **SQLite Store** | Plans, tasks, agents, merge queue, traces, prior-art histories |
| **Control-State Store** | Decision + actuation summaries for PID-like recovery logic |
| **Workflow Templates** | Declarative TOML pipelines (agent + deterministic steps) |
| **Worker Runtime** | Agent transport: spawns tmux or ACP sessions, logs output/events, supports human attach/watch |

### Control model

The reconciler uses a stateful, deterministic control loop rather than binary success/fail heuristics:

- **Observed state** (`session alive`, `heartbeat`, `worktree changes`, `ACP events`, command outcomes),
- **Error vector** (`liveness`, `progress`, `verification`, `coordination`, `stability`),
- **Escalation policy** from cheap interventions (nudge) to heavier recovery (restart/escalate/requeue).

This design is intentionally conservative: it still prefers reproducible controls over model intelligence, while making stalls and flapping work recoverable instead of silent.

---

## Quick Start

### Build

```sh
make build
```

### Start the daemon

```sh
./bin/clankwork daemon start          # foreground
./bin/clankwork daemon start --background  # background mode (-b)
./bin/clankwork daemon stop
```

The daemon creates `$CLANKWORK_HOME` (default: `~/.clankwork`) on first run:
- `clankwork.db` — SQLite database
- `worktrees/` — isolated git worktrees per task
- `logs/` — runtime logs per agent
- `config.toml` — scheduler and runtime configuration
- `clankwork.sock` — Unix socket for the HTTP API

### Register a repository

```sh
./bin/clankwork repo add /path/to/myrepo --name myrepo --branch main
```

### Create and dispatch a task

```sh
echo "Add JWT-based authentication to the API" > body.md
./bin/clankwork task create \
  --title "Implement user auth" \
  --repo <repo-id> \
  --template feature \
  --body body.md
```

The scheduler picks it up, allocates a slot, creates a worktree, and spawns an agent.

### Monitor

```sh
./bin/clankwork status              # system overview
./bin/clankwork agents list         # running agents
./bin/clankwork task list           # task list
./bin/clankwork queue list          # merge queue
```

### Attach to an agent

```sh
./bin/clankwork attach <task-id-or-agent-id>  # task/agent attach shortcut
```

---

## CLI Reference

### Daemon

```sh
clankwork daemon start                   # start the control plane daemon
clankwork daemon start --background      # background mode (or -b)
clankwork daemon stop                    # stop the daemon
```

### Observation

```sh
clankwork status                      # system overview: tasks, agents, queue pressure, blocked work
clankwork task list                   # list all tasks with status and priority
clankwork task list --plan <id>       # filter by plan
clankwork task list --status pending,running # comma-separated statuses
clankwork task show <id>              # show task details + traces
clankwork agents list                 # running agents, health, current task
clankwork attach <task-id-or-agent-id> # attach task agent (tmux or ACP transcript)
clankwork agents attach <task-id-or-agent-id> # legacy/explicit attach
clankwork agents events <task-id-or-agent-id> # print persisted ACP/runtime events
clankwork agents watch <task-id-or-agent-id> # follow persisted ACP/runtime events
clankwork agents send <task-id-or-agent-id> "..." # send a follow-up message to running agent
clankwork agents cancel <task-id-or-agent-id> # cancel current turn or stop runtime
clankwork agents permissions <task-id-or-agent-id> # list pending ACP permission requests
clankwork agents approve <task-id-or-agent-id> <request-id>
clankwork agents approve-session <task-id-or-agent-id> <request-id> # approve session-level request
clankwork agents deny <task-id-or-agent-id> <request-id>
clankwork events <task-id-or-item-id>  # control-plane audit events (traces + decisions + actuations)
clankwork logs <task-id>                  # print task agent log file (tail with --follow)
clankwork verify                         # run repo verification (or verify test/lint/typecheck)
clankwork plan list                   # list plans
clankwork plan show <id>              # show plan details
clankwork queue list                  # merge queue state
clankwork queue skip <item-id>        # reject a queued item
clankwork queue retry <item-id>       # re-queue a failed merge item
clankwork dispatch status             # show whether dispatch is paused
```

### Control

```sh
clankwork dispatch pause     # pause dispatch (finish in-flight, stop starting new)
clankwork dispatch resume    # resume dispatch
clankwork reconcile task <id>   # run one task diagnosis immediately
clankwork reconcile all          # reconcile all eligible control-plane work
clankwork refresh task <id>     # refresh one task's observed state
clankwork refresh agent <id>    # refresh one agent's observed state
clankwork refresh worktree <id> # refresh one task worktree state
clankwork escalation list                 # list escalations
clankwork escalation resolve <id> --outcome <outcome>
```

### Repositories

```sh
clankwork repo add <path> [--name <name>] [--branch main] \
  [--verify-command "go test ./..."] [--lint-command "golangci-lint run ./..."] [--typecheck-command "go build ./..."] [--auto-push]
clankwork repo list
clankwork repo prune [repo-id] [--dry-run]
```

### Plans

```sh
clankwork plan create <file.md> [--title "Override title"]
clankwork plan list
clankwork plan show <id>
```

### Tasks

```sh
clankwork task create \
  --title "Task title" \
  --body <path-to-body.md> \
  [--plan <id>] \
  [--repo <id>] \
  [--template feature|bugfix|refactor|simple|critique|<custom>] \
  [--role <role>] \
  [--runtime <runtime>] \
  [--priority N]
clankwork task add-dep <task-id> <depends-on-id>
clankwork task set-priority <task-id> N
clankwork task retry <task-id>                           # force status back to pending and re-enqueue
clankwork task diagnose <task-id>                        # explain current control/state blockers
clankwork task retry-step <task-id> [<step>]             # retry one step immediately
clankwork task reset-step <task-id> <step>               # reset task to an earlier step
clankwork task escalate <task-id> --target-type <type> --reason <reason> [--target-ref ...] [--requested-action ...] [--step ...]
```

### Templates

```sh
clankwork template list [--repo-path /path/to/repo]  # list available templates
```

### Configuration

```sh
clankwork config                         # print effective scheduler/runtime configuration
clankwork config get scheduler.max_slots  # read one key
clankwork config set <key> <value>       # exists, but currently not yet implemented
```

### Signals (for worker agents)

```sh
clankwork bootstrap              # load agent context (task context if CLANKWORK_TASK_ID set)
clankwork signal started         # mark task running
clankwork signal progress <msg>  # heartbeat with status update
clankwork signal done [<msg>]    # simple task complete
clankwork signal done --spec artifacts/acceptance-spec.json
clankwork signal done --bundle artifacts/done-bundle.json
clankwork signal done --report artifacts/verification-report.json
clankwork signal failed [<msg>]  # task failed
clankwork signal blocked [<msg>] # request human input
clankwork context <task-id>      # get task + plan context
```

### Acceptance Artifacts

```sh
clankwork acceptance validate-spec artifacts/acceptance-spec.json
clankwork acceptance validate-report [--spec artifacts/acceptance-spec.json] artifacts/verification-report.json
clankwork acceptance validate-plan --spec artifacts/acceptance-spec.json artifacts/verification-plan.json
clankwork acceptance run-plan --spec artifacts/acceptance-spec.json artifacts/verification-plan.json --out artifacts/verification-report.json
clankwork acceptance show <task-id>                      # inspect artifact set + computed confidence
clankwork acceptance smoke --repo <repo-id> --runtime default --case all --wait
clankwork artifact add --type cli_transcript --path artifacts/run.txt --producer acceptance-verifier
```

### Prior Art (for planners)

```sh
clankwork prior-art search "auth middleware change"
clankwork prior-art search "database migration rollback" --repo <repo-id>
clankwork prior-art show <task-id>
clankwork prior-art rebuild
clankwork plan create plan.md --with-prior-art
```

### Trace and Evidence Inspection

```sh
clankwork traces                                      # query traces by task, type, outcome, path, duration
clankwork artifact add --type <type> --path <file> --producer <name> --step <step>
clankwork events <task-id-or-item-id>                 # unified audit event timeline
```

---

## Workflow Templates

Templates are declarative TOML files defining a mini-DAG of steps with edges for success/failure. Before dispatch, Clankwork compiles the selected template into a persisted workflow graph stored with the task. The compiled graph, not the raw template file, is the scheduler's routing authority for step transitions. Five templates are built in:

### Built-in Templates

| Template | Flow |
|----------|------|
| `feature` | `acceptance_spec` → `implement (agent, 5 retries)` → `lint` → `typecheck` → `test` → `acceptance (agent, inherits task runtime)` → `complete` |
| `bugfix` | `implement (agent, 3 retries)` → `lint` → `typecheck` → `test` → `complete` |
| `refactor` | `implement (agent, 3 retries)` → `lint` → `typecheck` → `test` → `complete` |
| `simple` | `implement (agent, 3 retries)` → `complete` |
| `critique` | `implement (agent, 5 retries)` → `lint` → `critic` → `verify` → `complete` |

### Template Format

```toml
# templates/my-workflow.toml

name        = "my-workflow"
description = "Custom workflow description"
entry       = "step1"
auto_merge  = true   # enqueue in merge queue when task reaches "complete"

[steps.step1]
type        = "agent"
role        = "implementer"
runtime     = "default"
max_retries = 5
on_success  = "step2"
on_failure  = "step1"   # retry same step

[steps.step2]
type     = "deterministic"
command  = "go"
args     = ["test", "./..."]
on_success = "complete"
on_failure = "step1"
```

**Step types:**
- `agent` — spawns an LLM agent through the configured runtime transport (ACP by default; tmux remains supported)
- `deterministic` — runs a shell command directly (no LLM cost)

**Step fields:**
| Field | Required | Description |
|-------|----------|-------------|
| `type` | Yes | `agent` or `deterministic` |
| `role` | For agent | Role definition name (looks for `roles/<role>.md` in the repo) |
| `runtime` | No | Runtime name from `runtime` config (empty means inherit task runtime, then fall back to `default`) |
| `command` | For deterministic | Binary to run (not a shell string) |
| `args` | No | Arguments for `command` |
| `max_retries` | No | Fail task after N retries of the same step (0 = unlimited) |
| `on_success` | No | Next step name on success (default: `complete`) |
| `on_failure` | No | Next step name on failure (default: `complete`) |

### Graph Compilation and Conformance

Template loading validates TOML shape and local edge references. Graph compilation then adds workflow policy checks. For example, a graph with an `acceptance_spec` gate is treated as substantive work and must also include implementation, deterministic verification, and acceptance verification gates in the right success-edge order. If compilation emits diagnostics, dispatch is blocked; Clankwork records a `graph_compilation` observation and a `graph_compilation_failure` controller decision.

When a graph compiles successfully, Clankwork stores it in `compiled_workflows` and uses that row for routing. `signal.done` / `signal.failed` outcomes resolve through the compiled graph's success/failure edges. Trace conformance validation can later compare the execution trace against that same graph and flag impossible transitions, missing targets, or terminal completion without a success route to `complete`.

### Template Search Order

1. `$repoPath/templates/<name>.toml` — project-specific templates
2. `$CLANKWORK_HOME/templates/<name>.toml` — user-level templates
3. Embedded built-ins — `feature.toml`, `bugfix.toml`, `refactor.toml`, `simple.toml`, `critique.toml`

### Role Definitions

Agent roles are markdown files in the repository's `roles/` directory:

```sh
roles/
  planner.md
  triager.md
  implementer.md
  bugfixer.md
  refactorer.md
  acceptance-author.md
  critic.md
  acceptance.md
  conflict-resolver.md
  learning-extractor.md
```

A role definition describes what the agent does, how it should approach its work, quality standards, and signal conventions. The template step's `role` field maps to `roles/<name>.md`. This separates **what to do** (role definition) from **how to do it** (template) from **what runs it** (runtime config).

---

## Signals and Evidence

Clankwork enforces an evidence-based completion contract. A task cannot complete the
control-plane acceptance path unless it produces three structured artifacts:

- **Acceptance spec** (`clankwork signal done --spec`) — executable criteria + probe definitions + required evidence per probe + fail conditions.
- **Done bundle** (`clankwork signal done --bundle`) — files changed, tests run, and claims linked to criteria.
- **Verification report** (`clankwork signal done --report`) — probe-mapped evidence plus verdict.

The control plane validates artifact presence, schema, provenance, probe mapping, and
artifact hashes before transitioning steps. It also computes deterministic confidence from
evidence coverage and retry history, so `confidence` is not left to model self-reporting.

For reproducible checks, evidence is typically registered with `clankwork artifact add`
and referenced by stable `artifact_id`s in reports.

Negative controls are first-class: structurally valid failures route the task back to
implementation with concrete, probe-scoped failure context.

The canonical acceptance reference is [docs/acceptance-verification.md](docs/acceptance-verification.md).
It summarizes the full hardened acceptance design from
[docs/hardened-acceptance-verification.md](docs/hardened-acceptance-verification.md)
and is the starting point for artifact schemas, computed confidence, registry
requirements, invalidation, negative controls, and smoke coverage.

## Configuration

Configuration lives at `$CLANKWORK_HOME/config.toml`. Missing file uses all defaults.

```toml
[scheduler]
max_slots                = 4       # max concurrent agent slots
heartbeat_timeout_secs   = 600     # kill agent after 10 min without heartbeat
tick_secs                = 2        # scheduler loop interval
deterministic_timeout_sec = 1800    # timeout for deterministic step commands
merge_queue_max_depth    = 10       # backpressure: pause dispatch above this depth
merge_queue_tick_secs    = 5        # merge queue processor interval
merge_queue_max_attempts = 3        # max merge/rebase attempts before rejection
verify_timeout_secs      = 600     # timeout for repo verify command

[runtimes.default]
command = "acp-adapter"
args    = ["--adapter", "claude"]
transport = "acp"
model   = "claude"
acp_permission_policy = "worktree"

[runtimes.claude]
command = "claude"
args    = ["--model", "opus-4"]
transport = "tmux"

[runtimes.local]
command = "pi"
args    = ["--model", "qwen3"]
transport = "tmux"

[runtimes.claude-acp]
command = "acp-adapter"
args    = ["--adapter", "claude"]
transport = "acp"
model   = "claude"

[runtimes.pi-acp]
command = "acp-adapter"
args    = ["--adapter", "pi"]
transport = "acp"
model   = "pi"
```

Runtime escalation is configured in the daemon's `config.toml`. Project-local
runtime policy in `clankwork.toml` is not implemented yet.

For every supported key, defaults, and limitations, see
[docs/config-reference.md](docs/config-reference.md).

---

## Data Model

### Plans

Metadata containers for related tasks. Markdown body stored at `$CLANKWORK_HOME/plans/<id>.md`.

```go
type Plan struct {
    ID        string    // ULID
    Title     string
    Status    string    // "active", "done"
    Path      string    // path to .md file
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

### Repos

Git repositories managed by the control plane.

```go
type Repo struct {
    ID            string
    Name          string    // unique name
    Path          string    // absolute path to the git repo
    TargetBranch  string    // default "main"
    VerifyCommand string    // optional: run after rebase, before merge
    LintCommand   string    // optional: run before merge
    TypecheckCommand string // optional: run before merge
    AutoPush      bool
}
```

### Tasks

Units of work dispatched to agents.

```go
type Task struct {
    ID             string
    Name           string
    PlanID         string    // optional
    RepoID         string    // optional
    Title          string
    Body           string    // task description / spec
    Template       string    // workflow template name (e.g. "feature")
    Role           string    // default role
    Runtime        string    // default runtime (default: "default")
    Priority       int       // higher = dispatched first
    Status         string    // pending, running, done, failed, blocked, merged
    RetryCount     int       // workflow-level retry count
    CurrentStep    string    // for template tasks: current step name
    StepAttempts   map[string]int // retries keyed by step name
}
```

### Task Dependencies

The work DAG is stored in `task_deps`. The scheduler dispatches tasks whose all dependencies are satisfied and whose priority is highest.

### Agents

Runtime instances. One agent row per slot used (tmux/ACP session or goroutine for deterministic steps).

```go
type Agent struct {
    ID            string
    TaskID        string
    Slot          int
    Status        string    // running, done, killed
    TmuxSession   string    // compatibility/control session name (empty for deterministic)
    Transport     string    // tmux, acp, deterministic
    RuntimeSessionID string // adapter-native session id when available
    Model         string    // runtime model identifier, when available
    PID           int       // runtime process id when available
    LogfilePath   string    // path to session log
    WorktreePath  string
    Runtime       string
    StartedAt     time.Time
    LastHeartbeat *time.Time
    LastEventAt   *time.Time
    LastStopReason string    // opportunistic; ACP adapters may not always emit it
}
```

### Merge Queue Items

Completed tasks awaiting integration.

```go
type MergeQueueItem struct {
    ID             string
    TaskID         string
    RepoID         string
    Branch         string    // clankwork/<task-id>
    Target         string    // e.g. "main"
    Status         string    // queued, rebasing, verifying, merging, merged, conflicted, rejected, failed
    AttemptCount   int
    Priority       int
    ConflictTaskID string    // set when rebase produces conflicts
    MergeSHA       string    // set on successful merge
}
```

### Traces

Append-only event log for audit and prior-art indexing. Every signal, step transition, and workflow outcome is recorded.

```go
type Trace struct {
    ID        int64     // trace row ID
    TaskID    string
    AgentID   string
    EventType string    // e.g. "signal.done", "step.routed", "merge.merged"
    StepName  string
    RetryNum  int
    Runtime   string
    Model     string
    Payload   string    // JSON
    CreatedAt time.Time
}
```

### Acceptance Spec

Structured contract generated in the acceptance-spec step.

```go
type AcceptanceSpec struct {
    TaskID    string
    StepID    string
    Path      string
    SHA256    string
    RiskLevel string
    Criteria  []AcceptanceCriterion
}

type AcceptanceCriterion struct {
    ID                        string
    Description               string
    Probes                    []AcceptanceProbe
    RequiredArtifacts         []string
    FailIf                    []string
    RiskLevel                 string
    RequiresStateTransition   bool
    RequiresNegativeAssertion bool
}
```

### Done Bundle

Implementation completion payload from the worker.

```go
type DoneBundle struct {
    TaskID       string
    Summary      string
    FilesChanged []string
    TestsRun     []string
    Claims       []CompletionClaim
    Artifacts    []CompletionArtifact
    KnownRisks   []string
}
```

### Verification Report

Verifier output and verdict with deterministic confidence.

```go
type VerificationReport struct {
    TaskID             string
    StepID             string
    Path               string
    SHA256             string
    Results            []VerificationResult
    Failures           []VerificationFailure
    Confidence         string  // worker-provided label
    ComputedConfidence float64 // control-plane computed score
    ConfidenceLabel    string  // low/medium/high
    AdversarialReview  *AdversarialReview
}
```

### Artifacts

Evidence registry rows with integrity metadata.

```go
type Artifact struct {
    ID               string
    TaskID           string
    StepID           string
    Producer         string
    ProducerType     string
    Path             string
    ArtifactType     string
    CreatedAt        string
    SHA256           string
    Command          string
    WorkingDirectory string
    ExitCode         int
    Status           string
    InvalidatedAt    string
}
```

### Prior-Art Task Histories

Indexed from completed task, trace, acceptance, verification, artifact, and merge records.

```go
type PriorArtHistory struct {
    TaskID      string
    RepoID      string
    Title       string
    Status      string
    Summary     string
    ReworkScore float64
    RiskScore   float64
    Tags        []string
}
```

---

## Agent Lifecycle

### Spawning

1. Control plane selects a ready task, allocates a slot
2. Creates a `git worktree` on a new branch (`clankwork/<task-id>`) from the target branch
3. Writes `CLAUDE.md` into the worktree with agent instructions
4. Launches a runtime session with `CLANKWORK_*` environment variables
5. Delivers the bootstrap prompt through tmux input, a CLI prompt arg, or ACP `session/prompt`
6. Records an `Agent` row and sets task status to `running`

### Bootstrapping

The agent's first action is `clankwork bootstrap`. This reads `CLANKWORK_TASK_ID`/`CLANKWORK_ROLE`/`CLANKWORK_REPO_ID` from the environment and calls the daemon API, which returns:

- Task title, body, and acceptance criteria
- Role definition (markdown from `roles/<name>.md` in the repo)
- Failure context from prior attempts (last 3 failure traces, truncated to 4KB)
- CLI reference card

### Signaling

Agents send heartbeats via `clankwork signal progress` and terminal signals (`done`, `failed`, `blocked`). The control plane validates signal payloads, then routes outcomes through the task's persisted compiled graph:

- **Template task:** `signal.done` → validate payload → `RouteStep(task, currentStep, "success")` → compiled graph edge → next step or marks `done` → enqueue merge
- **Template task:** `signal.failed` → `RouteStep(task, currentStep, "failure")` → compiled graph edge → retry step, escalate runtime, or mark `failed`
- **Simple task:** `signal.done` → mark `done`

### Reconciler

A periodic loop detects:
- **Dead tmux sessions** — session went away but task still `running` → kill and fail
- **Heartbeat timeout** — no progress signal for `heartbeat_timeout_secs` → kill and fail

Failed tasks are requeued (for template retry logic) or marked `failed`.

### Session Transport: tmux

Each agent runs inside a detached tmux session. Rationale:
- Universal: works with any CLI runtime, no custom harness required
- Human attach/detach: `tmux attach` is always available as an escape hatch
- Log capture: `tmux pipe-pane` writes a per-session logfile recorded on the trace row
- Ad-hoc human steering: `tmux send-keys` for emergency intervention

### Session Transport: ACP

ACP runtimes use `acp-adapter` over stdio. Clankwork initializes the adapter, creates an ACP session, sends the bootstrap prompt via `session/prompt`, and persists all early handshake/session/update events to `agent_events`.

`acp-adapter` is an external Go adapter (`github.com/beyond5959/acp-adapter`), not a binary built by Clankwork itself. Install the pinned adapter into `$CLANKWORK_HOME/bin` before using ACP runtimes:

```sh
make install-acp-adapter
```

Clankwork prepends `$CLANKWORK_HOME/bin` to ACP runtime PATHs, so the default `command = "acp-adapter"` works without manually creating `/tmp/acp-adapter-bin/acp-adapter`. You can still set an absolute `[runtimes.<name>].command` if you manage the adapter elsewhere.

Useful commands:

```sh
clankwork acp doctor --handshake
clankwork acp smoke --runtime claude-acp
clankwork acceptance smoke --repo <repo-id> --runtime default --case all --wait
clankwork agents attach <task-id>
clankwork agents watch <task-id>
clankwork agents send <task-id> "status?"
clankwork agents cancel <task-id>
clankwork agents permissions <task-id>
clankwork agents approve <task-id> <request-id>
clankwork agents deny <task-id> <request-id>
```

ACP attach prints a compact transcript from persisted events. For running agents it follows new events; for completed agents it prints the transcript and exits. `last_stop_reason` is best-effort because some adapters stream updates and may not emit the final prompt result before the agent signals completion. Reconciliation therefore relies on persisted events, `last_event_at`, process liveness, heartbeat/progress signals, and timeout+nudge handling rather than stop reason alone.

ACP permission policy is configured per runtime:

```toml
[runtimes.pi-acp]
command = "acp-adapter"
args = ["--adapter", "pi"]
transport = "acp"
acp_permission_policy = "worktree" # worktree, trusted, manual, deny
acp_permission_allow_paths = []
acp_permission_deny_paths = ["~/.ssh", "~/.aws"]
acp_permission_timeout_sec = 300
```

`worktree` is the default: Clankwork allows `clankwork ...` control commands for the session, allows commands whose explicit paths stay inside the agent worktree or configured allow paths, and denies sensitive/system paths. `manual` applies the same automatic policy, then leaves misses pending for `agents approve` / `agents deny`. Each request and decision is persisted in `agent_events` as `acp.permission.request` and `acp.permission.decision`. `trusted` accepts adapter permission requests automatically; for Pi this is separate from the adapter escape hatch `--pi-disable-gate`, which bypasses adapter permission requests entirely.

---

## Merge Queue

After a task signals `done` and reaches the template's `complete` step, the control plane enqueues it (if `auto_merge = true`). The merge queue processor:

1. **Select** — picks the highest-priority queued item for a repo (one at a time per repo)
2. **Worktree** — creates a temporary worktree from the task's branch
3. **Rebase** — fetches and rebases onto the current HEAD of the target branch
4. **Verify** — runs the repo's `verify_command` if configured (e.g. `go test ./...`)
5. **Merge** — fast-forward advances the target branch via compare-and-swap (detects external advances)
6. **Push** — optionally pushes to remote if `auto_push = true`
7. **Cleanup** — removes the temporary worktree and task branch

---

## Merge Queue Auditability

The merge queue is not a black box. Every meaningful controller decision and actuation is persisted as:

- `controller_decisions` records (what it is going to do and why),
- `controller_actuations` records (what it attempted and the result),
- `traces` records (workflow-level state changes).

`clankwork events --task <id>` and `clankwork events <item-id>` render a unified timeline of these sources with a reason string for each transition.

Typical decision kinds include:
- `merge_attempt` (start processing),
- `merge_ready` (advance to target branch),
- `merge_conflict` (classify and route conflict handling),
- `merge_verify_failed` (retry or reject on verify failure),
- `merge_processing_failed` (infrastructure/command failure after retries).

You can re-queue a failed item (`clankwork queue retry <id>`) or skip it permanently (`clankwork queue skip <id>`), and both are visible in the event stream.

### Conflict Handling

If rebase produces conflicts:
- Up to `merge_queue_max_attempts` attempts
- After N failures: spawns a conflict-resolution task, re-queues the merge item
- When the conflict task completes and signals `done`, the parent merge item is re-queued

### Backpressure

When the queue depth exceeds `merge_queue_max_depth`, dispatch is paused. This prevents starting new work when integration is backing up.

### Startup Recovery

On daemon restart, stuck in-progress items are reset to `queued`, and stranded `done` tasks (missed signal between done and enqueue) are enqueued.

---

## Prior-Art Index

Clankwork does not use task history as ambient agent memory. Instead, it indexes prior execution histories so planners can search for relevant prior art before decomposing new work. High-rework tasks, failed probes, merge conflicts, and verifier failures become planning input for future task DAGs and acceptance specs.

Every signal, step transition, and workflow outcome is still written to `traces`. Acceptance specs, done bundles, verification reports, artifacts, controller decisions, escalations, and merge outcomes remain task-local evidence records.

The `task_history_index` projection assembles those records into searchable planner context: final status, retry counts, acceptance criteria and probes, negative assertions, done-bundle claims, tests run, verification outcomes, deterministic command artifacts, merge results, and human escalations.

Search results are ranked with visible `rework_score` and `risk_score` bias so planners see instructive failures early. Prior art informs planning and acceptance obligations only. It is not injected into worker agents by default, it does not relax verification, and it never reuses previous evidence as proof for a new task.

---

## Reference Docs

- [CONTRIBUTING.md](CONTRIBUTING.md) — local development, testing, and PR expectations.
- [docs/api-reference.md](docs/api-reference.md) — Unix-socket HTTP endpoints, request style, response envelope, and error codes.
- [docs/config-reference.md](docs/config-reference.md) — complete `$CLANKWORK_HOME/config.toml` reference.
- [docs/migration-guide.md](docs/migration-guide.md) — SQLite migration conventions and schema-change workflow.
- [docs/troubleshooting.md](docs/troubleshooting.md) — common daemon, runtime, ACP, acceptance, and merge-queue failures.
- [docs/deployment.md](docs/deployment.md) — workstation, systemd, Docker, and production deployment notes.
- [docs/implementation-status.md](docs/implementation-status.md) — implemented, partial, and aspirational feature status.

---

## Directory Structure

```
clankwork/
├── cmd/clankwork/          # CLI commands (plan, task, signal, etc.)
├── internal/
│   ├── api/                # HTTP API handlers (Unix socket)
│   ├── client/             # HTTP client for the API
│   ├── config/             # config.toml loader
│   ├── daemon/              # daemon entry point + HTTP server
│   ├── mergequeue/         # merge queue processor + git operations
│   ├── model/               # all data types
│   ├── scheduler/          # dispatch logic + reconciler
│   ├── store/              # SQLite store (plans, tasks, agents, traces, prior-art histories)
│   ├── template/           # TOML template loader + validation
│   └── worker/             # tmux spawner + git worktree creator
├── migrations/             # SQLite migrations (embedded)
└── docs/
    ├── acceptance-verification.md
    ├── api-reference.md
    ├── architecture.md
    ├── config-reference.md
    ├── deployment.md
    ├── implementation-status.md
    ├── migration-guide.md
    └── troubleshooting.md
```
