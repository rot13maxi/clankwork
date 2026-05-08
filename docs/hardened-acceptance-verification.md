# Clankwork - Hardened Acceptance Verification Spec

## Goal

Strengthen the acceptance verification pipeline so completion cannot be achieved through weak specs, fake artifacts, skipped probes, verifier drift, or poisoned learnings.

Core invariant:

> Agents may propose completion; only the control plane can recognize completion.

This change hardens the existing artifact pipeline:

```text
Plan -> Acceptance Spec -> Implementation -> Done Bundle -> Verification Report -> Verdict
```

by adding:

1. Acceptance spec strength validation
2. Artifact provenance tracking
3. Probe-level evidence coverage
4. Semi-deterministic verifier execution
5. Computed verification confidence
6. Safe learning eligibility gates
7. Optional adversarial/counterfactual checks

Implementation should prioritize hard rejection of weak specs and incomplete evidence before adding generic execution machinery. The first useful milestone is a stricter control-plane verdict over already-submitted artifacts, not a fully generic verifier runner.

## 1. Acceptance Spec Strength Validation

### Problem

A structurally valid spec can still be weak.

Example of bad spec:

```json
{
  "criterion_id": "C1",
  "probes": ["call_api"],
  "required_artifacts": ["api_response"],
  "fail_if": []
}
```

This is technically executable but too shallow. It lets bad implementations pass.

### Requirement

The control plane must validate acceptance specs for minimum strength before accepting:

```bash
clankwork signal done --spec artifacts/acceptance-spec.json
```

If the spec fails strength validation, reject the step and return structured feedback to the acceptance author.

Risk classification is authoritative only after control-plane validation. The acceptance author may propose `risk_level`, but the control plane must compute or raise the effective risk level from deterministic task metadata, changed files, touched subsystems, labels, or configured path/domain rules. A user- or agent-supplied `normal` risk value cannot downgrade auth, payments, permissions, data deletion, migrations, infra/IAM, or public API contract work.

### Required Checks

For each criterion:

- Must have at least one probe.
- Must have at least one required artifact type.
- Must have at least one `fail_if` condition.
- Must include at least one observable side effect when applicable.
- Must include at least one negative assertion when applicable.
- Must avoid pure "status code only" validation.
- Must map each probe to required evidence.

Applicability must be deterministic. Do not rely on a verifier's subjective judgment of "when applicable." Start with explicit fields and policy-derived requirements:

```json
{
  "requires_state_transition": true,
  "requires_negative_assertion": true,
  "risk_level": "normal"
}
```

The control plane may also require these fields based on configured task domains. For example, auth and permissions tasks require negative assertions; migrations and data deletion tasks require state transition or data integrity checks.

### Recommended Schema Extension

```json
{
  "criteria": [
    {
      "id": "C1",
      "description": "User can reset password",
      "probes": [
        {
          "id": "P1",
          "description": "Seed user with known password",
          "type": "setup",
          "required_evidence": ["db_assertion"]
        },
        {
          "id": "P2",
          "description": "Request password reset",
          "type": "action",
          "required_evidence": ["api_response", "email_log"]
        },
        {
          "id": "P3",
          "description": "Confirm old password no longer works",
          "type": "negative_assertion",
          "required_evidence": ["api_response", "cli_transcript"]
        }
      ],
      "required_artifacts": [
        "api_response",
        "email_log",
        "db_assertion",
        "cli_transcript"
      ],
      "fail_if": [
        "reset email is not sent",
        "old password still works",
        "new password login fails"
      ],
      "risk_level": "normal",
      "requires_state_transition": true,
      "requires_negative_assertion": true
    }
  ]
}
```

### Spec Strength Heuristic

Implement a deterministic validator that assigns a score.

Example:

```text
+1 has probes
+1 has required artifacts
+1 has fail_if
+1 has negative assertion
+1 has state transition check
+1 has independent evidence source
-2 only checks status code
-2 no probe/evidence mapping
-3 no failure conditions
```

Minimum score:

- normal task: >= 3
- high-risk task: >= 5

High-risk domains include:

- auth
- payments
- permissions
- data deletion
- migrations
- infra/IAM
- public API contracts

Suggested policy config:

```toml
[acceptance.risk]
high_risk_labels = ["auth", "payments", "permissions", "data-deletion", "migration", "infra", "iam", "public-api"]
high_risk_paths = [
  "internal/auth/**",
  "internal/billing/**",
  "migrations/**",
  "infra/**"
]
```

If any configured rule matches, the effective risk level is at least `high`.

## 2. Artifact Provenance

### Problem

Artifacts can be spoofed.

A worker can write:

```text
artifacts/login-success.txt
```

without actually running the login flow.

### Requirement

All artifacts must have provenance metadata.

