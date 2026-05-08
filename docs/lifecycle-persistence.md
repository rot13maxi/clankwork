# Lifecycle Persistence Model

Clankwork separates mutable operational projections from durable evidence. This
keeps audit, replay, and retry behavior deterministic instead of relying on
in-place replacement semantics.

## Core Model

Tasks are mutable state-machine projections. Fields such as `status`,
`current_step`, `step_attempts`, current verdict, and open escalation count are
the current view of the workflow. They are useful for scheduling and status, but
they are not the audit source of truth.

Traces, controller observations, controller decisions, controller actuations,
escalations, artifact registrations, and lifecycle submissions are durable
history. Replay should be able to rebuild the task projection from these records.

Worktrees are mutable scratch space for agents. A worktree path can explain where
bytes came from, but it is not durable evidence. Anything used for acceptance
must be captured by the control plane.

## Acceptance Submissions

Acceptance specs, done bundles, verification reports, and verdicts should be
treated as versioned immutable submissions. The current single-row tables are
projections over the latest accepted submission and should evolve toward:

- `acceptance_spec_submissions`
- `done_bundle_submissions`
- `verification_report_submissions`
- `verification_verdicts`

Each submission should have:

- submission ID
- task ID
- step name
- attempt number
- idempotency key
- content hash
- validation status and errors
- created timestamp

The task-level "current spec", "current done bundle", and "current report" should
be pointers or projections, not replacements that erase prior submissions.

## Artifact Capture

Artifact registration captures bytes into control-plane-owned storage. The
registered artifact keeps the report-facing relative path, but hash validation
must read the captured copy, not the mutable worktree source file.

Rules:

- Artifact paths should be worktree-relative.
- Absolute paths are rejected for acceptance evidence.
- Registration verifies the submitted hash against the source file.
- Registration copies bytes into content-addressed control-plane storage.
- Later worktree mutations do not invalidate already-captured evidence.
- A new artifact registration is required for new bytes.

This resolves the artifact mutation failure mode: if an agent edits
`artifacts/report.txt` after registration, the registered evidence remains stable
because validation reads the captured copy.

## Retry And Idempotency

Retries create new attempts. They do not mutate old attempts.

Repeated submission with the same idempotency key and same content should return
the existing submission. Repeated submission with the same idempotency key and
different content should be rejected as an idempotency conflict.

Step retry/reset should clear stale current projections for later steps while
preserving history. For example, resetting to `acceptance` clears the current
verification report and verifier artifact projection, but the old submissions and
traces remain auditable.

## Escalations

Escalations are durable operator-attention records. Status and diagnose should
read open escalations as the source of truth for operator attention.

When an operator retries or resets the step that created a validation-loop
escalation, that escalation should be resolved because the operator has acted.
If the same problem recurs, the control plane should create a new escalation with
the new failure context.

## Ephemeral And Control Tasks

Dogfood controls, smoke tests, and acceptance fixtures are real audit records but
should not become permanent product backlog by accident. They should be marked as
ephemeral or closed after the expected observation is captured.

Recommended lifecycle:

- create with an explicit control/ephemeral marker
- record the expected outcome
- run through the normal control plane
- close as `expected_failure`, `superseded`, `obsolete`, or `manual_abandon`
- exclude closed control tasks from normal blocked/running status views
- keep traces, artifacts, and escalations for audit

This resolves leaked fixture failures: a validation-loop test task can prove the
loop behavior and then be closed without polluting operator workload.

## Implementation Phases

1. Keep current projections, but capture artifact bytes immutably at registration.
2. Add explicit close/archive semantics for stale control tasks.
3. Add idempotency keys to artifact and lifecycle submission APIs.
4. Introduce versioned submission tables alongside current projection tables.
5. Move status, diagnose, and acceptance show to read projections derived from
   immutable submissions.
6. Add replay tooling that rebuilds task projections from durable history.

## Invariant

Mutable state drives scheduling. Immutable history proves what happened.
