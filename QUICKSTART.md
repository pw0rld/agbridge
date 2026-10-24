# agbridge quickstart

This walkthrough connects an AI assistant (or just your shell) to a
remote machine through agbridge:

```
AI agent / shell -> agbridge CLI -> gateway -> agbridge daemon -> remote machine
```

The CLI is one-shot: every command dials the gateway, does its work,
and exits; every verb also speaks `--json` for machines. The daemon
runs on the machine where files, commands, and private services live.
Both CLI and daemon dial the gateway outbound over TLS WebSocket.

## 0. Before You Start

You need:

- A gateway host reachable by both machines, usually on TCP 443.
- The `agbridge` binary installed on the gateway host, daemon host, and
  AI client host.
- A hostname for the gateway, for example `gw.example.com`.

Install:

```bash
curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash
agbridge --help
```

In the examples below:

- Gateway config lives under `/etc/agbridge`.
- The daemon is named `lab01`.
- The client (bridge credential) is named `peter-laptop`.
- The daemon is advertised with working dir `/home/me/projects` and
  exec enabled.

Commands that write under `/etc` or bind port 443 must run with the
appropriate service permissions. Use `sudo`, a reverse proxy, or a
non-privileged test port to match your environment.

## 1. Start the Gateway

On the gateway host:

```bash
sudo mkdir -p /etc/agbridge /var/log/agbridge

agbridge cert gen \
  --cn gw.example.com \
  --out /etc/agbridge
```

Create `/etc/agbridge/gateway.yaml`:

```yaml
listen: 0.0.0.0:443
public_url: wss://gw.example.com/
audit_path: /var/log/agbridge/audit.jsonl
metrics_listen: 127.0.0.1:9464   # optional Prometheus endpoint, see docs/deployment.md
agents: []
daemons: []
```

Start the gateway:

```bash
agbridge gateway \
  --config /etc/agbridge/gateway.yaml \
  --cert /etc/agbridge/cert.pem \
  --key /etc/agbridge/key.pem
```

Keep this process running with systemd, tmux, or your supervisor of
choice.

## 2. Enroll the Daemon

On the gateway host, issue a one-shot token for the daemon:

```bash
agbridge issue-token \
  --config /etc/agbridge/gateway.yaml \
  --role daemon \
  --name lab01 \
  --exec-enabled \
  --default-working-dir /home/me/projects \
  --ttl 30 \
  --json
```

The JSON output includes `token`, `gateway`, `expires_at`, and
`paste_command`. Run the paste command on the daemon host, or run the
equivalent command manually:

```bash
agbridge enroll \
  --gateway wss://gw.example.com/ \
  --token et_XXXXXXXXXXXXXXXXXX \
  --state-dir ~/.config/agbridge
```

Enrollment runs connectivity diagnostics, generates the daemon Noise key
locally, exchanges the token for an API key and policy, then writes:

```text
~/.config/agbridge/state.json
~/.config/agbridge/daemon_noise.key
```

Start the daemon:

```bash
agbridge daemon --state-dir ~/.config/agbridge
```

Expected log:

```text
daemon: handshake ok, awaiting Route frames
```

On Linux, `enroll` also installs and enables an `agbridge-daemon.service`
systemd unit (system-level when run as root) so the daemon starts on
boot; pass `--no-start-service` to skip that.

## 3. Enroll the Client

On the gateway host, issue a bridge token targeting the daemon:

```bash
agbridge issue-token \
  --config /etc/agbridge/gateway.yaml \
  --role bridge \
  --name peter-laptop \
  --target lab01 \
  --ttl 30 \
  --json
```

On the AI client host, run the printed paste command, or enroll manually:

```bash
agbridge enroll \
  --gateway wss://gw.example.com/ \
  --token et_YYYYYYYYYYYYYYYYYY \
  --state-dir ~/.config/agbridge
```

Client enrollment writes:

```text
~/.config/agbridge/state.json
~/.config/agbridge/bridge_noise.key
```

Verify the whole path end to end:

```bash
agbridge doctor
agbridge -t lab01 exec -- uname -a
```

## 4. Run Commands

