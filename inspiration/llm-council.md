# LLM Council (Gurisingh Post)

> Force multiple AI advisors with different thinking styles to argue about your question, peer-review each other anonymously, then synthesize a verdict — based on Karpathy's LLM Council method.

## Origin

Andrej Karpathy built the original LLM Council: poll multiple models (GPT, Claude, Gemini) on the same question, have them peer-review each other anonymously, then a chairman model synthesizes the final answer.

Gurisingh rebuilt it to work **entirely inside Claude Code** using sub-agents with different thinking styles instead of different models.

## The Problem It Solves

Claude is agreeable. Ask "should I launch this?" → 5 reasons yes. Ask "is this a bad idea?" → 5 reasons yes. Same question, different framing, opposite answers. Your assumptions, framing, and emotional lean shape the response. Fine for writing emails. Dangerous for decisions.

The council breaks this by forcing **5 different thinking styles** onto the same question — they can't all agree with you because they're not looking from your angle.

## The 5 Advisors

1. **The Contrarian** — assumes your idea has a fatal flaw, tries to find it. Catches "sounds great but..." gaps you skip when excited.

2. **The First Principles Thinker** — ignores your question, asks what you're actually trying to solve. Strips assumptions. Catches "optimizing the wrong variable" problems.

3. **The Expansionist** — hunts for upside you're missing. What could be bigger? What adjacent opportunity are you not seeing? Catches "thinking too small."

4. **The Outsider** — zero context about you or your field. Responds purely to what's in front of them. Catches curse-of-knowledge blind spots.

5. **The Executor** — only cares about: what do you do Monday morning? Catches brilliant plans with no path to actually doing them.

## The Peer Review (Key Innovation)

After all 5 advisors respond:
1. **Anonymize** everything — shuffle which advisor maps to which letter
2. **5 reviewers** read all responses and answer:
   - Which response is strongest and why?
   - Which has the biggest blind spot?
   - **What did all five miss?** ← most valuable question

> "Every time I've run the council, the peer review round catches something no individual advisor saw."

The gap between perspectives reveals what nobody thought to mention.

## Why This Matters for Agent Orchestration

This is a concrete pattern for **LLM-as-verifier through disagreement**:
- Multiple perspectives on the same input catch blind spots
- Anonymized peer review prevents anchoring bias
- Cross-review surfaces emergent insights that no single pass catches
- The chairman synthesis is a structured way to resolve disagreement

## Connections

- Directly implements [[llm-as-verifier]] — verification through multi-perspective disagreement rather than single-model checking
- [[agent-flywheel]]'s multi-model synthesis uses the same insight (different models surface different blind spots) but applies it to planning rather than decision-making
- [[dspy]]'s BestOfN and Ensemble modules are the programmatic equivalent — sample multiple candidates, rank them
- [[ralph-orchestrator]]'s Reviewer hat plays a similar role but as a single perspective, not a council
- [[compound-engineering]]'s 14+ parallel reviewers is this pattern applied to code review
- Could be systematized: [[atlas]]'s confidence routing could decide WHEN to invoke a council (high-uncertainty decisions)
