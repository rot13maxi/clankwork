# Config Reference

Configuration lives at `$CLANKWORK_HOME/config.toml`. If the file is missing,
Clankwork uses the defaults from `internal/config/config.go`.

`clankwork config` prints the effective daemon configuration. `clankwork config
get` supports selected scheduler keys. `clankwork config set` is not implemented.

## Example

```toml
[scheduler]
max_slots = 4
heartbeat_timeout_secs = 600
tick_secs = 2
deterministic_timeout_sec = 1800
merge_queue_max_depth = 10
merge_queue_tick_secs = 5
merge_queue_max_attempts = 3
verify_timeout_secs = 600
synthesis_interval_secs = 3600
synthesis_retry_threshold = 2
learning_max_age_days = 30
learning_max_count = 1000
learning_min_access_count = 0

[acceptance.adversarial]
enabled = true
sample_rate = 0.10
always_for_high_risk = true

[acceptance.risk]
high_risk_labels = ["auth", "payments", "permissions", "data-deletion", "migration", "infra", "iam", "public-api"]
high_risk_paths = ["internal/auth/**", "internal/billing/**", "migrations/**", "infra/**"]

[runtimes.default]
command = "acp-adapter"
args = ["--adapter", "pi"]
transport = "acp"
model = "pi"
acp_permission_policy = "worktree"
```

## Scheduler

| Key | Default | Description |
| --- | ---: | --- |
| `max_slots` | `4` | Maximum concurrent agent slots. |
| `heartbeat_timeout_secs` | `600` | Time without heartbeat/progress before reconciliation treats an agent as stale. |
| `tick_secs` | `2` | Scheduler loop interval. |
| `deterministic_timeout_sec` | `1800` | Timeout for deterministic workflow step commands. |
| `merge_queue_max_depth` | `10` | Dispatch backpressure threshold. |
| `merge_queue_tick_secs` | `5` | Merge queue processor interval. |
| `merge_queue_max_attempts` | `3` | Maximum merge/rebase processing attempts before failure handling. |
| `verify_timeout_secs` | `600` | Timeout for repo verification commands. |
| `synthesis_interval_secs` | `3600` | Learning synthesis loop interval for compatibility learning tables. |
| `synthesis_retry_threshold` | `2` | Retry threshold used by learning synthesis. |
| `learning_max_age_days` | `30` | Maximum learning age retained by garbage collection. |
| `learning_max_count` | `1000` | Maximum learning count retained by garbage collection. |
| `learning_min_access_count` | `0` | Minimum access count retained by garbage collection. |

## Acceptance

| Key | Default | Description |
| --- | --- | --- |
| `acceptance.adversarial.enabled` | `true` | Enables adversarial review checks for acceptance reports. |
| `acceptance.adversarial.sample_rate` | `0.10` | Sampling rate for normal-risk adversarial review. |
| `acceptance.adversarial.always_for_high_risk` | `true` | Requires adversarial review for high-risk tasks. |
| `acceptance.risk.high_risk_labels` | auth/payments/permissions/data deletion/migration/infra/IAM/public API | Text labels that raise effective task risk. |
| `acceptance.risk.high_risk_paths` | auth, billing, migrations, infra paths | Path globs/prefixes that raise effective task risk. |

Agent-provided `risk_level: "normal"` cannot lower a task raised to high risk by
these rules.

## Runtimes

Each `[runtimes.<name>]` entry defines an agent runtime.

| Key | Description |
| --- | --- |
| `command` | Binary to execute. |
| `args` | Argument list for `command`. |
| `env` | Extra environment variables. |
| `transport` | `tmux` or `acp`; empty means `tmux`. |
| `model` | Model/provider label recorded on agents and traces. |
| `escalate_after` | Attempts before escalation; `0` disables. |
| `escalate_to` | Runtime name to use after escalation. |
| `non_interactive` | For tmux runtimes, skip prompt injection and rely on args. |
| `acp_permission_policy` | `worktree`, `trusted`, `manual`, or `deny`; empty ACP policy becomes `worktree`. |
| `acp_permission_allow_paths` | Additional absolute path prefixes allowed by ACP policy. |
| `acp_permission_deny_paths` | Additional deny path prefixes or sensitive markers. |
| `acp_permission_timeout_sec` | Manual permission timeout; `0` uses the default. |

Default runtime names are `default`, `claude`, `pi`, `pi-acp`, `claude-acp`, and
`frontier`.

