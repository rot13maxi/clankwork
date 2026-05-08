---
name: clankwork
version: 1.0.0
description: |
  How to drive the clankwork AI agent system: create tasks, dispatch work, monitor
  progress, review landed changes, and retry failures. Use this skill any time you
  are working with clankwork tasks — creating new work, checking status, waiting for
  agents to finish, reviewing what landed, or handling failures. Covers the full
  task lifecycle from dispatch to merge. (clankwork)
allowed-tools:
  - Bash
  - Read
triggers:
  - create a task
  - dispatch to clankwork
  - watch the tasks
  - check on the agents
  - retry the failed task
  - review what landed
---

# Driving Clankwork

Clankwork is a deterministic control plane that dispatches AI agents to implement,
verify, and merge code changes. You interact with it entirely through the CLI.

## Key facts

- **Repo ID**: `01KPF4X50GT9ZZSSQ0XCWMDJH5`
- **Default runtime**: `pi-acp` (Pi CLI over ACP protocol — more stable than `claude`)
- **Default template**: `feature` (acceptance spec → implement → test → acceptance → merge)
- **Max concurrent agents**: 4 slots
- **Daemon socket**: `$CLANKWORK_HOME/clankwork.sock` (daemon must be running)

Check daemon: `clankwork daemon status`

---

## Task lifecycle

```
created (pending) → running → test → acceptance → merged
                            ↘ failed (retry available)
```

Terminal states: `merged`, `done`, `failed`. Tasks in `pending` are queued; the
scheduler dispatches up to 4 at a time by priority (higher = sooner).

---

## Creating tasks

### Detect available CLI features first

```bash
clankwork task --help 2>&1 | grep -q "wait"   && echo "has-wait"   || echo "no-wait"
clankwork task --help 2>&1 | grep -q "retry"  && echo "has-retry"  || echo "no-retry"
clankwork task --help 2>&1 | grep -q "hold"   && echo "has-hold"   || echo "no-hold"
clankwork task create --help 2>&1 | grep -q -- "--body -\|stdin" && echo "has-stdin" || echo "no-stdin"
```

Use newer primitives when available; fall back gracefully.

### Single task — new CLI (`--body -`)

```bash
TASK_ID=$(clankwork task create \
  --repo 01KPF4X50GT9ZZSSQ0XCWMDJH5 \
  --title "Brief, specific title" \
  --body - \
  --template feature \
  --runtime pi-acp \
  --priority 50 <<'EOF'
## What to build
[Specific description. File paths, function names, expected behavior.]

## Implementation notes
[Constraints, patterns to follow, what NOT to do.]

## Definition of done
- Observable acceptance criterion 1
- Observable acceptance criterion 2
- `go test ./...` passes
EOF
)
echo "created: $TASK_ID"
```

### Single task — current CLI (temp file)

```bash
_body=$(mktemp /tmp/cw-XXXXXX.md)
cat > "$_body" <<'EOF'
[task body]
EOF
TASK_ID=$(clankwork task create \
  --repo 01KPF4X50GT9ZZSSQ0XCWMDJH5 \
  --title "..." \
  --body "$_body" \
  --template feature \
  --runtime pi-acp \
  --priority 50)
rm "$_body"
echo "created: $TASK_ID"
```

### Batch with dependencies — new CLI (`--hold`)

When tasks depend on each other, hold them all before wiring the graph so nothing
starts before the dependency order is set:

```bash
A=$(clankwork task create --hold --repo 01KPF4X50GT9ZZSSQ0XCWMDJH5 \
  --title "Step 1" --body - --template feature --runtime pi-acp --priority 50 <<'EOF'
[body]
EOF
)
B=$(clankwork task create --hold --repo 01KPF4X50GT9ZZSSQ0XCWMDJH5 \
  --title "Step 2" --body - --template feature --runtime pi-acp --priority 48 <<'EOF'
[body — depends on step 1]
EOF
)

clankwork task add-dep --task "$B" --depends-on "$A"
clankwork task release --all   # scheduler dispatches in dependency order
```

### Templates

| Template | Pipeline | Use when |
|----------|----------|----------|
| `feature` | acceptance_spec → implement → test → acceptance → merge | New functionality, any meaningful change |
| `bugfix` | implement → test → merge | Targeted fixes with clear reproduction |
| `simple` | implement → merge | Tiny one-liners, doc updates, experiments |