The `-t` flag picks the target (the daemon name). From a tool
reference point of view the CLI verbs are the tools:

```bash
agbridge -t lab01 caps                  # daemon policy + capabilities
agbridge -t lab01 ls /home/me/projects
agbridge -t lab01 grep "TODO" /home/me/projects
agbridge -t lab01 exec -- go test ./...
agbridge -t lab01 put ./build.tar.gz /tmp/build.tar.gz
agbridge -t lab01 get /var/log/app.log ./app.log
```

For scripts and AI agents, add `--json`: stdout is then exactly one
JSON object on both the success and the failure path, and stderr
carries a structured `{"code","message","retry_safe"}` line:

```bash
agbridge -t lab01 --json exec -- git rev-parse HEAD
```

To let an AI agent drive the CLI itself, paste `agbridge ai-help`
into its system prompt — it is a ready-made tool cheatsheet. See
[examples/ai-agent-cli.md](examples/ai-agent-cli.md) for the full
machine-mode contract and retry recipes.

## 5. Speed It Up (Optional)

Each one-shot command pays a full connect+handshake. Running the
local **agent broker** once per machine makes subsequent commands
reuse hot connections:

```bash
agbridge agent          # foreground; templates for login-start in examples/services/
```

The CLI uses the broker automatically whenever its socket exists —
no other flags change.

## CLI Verb Reference

| Verb | Use it for | Notes |
|---|---|---|
| `caps` | Inspect daemon policy | Best first call in a new setup |
| `exec` | Run one command | Non-interactive, no TTY; `--cwd`, `--env`, `--exec-timeout` |
| `cat` / `stat` / `ls` / `grep` | Inspect remote files & contents | Inline results capped at 10 MiB |
| `write` / `edit` | Write a file / replace text in place | `edit` is an atomic rewrite with backup |
| `put` / `get` | Move whole files | No 10 MiB cap; `get` parallelises at ≥16 MiB |
| `cd` / `pwd` | Session working directory | Stored per target in client state |
| `targets` / `status` / `doctor` / `caps` | Self-service diagnostics | All support `--json` |

The full flag-level reference is `agbridge <verb> --help` and
[README.md](README.md#cli-reference).

## Troubleshooting

### `unknown_target`

`-t` names a daemon that is not in your local state. List what this
client may talk to:

```bash
agbridge targets
```

### Daemon never reaches `handshake ok`

Check:

- The daemon can reach the gateway hostname and port.
- The gateway process is running.
- The token was issued against the same gateway state.
- The gateway audit log contains `auth_failed`, `daemon_offline`, or
  other route/auth events.

If you re-enrolled a device with the same name, revoke the stale record
and re-issue a token:

```bash
agbridge gateway-revoke --config /etc/agbridge/gateway.yaml --name lab01
```

Then send SIGHUP to the running gateway process.

### `tool_forbidden` on `exec`

The daemon was enrolled from a token issued without `--exec-enabled`,
so it refuses `exec` outright. Policy is baked in at issue time:
re-issue the daemon token with `--exec-enabled` and re-enroll.
Beyond that gate the daemon has no path allowlist — file and command
access is exactly what the daemon's (unprivileged, never-root) OS
user can do.

### `exceeds_max_size`

Inline results are capped at 10 MiB: `cat` (file content) and
`write` / `edit` payloads. `exec` stdout/stderr are additionally
truncated at 1 MiB per stream. Move whole files with `put` / `get`
instead — they stream in chunks and have no such cap.

### State Locations

| Role | Files |
|---|---|
| daemon | `~/.config/agbridge/state.json`, `daemon_noise.key` |
| client (bridge) | `~/.config/agbridge/state.json`, `bridge_noise.key` |
| gateway | `gateway.yaml`, `gateway-state.json`, `tokens.json`, audit JSONL |

## Going Further

- [README](README.md): architecture, security model, and CLI reference.
- [docs/deployment.md](docs/deployment.md): production gateway hosting,
  credential lifecycle, monitoring.
- [examples/](examples/): AI-agent/`--json` cookbook and login-start
  service templates.
- [ROADMAP](ROADMAP.md): shipped work and known next items.
- `agbridge --help`: command and flag reference.
