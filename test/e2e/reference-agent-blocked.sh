#!/usr/bin/env bash
# reference-agent-blocked.sh — Variant agent that signals "blocked" instead of "done".
#
# Used in e2e tests to verify the blocked signal path.

set -euo pipefail

fail() {
    clankwork signal failed "$1" 2>/dev/null || true
    echo "AGENT FAILED: $1" >&2
    exit 1
}

# Bootstrap to load task context.
BOOTSTRAP_JSON=$(clankwork bootstrap --format json 2>&1) || fail "bootstrap failed: $BOOTSTRAP_JSON"
TASK_TITLE=$(echo "$BOOTSTRAP_JSON" | jq -r '.task.title // "untitled"')

echo "Blocked agent starting for: $TASK_TITLE"

# Signal started.
clankwork signal started || fail "could not signal started"

# Instead of doing work, signal that we're blocked.
clankwork signal blocked "Need human input: unclear requirements for $TASK_TITLE" || fail "could not signal blocked"

echo "Agent signaled blocked."
