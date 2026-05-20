# agbridge quickstart

A 5-minute walkthrough to wire agbridge into your AI assistant of choice
(Codex CLI, Claude Code, Cline, Continue, or any other MCP host).

agbridge is **stdio-MCP on the client side, WSS on the wire**. Your AI
client spawns a small `agbridge bridge` process locally; that bridge
forwards tool calls over a TLS+Noise tunnel to a daemon on your dev
machine.

## 0. Pre-flight

You need:

- A reachable gateway (own host or hosted) — see [README](README.md)
  for setup if you don't have one.
- The `agbridge` binary on the dev machine (where files live) **and** on
  the agent host (where the AI client runs). Same binary; same release.

```bash
# install (Linux/macOS, x86_64/arm64)
curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash
agbridge --help
```

## 1. Enroll a daemon (on the dev machine)

On the **gateway host**, mint a one-shot enrollment token:

```bash
agbridge issue-token \
  --role daemon \
  --name my-laptop \
  --allowed-paths "$HOME/code" \
  --allowed-paths "$HOME/code/*" \
  --ttl 30 \
  --json
```

The output prints a `token` field and a ready-to-paste `paste_command`.

On the **dev machine**, run that paste command (or equivalent):

```bash
agbridge enroll \
  --gateway wss://gw.example.com/ \
  --token et_XXXXXXXXXXXXXXXXXX
```

The daemon's state — device id, api key, Noise key — is written under
`~/.config/agbridge/`. Start it:

```bash
agbridge daemon
# → "daemon: handshake ok, awaiting Route frames"
```

Keep it running. Use tmux, systemd, or your supervisor of choice.

## 2. Enroll a bridge (on the agent host)

Same idea, different role:

```bash
# on the gateway host
agbridge issue-token \
  --role bridge \
  --name my-laptop-agent \
  --target my-laptop \
  --ttl 30 \
  --json
```

```bash
# on the agent host
agbridge enroll \
  --gateway wss://gw.example.com/ \
  --token et_YYYYYYYYYYYYYYYYYY
```

State lands in `~/.config/agbridge/`. **Do not start the bridge by
hand** — your MCP client will spawn it on demand.

## 3. Wire the bridge into your MCP client

The bridge is a stdio MCP server. Every client needs the same three
things:

- Command: `agbridge`
- Args: `bridge` (optionally `--state-dir <path>` if you used a custom
  location)
- Env: none required

### Codex CLI

Append to `~/.codex/config.toml`:

```toml
[mcp_servers.agbridge]
command = "agbridge"
args = ["bridge"]
```

Restart Codex.

### Claude Code

```bash
claude mcp add agbridge agbridge -- bridge
```

Restart Claude Code (or `/clear` for a fresh session).

### Cline / Continue / generic JSON-config clients

```json
{
  "mcpServers": {
    "agbridge": {
      "command": "agbridge",
      "args": ["bridge"]
    }
  }
}
```

Restart the client.

## 4. Confirm and try it

After the client restarts, ask it to list tools — you should see
`exec`, `read_file`, `write_file`, `edit_file`, `download`, `upload`,
`list_directory`, `stat`, `port_forward`, `search`, and a handful of
`exec_*` background-process tools.

Try:

```
read the README at <a path inside your daemon's allow-list>
list everything in <some directory on the dev machine>
download <a binary file> to /tmp/<filename> on this machine
```

## Tool quick-reference

| Tool | Use it for | Notes |
|---|---|---|
| `exec` | Run a command remotely | 10 MB stdout/stderr cap; own pgroup; SIGKILL on timeout |
| `read_file` | Read text/binary | 10 MB inline cap; `offset`/`limit` for partial reads; progress events |
| `write_file` | Atomic write via temp + rename | 10 MB content cap |
| `edit_file` | `old_string` → `new_string` partial edit | Auto-GC'd backups |
| `download` | Stream a file back to the agent host | With `dest_path`: streams to local disk, no inline cap. Without: 10 MB inline cap, returns content |
| `upload` | Send a file to the daemon machine | Inline bytes or URL fetch |
| `list_directory` / `stat` | Filesystem inspection | — |
| `port_forward` / `_list` / `_close` | Local-port → remote TCP | Blocks `forbidden_ports` set at enrollment |
| `search` | Pure-Go content search | Replaces `exec grep` |
| `exec_start` / `_status` / `_logs` / `_kill` / `_list` | Background processes | Long-running servers, watchers, etc. |

### Progress notifications

`read_file`, `download`, and `exec` emit MCP `notifications/progress`
events when the client supplies `_meta.progressToken` in the request.
Clients that don't send a token see byte-identical pre-progress
behaviour. No throttling — events fire per chunk (1 MiB for file
streams).

## Troubleshooting

**Daemon never prints "handshake ok"**
Auth failure. Check the gateway audit log for `auth_failed` events.
If the gateway state has a stale device record (e.g. you re-enrolled
without revoking), `gateway-revoke --name <device>` on the gateway host,
then SIGHUP the gateway and re-issue the token.

**Client says "tool not found"**
Restart the client. MCP tool lists are fetched once at startup.

**`exceeds_max_size` on `read_file` or inline `download`**
File over 10 MB. Use `read_file` with `offset`/`limit` for partial
reads, or `download` with `dest_path` to stream the whole thing to
local disk.

**Where state lives**
- Daemon: `~/.config/agbridge/state.json` + `daemon_noise.key`
- Bridge: `~/.config/agbridge/state.json` + `bridge_noise.key`
- Gateway state (source of truth): `<gateway-root>/etc/gateway-state.json`

## Going further

- [README](README.md) — architecture overview, security model, full
  command reference
- [ROADMAP](ROADMAP.md) — what shipped, what's next
- `agbridge --help`, `agbridge <command> --help` — every flag is
  documented inline