The control plane must distinguish:

- worker-produced artifacts
- verifier-produced artifacts
- deterministic command artifacts
- control-plane-generated artifacts

Verifier artifacts are authoritative. Worker artifacts are advisory unless independently verified.

Command provenance is evidence only when the command is executed or wrapped by the control plane or a trusted verifier harness. Agent-reported command strings are metadata, not proof of execution.

### Artifact Metadata

Add an artifact registry table or structured trace record.

```json
{
  "artifact_id": "artifact_123",
  "task_id": "task_456",
  "step_id": "acceptance",
  "producer": "acceptance-verifier",
  "producer_type": "agent",
  "path": "artifacts/reset-password.trace.zip",
  "artifact_type": "playwright_trace",
  "created_at": "2026-05-06T12:34:56Z",
  "sha256": "...",
  "command": "npx playwright test reset-password.spec.ts",
  "working_directory": "...",
  "exit_code": 0
}
```

### Rules

- Every referenced artifact must exist in the artifact registry.
- Every registered artifact must have a hash.
- Every verifier artifact must include producer step metadata.
- Worker artifacts cannot satisfy verifier-required evidence in the initial implementation.
- A later version may allow worker artifacts to be explicitly marked reusable, but only after hash validation and independent verifier/control-plane checks.
- If a report references an unregistered artifact, reject the report.
- If a file hash changes after registration, invalidate the artifact.

### Invariant

> Worker artifacts are claims. Verifier artifacts are evidence.

## 3. Probe-Level Evidence Coverage

### Problem

A verifier might produce some evidence, but not evidence for every probe.

Example:

- Spec has 7 probes.
- Verifier runs 3.
- Report includes all required artifact types.
- Current validation may pass even though coverage is incomplete.

### Requirement

Verification reports must map evidence to individual probes.

### Verification Report Extension

```json
{
  "task_id": "task_456",
  "criteria": [
    {
      "criterion_id": "C1",
      "status": "pass",
      "probes": [
        {
          "probe_id": "P1",
          "status": "pass",
          "evidence": [
            {
              "artifact_id": "artifact_001",
              "type": "db_assertion"
            }
          ]
        },
        {
          "probe_id": "P2",
          "status": "pass",
          "evidence": [
            {
              "artifact_id": "artifact_002",
              "type": "api_response"
            },
            {
              "artifact_id": "artifact_003",
              "type": "email_log"
            }
          ]
        }
      ]
    }
  ]
}
```

### Validation Rules

For every criterion in the acceptance spec:

```text
criterion must appear in report
every probe must appear in report
every probe must have status
every passing probe must have evidence
evidence type must satisfy required_evidence
artifact IDs must resolve to registered artifacts
```

A criterion may only pass if all required probes pass.

A report may only pass if all required criteria pass.

## 4. Semi-Deterministic Verifier Execution

### Problem

The verifier is still an LLM agent. It can skip steps, misread probes, or invent execution.

### Requirement

Acceptance verification should be driven by an execution plan derived from the acceptance spec.

The agent may orchestrate and interpret, but it should not freely invent what to test.

### Design

Add an intermediate execution plan:

```text
Acceptance Spec -> Verification Execution Plan -> Execution -> Verification Report
```

The execution plan may be generated by:

- deterministic compiler
- acceptance verifier bootstrap
- acceptance author
- project-specific adapter

### Execution Plan Format

```json
{
  "task_id": "task_456",
  "steps": [
    {
      "id": "E1",
      "probe_id": "P1",
      "type": "shell",
      "command": "bun run seed:user --email test@example.com",
      "expected_exit_code": 0,
      "produces": ["cli_transcript", "db_assertion"]
    },
    {
      "id": "E2",
      "probe_id": "P2",
      "type": "http",
      "method": "POST",
      "url": "http://localhost:3000/api/password-reset",
      "body": {
        "email": "test@example.com"
      },
      "expected_status": 200,
      "produces": ["api_response", "email_log"]
    },
    {
      "id": "E3",
      "probe_id": "P3",
      "type": "playwright",
      "script": "artifacts/generated/reset-password.spec.ts",
      "produces": ["playwright_trace", "screenshot"]
    }
  ]
}
```

### Execution Step Types

Initial supported types:

- `shell`
- `http`
- `playwright`
- `db_query`
- `file_assertion`

### Rules

- Execution steps must reference probe IDs.
- Execution results must be captured automatically.
- Command stdout/stderr should be stored as artifacts.
- Exit codes must be captured.
- The verifier may add notes, but cannot mark skipped required probes as pass.
- The control plane should prefer deterministic execution where possible.

## 5. Computed Confidence

### Problem

`confidence: "high"` is decorative if the verifier writes it.

### Requirement

Confidence must be computed by the control plane.

