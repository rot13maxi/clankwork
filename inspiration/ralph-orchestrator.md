# Ralph Orchestrator

Hat-based agent orchestration framework that keeps AI agents looping until the task is done.

## What it is / what problem it solves

Ralph implements the "Ralph Wiggum technique" (coined by Geoffrey Huntley): instead of running an AI agent once and hoping it gets things right, you put it in a loop with quality gates that reject incomplete work. The agent keeps iterating until it either signals `LOOP_COMPLETE` or hits an iteration limit. Think of it as a [[autoloop]] with built-in backpressure.

The core problem: a single agent pass rarely produces production-ready code. Tests fail, lint errors remain, edge cases get missed. Ralph's answer is structured iteration with role-based coordination, not a smarter single pass.

GitHub: https://github.com/mikeyobrien/ralph-orchestrator
Built in Rust (83%) + TypeScript (14%). MIT licensed.

## Key concepts and architecture

**Cargo workspace of 9 crates:**
- `ralph-cli` -- entry point, commands: run, init, plan, task, events, tools
- `ralph-core` -- orchestration engine: EventLoop, config loading, event parsing, memory/task storage
- `ralph-adapters` -- CLI backend integrations via a `CliBackend` trait (PTY-based execution)
- `ralph-proto` -- shared types: Event, Hat, EventBus
- `ralph-tui` -- terminal UI via ratatui (iteration count, elapsed time, hat info, activity)
- `ralph-api` -- RPC server for the web dashboard
- `ralph-telegram` -- Telegram bot for human-in-the-loop
- `ralph-e2e` -- end-to-end tests across 7 tiers
- `ralph-bench` -- benchmarks

**State model:** Persistent state lives in `.agent/` directory containing memories, tasks, event history, and scratchpad files. The in-memory EventBus tracks hats, pending events, and history during execution. Configuration loaded from `ralph.yml`.

**Multi-backend support:** Claude Code, Kiro, Gemini CLI, Codex, Amp, Copilot CLI, OpenCode. Each backend is a PTY-based adapter implementing a common trait. This is similar to Clankwork's RuntimeAdapter pattern but more backend-diverse.

## How it orchestrates agents

### The Hat System

Hats are specialized personas the orchestrator adopts. Each hat has:
- **Triggers** -- events that activate the persona
- **Publishes** -- events the hat can emit
- **Instructions** -- injected prompts that guide behavior
- Optional: backend spec, activation limits, default publish events

A typical configuration has 4 hats working in sequence:

1. **Planner** -- breaks work into granular sub-tasks scoped to individual modules
2. **Builder** -- implements a single sub-task, must verify fmt/clippy/tests before marking complete
3. **Reviewer** -- validates compliance, verification evidence, idioms, test coverage. Blocks on synthetic tests.
4. **Finalizer** -- creates changelog entries, emits `LOOP_COMPLETE`

### Event-Driven Coordination

Events are typed messages with topic, optional payload, source hat, and optional target routing. Supports exact matches and glob patterns (`build.*`, `*.error`). Hats communicate by emitting events that trigger other hats. This is a pub/sub model inside a single process.

Three workflow patterns:
- **Pipeline** -- linear progression through sequential hats (plan -> build -> review -> finalize)
- **Supervisor-Worker** -- one coordinator distributing tasks to multiple workers
- **Critic-Actor** -- iterative refinement between proposal and review stages

### Backpressure Gates

Quality gates that must pass before the loop can advance. Configured as shell commands:
- `cargo fmt --check` (formatting)
- `cargo clippy` (linting)
- `cargo test --all` (tests)

If any gate fails, the work is rejected and the agent iterates again. This is the core insight: make the loop cheap and the quality bar rigid. Similar to [[llm-as-verifier]] but using deterministic checks (tests, lint) rather than LLM-based verification.

### The Loop

```
ralph run -p "Implement feature X"
```

The event loop runs up to N iterations (default 150, 8-hour timeout). It starts with a `work.start` event, activates the appropriate hat, runs the agent, parses events from agent output, routes them to the next hat, and repeats. The loop exits when a hat emits `LOOP_COMPLETE` or limits are hit.

## Notable features and innovations

**Plan-Driven Development (PDD):** `ralph plan "Add JWT auth"` generates a spec directory with requirements.md, design.md, and implementation-plan.md before any code is written. The planner hat then decomposes this into sub-tasks. This aligns with [[compound-engineering]] principles -- planning up front produces dramatically better results.

