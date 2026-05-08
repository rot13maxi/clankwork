# Acceptance & Verification System

Clankwork treats completion as evidence, not as a message from an agent.

The control plane enforces this flow:

```text
Plan -> Acceptance Spec -> Implementation -> Done Bundle -> Verification Report -> Verdict
```

Agents propose completion. The system proves completion by executing an acceptance specification and recording artifacts that can be replayed or inspected later.

For the implementation spec covering spec strength validation, artifact provenance, probe-level evidence coverage, computed confidence, safe learning gates, and adversarial checks, see [Hardened Acceptance Verification Spec](hardened-acceptance-verification.md).

## Roles

| Role | Responsibility |
| --- | --- |
| Planning agent | Defines requirements and acceptance criteria |
| Acceptance author | Converts criteria into an executable acceptance spec |
| Worker agent | Implements the task and submits a done bundle |
| Acceptance verifier | Executes the spec and submits evidence |
| Control plane | Stores artifacts, validates invariants, and decides task completion |

## Acceptance Spec

The `feature` workflow now starts with an `acceptance_spec` agent step. That agent submits a spec with:

```sh
clankwork signal done --spec artifacts/acceptance-spec.json
```

### Acceptance-spec author boundary

The acceptance-spec author is not the implementer. The `acceptance_spec` step's only output is `artifacts/acceptance-spec.json`. It must **not**:

- Edit source files or implement the task.
- Run mutable integration tests against the shared daemon.
- Commit code changes.

Implementation begins only after the control plane accepts the spec and dispatches the `implement` step.

Format:

```json
{
  "task_id": "01...",
  "criteria": [
    {
      "id": "C1",
      "description": "User can reset password",
      "probes": [
        {
          "id": "reset_old_password_rejected",
          "description": "Verify reset changes login state and the old password no longer works",
          "command": "go test ./internal/api -run TestPasswordResetFlow",
          "required_evidence": ["db_assertion", "cli_transcript"],
          "before": "user can log in with old password and has no active reset token",
          "after": "user can log in with new password and old password is rejected",
          "observable_side_effect": "db_assertion and cli_transcript record the changed login state",
          "negative_assertion": "login with the old password fails after reset"
        }
      ],
      "required_artifacts": [
        "playwright_trace",
        "email_log",
        "db_assertion",
        "cli_transcript"
      ],
      "fail_if": [
        "no_email_sent",
        "login_succeeds_with_old_password",
        "reset_link_invalid"
      ]
    }
  ]
}
```

Specs must be executable, observable, adversarial, and replayable. Each criterion needs stable `probe.id` values, before/after state checks, observable side effects, required artifact types, per-probe `required_evidence`, and explicit failure conditions. Status-only probes such as "check status" or prose-only inspection are rejected.

The control plane computes an effective risk level while validating the spec. Agent-provided `risk_level: "normal"` cannot downgrade auth, payments, permissions, data deletion, migrations, infra/IAM, public API contract work, or paths/labels configured under `[acceptance.risk]`.

## Done Bundle

Worker completion must include a structured done bundle:

```sh
clankwork signal done --bundle artifacts/done-bundle.json
```

Format:

```json
{
  "task_id": "01...",
  "summary": "Implemented password reset through the CLI and API",
  "files_changed": ["internal/api/password_reset.go"],
  "tests_run": ["go test ./internal/api"],
  "claims": [
    {
      "criterion_id": "C1",
      "status": "satisfied"
    }
  ],
  "artifacts": [
    {
      "type": "test_output",
      "path": "artifacts/unit-tests.txt",
      "probe_id": "reset_old_password_rejected",
      "producer_step": "implement",
      "producer_role": "worker",
      "timestamp": "2026-05-04T20:00:00Z",
      "content_hash": "sha256:...",
      "authoritative": true
    },
    {
      "type": "cli_transcript",
      "path": "artifacts/reset-flow.txt",
      "probe_id": "reset_old_password_rejected",
      "producer_step": "implement",
      "producer_role": "worker",
      "timestamp": "2026-05-04T20:00:00Z",
      "content_hash": "sha256:...",
      "authoritative": true
    }
  ],
  "known_risks": []
}
```

The control plane rejects implementation completion when:

- the bundle is missing
- a claim references a criterion outside the spec
- a claim is not `satisfied`
- required artifact types from the spec are missing
- artifacts have no type or path
- artifacts have no authoritative provenance (`producer_step`, `producer_role`, `timestamp`, `authoritative: true`, and `probe_id` or `command`)
- path artifacts have no `content_hash`

## Verification Report

