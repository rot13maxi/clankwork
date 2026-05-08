# Agent Memory

Persistent memory system for AI coding agents that eliminates re-explaining context across sessions through automatic capture, consolidation, and hybrid retrieval.

**Repo:** https://github.com/rohitg00/agentmemory
**Author:** Rohit Ghumare
**License:** Apache-2.0
**Scale:** ~21,800 LOC, 646 tests, 118 source files, 44 MCP tools, 104 REST endpoints

## What It Is / Problem It Solves

AI coding agents lose all context between sessions. Every new conversation starts from zero, forcing developers to re-explain project architecture, past decisions, coding preferences, and known bugs. Agent Memory sits as a sidecar daemon that silently captures observations from agent tool use, consolidates them into long-term memories, and injects relevant context at session start. The goal: your agent should already know what it learned yesterday.

This is the same problem [[atlas]] solves for Clankwork's workers, but Agent Memory tackles it as a general-purpose, agent-agnostic service rather than a tightly integrated learning loop.

## Key Concepts and Architecture

**Runtime model:** A standalone daemon (port 3111 for API, port 3113 for viewer) built on "iii-engine" (three primitives: HTTP triggers, KV state, streams, workers). Agents connect via MCP server, REST API, or Claude Code hooks. The daemon is long-lived and persists across agent sessions.

**Observation-first design:** The atomic unit is an "observation" -- a captured event from an agent's tool use (file edit, test run, command output). Observations carry an importance score (1-10), concepts, file paths, and session ID. Everything flows from observations upward through consolidation.

**34 KV scopes:** All state lives in a key-value store (SQLite locally, Postgres for scale). Scopes include observations, memories, sessions, graph nodes, graph edges, semantic memories, procedural memories, actions, leases, and more.

**Hook-based capture:** 12 lifecycle hooks inject into the agent runtime:
- SessionStart, SessionEnd
- PreToolUse, PostToolUse, PostToolFailure
- PromptSubmit, PreCompact, Stop
- SubagentStart, SubagentStop
- TaskCompleted, Notification

This is comparable to how [[autoloop]] instruments its agent runs, but Agent Memory hooks are designed as a protocol any agent can implement rather than an internal instrumentation layer.

## Memory Types and Storage Mechanisms

### 4-Tier Consolidation Pipeline

Inspired by human memory consolidation (the neuroscience is real -- this maps to working memory, episodic buffer, semantic store, and procedural learning):

1. **Working Memory** -- Raw observations from the current session. High volume, unfiltered. Equivalent to "what just happened."

2. **Episodic Memory** -- Session summaries. After a session ends, observations are compressed into a narrative of what the agent did, what worked, what failed.

3. **Semantic Memory** -- Facts extracted across sessions. "The auth middleware lives in src/middleware/auth.ts." "The team uses jose instead of jsonwebtoken for Edge compatibility." Requires minimum 5 session summaries before extraction triggers. Facts carry confidence scores and are strengthened on repeated access.

4. **Procedural Memory** -- Recurring behavioral patterns consolidated into executable procedures with trigger conditions and sequential steps. "When adding a new API endpoint, the agent always creates the route, adds validation, writes tests, then updates the OpenAPI spec." Requires minimum 2 pattern occurrences.

**Decay:** Exponential decay (`strength * 0.9^periods`) based on days since last access. Prevents stale memories from dominating retrieval. This is more principled than [[atlas]]'s current approach of capping learnings count.

### Auto-Forgetting (3 strategies)

- **TTL expiry** -- Memories with a `forgetAfter` timestamp are deleted when expired
- **Contradiction detection** -- Jaccard similarity > 0.9 on tokenized content flags the older memory as `isLatest: false` (soft delete, preserves history)
- **Importance-based eviction** -- Observations older than 180 days with importance <= 2 are purged

### Knowledge Graph

Entities (nodes) and relationships (edges) extracted from observations via LLM. Supports:
- Text search across node names and properties
- BFS traversal up to 5 levels deep
- Filtering by entity type
- Edges carry weight (0-1) and track source observation lineage

## How Agents Retrieve and Use Memories

### Triple-Stream Hybrid Search

Three retrieval pathways fused via Reciprocal Rank Fusion (RRF, k=60):

1. **BM25** -- Stemmed keyword matching with synonym expansion. Weight: 0.4
2. **Vector search** -- Cosine similarity over dense embeddings (supports local, OpenAI, Voyage, Cohere, OpenRouter). Weight: 0.6
3. **Knowledge graph** -- Entity-based traversal expanding top vector results through relationships. Weight: 0.3

Additional features:
- **Session diversification** -- Max 3 results per session to prevent one session dominating
- **Query expansion** -- Multiple reformulations and entity extractions before search
- **Optional LLM reranking** -- Top 20 results refined by a small model
- **Graceful degradation** -- Falls back to BM25 if vector/graph fails

**Benchmark:** 95.2% recall@5 on LongMemEval-S (500 questions), compared to 68.5% for mem0 and 83.2% for Letta.

### Context Injection

At session start, the system can inject relevant memories into the agent's context window. Configurable token budget (default 2000 tokens). The `enrich` endpoint provides file context + relevant memories + known bugs for the current working set.

## Notable Features and Innovations

**Multi-agent coordination via leases:** Distributed locking mechanism where agents acquire exclusive control of an action with TTL (default 10min, max 60min). Keyed mutexes prevent race conditions. Failed agents auto-release via cleanup. This is a simpler version of what [[ralph-orchestrator]] does with its task queue, but designed for peer agents rather than a hierarchical orchestrator.

