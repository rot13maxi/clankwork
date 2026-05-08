# Merge-Controller Audit Events

The merge queue processor (`internal/mergequeue/processor.go`) is a deterministic controller inside the Clankwork control-plane daemon. Every significant decision and actuation it makes is persisted to SQLite and surfaced through the unified events API.

This document covers:

- The six decision event kinds the merge controller produces
- The actuation operations it performs and their outcomes
- The full merge lifecycle event sequence, including failure paths
- How to query merge-controller audit events via the CLI

## Event Sources

Merge-controller events appear in three tables:

| Table | Event Type | Produced by |
|---|---|---|
| `controller_decisions` | ReconcilerDecision | `recordDecision()` — the controller's *what to do and why* |
| `controller_actuations` | ControllerActuation | `recordActuation()` — the controller's *what it did and whether it worked* |
| `traces` | Trace | `TraceAppend()` — append-only workflow event log |

All three sources are unified by the `ControlPlaneEvents` store query and exposed through the same CLI command.

### ReconcilerDecision

Records the controller's decision at each processing step. Fields:

- `controller` — always `"merge_controller"` for merge queue decisions
- `task_id` — the task this merge item belongs to
- `target_type` — `"merge_item"`
- `target_id` — the merge queue item ID
- `decision_kind` — one of six kinds (listed below)
- `action` — the concrete action chosen
- `reason` — human-readable explanation
- `retryable` — whether the item can be re-queued after this decision

### ControllerActuation

Records the outcome of a state change or external operation. Fields:

- `requested_operation` — the operation name (e.g., `merge.create_worktree`)
- `actor_type` / `actor_id` — always `"controller"` / `"merge_controller"`
- `target_type` / `target_id` — the merge queue item
- `task_id` — the parent task
- `previous_state` / `new_state` — state transition (e.g., `"rebasing"` → `"verifying"`)
- `outcome` — `"success"`, `"failed"`, or `"skipped"`
- `error` — error text on failure

### Traces

Append-only event log entries with `event_type` prefixed by `merge.`. The payload is JSON with operation-specific fields. See [trace_payloads.go](../internal/model/trace_payloads.go) for the structured types.

---

## Six Decision Event Kinds

The merge controller emits exactly six decision events, each identified by a unique `(decision_kind, action)` pair:

### 1. `merge_attempt` / `process_item`

**When:** A queued merge item is dequeued and processing begins.

**Reason:** `"queued merge item is ready for processing"`

**Retryable:** `true`

This is the first decision in the merge pipeline. It marks the transition from idle to active processing.

### 2. `merge_conflict` / `classify_conflict`

**When:** The rebase produces conflict markers during `fetchAndRebase()`.

**Reason:** `"rebase conflict requires classification"`

**Retryable:** `true`

After this decision, the conflict is classified (trivial vs semantic) and either a conflict-resolver agent is spawned or the item is rejected and the original task is re-dispatched.

### 3. `merge_ready` / `advance_target`

**When:** All verification steps pass and the rebased commit is ready to advance the target branch.

**Reason:** `"verification passed; advancing target branch"`

**Retryable:** `false`

This is the green-light decision. The controller performs a compare-and-swap to advance the target branch to the rebased SHA.

### 4. `merge_verify_failed` / `reject_item`

**When:** Verification fails and the retry budget is exhausted (attempt count >= max attempts).

**Reason:** `"verification failed and retry budget is exhausted"`

**Retryable:** `false`

The item is permanently rejected. No further merge attempts will be made.

### 5. `merge_verify_failed` / `redispatch_task`

**When:** Verification fails but retry budget remains.

**Reason:** `"verification failed; task returned to pending for rework"`

**Retryable:** `true`

The original task is returned to `pending` status for re-implementation. The merge item status becomes `failed` and can be manually re-queued with `clankwork queue retry`.

### 6. `merge_processing_failed` / `mark_failed`

**When:** A processing error occurs (worktree creation, fetch error, etc.) and retry budget is exhausted.

**Reason:** `"merge processing failed and retry budget is exhausted"`

**Retryable:** `false`

