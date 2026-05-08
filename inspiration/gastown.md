# Gastown

> AI agent orchestration layer that treats agent work as structured data — attribution, quality measurement, and coordination at scale.

## What It Is

Gastown is an orchestration layer for AI agent workflows focused on three problems:
- **Accountability tracking** — all work attributed to specific agents with provenance
- **Quality measurement** — track records enabling model evaluation and A/B testing
- **Work coordination** — routing tasks across multiple repositories and teams

## Architecture

### Role Taxonomy

**Infrastructure roles** (system management):
- **Mayor** — global coordinator at town root; singleton, persistent
- **Deacon** — background supervisor daemon; singleton, persistent
- **Witness** — per-repo lifecycle manager for ephemeral workers
- **Refinery** — per-repo merge queue processor

**Worker roles** (project execution):
- **Polecat** — ephemeral workers with dedicated worktrees, Witness-managed
- **Crew** — persistent workers with personal clones, user-managed
- **Dog** — Deacon helpers for infrastructure tasks only (not general workers)

### Key Distinction: Crew vs Polecats

Crew members are persistent, human-controlled, handle exploratory/long-running work. Polecats are transient, automatically supervised, designed for discrete parallelizable tasks.

## Communication

### Beads Protocol

Native messaging system for agent-to-agent communication and event tracking, coordinated via `.beads/` directories at the town level.

### Mail Protocol

Formal message exchange between rigs (repos) and agents.

### The tmux Approach

Gastown runs agents in tmux sessions and uses `tmux send-keys` to send messages between them — a pragmatic alternative to formal protocols like [[acp]]. Simple, debuggable, but less structured.

## Work Tracking: Convoys

A "convoy" (🚚) batches related work for unified tracking:
- Single source of truth for in-flight work
- Cross-repository tracking
- Automatic notifications on completion
- Historical records

## Identity & Attribution

All work captures provenance:
- Git commits: `Author: gastown/crew/joe`
- Beads events: `created_by: gastown/crew/joe`
- Identity persists cross-repo — joe's work always appears on joe's record

This enables **model A/B testing** — assign same task to different models, compare completion time, quality, revision counts.

## The Propulsion Principle

Core operating principle: "If you find something on your hook, YOU RUN IT." Agents function as pistons — immediate response to assignment, no request-confirmation workflows.

## Directory Structure

```
~/gt/
├── .beads/              # town-level communication
├── mayor/               # global config
├── deacon/dogs/         # infrastructure workers
└── <rig>/
    ├── crew/            # persistent workers
    ├── polecats/        # ephemeral workers
    └── .repo.git/       # shared bare repository
```

## Connections

- Convoy tracking relates to [[agent-flywheel]]'s beads task graph
- Witness lifecycle management similar to [[ralph-orchestrator]]'s hat supervision
- Attribution system enables the kind of model comparison [[compound-engineering]] advocates
- The tmux-based communication is the pragmatic alternative to [[acp]]
- Polecat worktree isolation mirrors what [[autoloop]] does with parallel waves
