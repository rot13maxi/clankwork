# Contributing

Clankwork is a Go CLI and deterministic control-plane daemon. Keep changes
small, observable, and backed by tests or executable evidence.

## Development Loop

```sh
make build
make test
make lint
```

For focused work, run package tests directly:

```sh
go test ./internal/scheduler -count=1
go test ./cmd/clankwork -count=1
```

Before submitting behavior changes, run `make test`. It executes
`go test ./... -count=1 -race`.

## Code Style

- Run `gofmt` on edited Go files.
- Keep package names short, lowercase, and responsibility-focused.
- Add exported doc comments when introducing package API.
- Keep command handlers in `cmd/clankwork` and tests beside the package they
  verify.
- Do not add LLM calls to daemon, scheduler, store, merge queue,
  reconciliation, or API control logic.

## Tests And Evidence

Use table-driven unit tests for deterministic behavior. Add integration coverage
for CLI/API/control-plane flows and e2e coverage under `test/e2e/` when a change
affects task completion, workflow routing, agent contracts, or acceptance
evidence.

Acceptance-related changes should preserve executable evidence paths:

```sh
clankwork acceptance validate-spec artifacts/acceptance-spec.json
clankwork acceptance validate-report --spec artifacts/acceptance-spec.json artifacts/verification-report.json
clankwork acceptance smoke --repo <repo-id> --runtime default --case all --wait
```

## Migrations

SQLite migrations live in `migrations/` and are embedded. Never edit a migration
that may already have been applied. Add the next sequential
`NNNN_description.sql` file and include regression tests for store/API behavior
that depends on the new schema.

See [docs/migration-guide.md](docs/migration-guide.md).

## Roles And Templates

Treat `roles/*.md` and `internal/template/builtin/*.toml` as behavior-changing
code. If a role or workflow contract changes, update
`test/e2e/reference-agent.sh` when needed and run the relevant e2e coverage.

Templates must stay portable across repositories. Prefer `clankwork verify`,
`clankwork verify lint`, and `clankwork verify typecheck` over
project-specific hardcoded commands in built-in workflows.

## Pull Requests

Use focused commits with concise imperative subjects. PR descriptions should
include:

- Problem and solution summary.
- Tests run.
- Schema, daemon lifecycle, runtime, or workflow-template impact.
- Any acceptance evidence or smoke commands used.

