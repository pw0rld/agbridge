# agbridge quickstart

This walkthrough connects an AI assistant to a remote machine through
agbridge:

```
AI client -> agbridge bridge -> gateway -> agbridge daemon -> remote machine
```

The bridge is a local stdio MCP server. The daemon runs on the machine
where files, commands, and private services live. Both bridge and daemon
dial the gateway outbound over TLS WebSocket.

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
- The bridge is named `peter-laptop`.
- The daemon is allowed to operate under `/home/me/projects`.

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
  --allowed-paths /home/me/projects \
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

## 3. Enroll the Bridge

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

Bridge enrollment writes:

```text
~/.config/agbridge/state.json
~/.config/agbridge/bridge_noise.key
```

Do not start `agbridge bridge` by hand for normal use. Your MCP client
will spawn it.

## 4. Register the MCP Server

The bridge is a stdio MCP server. Every MCP client needs:

- Command: `agbridge`
- Args: `bridge --state-dir <state-dir>`
- Env: none

### Codex CLI

Append to `~/.codex/config.toml`:

```toml
[mcp_servers.agbridge]
command = "agbridge"
args = ["bridge", "--state-dir", "/home/peter/.config/agbridge"]
```

Restart Codex.

### Claude Code

```bash
claude mcp add agbridge agbridge -- bridge --state-dir /home/peter/.config/agbridge
```

Restart Claude Code or start a fresh session.

### Cline, Continue, and Generic JSON Clients

```json
{
  "mcpServers": {
    "agbridge": {
      "command": "agbridge",
      "args": ["bridge", "--state-dir", "/home/peter/.config/agbridge"]
    }
  }
}
```

Restart the client so it reloads the MCP tool list.

## 5. Try It

Ask the AI client to list MCP tools. You should see tools like:

```text
capabilities
exec
exec_start / exec_status / exec_logs / exec_kill / exec_list
read_file / write_file / edit_file
search
list_directory / stat
upload / download
port_forward / port_forward_list / port_forward_close
```

Useful first prompts:

```text
Use agbridge.capabilities and tell me what remote paths you can access.
```

```text
Use agbridge.list_directory on /home/me/projects.
```

```text
Use agbridge.search for "TODO" under /home/me/projects.
```

```text
Use agbridge.exec to run "go test ./..." in /home/me/projects/myrepo.
```

## Tool Reference

| Tool | Use it for | Notes |
|---|---|---|
| `capabilities` | Inspect daemon policy | Best first call in a new session |
| `exec` | Run one command | Non-interactive; default timeout is 5 minutes |
| `exec_start` / `_status` / `_logs` / `_kill` / `_list` | Manage background processes | For dev servers, watchers, and long-running jobs |
| `read_file` | Read remote text or binary | Inline cap 10 MiB; supports `offset` and `limit` |
| `write_file` | Write small files | Use `content` for text, `content_b64` for binary |
| `edit_file` | Replace text in place | Atomic rewrite, backup retained |
| `search` | Search file contents | Structured results; skips common cache/build dirs |
| `list_directory` / `stat` | Inspect filesystem | Avoids shell quoting and output parsing |
| `upload` | Send files to daemon | `local_path`, `url`, or inline `chunks`; cap 1 GiB |
| `download` | Bring files back to bridge host | Use `dest_path` for files over 10 MiB |
| `port_forward` / `_list` / `_close` | Forward local TCP to daemon side | Remote port denylist is enforced by daemon policy |

## Progress Notifications

`exec`, `read_file`, and `download` emit MCP
`notifications/progress` events when the client supplies
`_meta.progressToken`.

File progress reports bytes received so far. Exec progress reports
streamed output chunks. Clients that do not send a progress token get
the same final results without progress events.

## Troubleshooting

### `agbridge daemon` says `either --state-dir or --config is required`

Pass the state directory explicitly:

```bash
agbridge daemon --state-dir ~/.config/agbridge
```

### Client says `tool not found`

Restart the AI client. Most MCP clients read the tool list only at
startup.

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

### `path_forbidden`

The daemon policy does not allow that path. Use `capabilities` to inspect
allowed paths, or issue a new daemon token with a narrower or wider
policy and re-enroll the daemon.

### `exceeds_max_size`

Inline results are capped:

- `read_file`: 10 MiB
- `write_file`: 10 MiB
- inline `download`: 10 MiB
- `exec` stdout and stderr: 10 MiB each on the bridge side

For large reads, use `read_file` with `offset` and `limit`, or
`download` with `dest_path`.

### State Locations

| Role | Files |
|---|---|
| daemon | `~/.config/agbridge/state.json`, `daemon_noise.key` |
| bridge | `~/.config/agbridge/state.json`, `bridge_noise.key` |
| gateway | `gateway.yaml`, `gateway-state.json`, `tokens.json`, audit JSONL |

## Going Further

- [README](README.md): architecture, security model, and CLI reference.
- [ROADMAP](ROADMAP.md): shipped work and known next items.
- `agbridge --help`: command and flag reference.
