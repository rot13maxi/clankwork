# ATLAS (Adaptive Test-time Learning and Autonomous Specialization)

Self-hosted coding assistant that wraps a frozen local LLM in a multi-phase pipeline with energy-based scoring, self-verification, and a learned pattern cache. No fine-tuning, no cloud. Single consumer GPU.

## What It Is / What Problem It Solves

ATLAS addresses the gap between raw LLM code generation and production-quality output without relying on cloud APIs or model fine-tuning. The thesis: stop scaling models, start wrapping them in intelligent infrastructure. A frozen Qwen3.5-9B on a single RTX 5060 Ti hits 74.6% on LiveCodeBench pass@1, competitive with much larger models.

The system extracts constraints from the problem, generates diverse candidates, scores them with an energy-based lens, verifies via sandbox execution, and repairs failures through structured self-feedback. The whole thing runs as a Docker Compose stack with five services.

## Architecture

Two-layer design:

**Outer layer: atlas-proxy (Go)**
- Agent loop with grammar-constrained JSON output (tool_call / text / done)
- Tier classification: T0 (conversational), T1 (simple files), T2 (feature code), T3 (hard)
- Tool routing (8 tools: read_file, write_file, edit_file, run_command, etc.)
- Safety limits: conversation trimming at 12 messages, exploration budget (warns at 4 consecutive reads, blocks at 5), error loop breaker at 3 failures

**Inner layer: V3 pipeline (Python)**
- Activates for T2+ files (50+ lines with logic indicators)
- Four phases with early exits at every stage

### V3 Pipeline Phases

**Phase 0: Probe** - Generate single baseline, score with C(x)/G(x), test in sandbox. If it passes, done. This is the [[agent-flywheel]] fast path.

**Phase 1: Constraint-Driven Generation**
- PlanSearch: 3 structurally different plans
- DivSampling: 4 roles x 4 instruction styles x 4 code styles = 64 variant matrix
- BudgetForcing: Control thinking token allocation (nothink/light/standard/hard/extreme)
- Generate K candidates, build-verify each, sandbox test

**Phase 2: Verification and Selection**
- 2+ pass: S* Tiebreak (generate edge cases, run both, majority vote)
- 1 pass: Lens Select (lowest C(x) energy wins)
- 0 pass: proceed to Phase 3

**Phase 3: Repair**
- Failure analysis and categorization
- PR-CoT: 4 perspectives x (analysis + repair) = ~8 LLM calls, up to 3 rounds
- Refinement Loop: 2 iterations, 120s budget, cosine distance filtering to prevent hypothesis repetition
- Derivation Chains: decompose into up to 5 sub-problems, verify each, compose

### Service Map

| Service | Port | Role |
|---------|------|------|
| llama-server | 8080 | LLM inference (GPU) |
| atlas-proxy | 8090 | Agent loop, tool routing (Go) |
| v3-service | 8070 | V3 pipeline HTTP wrapper |
| geometric-lens | 8099 | Scoring, RAG, routing, pattern cache |
| sandbox | 30820 | Isolated code execution (7 languages) |

GPU used exclusively for inference and embedding extraction. Everything else on CPU.

## The Learning System

This is the most relevant part for Clankwork v3. ATLAS has three interconnected learning mechanisms:

### 1. Pattern Cache (the [[agent-memory]] system)

A Redis-backed short-term/long-term memory for code patterns. Three tiers:
- **STM (Short-Term Memory)**: max 100 patterns, fast decay
- **LTM (Long-Term Memory)**: promoted from STM after proving useful
- **Persistent**: never decays

**Write path** (after successful task completion):
1. LLM extracts a reusable pattern from the solution (pattern_extractor.py)
2. Surprise score computed from retry count (more retries = more surprising = more valuable)
3. Pattern classified by type: ERROR_FIX, API_PATTERN, BUG_FIX, ARCHITECTURAL, IDIOM
4. Storage score computed and pattern stored in Redis
5. Co-occurrence graph updated (Hebbian: patterns that succeed together link together)

**Read path** (before generation):
1. BM25 search against pattern summaries
2. Co-occurrence expansion: follow graph edges to find linked patterns (DFS, depth 1-2)
3. If BM25 matches are weak (< 0.3), fall back to direct LTM search
4. Score all candidates with Ebbinghaus decay: `similarity * 0.5^(days/half_life) * log(1 + access_count)`
5. Top-k patterns injected into system prompt as "Learned Patterns"

