# Clankwork v3 — Inspiration Sources

Research notes for building an automated software factory that continuously learns and improves.

## Sources

| Source | Type | Key Idea |
|--------|------|----------|
| [[gastown]] | Orchestration platform | Agent attribution, role taxonomy, tmux-based communication |
| [[autoloop]] | Loop harness | Topology-as-TOML, fail-closed verification, parallel waves |
| [[ralph-orchestrator]] | Agent framework | Specialized hats, backpressure quality gates, event-driven coordination |
| [[atlas]] | Learning system | Pattern cache with decay, confidence routing, surprise-based learning |
| [[compound-engineering]] | Methodology | Plan→Work→Review→Compound loop, 50/50 system improvement, taste extraction |
| [[dspy]] | Optimization framework | Signatures, modules, automatic prompt optimization, metrics-driven |
| [[llm-as-verifier]] | Verification concept | Verification easier than generation, hybrid verification hierarchy |
| [[llm-council]] | Decision pattern | Multi-perspective advisors, anonymous peer review, disagreement as signal |
| [[agent-memory]] | Memory system | 4-tier consolidation, triple-stream retrieval, Ebbinghaus decay |
| [[acp]] | Communication protocol | ACP formal protocol vs tmux pragmatic approach |
| [[agent-flywheel]] | Methodology | Exhaustive planning leverage, beads task graph, fungible agents |

## Cross-Cutting Themes

### 1. The Compound Loop
Every system that works well has some version of: do work → extract learnings → feed learnings back → do better work. See [[compound-engineering]], [[agent-flywheel]], [[atlas]].

### 2. Verification > Generation
Don't trust first outputs. Layer deterministic checks (tests, types) with LLM verification (reviews, councils). See [[llm-as-verifier]], [[llm-council]], [[ralph-orchestrator]], [[compound-engineering]].

### 3. Planning Leverage
Investment in planning pays exponential returns. 85/15 or 80/20 planning-to-implementation. See [[agent-flywheel]], [[compound-engineering]].

### 4. Memory That Decays
Not all learnings are permanent. Ebbinghaus decay, TTL expiry, promotion gates. See [[atlas]], [[agent-memory]], [[agent-flywheel]].

### 5. Agent Communication
Spectrum from formal protocols (ACP, JSON-RPC) to pragmatic approaches (tmux send-keys, file-based). See [[acp]], [[gastown]], [[agent-flywheel]].

### 6. Fungible vs Specialized Agents
Tension between generalist agents (any can do anything, [[agent-flywheel]]) and specialized roles (hats, [[ralph-orchestrator]]). Both work. The question is when to specialize.