The item is marked permanently failed after all retry attempts are consumed. If retry budget remains, the item is re-queued instead (no decision event for the re-queue — the next processing cycle will emit a fresh `merge_attempt`).

---

## Actuation Operations

The merge controller performs six actuation operations, each recording a state transition and outcome:

### `merge.create_worktree`

Create a temporary worktree from the task's branch for the merge attempt.

- **Previous state:** `"rebasing"`
- **New state:** `"rebasing"` (worktree creation doesn't change status, it enables the rebase)
- **Outcome:** Always `"success"` on normal paths; failure before this actuation returns from `processItem` directly
- **Reason:** `"created merge worktree"`

### `merge.advance_target`

Advance the target branch to the rebased SHA via compare-and-swap.

- **Previous state:** Target branch SHA (on success) or `"merging"` (on failure)
- **New state:** Rebased SHA (on success) or `"queued"` (on failure — external advance, re-queued)
- **Outcomes:**
  - `"success"` / `"target branch advanced"` — compare-and-swap succeeded
  - `"failed"` / `"target branch advanced before compare-and-swap"` — another merge landed first, item re-queued for re-rebase
- **Note:** This is the only actuation where failure is a *transient* condition that resolves on the next processing cycle.

### `merge.push_target`

Optional push of the advanced target branch to origin (only when `repo.AutoPush` is enabled).

- **Previous state:** `"local"`
- **New state:** `"origin"`
- **Outcomes:**
  - `"failed"` / `"auto-push failed after local merge"` — push rejected (e.g., remote rejected)
  - Not emitted on success — the operation is best-effort and success is implicit in the subsequent `merge.complete`
- **Note:** Push failure is logged but does *not* affect the merge item status (the local merge already succeeded).

### `merge.verify`

Records the outcome of the verification step (lint, test, etc.).

- **Previous state:** `"verifying"`
- **Outcomes:**
  - `"rejected"` / `"merge verification failed"` — retry budget exhausted; item rejected permanently
  - `"failed"` / `"merge verification failed"` — retry budget remains; task re-dispatched
- **New state:** `"rejected"` or `"failed"` respectively

### `merge.process`

Records a processing-level failure (worktree creation, fetch, etc.) after retry exhaustion.

- **Previous state:** Current item status at time of failure
- **New state:** `"failed"`
- **Outcome:** `"failed"`
- **Reason:** `"merge processing failed"`

### `merge.complete`

Records successful completion of the merge pipeline.

- **Previous state:** `"merging"`
- **New state:** `"merged"`
- **Outcome:** `"success"`
- **Reason:** `"merge queue item completed"`

This is the terminal success actuation. After this, the task status is set to `merged`, the branch is deleted, and the worktree is cleaned up.

---

## Full Merge Lifecycle Event Sequence

### Happy Path

The complete sequence of events for a successful merge:

```
1.  ReconcilerDecision:    merge_attempt / process_item
2.  ControllerActuation:   merge.create_worktree → success
3.  (rebase succeeds, no conflict)
4.  (verification runs — if configured)
5.  ReconcilerDecision:    merge_ready / advance_target
6.  ControllerActuation:   merge.advance_target → success
7.  (optional push — may emit merge.push_target if AutoPush)
8.  (worktree cleaned up, branch deleted)
9.  ControllerActuation:   merge.complete → success
10. Trace:                 merge.merged {sha, branch}
```

### Trivial Conflict Path (resolved by agent)

When rebase produces mechanical conflicts:

```
1.  ReconcilerDecision:    merge_attempt / process_item
2.  ControllerActuation:   merge.create_worktree → success
3.  (rebase produces conflicts)
4.  ReconcilerDecision:    merge_conflict / classify_conflict
5.  Trace:                 merge.conflict_classified {class: "trivial", reason, files}
6.  (conflict-resolver task created, merge item → "conflicted")
7.  Trace:                 merge.conflicted {conflict_task_id}
    [agent resolves conflicts on separate task]
8.  Trace:                 merge.conflict_resolved {conflict_task_id}
    (merge item re-queued to "queued", merge cycle restarts from step 1)
```

### Semantic Conflict Path (rejected)

When rebase produces behavioral conflicts:

```
1.  ReconcilerDecision:    merge_attempt / process_item
2.  ControllerActuation:   merge.create_worktree → success
3.  (rebase produces conflicts)
4.  ReconcilerDecision:    merge_conflict / classify_conflict
5.  Trace:                 merge.conflict_classified {class: "semantic", reason, files}
6.  (merge item → "rejected", original task → "pending")
7.  Trace:                 merge.semantic_conflict_rejected {class, reason}
```

### Verification Failure (retry remaining)

When post-rebase verification fails and attempts remain:

```
1.  ReconcilerDecision:    merge_attempt / process_item
2.  ControllerActuation:   merge.create_worktree → success
3.  (rebase succeeds)
4.  (verification fails)
5.  ReconcilerDecision:    merge_verify_failed / redispatch_task
6.  ControllerActuation:   merge.verify → failed
7.  (task → "pending" for rework, merge item → "failed")
8.  Trace:                 merge.verify_failed {reason, log}
```

### Verification Failure (retry exhausted)

When post-rebase verification fails and no retry budget remains:

```
1.  ReconcilerDecision:    merge_attempt / process_item
2.  ControllerActuation:   merge.create_worktree → success
3.  (rebase succeeds)
4.  (verification fails)
5.  ReconcilerDecision:    merge_verify_failed / reject_item
6.  ControllerActuation:   merge.verify → rejected
7.  (merge item → "rejected")
```

### Target Advanced Externally (transient)

When another merge lands between rebase and compare-and-swap:

```
1.  ReconcilerDecision:    merge_attempt / process_item
2.  ControllerActuation:   merge.create_worktree → success
3.  (rebase succeeds)
4.  (verification runs)
5.  ReconcilerDecision:    merge_ready / advance_target
6.  ControllerActuation:   merge.advance_target → failed (target advanced before CAS)
7.  (merge item → "queued", attempt incremented — will re-process from step 1)
```

### Conflict Resolver Failure

When a conflict-resolver task fails:

```
1.  [conflict-resolver agent fails]
2.  (HandleConflictFailed: resolver worktree removed)
3.  Trace:                 merge.conflict_resolver_failed {conflict_task_id, reason}
4.  (merge item → "queued", re-processed from merge_attempt)
```

### Processing Failure

When worktree creation, fetch, or another low-level operation fails:

```
1.  (processItem encounters error)
2.  (if retry budget remains: merge item → "queued", no decision event emitted)
3.  (if retry budget exhausted:)
    ReconcilerDecision:    merge_processing_failed / mark_failed
    ControllerActuation:   merge.process → failed
```

---

## Querying Merge-Controller Events via CLI

### All events for a target

The `clankwork events` command queries the unified control-plane event stream (traces, decisions, actuations, escalations) filtered by a target ID or task ID:

```bash
# Events for a specific task
clankwork events <task-id>

# Events for a specific merge queue item
clankwork events <merge-item-id>

# All recent events, limited to 20
clankwork events --limit 20
```

### Filtering by task

```bash
# All events for a task (decisions + actuations + traces)
clankwork events --task <task-id>
```

### JSON output

For programmatic consumption, use `--format json`:

```bash
clankwork events <merge-item-id> --format json
```

Each event object contains:
- `source` — one of `"trace"`, `"decision"`, `"actuation"`, `"escalation"`
- `id` — the event/row ID
- `type` — event type or operation name
- `task_id` — parent task
- `target_type` / `target_id` — the controlled entity
- `summary` — human-readable one-liner
- `payload` — JSON string with structured data
- `created_at` — RFC3339 timestamp

### Diagnosing a task

The `clankwork reconcile task` command runs a full diagnosis including recent events:

```bash
clankwork reconcile task <task-id>
```

This returns the latest decision, latest actuation, recent events, and the recommended next action.

### Merge queue state

```bash
# List merge queue items
clankwork queue list

# Skip (reject) an item
clankwork queue skip <item-id>

# Re-queue a failed item
clankwork queue retry <item-id>
```

Each queue operation also produces actuation events (`queue.skip`, `queue.retry`) visible via `clankwork events`.
