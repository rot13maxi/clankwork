# Clankwork v3 — Core Principles

## The Plausibility Principle

**Agents generate plausible solutions. The system's job is to progressively constrain the plausibility space until plausible = correct.**

Constraints form a hierarchy of increasing cost and power — push cheap ones left (continuous), expensive ones right (after cheaper checks pass):

| Constraint | Cost | When | Eliminates |
|---|---|---|---|
| Linter (on every file write) | Near-zero | Continuous | Syntactically invalid solutions |
| Type checker | Near-zero | Continuous | Structurally invalid solutions |
| Unit tests | Cheap | After implementation | Logically invalid solutions |
| E2E tests | Medium | After unit tests pass | Integration failures |
| Acceptance (agent-as-user) | Expensive | After all tests pass | "Plausible but wrong" solutions |

Each layer shrinks the space. The earlier and more continuously you apply constraints, the less the agent drifts, and the less expensive later stages become.

## The Deterministic Control Plane Principle

**The control plane is not an agent. It's a program. Agents are resources it dispatches, monitors, and garbage-collects.**

Everything that can be deterministic should be deterministic. Escalate to an agent only when the situation requires judgment. Health checks, state reconciliation, retry logic, lifecycle tracking, queue management — all deterministic code. Token spend is reserved for work that actually requires intelligence.

Graduated escalation: try deterministic fix N times → escalate to agent.

## Learning System: Capture vs Synthesis Split

Learning happens in two phases:

**Phase 1: Per-workflow trace archival (inline, cheap)**
When a workflow completes (success or failure), archive the structured trace — what happened, how many retries, where it got stuck, what acceptance caught. No interpretation yet. Just raw material. Append-only trace store.

**Phase 2: Periodic batch synthesis (background, expensive)**
Every N workflow runs, a learning extraction agent sweeps recent traces and looks for patterns *across* multiple runs. Prioritize high-struggle workflows (surprise-based, à la ATLAS). This is where real insight lives — single failures can be misleading, but patterns across runs are signal.

Synthesis produces three types of output:
- **System-wide rules** → update CLAUDE.md or equivalent (every agent sees these)
- **Topical learnings** → searchable learning store with progressive disclosure (one-line summary → digest → source)
- **Workflow template fixes** → the system improves its own process

Learnings use **access-weighted retention**: agents that find a learning useful bump its timestamp. Old learnings that nobody accesses decay and get garbage-collected. Old learnings that keep being useful survive.

Retrieval is search-based (semantic + keyword), not injection-based. Agents pull what's relevant to their current task rather than getting everything in the prompt.

## Work Triage

Work falls into (at least) two categories that determine how much verification is needed:

1. **Trivial changes** — color changes, copy updates, simple bug fixes. Auto-merge if tests pass. Cheap constraints are sufficient.
2. **Substantive changes** — new features, complex fixes, behavioral changes. Require acceptance testing. Human either defines acceptance criteria directly, or provides enough intent that the system can derive acceptance criteria during planning.

The triage decision happens early in the pipeline and determines which workflow template (and how much verification) gets applied.

## Agent Architecture: Identity Decoupled from Runtime

Agents are generic runtimes that receive their identity (role, prompt, instructions) as data — not baked-in. A "planner" and an "implementer" are the same executable; they differ only in the prompt/molecule they load.

This enables:
- Changing agent behavior without changing runtime code
- A/B testing prompt variants for the same role
- Version-controlling prompts alongside workflow templates
- Swapping models underneath without touching the prompt layer
- Future DSPy-style prompt optimization over historical task outcomes

## Model Routing

Not all work needs the same model. Route based on task type and difficulty:
- **Triage, simple implementation** → local/cheap model (e.g., local GPU)
- **Planning, complex tasks** → frontier model (e.g., Claude)
- **Escalation rule**: if a task exceeds N rework iterations, promote to a more powerful model. The control plane makes this decision deterministically based on policy.

This is conceptually similar to ATLAS's confidence router (Thompson Sampling over strategies) but simpler — policy-based routing from the deterministic control plane.

## Human Interaction Model

Three input channels, each optimized for different work:

1. **Conversational planning** — human sits with a planning agent (crew-style, persistent, not the control plane) to design features, define acceptance criteria, iterate on requirements. When satisfied, the agent slings work to the control plane. This is where high-value human judgment happens.