**Guardrails system:** Seven guardrails enforced in the default config: fresh context on each iteration, mandatory commits, verification evidence, real acceptance tests (not synthetic), confidence protocols, source preservation, and targeted testing. The reviewer hat blocks if it detects synthetic/fake tests.

**RObot (Human-in-the-Loop):** Telegram integration where agents can ask humans questions mid-loop and block until answered. Humans can also send proactive guidance at any time. Supports parallel loop routing via reply-to or `@loop-id` prefix. This is a real operational feature, not a toy.

**Memory and Tasks:** Persistent memory stored in `.agent/` directory. Events carry routing signals, not data. Detailed information goes into memories. This separation keeps the event bus lightweight. Related to [[agent-memory]] patterns.

**MCP Server Mode:** Can run as an MCP server over stdio, scoped to a single workspace root. One server instance per repo. This positions Ralph as both a standalone tool and a composable component in larger systems like [[acp]].

**Web Dashboard:** Alpha-stage browser UI for monitoring orchestration loops. Rust RPC backend + frontend.

**Presets:** Built-in patterns for common workflows: code-assist, debug, research, review, pdd-to-code-assist. You can compose these or define custom hat configurations via YAML.

## Strengths and weaknesses

### Strengths

- **The loop-until-done model is the right primitive.** Most agent failures are from stopping too early. Ralph's core loop + backpressure gates is a simple, correct insight. Same principle behind [[autoloop]] and [[agent-flywheel]].
- **Hat system is a clean abstraction.** Role separation via events is more composable than hardcoded pipelines. You can reconfigure the workflow by editing YAML, not code.
- **Multi-backend is genuinely useful.** Supporting 7 different AI CLIs means you're not locked to one provider. The CliBackend trait is a good interface boundary.
- **Backpressure is deterministic.** Using tests/lint/typecheck as gates rather than LLM-as-judge avoids the unreliability of [[llm-as-verifier]] for objective checks. Reserve LLM judgment for subjective review.
- **Rust implementation** gives good performance and reliability for a long-running daemon.
- **PDD workflow** encodes the insight from [[compound-engineering]] that planning before coding produces dramatically better agent output.
- **Human-in-the-loop via Telegram** is a practical ops feature. Agents asking for help when stuck is better than agents guessing wrong.

### Weaknesses

- **Single-machine, single-agent model.** One agent iterates in a loop. No parallel task execution across multiple workers/worktrees. Clankwork's per-worker-worktree model with a central scheduler is more scalable for large plans.
- **No learning across runs.** Memories persist in `.agent/` but there's no structured feedback loop where outcomes from run N improve run N+1. No equivalent to [[atlas]] learning or [[dspy]] optimization. The memory is a scratchpad, not a training signal.
- **Event bus is in-process.** Hats communicate within a single orchestrator process. This limits scaling to multiple machines or long-lived daemon architectures. Compare with Clankwork's HTTP API approach.
- **No integration branch model.** Ralph runs in the working directory directly. No worktree isolation, no merge queue, no integration branch per plan. For multi-task plans, this means tasks can clobber each other.
- **Quality gates are pass/fail only.** No graduated feedback -- the agent doesn't learn which specific test failed or why. A richer diagnostic (like [[atlas]] failure diagnosis) would help the agent fix issues faster rather than just re-running.
- **Dashboard is alpha.** The web UI exists but is early-stage. Operational visibility for long-running agent loops is critical and this isn't mature yet.
- **150-iteration default feels high.** If your agent needs 150 iterations, something is wrong with the task decomposition. The guardrails should catch this earlier. A smarter escalation policy (e.g., after N failures on the same gate, change strategy) would be more efficient.

### Relevance to Clankwork v3

Ralph validates several ideas Clankwork already implements: loop-until-done, backpressure gates, plan-before-build. The hat/event system is an interesting alternative to Clankwork's scheduler-based task dispatch. The key gaps Ralph has (no parallel execution, no cross-run learning, no worktree isolation) are exactly the things Clankwork v2 already handles. For v3, the interesting takeaways are:

1. **Event-driven hat coordination** could inspire a more flexible task-routing layer than the current sequential scheduler
2. **Telegram human-in-the-loop** is worth considering for operator interaction during long-running plans
3. **The guardrail/backpressure pattern** reinforces that deterministic checks should be the primary quality gate, with [[llm-as-verifier]] reserved for subjective judgment
4. **PDD spec generation** before implementation is a pattern worth formalizing in the planning phase
