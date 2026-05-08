#!/usr/bin/env bash
# reference-agent.sh — Minimal reference implementation of a Clankwork agent.
#
# This script demonstrates the complete agent lifecycle:
#   1. Bootstrap — load task context from the control plane
#   2. Signal started — tell the control plane we're working
#   3. Do work — read the task, create/modify files, commit
#   4. Signal progress — heartbeat with status update
#   5. Signal done with a structured spec, bundle, or verification report
#
# Environment variables (set by the control plane at dispatch):
#   CLANKWORK_TASK_ID  — the task being worked on
#   CLANKWORK_HOME     — path to the daemon's home directory (socket lives here)
#   CLANKWORK_REPO_ID  — the repo this task targets (may be empty)
#   CLANKWORK_STEP     — the current template step (e.g. "implement", "acceptance")
#
# Requirements: jq, git, and the clankwork binary on PATH.

set -euo pipefail

# --------------------------------------------------------------------------
# Helper: signal failure and exit if something goes wrong.
# --------------------------------------------------------------------------
fail() {
    local msg="$1"
    clankwork signal failed "$msg" 2>/dev/null || true
    echo "AGENT FAILED: $msg" >&2
    exit 1
}

# --------------------------------------------------------------------------
# Step 1: Bootstrap — fetch task context from the control plane.
# --------------------------------------------------------------------------
BOOTSTRAP_JSON=$(clankwork bootstrap --format json 2>&1) || fail "bootstrap failed: $BOOTSTRAP_JSON"

# Parse the bootstrap response.
TASK_TITLE=$(echo "$BOOTSTRAP_JSON" | jq -r '.task.title // "untitled"')
TASK_BODY=$(echo "$BOOTSTRAP_JSON" | jq -r '.task.body // ""')
TASK_STATUS=$(echo "$BOOTSTRAP_JSON" | jq -r '.task.status // "unknown"')
ROLE=$(echo "$BOOTSTRAP_JSON" | jq -r '.role // "agent"')
ROLE_BODY=$(echo "$BOOTSTRAP_JSON" | jq -r '.role_body // ""')
FAILURE_CONTEXT=$(echo "$BOOTSTRAP_JSON" | jq -r '.failure_context // ""')
CLI_REF=$(echo "$BOOTSTRAP_JSON" | jq -r '.cli_reference // [] | join("\n")')
LEARNINGS_COUNT=$(echo "$BOOTSTRAP_JSON" | jq '.learnings | length')

echo "=== Clankwork Reference Agent ==="
echo "Task:     $TASK_TITLE"
echo "Step:     ${CLANKWORK_STEP:-none}"
echo "Role:     $ROLE"
echo "Learnings: $LEARNINGS_COUNT"
if [ -n "$FAILURE_CONTEXT" ]; then
    echo "Failure context from prior attempt:"
    echo "$FAILURE_CONTEXT"
fi
echo "================================="

# --------------------------------------------------------------------------
# Step 2: Signal started — let the control plane know we're active.
# --------------------------------------------------------------------------
clankwork signal started || fail "could not signal started"

mkdir -p artifacts

if [ "${CLANKWORK_STEP:-}" = "acceptance_spec" ]; then
    jq -n --arg task_id "$CLANKWORK_TASK_ID" '{
      task_id: $task_id,
      criteria: [{
        id: "C1",
        description: "Reference agent output file exists and records the task",
        probes: [{
          id: "agent_output_contains_title",
          description: "verify agent-output.txt exists after implementation and contains the task title",
          command: "test -f agent-output.txt && grep -q \"$TASK_TITLE\" agent-output.txt",
          required_evidence: ["test_output"],
          before: "agent-output.txt does not exist before implementation",
          after: "agent-output.txt exists and contains the task title",
          observable_side_effect: "artifacts/verification.txt records the file assertion",
          negative_assertion: "verification fails when agent-output.txt is missing or lacks the task title"
        }],
        required_artifacts: ["test_output"],
        fail_if: ["agent-output.txt is missing", "task title is absent from output"]
      }]
    }' > artifacts/acceptance-spec.json
    clankwork signal progress "wrote acceptance spec" || true
    clankwork signal done --spec artifacts/acceptance-spec.json || fail "could not signal done with acceptance spec"
    echo "Acceptance spec completed successfully."
    exit 0
fi

