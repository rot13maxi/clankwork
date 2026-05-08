# Autoloop

A preset-driven loop harness for long-running agent workflows with inspectable iterations, role-based topology, and parallel execution. Spinoff of [[ralph-orchestrator]], stripped down and opinionated.

## What it is / what problem it solves

Autoloop addresses the gap between "fire an agent at a task" and "run a disciplined multi-step workflow with visibility." The core problem: agents doing real work (coding, QA, security review) need structure around them. They need to know what role they're playing, what events they can emit, when to stop, and how to hand off to the next phase. Without this, you get uncontrolled loops that burn tokens, clobber each other's work, and produce unverifiable results.

Autoloop wraps any LLM backend (Claude via Pi, Kiro, or a mock) in an iteration harness that enforces a topology of roles and events, journals everything, and supports parallel branching when work can fan out.

## Key concepts and architecture

**Presets.** Bundled workflow templates (autocode, autoqa, autosec, autotest, autodoc, etc.). Each preset is a directory containing a `topology.toml` (role graph), `autoloops.toml` (loop config), `harness.md` (instructions injected every iteration), and role prompt files. 16 presets ship out of the box. User presets can extend or override bundled ones.

**Topology.** A TOML-defined directed graph of roles and events. Each role has an ID, a prompt file, and a list of events it can emit. A handoff map routes events to the next role. Example from autocode:

```
planner --tasks.ready--> builder --review.ready--> critic --review.passed--> finalizer
                                                     |                          |
                                                     +--review.rejected--> builder
                                                     finalizer --finalization.failed--> builder
```

Completion is a special event (`task.complete`) that terminates the loop. Only the finalizer can emit it. This is the [[llm-as-verifier]] pattern baked into the topology itself.

**Journal.** Append-only JSONL file recording every event: iteration starts/finishes, role activations, emitted topics, operator guidance, review outcomes. The journal is the single source of truth. Everything else (scratchpad, coordination state, metrics) is derived from it.

**Harness.** The iteration engine. Each cycle: build a prompt (injecting memory, tasks, scratchpad, topology context, backpressure notes), call the backend, parse emitted events from output, route to next role via topology, check stop conditions. The harness enforces that agents can only emit events allowed by their current role.

**Memory.** JSONL-based persistent store with three entry types: preferences, learnings, and meta. Soft-delete via tombstones. Budget-capped rendering (default 8000 chars) prevents prompt bloat. This is a simpler take on [[agent-memory]] without the structured knowledge graph.

**Tasks.** Another JSONL store tracking work items with open/done status. Materialized from the journal on demand.

**Scratchpad.** Inter-iteration context window. Collects iteration.finish events and renders them as a rolling history. Older iterations get compacted to one-line summaries, recent ones shown in full. Smart context management.

**Coordination.** Tracks issues, slices, commits, and archived files across iterations. State machine approach: events like `issue.discovered`, `slice.started`, `slice.verified`, `slice.committed` update a shared coordination record rendered as markdown.

**Profiles.** Composable prompt fragments scoped to repo or user level. A profile can inject extra instructions into any role's prompt for any preset. Enables per-project or per-developer customization without forking presets.

## How it orchestrates agent loops

The core loop in pseudocode:

```
1. Load preset (topology + config + harness instructions)
2. Generate run ID, create isolated run directory
3. For each iteration (up to max_iterations):
   a. Derive context: memory, tasks, scratchpad, routing constraints
   b. Build prompt: harness.md + role prompt + context layers + backpressure
   c. Call backend (Pi/Kiro/mock) with timeout
   d. Parse emitted events from output
   e. Validate events against topology (reject invalid, log backpressure)
   f. If event triggers parallel wave: fan out to concurrent branches, join results
   g. If event matches completion: stop
   h. If metareview interval hit: run a separate review agent on the run so far
   i. Route to next role via handoff map
   j. Journal everything
4. Return run summary
```

**Parallel waves.** When an agent emits an event that triggers fan-out, the harness parses objectives from the payload, creates isolated branch directories, launches concurrent agents (each with their own environment, journal, and allowed events), waits for all to complete, writes a join report, and resumes the main loop.

**Chains.** Multiple presets composed into sequential pipelines. Each step runs a full loop, produces a `result.md`, and passes a `handoff.md` to the next step with accumulated context. Failure in any step halts the chain. Dynamic chains can be spawned mid-run with budget constraints and a circuit breaker (2 consecutive failures blocks further spawning).

**Metareview.** Periodic self-assessment. Every N iterations (configurable), a separate agent reviews the run's journal and can adjust the loop's behavior. The loop context reloads after review, incorporating any changes. This is a lightweight version of the [[agent-flywheel]] concept.

**Backpressure.** When an agent emits an invalid event (not in its allowed set), the system captures the rejection and injects it into the next prompt as a constraint: "you emitted X which is not allowed, use one of [Y, Z] instead." This is a practical approach to keeping agents on-rails without hard crashes.

## Notable features or innovations

