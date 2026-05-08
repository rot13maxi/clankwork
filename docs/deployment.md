# Deployment

Clankwork is single-machine first. A production deployment is one daemon process,
one `$CLANKWORK_HOME`, one SQLite database, and local access to managed git
repositories and runtime binaries.

## Workstation

```sh
make build
./bin/clankwork daemon start --background
./bin/clankwork repo add /path/to/repo --name repo --branch main
```

Install ACP support when using ACP runtimes:

```sh
make install-acp-adapter
clankwork acp doctor --runtime default --handshake
```

## Filesystem Layout

Default home is `~/.clankwork`:

| Path | Purpose |
| --- | --- |
| `clankwork.db` | SQLite state. Back this up. |
| `clankwork.sock` | Unix-socket HTTP API. Restrict filesystem access. |
| `worktrees/` | Per-task and temporary merge worktrees. |
| `logs/` | Runtime logs. |
| `templates/` | User-level workflow templates. |
| `bin/` | Installed adapter binaries such as `acp-adapter`. |
| `config.toml` | Daemon/runtime configuration. |

Do not commit this directory.

## systemd

Example unit:

```ini
[Unit]
Description=Clankwork daemon
After=network.target

[Service]
Type=simple
User=clankwork
WorkingDirectory=/srv/clankwork
Environment=CLANKWORK_HOME=/var/lib/clankwork
ExecStart=/usr/local/bin/clankwork daemon start
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Create directories and permissions:

```sh
sudo useradd --system --home /var/lib/clankwork --shell /usr/sbin/nologin clankwork
sudo mkdir -p /var/lib/clankwork /srv/clankwork
sudo chown -R clankwork:clankwork /var/lib/clankwork /srv/clankwork
```

Keep provider CLI credentials available to the service user. Test ACP handshakes
from the same user before enabling unattended dispatch.

## Docker

There is no official checked-in Dockerfile or published image. If you package
Clankwork yourself, mount persistent state and repositories explicitly:

```sh
docker run --rm \
  -e CLANKWORK_HOME=/data \
  -v clankwork-data:/data \
  -v /srv/repos:/repos \
  clankwork:local \
  clankwork daemon start
```

A useful image needs:

- The `clankwork` binary.
- Git and any repo verification tools.
- Runtime provider CLIs.
- `acp-adapter` when using ACP.
- Persistent `$CLANKWORK_HOME`.

Be careful with Docker socket mounts or broad host filesystem mounts; ACP
permission policy can only reason about paths visible to the container.

## Production Checklist

- Set `CLANKWORK_HOME` to a persistent, backed-up directory.
- Restrict filesystem permissions on `clankwork.sock`.
- Register repos with explicit verify/lint/typecheck commands.
- Keep `max_slots` conservative until runtime behavior is understood.
- Run `clankwork acp doctor --handshake` for each ACP runtime.
- Run `clankwork acceptance smoke --repo <repo-id> --runtime default --case all --wait`.
- Monitor `clankwork events`, daemon logs, and merge queue depth.
- Back up `clankwork.db` while the daemon is stopped or with SQLite-safe backup tooling.