**Consolidation** (background):
- Promotion from STM to LTM requires: 3+ retrievals, 60%+ success rate, 3+ days old
- LTM pruning below score threshold of 0.01
- Category surprise tracked via exponential moving average

**Outcome recording**: After task completion, patterns that were injected get their success/failure counts updated. This creates a [[agent-flywheel]] where good patterns get reinforced and bad ones decay.

### 2. Geometric Lens (the scoring/[[llm-as-verifier]] system)

Energy-based code quality prediction without execution:

**C(x) Cost Field**: MLP (4096->512->128->1) trained on 597 LCB embeddings. Low energy = clusters with known-correct code. Val AUC 0.9467. Uses the model's own embeddings (self-embeddings from llama-server), not a separate embedding model.

**G(x) Metric Tensor**: PCA(4096->128) + XGBoost trained on 13,398 embeddings. Answers "which direction improves this candidate?" Diagonal tensor in PCA-reduced space.

**Correction engine**: Geometry-aware gradient steps: `-alpha * G^-1 * gradient(C)`, steering candidates downhill along the natural manifold curvature. This is the [[dspy]]-adjacent idea of optimizing in embedding space rather than prompt space.

Training uses:
- Contrastive ranking loss on C(x)
- EWC (Elastic Weight Consolidation) to prevent catastrophic forgetting
- Replay buffer: domain-stratified (30% old / 70% new)

### 3. Confidence Router (the [[compound-engineering]] routing system)

Thompson Sampling to pick the right generation strategy per query:

**Signal collection** (4 signals, all [0,1]):
- Pattern cache score (BM25 similarity weighted by tier)
- Retrieval confidence (PageIndex/BM25 result quality)
- Query complexity (token count, line count, code blocks - pure heuristic)
- Geometric energy (C(x) on the query itself)

**Difficulty estimation**: Weighted fusion of signals into D(x)

**Route selection**: Thompson Sampling with Beta posteriors per difficulty bin per route. Routes:
- CACHE_HIT: cost=1, retry budget k=0 (just use the cached pattern)
- FAST_PATH: cost=50, k=1
- STANDARD: cost=300, k=5
- HARD_PATH: cost=1500, k=20

Cost-weighted efficiency: `sample / route_cost`. With difficulty constraints (penalize FAST_PATH for hard tasks, HARD_PATH for easy tasks).

**Feedback**: Redis-backed. Route outcome (success/failure) updates Beta(alpha, beta) posteriors. Over time, the router learns which strategies work for which difficulty levels.

### Verify-Repair-Retry Loop

Closed-loop post-generation verification:
1. Extract code from LLM response
2. Score with G(x) + C(x) (single embedding call)
3. Run in sandbox
4. If passed + high G(x): return success
5. If no test code but G(x) >= 0.8 and no stderr: trust G(x) ("trusted_gx" verdict)
6. If failed + recoverable: build structured repair prompt from error analysis, regenerate
7. Repeat up to retry budget (set by confidence router)
8. Track best code (highest G(x)) across attempts as fallback

## Notable Features and Innovations

**Self-embeddings**: Uses the frozen LLM's own 4096-dim embeddings for scoring. No separate embedding model needed. The insight from ablation: self-embeddings restore C(x) discrimination by +39.5pp vs external embeddings. The model's internal representations are the best signal for whether its own output is correct.

**Surprise-based learning**: Patterns extracted from high-retry solutions (surprise_score based on retry count) get boosted in storage. The system learns most from its failures. This connects to the [[agent-flywheel]] idea that struggle = signal.

**Hebbian co-occurrence graph**: Patterns that co-activate in successful tasks strengthen their links. When you retrieve one pattern, you also get its neighbors via DFS traversal. This creates emergent "solution clusters" without explicit categorization.

**Ebbinghaus decay**: Memory follows a forgetting curve. Unused patterns decay. Frequently accessed patterns strengthen. This prevents unbounded memory growth and keeps the cache relevant.

**Early exit architecture**: Every pipeline phase can succeed and exit. Phase 0 (single probe) catches easy cases. The confidence router sets the retry budget. This means the system spends compute proportional to difficulty.

