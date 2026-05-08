# Agent Client Protocol (ACP)

> Standardized communication between code editors/IDEs and coding agents — the LSP of AI agents.

## What It Is

ACP standardizes how code editors talk to coding agents, inspired by how the Language Server Protocol (LSP) standardized editor-language server communication. Currently "each editor must build custom integrations for every agent" — ACP eliminates that with a shared protocol.

## Architecture

**Local agents**: Operate as subprocesses, communicate via **JSON-RPC over stdio**.

**Remote agents**: Run in cloud/separate infrastructure via **HTTP or WebSocket** (still in development).

## Design Principles

- Users stay primarily within their editor while invoking agents
- Reuses JSON representations from MCP where applicable
- Custom types for agentic features (diff display, etc.)
- Text defaults to Markdown for rich formatting without requiring HTML rendering
- Decouples agents from editors — both innovate independently

## The Alternative: tmux send-keys

[[gastown]] takes a fundamentally different approach: run agents in tmux sessions and use `tmux send-keys` to communicate between them.

### Comparison

| Aspect | ACP | tmux send-keys |
|--------|-----|----------------|
| **Complexity** | Formal protocol, typed messages | Shell commands, text-based |
| **Debugging** | Need protocol-aware tools | `tmux capture-pane`, plain text |
| **Structure** | JSON-RPC, typed schemas | Unstructured text |
| **Reliability** | Protocol guarantees | Best-effort, timing-dependent |
| **Setup** | SDK integration | `tmux new-session` + `send-keys` |
| **Flexibility** | Constrained to protocol | Anything you can type |
| **Agent coupling** | Needs ACP-aware agents | Works with any CLI agent |

### Trade-offs

**ACP strengths**: Type safety, discoverability, IDE integration, proper error handling. Good when you control both sides and want reliability.

**tmux strengths**: Zero integration cost, works with any agent that has a CLI, trivially debuggable, no protocol overhead. Good when you want pragmatic coordination between heterogeneous agents.

The tmux approach trades correctness guarantees for universality — any agent that can read stdin and write stdout can participate, regardless of whether it implements a protocol.

## Connections

- [[gastown]]'s communication model is the primary example of the tmux alternative
- [[autoloop]]'s topology-as-TOML defines communication patterns that could run over either ACP or tmux
- [[ralph-orchestrator]]'s event-driven hat coordination is essentially an internal protocol — could be externalized via ACP
- [[agent-flywheel]]'s Agent Mail is a custom coordination protocol that sits between ACP's formality and tmux's simplicity
- The protocol question matters for [[compound-engineering]]'s parallel agent execution — how do 14+ reviewers coordinate?