### Priority guidance

| Priority | Use for |
|----------|---------|
| 60–70 | Blocking / urgent |
| 40–55 | Active feature work |
| 25–39 | Background improvements |
| 0–24 | Low-priority / exploratory |

---

## Checking status

```bash
clankwork status                         # slots, task counts, running agents
clankwork task list                      # all tasks
clankwork task list --status running     # only running (new CLI)
clankwork task list --status pending,running  # active queue
clankwork task show <id-or-name>         # full task detail including body
clankwork traces list                    # live stream of agent events
clankwork logs <id>                      # tail agent log
```

Tasks have mnemonic names (e.g. `brave-gekko`, `soaring-petrel`) — use them
interchangeably with IDs in any command.

### Reading the trace stream

`clankwork traces list` shows the most recent events. Key ones to watch:

| Event | Meaning |
|-------|---------|
| `signal.started` | Agent picked up the task |
| `signal.progress` | Heartbeat — agent is alive and working |
| `step.routed` | Step transition (implement→test→acceptance) |
| `signal.done` | Agent signaled completion with bundle/report |
| `signal.failed` | Agent gave up — read the message |
| `merge.merged` | Branch landed on master |

---

## Waiting for tasks

### New CLI

```bash
clankwork task wait $A $B $C --timeout 3600
# Exits 0 = all succeeded, 1 = any failed, 2 = timeout
# Prints live progress while waiting
```

### Current CLI (polling loop)

```bash
_wait_tasks() {
  while true; do
    _done=true
    for _id in "$@"; do
      _s=$(clankwork task list 2>/dev/null | awk -v id="$_id" '$2==id{print $3}')
      printf "  %s: %s\n" "$_id" "${_s:-unknown}"
      case "$_s" in merged|done|failed) ;; *) _done=false ;; esac
    done
    $_done && break
    sleep 10
  done
}
_wait_tasks $A $B $C
```

---

## Reviewing landed work

When a task merges, verify the implementation before declaring success:

```bash
# What changed
git log --oneline -5
git show <merge-sha> --stat

# Tests still pass
go test ./... 2>&1 | tail -5

# Check the actual diff
git show <merge-sha>
```

**Review checklist:**
- Diff matches the task's definition of done
- No unintended files changed
- `go test ./...` passes
- If `feature` template: check that acceptance spec, done bundle, and verification report look substantive (not rubber-stamped)

If something's wrong, dispatch a follow-up fix task. Don't patch directly unless the issue is trivial.

---

## Retrying failures

### New CLI

```bash
clankwork task retry <id>
# Resets to pending; existing traces provide failure context to the next agent
```

### Current CLI (manual retry)

Diagnose first:
```bash
clankwork traces list 2>/dev/null | grep <id> | head -20
clankwork logs <id> 2>/dev/null | tail -40
```

Then create a new task. Prefix the body with failure context:

```
## Prior attempt context
Task <id> failed at the <step> step.
What went wrong: <specific issue from traces/logs>.
Avoid: <what didn't work>.

## Original task
[original body]
```

---

## Writing good task bodies

Agent output quality is roughly proportional to spec quality. A good body has:

- **Specific file paths and function names** to touch (not "update the scheduler")
- **Exact interface** — CLI flags, API shapes, output format
- **Concrete definition of done** with observable criteria (not "it should work")
- **What NOT to do** — common wrong approaches, things to avoid
- **Pointer to existing patterns** — "follow the pattern in `cmd/clankwork/tasks.go`"
- **`go test ./...` passes** as an explicit requirement (agents sometimes skip this)

---

## Known ACP runtime behavior

The `pi-acp` runtime runs Pi CLI over the ACP (Agent Communication Protocol). A few
things to know:

- **Orphaned sessions**: If the daemon restarts while an agent is running, the ACP
  session is lost. The agent will be re-dispatched automatically after a short wait
  (daemon startup kills orphaned PIDs and marks them failed immediately).
- **Stuck sessions**: Occasionally a Pi session enters a state where it reports
  "session already has an active run" and all nudges are rejected. The reconciler
  will kill and re-dispatch these. If a task is stuck for more than 15 minutes with
  no `signal.progress` events, check `clankwork traces list` — if you only see
  repeated ACP nudge events, the session is stalled and will self-recover.
- **Retry is safe**: Failed tasks retain their traces; the next attempt gets full
  failure context injected at bootstrap automatically.