2. **Ticket ingestion** — system pulls from GitHub issues / Linear / etc. Triage (cheap model) classifies trivial vs substantive. Trivial auto-dispatches through the appropriate template. Substantive gets parked for the next planning session.

3. **CLI/TUI/Dashboard** — observe system state. What's running, what's stuck, what's in the merge queue, agent health, last heartbeat. Primarily read-only. Light control (pause, kill, retry).

The control plane is none of these. It doesn't plan, doesn't chat, doesn't have opinions. It executes workflows, manages lifecycles, and runs the machine.

## Agent-to-Control-Plane Communication: CLI, not MCP

The control plane exposes a CLI that agents call for all coordination needs — signaling lifecycle events, reporting status, querying state, requesting work. Not an MCP server (too brittle, protocol overhead, connection lifecycle issues). A CLI call is a subprocess invocation — every agent runtime can do it, it's trivially testable from a terminal, and there's no persistent connection to manage.

This is the Gastown pattern: agents are just processes that happen to call `clankwork signal done` or `clankwork status` as needed.

## Merge Model

Code flows: agent finishes → verification funnel passes → merge queue → target branch.

The merge queue is part of the deterministic control plane. Not an agent. Tasks enter when they pass the full verification funnel (including acceptance for substantive work). The control plane merges sequentially, handles conflicts, and backs out + re-dispatches if post-merge tests fail.

Target branch is configurable per-project (main, release branch, etc.). No integration branches — those were a defensive measure in v2 against poor observability and unreliable verification. If the verification funnel works, code goes straight to the target.

Workers operate in git worktrees for isolation during implementation, but the worktree is temporary — it exists only while the agent is working. The merge queue consumes the finished work and the worktree gets cleaned up.

## Parallelism

- Configurable concurrency limit (N agent slots) — governs max simultaneous workers
- Work backlog is a DAG with explicit dependencies and priorities
- Dispatch: if slot free → topo-sort by dependencies → sort by priority → dispatch top of queue
- Each worker gets its own git worktree (isolation during implementation)
- Workers never coordinate with each other directly — they work independently
- Merge queue serializes results: rebase onto HEAD → verification funnel → merge
- Conflict handling:
  - Trivial (mechanical, no behavior change) → ephemeral agent resolves
  - Semantic (contradictory behavioral changes) → reject, re-dispatch for rework

**Backpressure:** If merge queue depth exceeds threshold, throttle dispatch (stop starting new work until queue drains). If oldest queue item exceeds age threshold, escalate to agent for diagnosis (could be crashed process, dead SSH agent, etc.). This is just the graduated escalation principle applied to the merge queue.

## Scope

**Multi-repo, single-user.** Each repo is a "rig" (Gastown term). The control plane coordinates across repos — a task in repo A can depend on a task in repo B. The deterministic control plane makes multi-repo cheaper than Gastown's approach (no per-repo nanny agent burning tokens).

**Single-user, design for eventual multi-user at boundaries.** The control plane is a local daemon. No auth, no multi-tenancy. Teammates collide on git (same as today). Future team features would be: shared merge queue, cross-user work dispatch, shared learning corpus. Core is unchanged.

## Tech Stack

**Go.** The control plane is infrastructure — daemon, CLI, scheduler, process manager. Go's sweet spot.

- Single binary distribution (agents call `clankwork status` as subprocess — no runtime dependency in worktrees)
- Goroutines for concurrency (reconciliation loops, parallel dispatch, merge queue)
- Excellent subprocess management for spawning agents
- SQLite via modernc.org/sqlite (pure Go, no CGo)
- Cross-compilation for Linux/Docker if needed
- Simple language = less room for agent-generated code to get creative in bad ways

## Plan Artifacts

Plans are durable, identifiable, traceable artifacts:
- Metadata in SQLite (ID, title, creator, timestamp, status, links to spawned tasks)
- Full content as markdown files on disk (referenced by DB record)
- Predefined sections: steps, acceptance criteria, dependencies
- Body is free-form markdown (lingua franca for agents)
- Learning system indexes the markdown separately
- Tasks are "on the hook" of their assigned agent — if agent restarts, it sees the hook and resumes