The verifier may include a self-assessed confidence field, but the authoritative confidence is deterministic.

Treat confidence as a deterministic gate score, not a statistical probability. It answers: "Did this workflow provide enough independent, complete, low-drift evidence for this task's risk level?"

### Inputs

Compute confidence from:

- probe coverage
- evidence coverage
- number of independent evidence sources
- required artifact satisfaction
- retry count
- flaky test behavior
- presence of negative assertions
- high-risk classification
- deterministic vs agent-interpreted evidence

### Example Gate Scoring

```text
start: 1.0

-0.25 if any optional probe skipped
-0.30 if only one evidence source
-0.20 if no negative assertion
-0.15 per verification retry
-0.25 if evidence is agent-interpreted rather than deterministic
-0.30 if high-risk task lacks adversarial check
```

Thresholds:

```text
>= 0.85 high
>= 0.65 medium
< 0.65 low
```

### Rules

- Passing verdict requires confidence above task threshold.
- Normal tasks require `medium`.
- High-risk tasks require `high`.
- Low confidence causes retry or escalation.
- Confidence value is stored in verification report metadata.
- Confidence must not override hard validation failures. Missing required probe evidence, invalid artifact provenance, failed criteria, or weak acceptance specs are rejects regardless of score.

## 6. Safe Prior-Art Eligibility

### Problem

Bad traces can mislead future planners if they are treated as durable instructions.

If acceptance was weak or wrong, turning it into broad behavioral rules can institutionalize bad behavior.

### Requirement

Task histories may be indexed for planner retrieval, but they must remain evidence context, not autonomous prompt mutation.

Low-confidence or failed histories should be retained as prior art with visible rework/risk scores, not promoted into worker instructions.

### Prior-Art Eligibility Rules

An indexed history should record:

- final outcome is merged
- verification verdict passed
- computed confidence is medium or high
- retry count is below threshold
- no unresolved acceptance ambiguity
- no artifact provenance violations
- no manual override was required

Histories that do not meet these clean-run properties remain searchable, but with higher rework/risk scores and explicit failure context.

### Candidate Prior Art

Candidate prior-art notes are stored separately for review:

```json
{
  "prior_art_id": "...",
  "status": "candidate",
  "source_trace_id": "...",
  "reason": "workflow had 4 retries and low confidence verification",
  "proposed_note": "..."
}
```

Candidate prior-art notes require:

- human review, or
- frontier model review, or
- repeated confirmation across multiple traces

before promotion.

### Rule

> Failed workflows are useful for diagnosis, not automatic instruction.

## 7. Counterfactual / Adversarial Checks

### Problem

A system can pass its own acceptance spec while still being wrong.

### Requirement

For high-risk tasks, and for a configurable sample of normal tasks, run an adversarial check.

The adversarial check asks:

> How could this implementation pass the acceptance spec while violating the user's intent?

### Flow

```text
verification pass -> adversarial review -> optional extra probe -> final verdict
```

### Output

```json
{
  "task_id": "task_456",
  "adversarial_findings": [
    {
      "risk": "Implementation may accept expired reset tokens",
      "suggested_probe": "Attempt password reset using expired token",
      "severity": "high"
    }
  ],
  "required_followup": true
}
```

### Rules

- High severity adversarial findings block completion unless dismissed by policy.
- Suggested probes may be appended to the acceptance spec.
- Appended probes must be executed before final acceptance.
- Random sampling rate should be configurable.

Suggested config:

```toml
[acceptance.adversarial]
enabled = true
sample_rate = 0.10
always_for_high_risk = true
```

## 8. Control Plane Verdict Algorithm

The control plane computes the final verdict.

Pseudo-code:

```go
func ComputeVerdict(spec AcceptanceSpec, bundle DoneBundle, report VerificationReport, artifacts []Artifact) Verdict {
    effectiveRisk := ComputeEffectiveRisk(spec, bundle)
    spec.RiskLevel = MaxRisk(spec.RiskLevel, effectiveRisk)

    if !ValidateSpecStrength(spec) {
        return Reject("weak_acceptance_spec")
    }

    if !ValidateDoneBundle(bundle, spec) {
        return Reject("invalid_done_bundle")
    }

    if !ValidateArtifactProvenance(report, artifacts) {
        return Reject("invalid_artifact_provenance")
    }

    if !ValidateProbeCoverage(spec, report) {
        return Reject("incomplete_probe_coverage")
    }

    if !AllRequiredCriteriaPassed(spec, report) {
        return Fail("acceptance_failed")
    }

    confidence := ComputeConfidence(spec, report, artifacts)

    if confidence < RequiredConfidence(spec.RiskLevel) {
        return RetryOrEscalate("low_confidence")
    }

    if RequiresAdversarialCheck(spec) && !AdversarialCheckPassed(report) {
        return RetryOrEscalate("adversarial_check_failed")
    }

    return Pass(confidence)
}
```

