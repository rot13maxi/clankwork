# Clankwork v3

Clankwork is an automated software factory — a deterministic control plane that dispatches AI agents to implement, verify, and merge code changes. It continuously learns from its own work to get better over time.

## Core Principle

**Agents generate plausible solutions. The system's job is to progressively constrain the plausibility space until plausible = correct.**

LLMs don't write correct code — they write plausible code. Clankwork layers constraints from cheap (linter, every file write) to expensive (acceptance testing, after all cheaper checks pass). Each layer shrinks what's plausible. The final layer — acceptance testing where an agent actually *uses* the software — produces an unfakeable signal that closes the gap between "tests pass" and "software works."

## Key Design Decisions

Read these docs before making architectural changes:

- **`docs/architecture.md`** — the complete system design. This is the source of truth.
- **`inspiration/principles.md`** — the "why" behind every design decision (13 principles).
- **`inspiration/README.md`** — index of 11 research sources that shaped the design.
- **`inspiration/*.md`** — detailed notes on each inspiration source (Gastown, ATLAS, Autoloop, etc.)

The inspiration folder is indexed with QMD for semantic search:
```
qmd search "verification funnel" -c inspiration
qmd search "learning decay" -c inspiration
```

### Principles that are easy to violate

1. **The control plane is deterministic. It never calls an LLM.** Agents are resources it dispatches, monitors, and garbage-collects. Health checks, retry logic, state reconciliation, lifecycle tracking, queue management — all deterministic code. If you're tempted to add an LLM call to the daemon, stop. That logic belongs in an agent step.

2. **Three separated concerns: what to do (role .md), how to do it (template .toml / compiled graph), what runs it (runtime config).** Roles define the prompt. Templates define the candidate workflow DAG. Before dispatch, the deterministic control plane compiles the template into a persisted workflow graph and refuses graphs that violate policy. Runtimes define the command + args. Don't conflate them. A "planner" and an "implementer" are the same executable — they differ only in the role they load.

3. **CLI is the universal interface.** Humans, planning agents, and worker agents all use the same `clankwork` CLI. Not MCP, not a custom protocol. A CLI call is a subprocess invocation — every agent runtime can do it.

4. **Acceptance testing is the verification layer that matters most.** An agent actually uses the software (Playwright, CLI, HTTP requests). You can't hardcode your way past "boot the service and show me a transaction." The `acceptance` role in `roles/acceptance.md` exists for this.

5. **Prior art is search-based, not injection-based.** Agents pull relevant task history through prior-art search. Legacy learnings tables remain only for compatibility and should not be used as a new command surface.

## Project Layout

```
cmd/clankwork/          CLI entry points (one file per command)
internal/
  api/                  HTTP API handlers (signals, bootstrap, CRUD)
  client/               Go client for the HTTP API (used by CLI)
  config/               config.toml loader
  daemon/               Daemon startup and tick loop wiring
  learning/             Deprecated compatibility synthesis
  mergequeue/           Merge queue processor (rebase, verify, merge)
  model/                Data types (Task, Agent, Trace, Learning, etc.)
  scheduler/            Dispatcher, reconciler, triage, step routing
  store/                SQLite persistence (one file per table)
  template/             TOML template loader + built-in templates
    builtin/            Built-in workflow templates (feature, bugfix, etc.)
  workflow/             Compiled workflow graphs + trace conformance validation
  worker/               ACP/tmux runtime adapters + git worktree creator
migrations/             SQLite schema migrations (applied in order)
roles/                  Agent role definitions (markdown, loaded at bootstrap)
docs/                   Architecture doc
inspiration/            Research notes + QMD-indexed source material
test/e2e/               End-to-end tests with reference agent implementation
```

## Contributor Guide

- `make build` builds `bin/clankwork` with the current git version injected.
- `make test` runs `go test ./... -count=1 -race`; use it before submitting behavior changes.
- `make lint` runs `go vet ./...`.
- `make run` builds, then starts the daemon via `bin/clankwork daemon`.
- `make install-acp-adapter` installs the configured ACP adapter into `$CLANKWORK_HOME/bin`.
- `make clean` removes `bin/`.

For focused loops, run package tests directly, for example `go test ./internal/scheduler -count=1`.

Keep SQL migrations in `migrations/` numbered sequentially. Keep built-in workflow templates in `internal/template/builtin/*.toml`, role prompts in `roles/`, design notes in `docs/`, and end-to-end tests in `test/e2e/`.

Recent commits use concise imperative subjects, sometimes scoped with conventional prefixes such as `fix(scheduler): ...` or area prefixes like `acceptance: ...`. Pull requests should include a short problem/solution summary, tests run, and any schema, daemon lifecycle, or workflow-template impacts.

## Coding Conventions

### Go style

- Standard `gofmt`. No custom linter config.
- `internal/` for everything — nothing is a public API.
- One file per concern in `internal/store/` (tasks.go, agents.go, traces.go, etc.)
- CLI commands in `cmd/clankwork/` — one file per command, registered in `main.go`.
- Errors: return `error`, don't panic. Use `fmt.Errorf("context: %w", err)` for wrapping.
- Logging: `log/slog` (structured). Info for normal operations, Warn for recoverable issues, Error for failures.
- No generics, no clever abstractions. Boring, obvious code.

