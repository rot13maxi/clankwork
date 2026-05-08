# Clankwork Architecture

Clankwork is a single-machine control plane for dispatching AI agents against git
repositories. The daemon is deterministic: it schedules work, launches runtimes,
routes workflow steps, validates evidence, reconciles stuck agents, and merges
verified work without calling an LLM itself.

The operating principle is unchanged:

> Agents generate plausible solutions. The system's job is to progressively constrain the plausibility space until plausible = correct.

This document describes the system as implemented. Design notes that have been
absorbed into the implementation are summarized here instead of treated as future
plans. Remaining aspirational work is called out in [Not Done Yet](#not-done-yet).

Related reference docs:

- [Acceptance & Verification System](acceptance-verification.md) covers artifact schemas and acceptance invariants.
- [Lifecycle Persistence Model](lifecycle-persistence.md) covers audit, replay, retry, and artifact mutability semantics.
- [Merge-Controller Audit Events](merge-controller-audit-events.md) covers merge-controller event sequences.

## Runtime Shape

```text
Human / planner / CLI
        |
        v
Unix-socket HTTP API + clankwork CLI
        |
        v
Control plane daemon
  - scheduler and dispatcher
  - workflow graph compiler and router
  - reconciler and control-state recorder
  - merge queue processor
  - prior-art indexing
        |
        v
Worker runtimes
  - tmux sessions for arbitrary CLIs
  - ACP adapter sessions for typed event streams
        |
        v
Per-task git worktrees and evidence artifacts
```

The daemon stores state in `$CLANKWORK_HOME/clankwork.db`, listens on
`$CLANKWORK_HOME/clankwork.sock`, and keeps runtime logs, plans, templates, and
worktrees under `$CLANKWORK_HOME`.

## Core Components

| Component | Implemented responsibility |
| --- | --- |
| CLI | Human commands and agent-facing `bootstrap`, `signal`, `context`, and artifact commands. |
| Unix-socket API | JSON HTTP API used by the CLI and workers. |
| SQLite store | Durable plans, repos, tasks, dependencies, agents, traces, prior-art task histories, acceptance artifacts, merge queue, controller events, control state, and compiled workflows. |
| Scheduler | Selects runnable tasks from the dependency DAG, compiles workflow templates, fills runtime slots, and dispatches agent or deterministic steps. |
| Workflow router | Routes `success` / `failure` step outcomes through the persisted compiled graph and records step traces. |
| Worker runtime layer | Creates worktrees and starts tmux, ACP, or deterministic command executions. |
| Reconciler | Diagnoses agent/task state, handles stale sessions, nudges or cancels runtimes, records control decisions, and reroutes failures through workflow policy. |
| Acceptance controller | Validates specs, done bundles, verification reports, artifact provenance, computed confidence, and adversarial follow-up requirements. |
| Merge queue | Rebases completed task branches, verifies, advances target branches with compare-and-swap, optionally pushes, classifies conflicts, and records audit events. |
| Prior-art index | Projects task histories, evidence outcomes, failed probes, retries, and merge outcomes into searchable planner-only context. |

## Work Model

Plans are optional containers for related tasks. Tasks are the unit of execution.
Each task can belong to a repo, have dependencies, carry a priority, select a
workflow template, and choose a runtime.

Dispatch order is deterministic:

1. Load pending tasks whose dependencies are satisfied.
2. Apply merge-queue backpressure and dispatch pause state.
3. Sort by priority.
4. Allocate available slots.
5. Create a `clankwork/<task-id>` branch and worktree.
6. Start the task's current workflow step.

Workers do not share worktrees. Interaction between independent tasks is resolved
later by the merge queue.

## Workflow Templates

Templates are TOML mini-graphs. Before a task is dispatched, the selected template
is compiled into a persisted workflow graph in `compiled_workflows`; routing uses
that stored graph, not the mutable template file.

Template lookup order is:

1. `$repoPath/templates/<name>.toml`
2. `$CLANKWORK_HOME/templates/<name>.toml`
3. Embedded built-ins

Built-in templates:

| Template | Flow |
| --- | --- |
| `feature` | `acceptance_spec` -> `implement` -> `lint` -> `typecheck` -> `test` -> `acceptance` -> `complete` |
| `bugfix` | `implement` -> `lint` -> `typecheck` -> `test` -> `complete` |
| `refactor` | `implement` -> `lint` -> `typecheck` -> `test` -> `complete` |
| `simple` | `implement` -> `complete` |
| `critique` | `implement` -> `lint` -> `critic` -> `verify` -> `complete` |

Graph compilation validates local edges and policy. A graph with an
`acceptance_spec` gate is treated as substantive work and must include
implementation, deterministic verification, and acceptance verification in the
expected success-edge order. Invalid graphs block dispatch and produce
controller/audit events rather than starting a worker.

Agent steps load role text from `roles/<role>.md`. Deterministic steps run
configured commands without a model.

## Agent Runtime

The runtime layer supports three execution modes:

- `tmux`: universal fallback for arbitrary agent CLIs, with pane logging,
  attach, send, and kill support.
- `acp`: stdio JSON-RPC sessions through `acp-adapter`, with persisted
  `agent_events`, transcript rendering, prompt/cancel, and permission handling.
- `deterministic`: direct command execution for workflow gates such as lint,
  typecheck, and test.

The daemon injects `CLANKWORK_TASK_ID`, `CLANKWORK_ROLE`, `CLANKWORK_REPO_ID`,
and related context into agent processes. Agents are expected to run
`clankwork bootstrap`, use the returned task/role/failure context, and
then emit lifecycle signals:

```sh
clankwork signal started
clankwork signal progress "..."
clankwork signal done --spec artifacts/acceptance-spec.json
clankwork signal done --bundle artifacts/done-bundle.json
clankwork signal done --report artifacts/verification-report.json
clankwork signal failed "..."
clankwork signal blocked "..."
```

ACP permission policy is configured per runtime. The default `worktree` policy
allows `clankwork ...` control commands and commands whose explicit paths stay
inside the task worktree or configured allow paths; sensitive paths are denied
unless the runtime is explicitly trusted. Manual misses can be reviewed with
`clankwork agents permissions`, `approve`, `approve-session`, and `deny`.

## Reconciliation And Control State

The reconciler is now more than a timeout watchdog, but it remains deliberately
deterministic. It derives observations from task rows, agent rows, heartbeats,
tmux pane activity, ACP events, worktree facts, and recent decisions. It records:

- `control_observations`
- `controller_decisions`
- `controller_actuations`
- `task_control_states`

`clankwork task diagnose`, `clankwork reconcile task`, `clankwork refresh ...`,
and `clankwork events` expose this state.

The implemented control vocabulary includes runtime health, progress buckets,
error categories, escalation level, oscillation score, and normalized failure
signatures. Repeated failure signatures can block infinite retry loops and route
the task to escalation or blocked states. This is PID-inspired discrete control,
not a numeric PID loop.

ACP turn state is derived by replaying persisted events. A completed ACP turn
without a terminal lifecycle signal is treated differently from an active turn or
a permission wait. tmux uses lower-resolution signals: process liveness, pane
activity, heartbeat age, and context-limit text detection.

## Acceptance And Evidence

Feature work uses an evidence funnel:

```text
Acceptance Spec -> Implementation -> Done Bundle -> Verification Report -> Verdict
```

The control plane persists and validates:

| Artifact | Producer | Submitted with |
| --- | --- | --- |
| Acceptance spec | acceptance author | `clankwork signal done --spec <path>` |
| Done bundle | implementer | `clankwork signal done --bundle <path>` |
| Verification report | acceptance verifier | `clankwork signal done --report <path>` |
| Evidence artifact | verifier/control plane/worker | `clankwork artifact add ...` |

Acceptance specs are checked for executable probes, required artifacts, explicit
failure conditions, probe-to-evidence mappings, negative assertions where
required, and control-plane risk classification. Configured high-risk labels and
paths can raise risk for domains such as auth, payments, permissions, data
deletion, migrations, infrastructure/IAM, and public API contracts.

Done bundles must link claims to criteria and carry authoritative artifact
metadata. Verification reports must cite registered artifact IDs, map evidence to
probes, satisfy required artifact types, and include valid provenance. Registered
artifacts include hashes; changed files invalidate prior evidence.

The verifier's `confidence` field is preserved as model metadata only. Routing
uses control-plane computed confidence and label. High-risk tasks require higher
confidence and adversarial review satisfaction; unresolved high-severity
follow-up findings append probes or block completion.

Supported acceptance utilities include:

```sh
clankwork acceptance validate-spec artifacts/acceptance-spec.json
clankwork acceptance validate-report --spec artifacts/acceptance-spec.json artifacts/verification-report.json
clankwork acceptance validate-plan --spec artifacts/acceptance-spec.json artifacts/verification-plan.json
clankwork acceptance run-plan --spec artifacts/acceptance-spec.json artifacts/verification-plan.json --out artifacts/verification-report.json
clankwork acceptance show <task-id>
clankwork acceptance smoke --repo <repo-id> --runtime default --case all --wait
```

The execution-plan runner supports shell, HTTP, Playwright, database query, and
file assertion steps.

## Merge Queue

When a task reaches `complete` and the workflow has `auto_merge = true`, the
daemon enqueues a merge item. Merge processing is per repo and deterministic:

1. Select the next queued item.
2. Create a temporary merge worktree.
3. Fetch and rebase the task branch onto the target branch.
4. Run configured lint, typecheck, and verify commands.
5. Advance the target branch with compare-and-swap.
6. Optionally push when `auto_push` is enabled.
7. Mark the task merged and clean up task/merge worktrees.

Conflict classification is heuristic and implemented. Mechanical conflicts, such
as lockfile or generated-file conflicts, can spawn a conflict-resolution task.
Semantic conflicts reject the merge item and return the original task for rework.

The merge controller writes decisions, actuations, and traces. `clankwork events`
shows one timeline across `controller_decisions`, `controller_actuations`, and
`traces`. Manual controls are `clankwork queue retry <item-id>` and
`clankwork queue skip <item-id>`.

Backpressure pauses dispatch when queue depth crosses the configured threshold.
Graduated queue-pressure state is recorded for diagnostics; dispatch still uses
conservative threshold behavior.

## Prior-Art Index

Every workflow transition and signal creates structured traces. The prior-art
indexer projects terminal or integration-relevant task histories into
`task_history_index` and `task_history_fts` for planner retrieval.

Prior art is exposed through explicit planner commands such as
`clankwork prior-art search` and `clankwork plan create --with-prior-art`. It is
not injected into implementation agents unless the planner deliberately turns a
lesson into task text or an acceptance obligation.

Each indexed history summarizes acceptance criteria, probes, negative
assertions, done-bundle claims, tests run, verification reports, deterministic
command artifacts, retry counts, merge outcomes, and human escalations. Prior art
never relaxes verification; every new task still needs fresh task-local evidence.

## Data Model Reference

The durable schema is in `migrations/`. The important tables are:

- `plans`, `tasks`, `task_deps`, `repos`
- `agents`, `agent_events`
- `traces`
- `compiled_workflows`
- `acceptance_specs`, `done_bundles`, `verification_reports`, `artifacts`
- `merge_queue`
- `task_history_index`, `task_history_fts`
- `learnings`, `learnings_fts`, `candidate_learnings` (deprecated compatibility tables)
- `control_observations`, `controller_decisions`, `controller_actuations`,
  `escalations`, `task_control_states`

Most API-facing structs live in `internal/model/`. Store methods are grouped by
domain in `internal/store/`.

## Configuration

Global config lives in `$CLANKWORK_HOME/config.toml`. Runtime config is currently
daemon-level. Repo records carry target branch and verification commands.

Important scheduler settings include max slots, heartbeat timeout, deterministic
step timeout, merge queue depth, merge attempt count, and prior-art indexing
behavior.

Runtime entries define command, args, transport, model label, ACP permission
policy, allow/deny paths, timeout, and optional escalation target.

## Not Done Yet

The following ideas remain aspirational or partial and should not be described as
complete system behavior:

- **Project-local runtime config in `clankwork.toml`:** runtime config currently
  lives in the daemon config; repo-local runtime policy is still future work.
- **Automated ticket ingestion:** GitHub/Linear ingestion is not implemented.
  Plans and tasks are created through CLI/API today.
- **Rich dashboard/TUI:** the implemented operator surface is CLI plus JSON API.
- **Custom native agent harness:** Clankwork still launches existing runtimes
  through tmux or ACP adapters.
- **Learned prompt optimization:** roles are data, but there is no DSPy-style
  optimizer changing prompts from outcomes.
- **Structured checkpoint signal:** `signal progress` is still mostly free-form;
  richer milestone/evidence checkpoint fields are a future control-plane input.
- **Fully adaptive dispatch based on queue pressure:** queue pressure is recorded
  and can pause dispatch, but it does not yet reprioritize smaller/lower-risk
  tasks or tune verification strictness.
- **Cryptographic artifact signing and identity-backed provenance:** artifacts
  have hashes and provenance metadata, not signed attestations.
- **Mutation testing against known-bad implementations:** acceptance hardening is
  implemented through specs, provenance, probe coverage, confidence, and
  adversarial follow-up, not mutation testing.
- **Team/multi-user product features:** the architecture is still single-user,
  single-machine first.