The acceptance verifier executes the spec and submits evidence:

```sh
clankwork signal done --report artifacts/verification-report.json
```

Every evidence artifact must be registered before the report is submitted:

```sh
clankwork artifact add \
  --type cli_transcript \
  --path artifacts/reset-flow.txt \
  --producer acceptance-verifier \
  --command "go test ./internal/api -run TestPasswordResetFlow" \
  --exit-code 0
```

The command returns an `artifact_id`. Reports must reference that ID; path/hash metadata alone is not enough.

Format:

```json
{
  "task_id": "01...",
  "results": [
    {
      "criterion_id": "C1",
      "status": "pass",
      "evidence": [
        {
          "artifact_id": "artifact_001",
          "type": "playwright_trace",
          "path": "artifacts/reset.trace.zip",
          "probe_id": "reset_old_password_rejected",
          "producer_step": "acceptance",
          "producer_role": "verifier",
          "timestamp": "2026-05-04T20:00:00Z",
          "content_hash": "sha256:...",
          "authoritative": true
        },
        {
          "artifact_id": "artifact_002",
          "type": "db_assertion",
          "probe_id": "reset_old_password_rejected",
          "query": "select count(*) from password_resets where used_at is not null",
          "result": 1,
          "producer_step": "acceptance",
          "producer_role": "verifier",
          "timestamp": "2026-05-04T20:00:00Z",
          "authoritative": true
        },
        {
          "artifact_id": "artifact_003",
          "type": "cli_transcript",
          "path": "artifacts/login.txt",
          "probe_id": "reset_old_password_rejected",
          "producer_step": "acceptance",
          "producer_role": "verifier",
          "timestamp": "2026-05-04T20:00:00Z",
          "content_hash": "sha256:...",
          "authoritative": true
        }
      ],
      "reason": "Observed full reset flow and validation"
    }
  ],
  "failures": [],
  "confidence": "high"
}
```

The verifier does not decide correctness by opinion. It records observable evidence. The control plane computes the verdict:

- all criteria present
- all results pass
- no failures listed
- each passing criterion has evidence
- each required artifact type appears in the criterion evidence
- each probe has at least one linked evidence item via `probe_id`
- each evidence item has authoritative provenance (`producer_step`, `producer_role`, `timestamp`, `authoritative: true`, and `probe_id` or `command`)
- every evidence `artifact_id` exists in the registry, belongs to the same task, has the expected type/path/hash, and has not been invalidated
- verifier-required evidence comes from a verifier/control-plane producer role, not from a worker artifact

If the report fails structurally, `signal done` is rejected. If the report is valid but contains failed criteria, the control plane routes the task back to implementation with the failure evidence.

`clankwork acceptance validate-report artifacts/verification-report.json` performs the same structural, provenance, hash, coverage, and computed-confidence checks without transitioning the task.

For deterministic probes, the verifier can use an execution plan:

```sh
clankwork acceptance validate-plan --spec artifacts/acceptance-spec.json artifacts/verification-plan.json
clankwork acceptance run-plan --spec artifacts/acceptance-spec.json artifacts/verification-plan.json --out artifacts/verification-report.json
```

The initial runner supports `shell`, `http`, `playwright`, `db_query`, and `file_assertion` steps. It captures stdout/stderr or response/query/file output as artifacts, registers them, and writes a probe-mapped verification report.

## Evidence Model

Evidence should be hard or expensive to fake. Supported artifact types are strings so repos can extend them, but common types include:

- `playwright_trace`
- `screenshot`
- `video`
- `cli_transcript`
- `api_log`
- `db_assertion`
- `db_diff`
- `file_output`
- `test_output`
- `structured_test_result`

If a model can pass acceptance by hardcoding plausible text, the spec is too weak.

Worker artifacts are claims. Verifier and control-plane artifacts are evidence. If a registered artifact file changes after registration, the control plane invalidates it and rejects reports that cite it.

## Invariants

- No graph dispatch when template compilation violates workflow policy.
- No implementation completion without a done bundle.
- No acceptance spec without executable probes and required artifacts.
- No claim without artifacts.
- No acceptance without a verification report.
- No passing verification without evidence.
- No terminal success trace that bypasses the compiled workflow graph's required route.
- Acceptance must be replayable.
- The control plane is the only authority on completion.

## Failure Handling

When verification finds a real behavior failure, the acceptance verifier should submit a report with failed results instead of prose-only feedback. Clankwork records the report, computes a failing verdict, and routes the task back to the worker with concrete failure context such as:

```text
criterion C1 failed: login still works with the old password after reset
```