### API patterns

- All endpoints return `model.APIResponse{OK: bool, Data: any, Error: *APIError}`.
- `OK(w, data)` and `Fail(w, status, code, message)` helpers in `internal/api/helpers.go`.
- GET for reads, POST for writes. Query params for GET filters, JSON body for POST.
- Route naming: `/v1/<resource>.<action>` (e.g., `/v1/tasks.list`, `/v1/signals.done`).

### Templates

- TOML files in `internal/template/builtin/`.
- Steps have `type` (agent or deterministic), `on_success`, `on_failure`.
- Deterministic test steps use `command = "clankwork"`, `args = ["verify"]` — the scheduler resolves this to the repo's `verify_command`.
- `"complete"` is a special on_success value that marks the task done.
- Dispatch compiles templates into persisted workflow graphs in `compiled_workflows`; routing should use the compiled graph, not re-interpret the template on every step.
- If compilation reports missing required gates or illegal edges, dispatch must be blocked and a `graph_compilation` observation / `graph_compilation_failure` decision recorded.
- Trace conformance validation lives in `internal/workflow` and checks that execution traces follow the compiled graph.

### Store / migrations

- Pure Go SQLite via `modernc.org/sqlite` (no CGo).
- Migrations in `migrations/` named `NNNN_description.sql`, applied in order at startup.
- Schema version tracked in `schema_version` pragma.
- All queries use `?` placeholders, never string interpolation.

### Testing

- Unit tests alongside code (`*_test.go`).
- Integration tests in `cmd/clankwork/m*_integration_test.go` use `FakeSpawner` (no real tmux).
- E2E tests in `test/e2e/` use real tmux, real git, real daemon. Skip if tmux/jq unavailable.
- `test/e2e/reference-agent.sh` is the canonical reference implementation of an agent.

### Agent lifecycle (for agent builders)

The contract between the control plane and an agent:

1. Control plane compiles and persists the task's workflow graph before dispatch.
2. Control plane spawns an agent runtime session (ACP by default; tmux supported) with env vars: `CLANKWORK_TASK_ID`, `CLANKWORK_HOME`, `CLANKWORK_REPO_ID`, `CLANKWORK_STEP`, `CLANKWORK_ROLE`.
3. Agent calls `clankwork bootstrap` → gets task, role definition, failure context, prior-art context, CLI reference.
4. Agent calls `clankwork signal started`.
5. Agent does work (reads task, writes code, commits).
6. Agent calls `clankwork signal progress "<message>"` periodically (heartbeat).
7. Agent calls `clankwork signal done` or `clankwork signal failed "<reason>"` or `clankwork signal blocked "<question>"`.
8. Control plane validates the signal payload and routes the outcome through the compiled graph.

See `test/e2e/reference-agent.sh` for a working example and `roles/*.md` for role definitions.

## Non-Negotiable Rules

- **Never commit directly to master in agent worktrees.** Agents work on `clankwork/<task-id>` branches. The merge queue handles integration.
- **Never add LLM calls to the control plane.** The daemon is deterministic. Agent intelligence goes in role definitions and agent runtimes.
- **Do not leave requested work half-done, stubbed, or parked behind TODOs.** If the user asks for something, build it all the way: implement the code, migrations, templates, tests, docs, and command wiring needed for the request to be genuinely complete.
- **Run `go test ./...` before considering any change complete.** Currently 115 tests across 14 packages.
- **Don't modify the agent contract without updating the reference agent** (`test/e2e/reference-agent.sh`) and running e2e tests.
- **Don't hardcode project-specific commands in templates.** Use `clankwork verify` (resolved to repo config). Templates must be portable across repos.


<!-- Clankwork agent instructions injected at dispatch -->
# Clankwork Agent Instructions

You are an autonomous agent dispatched to complete a task (step: **implement**).

## How to start

Run this command immediately to load your task context:

```bash
clankwork bootstrap
```

The bootstrap output contains your task title, body, role definition, failure context
from prior attempts and relevant prior art. Read it carefully before starting work.

## Signaling

When done, signal the outcome — do not exit without signaling:

```bash
clankwork signal done                     # success
clankwork signal failed "reason"          # unrecoverable failure
clankwork signal blocked "what you need"  # need human input
```

Send heartbeats every few minutes while working:

```bash
clankwork signal progress "brief status"
```

## Git workflow

You are working in a git worktree on branch **clankwork/$CLANKWORK_TASK_ID**.
Commit your changes before signaling done — the merge queue will rebase and merge your branch.

```bash
git add -A && git commit -m "<describe what you did>"
clankwork signal done
```

Do not push. Do not merge. Just commit and signal.

## Environment

Your task ID is in **$CLANKWORK_TASK_ID**. The daemon socket is at **$CLANKWORK_HOME/clankwork.sock**.
All clankwork CLI commands read these automatically — you do not need to pass them as flags.

## Continuous Verification

This repo has lint/typecheck commands configured. Run them **after every file change** to catch errors early.
A pre-commit hook will also enforce these before any commit.

**Lint** (run after every change):
```bash
clankwork verify lint
```

Fix any issues immediately — do not accumulate lint or type errors.
