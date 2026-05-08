# Repository Guidelines

## Project Structure & Module Organization

Clankwork is a Go CLI and deterministic control-plane daemon. The main command lives in `cmd/clankwork`, with command handlers and CLI-facing tests beside it. Core implementation is under `internal/`, grouped by concern: `scheduler`, `store`, `worker`, `workflow`, `mergequeue`, `api`, and `config`. SQL migrations are embedded from `migrations/`; keep new files numbered sequentially. Built-in templates live in `internal/template/builtin/*.toml`, role prompts in `roles/`, docs in `docs/`, and e2e tests in `test/e2e/`.

## Build, Test, and Development Commands

- `make build`: builds `bin/clankwork` with the current git version injected.
- `make test`: runs `go test ./... -count=1 -race`; use before submitting behavior changes.
- `make lint`: runs `go vet ./...`.
- `make run`: builds, then starts the daemon via `bin/clankwork daemon`.
- `make install-acp-adapter`: installs the configured ACP adapter into `$CLANKWORK_HOME/bin`.
- `make clean`: removes `bin/`.

For focused loops, run package tests directly, for example `go test ./internal/scheduler -count=1`.

## Coding Style & Naming Conventions

Use standard Go formatting: run `gofmt` on edited `.go` files and keep imports `goimports`-compatible. Package names are short, lowercase, and responsibility-focused. Exported identifiers need useful doc comments when they form package API. Keep CLI command files named after the command area (`status.go`, `queue.go`) and tests named `*_test.go` beside the package they verify.

## Architecture Rules

Keep the control plane deterministic. Do not add LLM calls to daemon, scheduler, store, merge queue, or reconciliation code; agent intelligence belongs in role definitions and runtimes. Preserve the separation between roles (`roles/*.md`), workflow templates (`internal/template/builtin/*.toml` and compiled graphs), and runtime configuration. The CLI is the universal interface for humans and agents, so prefer extending `cmd/clankwork` over adding custom side protocols.

Acceptance testing is the verification layer that matters most. Changes that affect task completion, workflow routing, or agent behavior should keep executable acceptance evidence in mind, not just unit-test pass/fail status.

## Testing Guidelines

The project uses Go’s standard `testing` package. Prefer table-driven unit tests for deterministic controller, store, and workflow behavior. CLI/control-plane integration coverage appears in `cmd/clankwork/*_integration_test.go`; full e2e scenarios live under `test/e2e/`. New migrations, scheduler transitions, workflow graph behavior, and acceptance evidence paths should include regression tests.

## Completion Standard

Do not leave requested work half-done, stubbed, or parked behind TODOs. If a feature, fix, command, migration, template change, or test update is needed, implement it fully in the same change. Temporary scaffolding is acceptable only while actively working; remove it before completion.

## Commit & Pull Request Guidelines

Recent history uses concise imperative subjects, sometimes scoped with conventional prefixes such as `fix(scheduler): ...` or area prefixes like `acceptance: ...`. Keep commits focused and describe the observable change, not the implementation steps.

Pull requests should include a short problem/solution summary, tests run, and any schema, daemon lifecycle, or workflow-template impacts. Link related issues when available. Include screenshots only for terminal output changes where formatting matters.

## Security & Configuration Tips

The daemon writes runtime state under `$CLANKWORK_HOME` (`~/.clankwork` by default), including SQLite data, logs, sockets, and worktrees. Do not commit local runtime state, generated binaries, secrets, or agent logs. Treat role prompts and workflow templates as behavior-changing code and review them with the same care as Go changes.

## Agent Workflow Notes

Agents work in `clankwork/<task-id>` branches and must not commit directly to `master`. If changing the agent contract, update `test/e2e/reference-agent.sh` and run the relevant e2e coverage. Templates must stay portable across repositories: use `clankwork verify` rather than hardcoded project-specific commands.