**Mesh sync:** Peer-to-peer memory sharing across agent instances. Push/pull with last-write-wins conflict resolution. Bearer token auth, URL validation blocking private IPs, scope-based filtering. Enables a team of agents (or a team of developers' agents) to share learned knowledge.

**Privacy filtering:** 12 categories of secret patterns auto-redacted (API keys, bearer tokens, AWS credentials, GitHub PATs, JWTs, Slack tokens, etc.) plus explicit `<private>` tag support. Critical for a system that persists everything.

**Branch-aware memories:** Memories tagged with git branch context, so knowledge about a feature branch doesn't pollute main branch context.

**Obsidian export:** Can export the knowledge graph and memories to Obsidian-compatible markdown, bridging agent memory with human knowledge management.

**Token efficiency:** Claims ~170K tokens/year vs 650K+ for LLM-summarized approaches. The key insight: structured extraction and BM25+vector retrieval is cheaper than re-summarizing everything with an LLM each session.

**13+ agent compatibility:** Works with Claude Code, Cursor, Gemini CLI, Cline, Goose, Hermes, OpenClaw, and others. The MCP server approach means any agent that speaks MCP can use it.

## Strengths

**The 4-tier consolidation model is well-designed.** It mirrors actual cognitive science (working -> episodic -> semantic -> procedural) and each tier has clear promotion criteria. The exponential decay on semantic and procedural memories is a smart way to handle relevance without manual curation. This is more sophisticated than what most agent memory systems do (flat vector store + vibes).

**Triple-stream retrieval is the right call.** BM25 alone misses semantic similarity. Vectors alone miss exact keyword matches. The knowledge graph adds relationship-aware traversal. RRF fusion is a proven technique from information retrieval. The 95.2% recall@5 benchmark is strong evidence this works. Relevant to how [[dspy]] optimizes retrieval pipelines.

**The hook-based capture model is elegant.** 12 hooks covering the full agent lifecycle means observations are captured without the agent needing to explicitly "remember" things. This is zero-friction instrumentation. [[autoloop]] could benefit from a similar hook protocol for its agent runs.

**Multi-agent leases solve a real problem.** When multiple agents work on the same codebase (exactly what [[ralph-orchestrator]] coordinates), you need exclusive locks to prevent conflicts. The lease model with TTL and auto-cleanup is simple and correct.

**Privacy-first is non-negotiable and they got it right.** Any system that persists agent observations WILL capture secrets. The 12-category regex filter plus `<private>` tags is a solid baseline.

## Weaknesses

**Last-write-wins mesh sync is fragile.** For a system that claims multi-agent coordination, LWW conflict resolution loses data. If two agents learn contradictory things about the same entity, the later timestamp wins regardless of correctness. A [[compound-engineering]] approach would use [[llm-as-verifier]] to resolve conflicts semantically rather than by timestamp.

**No verification of memory correctness.** Memories are extracted by an LLM and stored as-is. There's no mechanism to verify that "the auth middleware is in src/middleware/auth.ts" is still true after a refactor. Stale facts that pass decay thresholds will poison context injection. An [[agent-flywheel]] needs a verification loop that re-checks stored facts against the actual codebase.

**The iii-engine dependency is a concern.** The entire system is built on iii-engine (iii-sdk), which is a relatively unknown runtime abstraction. This adds a layer of indirection that makes it harder to understand, debug, and extend. For something as foundational as agent memory, simpler infrastructure (plain SQLite + HTTP) would be more robust.

**Consolidation requires LLM calls.** Promoting observations to semantic/procedural memories goes through an LLM. This means consolidation quality depends on the model, adds latency, and costs money. A system like [[dspy]] could optimize these prompts automatically, but as-is, the consolidation prompts are hand-tuned.

**No feedback loop from memory usage.** The system tracks access counts but doesn't close the loop: did the injected memory actually help the agent? Did it lead to a correct action or a wrong one? Without this signal, you can't optimize what memories to surface. [[atlas]]'s approach of capturing verifier outcomes tied to specific learnings is more aligned with the [[agent-flywheel]] model.

**Contradiction detection is shallow.** Jaccard similarity at 0.9 threshold only catches near-duplicates. Real contradictions ("we use Postgres" vs "we use MySQL") have low token overlap and would never be flagged. Semantic contradiction detection requires embedding comparison or LLM judgment.

## Relevance to Clankwork v3

The 4-tier consolidation pipeline is the most directly applicable idea. Clankwork's [[atlas]] currently stores learnings as flat entries. Promoting observations through working -> episodic -> semantic -> procedural tiers would give workers progressively richer context as the system accumulates experience.

The triple-stream retrieval (BM25 + vector + graph) is worth adopting for learning retrieval. Currently [[atlas]] likely uses simpler matching. RRF fusion is well-proven and the benchmark results are compelling.

The lease model maps directly to [[ralph-orchestrator]]'s task assignment. Adding TTL-based leases on top of the existing task queue would handle worker failures more gracefully.

The mesh sync concept, despite its LWW weakness, points toward something Clankwork v3 needs: sharing learned knowledge across plan executions. When Plan A discovers "this API requires auth headers," Plan B working on a related feature should know that immediately. This connects to the [[agent-flywheel]] vision of continuously accumulating institutional knowledge.

The privacy filtering should be adopted wholesale. Any system that persists agent tool outputs will inevitably capture `.env` contents, API keys from curl commands, and database credentials from config files. The 12-pattern regex filter is a solid starting point.
