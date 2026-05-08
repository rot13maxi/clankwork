# API Reference

The daemon exposes a JSON HTTP API on the Unix socket at
`$CLANKWORK_HOME/clankwork.sock`. The `clankwork` CLI is the supported public
interface; this API exists for the CLI, workers, and local automation.

## Response Envelope

Successful responses use:

```json
{ "ok": true, "data": {} }
```

Errors use:

```json
{
  "ok": false,
  "error": {
    "code": "not_found",
    "message": "task not found"
  }
}
```

Common HTTP statuses are `400` for invalid input, `404` for missing records,
`409` for state conflicts, and `500` for internal errors.

Common error codes include `bad_request`, `not_found`,
`invalid_acceptance_spec`, `invalid_done_bundle`, `invalid_verification_report`,
`low_confidence`, `acceptance_failed`, and `conflict`.

## Core Schemas

These are the common request shapes. Response `data` uses the corresponding
model objects from `internal/model`.

Create a repository:

```json
{
  "name": "repo",
  "path": "/abs/path/to/repo",
  "target_branch": "main",
  "verify_command": "go test ./...",
  "lint_command": "go vet ./...",
  "typecheck_command": "go build ./...",
  "auto_push": false
}
```

Create a plan:

```json
{
  "title": "Plan title",
  "body": "Markdown body",
  "with_prior_art": true
}
```

Create a task:

```json
{
  "plan_id": "optional-plan-id",
  "repo_id": "repo-id",
  "title": "Task title",
  "body": "Task body and acceptance criteria",
  "template": "feature",
  "role": "implementer",
  "runtime": "default",
  "priority": 0
}
```

Signal lifecycle events:

```json
{
  "task_id": "task-id",
  "message": "progress or terminal note",
  "acceptance_spec": {},
  "done_bundle": {},
  "verification_report": {}
}
```

Only one structured acceptance field is normally sent on a terminal
`signals.done` request. Acceptance object schemas are documented in
[acceptance-verification.md](acceptance-verification.md).

Register an artifact:

```json
{
  "task_id": "task-id",
  "step_id": "acceptance",
  "producer": "acceptance-verifier",
  "producer_type": "agent",
  "path": "artifacts/run.txt",
  "artifact_type": "cli_transcript",
  "sha256": "sha256:...",
  "command": "go test ./...",
  "working_directory": "/worktree",
  "exit_code": 0
}
```

Prior-art search:

```json
{
  "query": "auth middleware change",
  "repo_id": "optional-repo-id",
  "template": "feature",
  "status": "merged",
  "min_rework_score": 1,
  "min_risk_score": 1,
  "limit": 10
}
```

## Calling The Socket

```sh
curl --unix-socket "$CLANKWORK_HOME/clankwork.sock" \
  http://clankwork/v1/status
```

POST endpoints accept JSON bodies unless noted otherwise.

## Endpoints

### Health And Status

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/v1/health` | Daemon health. |
| `GET` | `/v1/status` | System overview. |

### Repositories

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/v1/repos.create` | Register a repository. |
| `GET` | `/v1/repos.get?id=<id>` | Fetch one repository. |
| `GET` | `/v1/repos.list` | List repositories. |

Repository create accepts path/name/branch and optional verify, lint,
typecheck, and auto-push settings. See `clankwork repo add`.