if [ "${CLANKWORK_STEP:-}" = "acceptance" ]; then
    TS=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    if [ -f agent-output.txt ] && grep -q "$TASK_TITLE" agent-output.txt; then
        printf 'agent-output.txt contains expected task title\n' > artifacts/verification.txt
        HASH=$(shasum -a 256 artifacts/verification.txt | awk '{print "sha256:" $1}')
        ARTIFACT_ID=$(clankwork artifact add --type test_output --path artifacts/verification.txt --producer acceptance-verifier --command "test -f agent-output.txt && grep -q \"$TASK_TITLE\" agent-output.txt" --exit-code 0)
        jq -n --arg task_id "$CLANKWORK_TASK_ID" --arg timestamp "$TS" --arg hash "$HASH" --arg artifact_id "$ARTIFACT_ID" '{
          task_id: $task_id,
          results: [{
            criterion_id: "C1",
            status: "pass",
            evidence: [{
              artifact_id: $artifact_id,
              type: "test_output",
              path: "artifacts/verification.txt",
              probe_id: "agent_output_contains_title",
              command: "test -f agent-output.txt && grep -q \"$TASK_TITLE\" agent-output.txt",
              producer_step: "acceptance",
              producer_role: "verifier",
              timestamp: $timestamp,
              content_hash: $hash,
              authoritative: true
            }],
            reason: "Observed agent-output.txt with the expected task title"
          }],
          failures: [],
          confidence: "high"
        }' > artifacts/verification-report.json
    else
        printf 'agent-output.txt missing or missing expected task title\n' > artifacts/verification.txt
        HASH=$(shasum -a 256 artifacts/verification.txt | awk '{print "sha256:" $1}')
        ARTIFACT_ID=$(clankwork artifact add --type test_output --path artifacts/verification.txt --producer acceptance-verifier --command "test -f agent-output.txt && grep -q \"$TASK_TITLE\" agent-output.txt" --exit-code 1)
        jq -n --arg task_id "$CLANKWORK_TASK_ID" --arg timestamp "$TS" --arg hash "$HASH" --arg artifact_id "$ARTIFACT_ID" '{
          task_id: $task_id,
          results: [{
            criterion_id: "C1",
            status: "fail",
            evidence: [{
              artifact_id: $artifact_id,
              type: "test_output",
              path: "artifacts/verification.txt",
              probe_id: "agent_output_contains_title",
              command: "test -f agent-output.txt && grep -q \"$TASK_TITLE\" agent-output.txt",
              producer_step: "acceptance",
              producer_role: "verifier",
              timestamp: $timestamp,
              content_hash: $hash,
              authoritative: true
            }],
            reason: "agent-output.txt missing or does not contain the expected task title"
          }],
          failures: [{
            criterion_id: "C1",
            reason: "agent-output.txt missing or does not contain the expected task title",
            evidence: [{
              artifact_id: $artifact_id,
              type: "test_output",
              path: "artifacts/verification.txt",
              probe_id: "agent_output_contains_title",
              command: "test -f agent-output.txt && grep -q \"$TASK_TITLE\" agent-output.txt",
              producer_step: "acceptance",
              producer_role: "verifier",
              timestamp: $timestamp,
              content_hash: $hash,
              authoritative: true
            }]
          }],
          confidence: "high"
        }' > artifacts/verification-report.json
    fi
    clankwork signal progress "wrote verification report" || true
    clankwork signal done --report artifacts/verification-report.json || fail "could not signal done with verification report"
    echo "Acceptance completed successfully."
    exit 0
fi

# --------------------------------------------------------------------------
# Step 3: Do the work.
#
# For this reference agent, the "work" is:
#   - Create a file called agent-output.txt with the task details
#   - Stage and commit it
#
# A real agent would read the task body, understand what's needed, write code,
# run tests, etc. This is intentionally trivial.
# --------------------------------------------------------------------------

# Write the output file.
cat > agent-output.txt <<WORK_EOF
Task: $TASK_TITLE
Body: $TASK_BODY
Step: ${CLANKWORK_STEP:-none}
Role: $ROLE
Failure Context: $FAILURE_CONTEXT
WORK_EOF

# Stage and commit.
git add agent-output.txt
git commit -m "agent: implement task - $TASK_TITLE" --allow-empty-message || fail "git commit failed"

# --------------------------------------------------------------------------
# Step 4: Signal progress — heartbeat with a status update.
# --------------------------------------------------------------------------
clankwork signal progress "implemented changes" || true

# --------------------------------------------------------------------------
# Step 5: Signal done — tell the control plane we finished successfully.
# --------------------------------------------------------------------------
printf 'agent-output.txt written and committed\n' > artifacts/implementation.txt
TS=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
HASH=$(shasum -a 256 artifacts/implementation.txt | awk '{print "sha256:" $1}')
jq -n --arg task_id "$CLANKWORK_TASK_ID" --arg timestamp "$TS" --arg hash "$HASH" '{
  task_id: $task_id,
  summary: "Reference agent wrote agent-output.txt",
  files_changed: ["agent-output.txt"],
  tests_run: ["reference-agent implementation smoke check"],
  claims: [{criterion_id: "C1", status: "satisfied"}],
  artifacts: [{
    type: "test_output",
    path: "artifacts/implementation.txt",
    probe_id: "agent_output_contains_title",
    producer_step: "implement",
    producer_role: "implementer",
    timestamp: $timestamp,
    content_hash: $hash,
    authoritative: true
  }],
  known_risks: []
}' > artifacts/done-bundle.json

clankwork signal done --bundle artifacts/done-bundle.json || fail "could not signal done"

echo "Agent completed successfully."