## 9. Database Additions

Add or update tables.

### acceptance_specs

```sql
CREATE TABLE acceptance_specs (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    strength_score INTEGER NOT NULL,
    risk_level TEXT NOT NULL,
    created_at TEXT NOT NULL,
    validation_status TEXT NOT NULL,
    validation_errors TEXT
);
```

### artifacts

```sql
CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    producer TEXT NOT NULL,
    producer_type TEXT NOT NULL,
    artifact_type TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    command TEXT,
    exit_code INTEGER,
    created_at TEXT NOT NULL
);
```

### verification_reports

```sql
CREATE TABLE verification_reports (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    step_id TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    computed_verdict TEXT NOT NULL,
    computed_confidence REAL NOT NULL,
    validation_status TEXT NOT NULL,
    validation_errors TEXT,
    created_at TEXT NOT NULL
);
```

### candidate_learnings

```sql
CREATE TABLE candidate_learnings (
    id TEXT PRIMARY KEY,
    source_trace_id TEXT NOT NULL,
    proposed_learning TEXT NOT NULL,
    reason TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    reviewed_at TEXT
);
```

## 10. CLI Changes

### Validate spec

```bash
clankwork acceptance validate-spec artifacts/acceptance-spec.json
```

Outputs:

```json
{
  "valid": false,
  "strength_score": 2,
  "errors": [
    "criterion C1 has no negative assertion",
    "criterion C1 has no fail_if conditions",
    "probe P2 has no required evidence mapping"
  ]
}
```

### Register artifact

```bash
clankwork artifact add \
  --type playwright_trace \
  --path artifacts/reset.trace.zip \
  --producer acceptance-verifier \
  --command "npx playwright test reset.spec.ts" \
  --exit-code 0
```

Returns artifact ID.

### Verify report

```bash
clankwork acceptance validate-report artifacts/verification-report.json
```

Outputs:

```json
{
  "valid": true,
  "probe_coverage": 1.0,
  "computed_confidence": 0.91,
  "computed_verdict": "pass"
}
```

### Existing signal commands become stricter

```bash
clankwork signal done --spec artifacts/acceptance-spec.json
clankwork signal done --bundle artifacts/done-bundle.json
clankwork signal done --report artifacts/verification-report.json
```

Each command must validate the artifact before transitioning the step.

## 11. Implementation Phases

### Phase 1 - Tight Structural Gates

Implement:

- acceptance spec strength validator
- probe/evidence schema
- done bundle validator
- verification report validator
- control-plane verdict computation
- effective risk classification from explicit config
- rejection of worker artifacts as verifier-required evidence

Scope constraint:

- no generic runner
- no reusable worker evidence
- no probabilistic confidence claims
- no learning workflow changes

No runner changes yet.

### Phase 2 - Artifact Provenance

Implement:

- artifact registry
- artifact hashing
- artifact producer metadata
- report references by artifact ID
- invalidation on hash mismatch
- trusted provenance for control-plane-wrapped commands

### Phase 3 - Execution Plan

Implement:

- acceptance spec -> execution plan format
- basic shell/http/db/file execution
- automatic artifact capture
- probe result mapping

Playwright can be initially agent-driven, then hardened later.

### Phase 4 - Confidence + Escalation

Implement:

- computed confidence
- thresholds by risk level
- low-confidence retry/escalation behavior

### Phase 5 - Safe Learning Gates

Implement:

- learning eligibility classifier
- candidate learning store
- promotion workflow later

This phase should remain independent from initial acceptance hardening. Learning safety is important, but it should not block the earlier validation and provenance rollout.

### Phase 6 - Adversarial Checks

Implement:

- high-risk adversarial review
- sampled normal-task review
- suggested probe append/retry loop

## 12. Non-Goals

Do not build yet:

- full web dashboard
- fully generic test compiler
- perfect semantic spec validation
- DSPy optimization
- team review workflow
- artifact signing with cryptographic identity

Start with deterministic validation and local artifact hashes.

## 13. Success Criteria

This project is successful when:

1. A worker cannot complete implementation without a valid done bundle.
2. An acceptance author cannot complete with a weak or empty spec.
3. A verifier cannot pass a criterion without probe-level evidence.
4. Artifacts referenced in reports are registered, hashed, and provenance-tracked.
5. The control plane computes verdict and confidence.
6. Low-confidence verification causes retry or escalation.
7. Learnings are not automatically extracted from low-quality traces.
8. High-risk work gets stricter acceptance requirements.

## Design Mantra

> Plausibility is cheap. Evidence is expensive. Completion requires evidence.
