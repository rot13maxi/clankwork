# Troubleshooting

Start with:

```sh
clankwork status
clankwork task diagnose <task-id>
clankwork events <task-id-or-item-id>
clankwork agents list
```

## Daemon Will Not Start

- Check `$CLANKWORK_HOME` permissions.
- Verify no other daemon owns `$CLANKWORK_HOME/clankwork.sock`.
- Run in the foreground first:

```sh
clankwork daemon start
```

## Agent Does Not Start

Inspect the task:

```sh
clankwork task diagnose <task-id>
clankwork agents events <task-id>
clankwork logs <task-id>
```

For ACP runtimes:

```sh
clankwork acp doctor --runtime <name> --handshake
```

Common causes are a missing provider binary, `acp-adapter` not installed under
`$CLANKWORK_HOME/bin`, permission requests waiting for approval, or a runtime
command that works in an interactive shell but not in the daemon environment.

## ACP Permission Request Is Stuck

```sh
clankwork agents permissions <task-id>
clankwork agents approve <task-id> <request-id>
clankwork agents deny <task-id> <request-id>
```

If the request should be handled automatically, adjust
`acp_permission_policy`, `acp_permission_allow_paths`, or
`acp_permission_deny_paths` in `$CLANKWORK_HOME/config.toml` and restart the
daemon.

## Acceptance Spec Rejected

Run local validation:

```sh
clankwork acceptance validate-spec artifacts/acceptance-spec.json
```

Common causes:

- Missing criteria or probe IDs.
- Probes are prose-only instead of executable.
- Missing `required_artifacts`, `required_evidence`, before/after state, or
  failure conditions.
- High-risk work lacks negative assertions or state-transition checks.

## Done Bundle Rejected

Check that every claim references a criterion in the accepted spec, every claim
is `satisfied`, and required artifact types are present with authoritative
provenance and hashes.

## Verification Report Rejected

Register evidence before submitting the report:

```sh
clankwork artifact add --type cli_transcript --path artifacts/run.txt --producer acceptance-verifier --command "go test ./..." --exit-code 0
```

Then validate:

```sh
clankwork acceptance validate-report --spec artifacts/acceptance-spec.json artifacts/verification-report.json
```

Common causes:

- Evidence references an unknown `artifact_id`.
- Evidence type/path/hash does not match the registry.
- Artifact was invalidated.
- Probe evidence is missing `probe_id`.
- Required artifact type is absent.
- Verifier-required evidence came only from the worker.
- Computed confidence is below the task risk threshold.

## Merge Queue Is Blocked

```sh
clankwork queue list
clankwork events <merge-item-id>
```

If verification failed, inspect the failure log and retry only after fixing the
task branch:

```sh
clankwork queue retry <item-id>
```

If the item should not merge:

```sh
clankwork queue skip <item-id>
```

## Prior-Art Search Looks Empty

Rebuild the projection:

```sh
clankwork prior-art rebuild
clankwork prior-art search "query terms"
```

Only terminal or integration-relevant task histories are indexed.

