# DSPy

> Declarative framework for building modular AI software — replaces prompt engineering with programmatic optimization of LLM pipelines.

## What It Is

DSPy (Stanford NLP, 2022) treats AI programming like compiler design: you write high-level specifications, and the framework compiles them into optimized prompts and weights. The core insight is separating **interface** (what the model should do) from **implementation** (how to communicate that task).

## Key Concepts

### Signatures

Input-output contracts for AI components. Define what goes in and what comes out, with types:

```python
"question -> answer: float"
# or
class Classify(dspy.Signature):
    text: str = dspy.InputField()
    label: Literal["pos", "neg"] = dspy.OutputField()
```

Signatures are portable across models — change the model, keep the signature.

### Modules

Strategies for invoking LLMs with signatures:
- **Predict** — basic generation
- **ChainOfThought** — reasoning before output
- **ReAct** — reason, act, observe with external tools
- **Refine** — iterative improvement
- **BestOfN** — sample multiple candidates, rank them

Modules are **composable** — nest them to build multi-stage pipelines.

### Optimizers

Automatically tune prompts and weights given:
1. Training inputs
2. A metric measuring output quality
3. The program to optimize

Key optimizers:
- **MIPROv2** — proposes and searches better instructions (most powerful general-purpose)
- **BootstrapFewShot** — generates in-context examples from successful runs
- **BootstrapFinetune** — builds datasets from traces, finetunes weights
- **Ensemble** — aggregates predictions from multiple compiled programs

Typical cost: ~$2, ~20 minutes per optimization run.

### Adapters

Translate signatures into concrete prompts. Chat, XML, JSON, two-step formats available. Testable components with validation suites.

## The Programming Model

```
Define Signature → Select Module → Invoke → (Optional) Optimize
```

Multi-stage pipelines: each stage independently optimizable. As long as you can evaluate final output, DSPy tunes all intermediate modules.

## Why It Matters

| Aspect | Traditional Prompting | DSPy |
|--------|----------------------|------|
| Iteration | Manual tweaking | Automatic optimization |
| Portability | Prompts tied to models | Signatures portable |
| Maintenance | Scattered prompt strings | Centralized modules |
| Scaling | Trial-and-error | Algorithmic optimization |
| Composition | Ad-hoc concatenation | Principled modules |

**Performance gains**: 2-3x improvement without model upgrades (e.g., 24% → 51% on ReAct, 66% → 87% on classification).

## Connections

- DSPy's automatic prompt optimization is the programmatic version of what [[compound-engineering]] does manually via CLAUDE.md iteration
- The optimizer concept maps to [[atlas]]'s confidence router — both learn to allocate resources based on task difficulty
- Signatures as contracts relate to [[agent-flywheel]]'s bead specifications — well-defined interfaces enable predictable execution
- Module composition parallels [[ralph-orchestrator]]'s hat pipeline — modular stages with clear interfaces
- The "compile don't prompt" philosophy supports [[llm-as-verifier]]'s approach — systematic verification beats ad-hoc checking
- Metrics-driven optimization aligns with [[gastown]]'s attribution and A/B testing infrastructure
