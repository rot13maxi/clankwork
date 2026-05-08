# Agent Flywheel

> Exhaustive planning as the foundational lever for agent productivity — every completed cycle upgrades the artifacts feeding the next one.

## Core Thesis

The central insight: **"The same project gets faster and safer because every completed cycle upgrades the artifacts feeding the next one."** Humans invest 85% of effort in creating detailed plans and polished task definitions (beads), enabling agents to execute predictably within well-constrained designs.

## The Flywheel Model

Three reasoning spaces with escalating rework costs:

| Space | Purpose | Rework Cost |
|-------|---------|-------------|
| **Plan Space** | Architecture, workflows, tradeoffs | 1x |
| **Bead Space** | Self-contained work units with dependencies | 5x |
| **Code Space** | Agent execution, constrained by plan | 25x |

Each cycle compounds: refined plans → polished beads → predictable execution → improved understanding for next cycle.

## The Three-Tool Coordination Stack

### Beads (br)
JSONL-based task graph with full dependency structure. Each bead carries rationale, testing obligations, and success criteria — agents never need to reopen the plan.

### Agent Mail
High-bandwidth negotiation layer. Agents reserve files (with TTL expiry), announce work via targeted threads, communicate intent. Prevents coordination collisions.

### bv (Beads Viewer)
Graph-theory routing: PageRank, betweenness, critical-path metrics. Agents autonomously select work that **unblocks the most downstream tasks**.

> "The trio is not three nice-to-have tools. It is one operating system split into memory, communication, and leverage analysis."

## Key Techniques

### Multi-Model Synthesis
Use competing frontier models (GPT, Claude, Gemini, Grok) to independently design the same system. Each surfaces different blind spots. Then synthesize a "best of all worlds" hybrid. Consistently more robust than single-model planning.

### Bead Polishing Convergence
Three signals indicate beads are ready:
1. Output size shrinking (diminishing returns)
2. Change velocity decelerating
3. Content similarity increasing

Convergence at 0.75+ weighted score = ready. Above 0.90 = diminishing returns.

### The "Lie to Them" Technique
Models satisfice around 20-25 issues when asked to find "all" problems. Claim 80+ errors exist and they'll keep searching exhaustively.

### Fungible Agent Architecture
All agents are **generalists** reading the same AGENTS.md. No role specialization. Like fountain codes: any agent can catch any bead in any order. Losing agents produces slowdown, not failure.

### Single-Branch Git Model
All agents commit to `main`. Worktrees create merge debt and context loss. Instead:
1. **File reservations** — advisory locks via Agent Mail with TTL expiry
2. **Pre-commit guards** — block commits to reserved files
3. **DCG** — mechanically blocks dangerous commands

### Launch Timing
Stagger agent starts by minimum 30 seconds. Synchronized starts cause lock contention as agents compete for the same frontier beads.

### Fresh Eyes Pattern
When improvements plateau, start a brand-new agent session without accumulated context — catches issues the original session missed.

## Planning Leverage Metrics

- **Planning leverage**: 70%
- **Swarm determinism**: 60%
- **Reusable memory**: 52%

Plans routinely reach 3,000-6,000+ lines. Not slop — the result of countless iterations across multiple frontier models.

## Implementation Metrics

- Agent sweet spot: 2-4 Claude + 1 Codex + 1 Gemini for 100-399 beads
- Maximum practical swarm: ~12 agents
- CASS example: 25 agents, 11,000 LOC, 204 commits in ~5 hours

## The Compounding Effect

Four upgrade pathways per cycle:
1. **Artifact quality** — refined plans become templates
2. **Skill libraries** — reusable instruction bundles accumulate
3. **AGENTS.md evolution** — operating manuals absorb project-tested rules
4. **Bead patterns** — dependency graphs reveal optimal task decomposition

## Human Role: Clockwork Deity

> "YOU are the bottleneck. Be the clockwork deity to your agent swarms: design a beautiful and intricate machine, set it running, and then move on to the next project."

- Every 10-30 min: check progress, handle compactions
- Periodic: trigger fresh-eyes rounds
- Strategic: ensure bead graph converges on actual goals

## Connections

- The compounding loop is the same core idea as [[compound-engineering]]'s Plan→Work→Review→Compound cycle
- Beads task graph relates to [[gastown]]'s convoy tracking system
- Fungible agents contrast with [[ralph-orchestrator]]'s specialized hats — different philosophy on specialization
- Agent Mail parallels [[gastown]]'s beads protocol for inter-agent communication
- AGENTS.md as control plane mirrors [[compound-engineering]]'s CLAUDE.md and [[atlas]]'s learning system
- Multi-model synthesis could be systematized with [[dspy]]'s optimizer approach
- The fresh-eyes pattern addresses the staleness problem [[agent-memory]] tries to solve with decay curves
- Bead convergence detection relates to [[dspy]]'s optimization convergence — both need to know when to stop iterating