**Topology as a first-class concept.** Defining the role graph in TOML is elegant. It makes the workflow inspectable, validatable (orphan roles, unreachable events), and renderable as ASCII graphs. The handoff map is essentially a state machine for agent coordination. This is more structured than what [[ralph-orchestrator]] had and more explicit than [[gastown]]'s implicit routing.

**Fail-closed verification contract.** Only the finalizer can emit `task.complete`. The critic role is a mandatory gate. This enforces [[llm-as-verifier]] structurally rather than hoping the agent will self-verify. The critic operates with "fresh perspective" and must independently execute validation checks.

**Event-driven handoffs over prose.** Agents communicate through typed events, not natural language summaries. The harness validates events against the topology. This is a cleaner coordination protocol than free-form agent-to-agent messaging, related to the ideas in [[acp]].

**Budget-aware context injection.** Memory, tasks, and scratchpad all respect character budgets. When context exceeds budget, the system compacts older entries and adds pressure signals suggesting consolidation. This prevents the "7MB prompt" failure mode.

**Worktree isolation.** Full git worktree support for risky operations. Each parallel branch can run in its own worktree. The worktree module handles create, list, merge, and cleanup.

**Profile composability.** The fragment system lets you layer behavioral modifications without editing presets. A user profile can add "always run tests with --verbose" to the builder role across all repos, while a repo profile can add project-specific constraints.

**Dashboard.** Hono-based local web UI for inspecting runs, iterations, and artifacts. Uses the same journal data.

**ACP integration.** Depends on `@agentclientprotocol/sdk`, suggesting it can participate in broader agent ecosystems via the Agent Client Protocol.

## Strengths

**Inspectability is genuine.** The append-only journal + derived views (scratchpad, coordination, metrics) means you can always reconstruct what happened and why. This is critical for debugging agent workflows and for the [[agent-flywheel]] where you need to analyze failures to improve.

**The topology abstraction is the right primitive.** Defining roles, events, and handoffs as a graph makes workflows composable, validatable, and visible. It separates "what the workflow looks like" from "what each role does." This is the kind of thing [[compound-engineering]] talks about: making the orchestration layer explicit and programmable.

**Preset ecosystem creates leverage.** 16 bundled presets covering coding, QA, security, testing, docs, research, etc. means new users get value immediately. The preset-as-directory convention (topology + config + harness + roles) is a good packaging format.

**Practical memory management.** Budget caps, compaction, tombstones, and pressure signals are all real solutions to real problems. The 8000-char default is conservative and correct.

**Clean separation between harness and backend.** The Pi adapter, Kiro bridge, and mock backend all implement the same interface. Swapping LLM providers is a config change, not a code change.

## Weaknesses

**Single-agent-per-iteration model.** Each iteration runs one agent with one role. The "parallel waves" fan out to multiple processes, but within a single iteration, there's no multi-agent collaboration. For complex tasks, the planner-builder-critic-finalizer cycle can be slow because each handoff requires a full iteration with prompt construction and backend call.

**No structured learning loop.** Memory stores preferences and learnings, but there's no mechanism to automatically extract learnings from failures, no [[dspy]]-style prompt optimization, no systematic improvement over time. The metareview is manual assessment, not automated extraction. The [[atlas]] pattern of capturing failure diagnoses and feeding them back is absent.

**Topology is static within a run.** The role graph is defined at preset load time and doesn't change. You can't dynamically add roles or modify handoffs based on what the agent discovers mid-run. Real-world workflows often need adaptive routing.

**Chain error handling is basic.** Two consecutive failures triggers a circuit breaker, but there's no retry-with-different-strategy, no escalation to a human operator, no fallback presets. The failure mode is "stop."

**No cost tracking or token budgets.** The budget system tracks character counts in prompts, not actual API costs or token usage. For a system designed for long-running workflows, this is a significant gap.

**Tight coupling to file-based state.** Everything lives in `.autoloop/` as JSONL files. There's no database, no API for querying state, no way to aggregate across multiple concurrent runs without reading files. This works for single-operator use but won't scale to a team or [[gastown]]-style multi-tenant system.

**Pi-specific backend assumptions.** The Pi adapter embeds a Python bridge script and assumes Pi's `--mode json` output format. Despite the generic backend interface, the primary path is deeply tied to Anthropic's Pi tool.

## Relevance to Clankwork v3

Autoloop validates several patterns Clankwork already uses (worktree isolation, journal-based inspectability, role separation) and introduces some worth stealing:

- **Topology-as-config** is a better primitive than hardcoded orchestration. Clankwork's task dispatch could be expressed as a TOML topology where roles emit events and handoffs are explicit.
- **Budget-aware context injection** with compaction and pressure signals is directly applicable to the prompt construction layer.
- **Profile fragments** for per-project behavioral customization without forking the core workflow.
- **The preset packaging format** (topology + config + harness + role prompts in a directory) is a good convention for workflow templates.

What Clankwork should NOT copy: the file-based state model (Clankwork's SQLite + HTTP API is superior), the lack of automated learning extraction (Clankwork's [[atlas]] integration handles this), and the single-agent-per-iteration bottleneck (Clankwork already supports concurrent workers).