**Grammar-constrained output**: llama-server enforces JSON schema at the token level. The agent loop never sees malformed output. This is underrated, it eliminates an entire class of parsing failures.

## Strengths

**Closed-loop verification**: The verify-repair-retry loop with sandbox execution is the right architecture. Generate, verify, diagnose, repair. Each iteration has structured error analysis, not just "try again." The repair prompts include specific failure type, failure line, and severity.

**Adaptive compute allocation**: The confidence router + tier classification + early exits mean easy tasks get fast paths and hard tasks get heavy compute. This is [[compound-engineering]] done well.

**Learning from failures**: The pattern cache write path fires on successful completions, but surprise scores are weighted by retry count. Patterns born from struggle are valued more. Combined with outcome tracking (success/failure counts per pattern), the system evolves its memory toward what actually works.

**Self-contained**: Runs on a single consumer GPU. No cloud dependencies. The energy-based scoring uses the model's own embeddings. This is a complete, self-hosted [[agent-flywheel]].

**Principled scoring**: The geometric lens is grounded in real math (energy landscapes, metric tensors, Fisher information). Not just "ask the LLM if this looks right." The correction engine uses geometry-aware gradient descent, which is more principled than prompt-based self-refinement.

## Weaknesses

**Single-model bottleneck**: Everything runs through one frozen Qwen3.5-9B. The pipeline is smart about how it uses the model (DivSampling, BudgetForcing, multiple perspectives), but it's still the same model reasoning about its own output. Cross-model verification ([[llm-as-verifier]] with a different model) would catch blind spots.

**Competitive programming focus**: The 74.6% LCB benchmark is impressive, but LiveCodeBench is competitive programming problems. Real software engineering involves multi-file changes, API design, test writing, refactoring. The V3 pipeline is optimized for single-function generation, not system-level changes.

**No cross-project learning**: The pattern cache is per-project (Redis-backed but project-scoped). Patterns learned from Project A don't benefit Project B. A global pattern store with project-aware filtering would be more powerful. See [[agent-memory]] for how to do cross-context learning.

**Heavy pipeline for simple tasks**: Even with early exits, the infrastructure overhead (5 Docker services, Redis, sandbox) is significant for simple edits. The tier classification helps, but T1 tasks still route through atlas-proxy's agent loop with grammar enforcement.

**No human feedback integration**: The system learns from its own success/failure signals. There's no mechanism for a human to say "this pattern is wrong" or "this approach is better." The [[acp]] (Agent Communication Protocol) direction of human-agent collaboration is missing.

**Redis as memory store**: Pattern cache lives in Redis. No persistence guarantees unless Redis is configured for it. A crash loses all learned patterns. SQLite or a proper DB would be more durable for something this valuable.

**Ablation showed G(x) contributes zero at optimal settings**: Their own ablation study (v2.5.1) found the metric tensor (G(x)) adds no value when C(x) is working well. The correction engine using G(x) gradients may be doing nothing useful. This is honest reporting but raises questions about the complexity of the geometric lens.

## Relevance to Clankwork v3

The most transferable ideas:

1. **Pattern cache with Ebbinghaus decay and co-occurrence graphs** - This is a better [[agent-memory]] model than flat key-value stores. The forgetting curve, promotion gates, and Hebbian linking are all applicable to Clankwork's learning system.

2. **Confidence router with Thompson Sampling** - Adaptive compute allocation based on learned difficulty estimation. Clankwork v3 could use this to decide when to use expensive verification vs fast paths. Connects to [[autoloop]] budget management.

3. **Surprise-based learning** - Weight learnings by how much struggle they cost. High-retry-count solutions are more informative than first-try successes. This should inform how Clankwork captures and prioritizes learnings.

4. **Verify-repair-retry with structured error analysis** - Not just "try again" but "here's the specific failure type, here's the failure line, here's the severity, here's a targeted repair prompt." Connects to [[ralph-orchestrator]] diagnostic capabilities.

5. **Early exit architecture** - Every stage should be able to declare victory and stop. Don't run the full pipeline when Phase 0 suffices.

The geometric lens (energy-based scoring) is interesting but may be over-engineered for Clankwork's use case. Clankwork already uses [[llm-as-verifier]] which is simpler and works across models. The self-embeddings insight is worth noting though: a model's own internal representations are the best signal for its output quality.