This gives the implementer the failing criterion, the execution trace, and observable evidence rather than a vague judgment.

## Diagnosing ACP Runtime Startup Failures

When `clankwork task diagnose <task-id>` reports `inspect_runtime` as the next action for an ACP dispatch failure, the runtime failed to start before the agent could begin work. Run:

```sh
clankwork acp doctor --runtime <name> --handshake
```

This performs a connectivity handshake with the named runtime and reports what failed. The most common cause is a missing provider CLI binary (e.g., `pi` is not installed or not on the daemon's PATH). This is an environment issue — fix it by adding the binary to the daemon/runtime PATH or by choosing a working runtime such as `claude-acp`.

Retry the task only after `acp doctor` reports a clean handshake.

## Exercising the Default Pi Runtime Before Trusting Acceptance

Before treating the acceptance pipeline as healthy, exercise the default Pi runtime with a repo-backed task — a small, deterministic change (such as a documentation edit) dispatched through the full feature workflow from implementation to acceptance verification. This end-to-end smoke test confirms that the Pi binary is reachable from the daemon's PATH, the ACP handshake completes, and an agent can drive a worktree, signal progress, and produce artifacts that the verifier can consume. Skip this step and you may miss environment misconfigurations that only surface under real agent load.

Use the built-in smoke controls to make this repeatable:

```sh
clankwork acceptance smoke --repo <repo-id> --runtime default --case all --wait
```

The smoke cases cover the positive pass/merge path, structurally valid failing verification that routes back to implementation, invalid done-bundle rejection, and invalid verification-report rejection. Negative controls are parked as blocked tasks after the expected observation is captured so they do not self-heal and merge.

For graph-level negative controls, use a temporary repo or home template that is structurally valid TOML but violates compiled graph policy, such as an `acceptance_spec` step that routes directly to `complete`. The expected observation is `graph_compilation` = `failed` and a `graph_compilation_failure` decision with `block_dispatch`; no worker should start. Trace-level conformance probes should load the persisted compiled graph and verify that impossible routes, missing targets, or terminal completion without a success route produce `valid: false`.

## Computed Verification Confidence

The `confidence` field in a verification report is agent-provided and decorative — it is not trusted by the control plane. Instead, the control plane computes a **deterministic confidence score** (0.0–1.0) at the time the report is submitted.

### Formula

The score is a weighted combination of five signals:

| Signal | Weight | Description |
| --- | --- | --- |
| Evidence coverage | 35% | Fraction of spec probes with linked evidence items by `probe_id` |
| Artifact coverage | 30% | Fraction of required artifact types from the spec that appear as evidence |
| Failure score | 15% | Fraction of criteria that passed (not failed) |
| Retry penalty | 10% | Decreases linearly from 1.0 (retry=0) to 0.0 (retry>=3) |
| Diversity bonus | 10% | Distinct evidence types across results, capped at 4 |

### Labels

The computed score is mapped to a label for routing decisions:

| Label | Threshold | Routing |
| --- | --- | --- |
| **high** | >= 0.85 | Required for high-risk tasks |
| **medium** | >= 0.65 | Sufficient for normal-risk tasks; logged with caution |
| **low** | < 0.65 | Soft failure — route back to implementation or escalate |

High-risk tasks also require an adversarial review. A required high-severity follow-up blocks completion until it is dismissed by policy or produces registered follow-up evidence. Suggested adversarial probes may be appended to the stored acceptance spec and executed on the next acceptance pass.

### Storage

The computed confidence and its label are stored alongside the verification report:
- `computed_confidence` (REAL): the 0.0–1.0 score
- `confidence_label` (TEXT): "low", "medium", or "high"

The agent-provided `confidence` string field is preserved for reference but is never used in routing decisions.

`clankwork acceptance show <task-id>` displays both values distinctly as agent confidence and computed confidence so operators can tell which value is decorative and which value was used for routing.

## Prior-Art Indexing

Acceptance and verification outcomes are indexed as prior art after terminal or integration-relevant task states. The index captures merged work, failed probes, verifier failures, retry history, merge conflicts, rejected merge items, and evidence artifacts so planners can search for similar histories before creating new task DAGs or acceptance specs.

Prior art is planner context only. It may suggest stricter criteria, negative assertions, or required probes for a future task, but it never relaxes validation and never lets a new task reuse old evidence.

## Future Extensions

- Automatic acceptance spec compilation from plans.
- Mutation testing against known-bad implementations.
- Rich review workflow for curating prior-art summaries.
- Cryptographic artifact signing and identity-backed provenance.