### Plans And Tasks

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/v1/plans.create` | Create a plan from markdown. |
| `GET` | `/v1/plans.list` | List plans. |
| `GET` | `/v1/plans.get?id=<id>` | Fetch a plan. |
| `POST` | `/v1/tasks.create` | Create a task. |
| `GET` | `/v1/tasks.list` | List tasks. |
| `GET` | `/v1/tasks.get?id=<id>` | Fetch a task. |
| `GET` | `/v1/tasks.getByName?name=<name>` | Fetch by generated task name. |
| `POST` | `/v1/tasks.addDep` | Add a task dependency. |
| `POST` | `/v1/tasks.setPriority` | Set priority. |
| `POST` | `/v1/tasks.retry` | Requeue a task. |
| `POST` | `/v1/tasks.close` | Close a task. |
| `GET` | `/v1/tasks.diagnose?id=<id>` | Explain current blockers and control state. |
| `POST` | `/v1/tasks.retryStep` | Retry one workflow step. |
| `POST` | `/v1/tasks.resetStep` | Reset to an earlier workflow step. |
| `POST` | `/v1/tasks.escalate` | Create a human escalation. |

### Worker Signals

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/v1/bootstrap` | Return task, role, failure context, and CLI card for an agent. |
| `POST` | `/v1/context.get` | Return task context. |
| `POST` | `/v1/signals.started` | Mark current task running. |
| `POST` | `/v1/signals.progress` | Heartbeat/progress update. |
| `POST` | `/v1/signals.done` | Submit simple done, acceptance spec, done bundle, or verification report. |
| `POST` | `/v1/signals.failed` | Route step failure. |
| `POST` | `/v1/signals.blocked` | Mark blocked and request help. |

### Acceptance And Artifacts

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/v1/acceptance.spec?task_id=<id>` | Fetch stored spec. |
| `POST` | `/v1/acceptance.spec` | Store/validate a spec. |
| `GET` | `/v1/acceptance.doneBundle?task_id=<id>` | Fetch done bundle. |
| `GET` | `/v1/acceptance.verificationReport?task_id=<id>` | Fetch verification report. |
| `POST` | `/v1/artifacts.add` | Register an evidence artifact. |
| `GET` | `/v1/artifacts.list?task_id=<id>` | List task artifacts. |

Schemas are documented in [acceptance-verification.md](acceptance-verification.md).

### Agents

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/v1/agents.list` | List agents. |
| `GET` | `/v1/agents.get?id=<id>` | Fetch an agent. |
| `GET` | `/v1/agents.getByTask?task_id=<id>` | Fetch current task agent. |
| `GET` | `/v1/agents.events` | List persisted ACP/runtime events. |
| `GET` | `/v1/agents.permissions` | List pending ACP permission requests. |
| `POST` | `/v1/agents.send` | Send a message to a running agent. |
| `POST` | `/v1/agents.cancel` | Cancel current turn or runtime. |
| `POST` | `/v1/agents.permissionDecision` | Approve or deny an ACP permission request. |

### Control Plane

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/v1/reconcile.task` | Run diagnosis/reconciliation for one task. |
| `POST` | `/v1/reconcile.all` | Reconcile eligible work. |
| `POST` | `/v1/refresh.task` | Refresh task observation state. |
| `POST` | `/v1/refresh.agent` | Refresh agent observation state. |
| `POST` | `/v1/refresh.worktree` | Refresh worktree observation state. |
| `GET` | `/v1/events.list` | Unified traces, decisions, and actuations. |
| `GET` | `/v1/escalations.list` | List escalations. |
| `POST` | `/v1/escalations.resolve` | Resolve an escalation. |
| `POST` | `/v1/dispatch.pause` | Pause dispatch. |
| `POST` | `/v1/dispatch.resume` | Resume dispatch. |
| `GET` | `/v1/dispatch.status` | Current dispatch pause state. |

### Merge Queue, Traces, Config, Learnings, Prior Art

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/v1/queue.list` | List merge queue. |
| `POST` | `/v1/queue.skip` | Reject/skip a queued item. |
| `POST` | `/v1/queue.retry` | Requeue a failed item. |
| `GET` | `/v1/traces.list` | Query traces. |
| `GET` | `/v1/config` | Effective config. |
| `POST` | `/v1/learnings.add` | Deprecated compatibility endpoint for legacy learnings tables. Prefer the prior-art index. |
| `POST` | `/v1/learnings.candidateAdd` | Deprecated compatibility endpoint for legacy candidate learnings. Prefer the prior-art index. |
| `GET` | `/v1/learnings.candidateList` | Deprecated compatibility endpoint listing legacy candidate learnings. |
| `GET` | `/prior-art/search` | Compatibility prior-art search route. |
| `GET` | `/v1/prior-art.search` | Search prior task histories. |
| `GET` | `/v1/prior-art.show?task_id=<id>` | Show indexed history. |
| `POST` | `/v1/prior-art.rebuild` | Rebuild the prior-art index. |
