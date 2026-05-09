# Implementation Status

This page separates implemented behavior from design notes. If another document
sounds broader than this page, treat this page as the current status reference.

## Implemented

| Area | Status |
| --- | --- |
| Computed verification confidence | Implemented. `internal/model/acceptance.go` computes a deterministic score from probe evidence coverage, required artifact coverage, pass/fail state, retry count, and evidence-type diversity. The API stores `computed_confidence` and `confidence_label` with verification reports. |
| Artifact registry | Implemented. `clankwork artifact add` stores task-local artifacts with type, path, SHA-256, producer, step, command, working directory, exit code, and status. Verification reports must reference registered `artifact_id`s. |
| Artifact invalidation | Implemented. Artifact rows carry `status` and `invalidated_at`; reset paths clear affected artifacts. Reports citing invalidated artifacts are rejected. Automatic file-change invalidation is limited to control-plane artifact lifecycle paths, not a background filesystem watcher. |
| Prior-art FTS5 search | Implemented. `task_history_index` and `task_history_fts` index terminal or integration-relevant histories. Search blends FTS rank with `rework_score` and `risk_score`, and exposes CLI/API retrieval for planners. |
| Rework/risk scoring | Implemented as deterministic heuristics. Rework is derived from retries, failed verification, failure context, merge outcome, escalations, and failed/closed terminal states. Risk is derived from task text, changed files, negative assertions, failures, conflicts, and escalations. |
| Acceptance verification | Implemented for spec validation, done-bundle validation, report validation, artifact provenance, probe-level evidence coverage, computed confidence, adversarial follow-up, and smoke controls. |
| Compiled workflow graphs | Implemented. Templates compile to persisted routing graphs and graph policy can block invalid dispatch. |
| ACP runtime support | Implemented through `acp-adapter`, with persisted events, attach/watch/send/cancel surfaces, and permission decisions. |

## Partial

| Area | Current limitation |
| --- | --- |
| PID-like control plane | Partial by design. The implemented reconciler uses deterministic discrete control state, observations, error categories, escalation levels, failure signatures, and controller decisions. It is PID-inspired; it is not a numeric PID loop with continuous gains. |
| Queue-pressure adaptation | Queue depth can pause dispatch and is recorded for diagnostics. It does not yet reprioritize lower-risk/smaller tasks or dynamically tune verification. |
| Runtime escalation | Runtime entries support `escalate_after` and `escalate_to`, and failures are recorded. Project-local runtime policy in `clankwork.toml` is not implemented. |
| Prior-art planner integration | `plan create --with-prior-art` and explicit prior-art commands exist. Prior art is not injected into worker agents by default. |
| Config mutation | `clankwork config` and `config get` work. `config set` is a placeholder and prints "not yet implemented". |

## Not Implemented

- Official Docker image or checked-in Dockerfile.
- GitHub/Linear ticket ingestion.
- Rich web dashboard. A terminal TUI and tmux workspace are implemented, but there is no browser-based dashboard or multi-user operator console.
- Learned prompt optimization.
- Cryptographic artifact signing.
- Mutation testing against known-bad implementations.
- Multi-user/team authorization model.

