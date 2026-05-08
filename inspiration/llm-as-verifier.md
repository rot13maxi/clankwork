# LLM as a Verifier

> Using LLMs to verify and validate outputs rather than (just) generate them — verification is easier than generation.

## Core Thesis

The fundamental asymmetry: **verifying a solution is easier than generating one**. This is true for humans (checking a proof vs writing one) and it's true for LLMs. An LLM that struggles to generate correct code on the first try can reliably identify whether a given piece of code is correct.

This has profound implications for agent systems: instead of hoping for correct generation, you can generate multiple candidates and use LLMs (or other signals) to select the best one.

## Key Concepts

### Generation vs Verification

- **Generation**: Open-ended, many possible outputs, hard to get right on first try
- **Verification**: Constrained, binary or scored output, much higher accuracy
- The gap between generation and verification quality is the **exploitable margin**

### Verification Strategies

1. **Self-verification** — ask the same model to check its own output (weakest, but cheap)
2. **Cross-model verification** — use a different model to verify (stronger, avoids correlated errors)
3. **Multi-perspective verification** — multiple models/prompts verify independently, aggregate results (strongest, see [[llm-council]])
4. **Deterministic verification** — use tests, type checkers, linters as ground-truth verifiers (gold standard when available)
5. **Hybrid verification** — combine deterministic checks with LLM judgment for aspects that can't be mechanically verified

### The Verification Hierarchy

```
Deterministic (tests pass, types check)     ← strongest signal
  ↓
Cross-model LLM verification                ← catches subtle issues
  ↓  
Self-verification (same model reviews)      ← catches obvious issues
  ↓
No verification (trust first output)        ← weakest
```

### Generate-and-Verify Loop

The practical pattern:
1. Generate N candidates
2. Run deterministic verification (tests, lint, typecheck) — filter
3. Run LLM verification on survivors — rank
4. Select best candidate (or iterate)

This is essentially what [[ralph-orchestrator]]'s quality gates do: generate → verify → loop until gates pass.

## Why Verification Matters for Agent Systems

- **Agents generate lots of code** — most of it needs checking
- **Deterministic signals are cheap** — tests, types, lint cost nearly nothing
- **LLM verification fills the gap** — catches design issues, logic errors, security problems that tests miss
- **Verification compounds** — a good verification system gets better as you add more signals

## Connections

- [[ralph-orchestrator]]'s backpressure gates are deterministic verification (tests must pass before proceeding)
- [[compound-engineering]]'s 14+ specialized reviewers are LLM verification at scale
- [[llm-council]]'s peer review is multi-perspective LLM verification applied to decisions
- [[dspy]]'s metrics and optimizers systematize verification — define what "correct" means, optimize toward it
- [[atlas]]'s confidence router decides how much verification to apply based on estimated difficulty
- [[agent-flywheel]]'s "lie to them" technique (claim 80+ errors) is a hack to improve LLM verification thoroughness
- [[autoloop]]'s fail-closed architecture (only finalizer can emit task.complete) enforces verification as a structural requirement
- [[gastown]]'s attribution system enables verification at the organizational level — track which agents produce verified-correct work

## Note

The original Notion page (llm-as-a-verifier.notion.site) was inaccessible — this doc is synthesized from the concept and its connections to other inspiration sources. Would benefit from the original source content if available.
