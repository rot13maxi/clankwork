# Compound Engineering

> An AI-native development philosophy where each unit of work makes subsequent work progressively easier — the system learns and improves with every cycle.

## Core Thesis

Traditional development accumulates complexity. Compound engineering inverts this: features should teach systems new capabilities, making codebases easier to understand, modify, and trust over time. The critical insight is that **building the system that builds software** matters more than building software directly.

## The Four-Step Main Loop

**Plan → Work → Review → Compound → Repeat**

The fourth step is the differentiator. Steps 1-3 produce a feature; step 4 produces systems that build better features. Time allocation: **80% planning and review, 20% work and compounding**.

### Plan
- Understand requirements and constraints
- Research codebase patterns and external best practices
- Design solutions and validate completeness

### Work
- Isolated branches via git worktrees
- Agent-assisted implementation
- Continuous validation (tests, lint, typecheck)

### Review
- **14+ specialized parallel reviewers** — security, performance, architecture, data integrity, quality, deployment, frontend
- P1/P2/P3 prioritization
- Pattern capture to prevent recurrence

### Compound (the key step)
- Document what worked and what didn't
- Update CLAUDE.md with patterns and agent instructions
- Create specialized agents when warranted
- Verify the system would catch similar issues automatically

## Five Adoption Stages

| Stage | Description | Key Shift |
|-------|-------------|-----------|
| 0 | Manual development | — |
| 1 | Chat-based assistance (copy-paste) | AI as reference |
| 2 | Agentic tools with line-by-line approval | AI as typist |
| 3 | Plan-first, humans review at PR level | **Compounding begins** |
| 4 | Idea-to-PR automation on single machine | AI as developer |
| 5 | Parallel cloud execution, multiple features | AI as team |

## Core Principles

1. **Every unit makes subsequent work easier** — code, docs, and tooling compound
2. **Taste belongs in automated systems**, not review gates
3. **Teaching the system pays exponential returns** vs typing solutions
4. **Build verification infrastructure** instead of manual review
5. **Structure projects so agents navigate autonomously**
6. **Apply compound thinking to all artifacts** — code, docs, tests, prompts
7. **Accept imperfect scalable results** over perfect non-scaling ones

## Beliefs to Adopt

- **Extract taste into systems** — document preferences in CLAUDE.md, create review agents, build custom skills
- **50/50 principle** — half of engineering time on features, half on system improvement
- **Trust with safety nets** — don't manually review every line; build guardrails that make outputs trustworthy
- **Agent-native environments** — agents access everything humans access
- **Plans as source of truth** — decisions captured before implementation prevent bugs

## Key Techniques

- **26 specialized agents** for different review/work roles
- **23 workflow commands** including the core loop
- **`/lfg` command** — full pipeline automation, plan through compound, 50+ agents
- **Vibe coding for discovery** — rapid prototyping, delete prototypes, then build properly
- **Three critical review questions**: What was hardest? What did you reject? What are you least confident about?

## Organizational Impact

Every Inc. runs five products with primarily **single-person engineering teams** using this system — demonstrating that compound engineering enables individual developers to manage complexity that traditionally required teams.

## Connections

- The compound loop directly implements what [[agent-flywheel]] calls the flywheel effect
- Review infrastructure parallels [[ralph-orchestrator]]'s quality gates and [[llm-as-verifier]]'s verification approach
- CLAUDE.md as institutional memory relates to [[agent-memory]]'s persistence and [[atlas]]'s learning extraction
- Plan-first approach aligns with [[agent-flywheel]]'s 85/15 planning-to-implementation ratio
- Parallel agent execution relates to [[autoloop]]'s parallel waves and [[gastown]]'s polecat swarms
- The optimization of agent instructions over time mirrors [[dspy]]'s automatic prompt optimization
