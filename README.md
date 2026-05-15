# agbridge

AI agent remote operation surface over restrictive networks (TLS:443 + MCP).

See the [design spec](https://github.com/pw0rld/my-wiki/blob/main/wiki/ai-research/plan/agbridge.md) for goals.

Status: Phase 5 — four MCP tools end-to-end plus production-grade
resilience: bridge + daemon auto-reconnect with exponential backoff,
WebSocket-layer keepalive, SIGHUP-driven credential reload with session
revocation, and size-based audit log rotation.

## Build

```
go build ./cmd/agbridge
```

## Quickstart (3-machine deploy, one command)

`agbridge bootstrap` generates everything in one shot — cert, key, and
three aligned YAML configs — so you don't have to run `openssl`, compute
hashes, or paste pins between files.

```
agbridge bootstrap \
  --gateway-url wss://gw.example.com/ \
  --agent claude-laptop \
  --daemon lab01 \
  --allowed-paths /home/me/projects \
  --out ./agbridge-cfg
```

Output (in `./agbridge-cfg/`):
- `cert.pem` + `key.pem` — deploy to the gateway host
- `gateway.yaml` — deploy to the gateway host
- `daemon.yaml` — deploy to the daemon host
- `bridge.yaml` — stays on the laptop where the MCP client runs

The command prints next-steps with the `scp` commands and the MCP client
config snippet you need. For automation, pass `--json` to get a structured
result the calling agent can parse.

**Deploy and run:**

```
# Gateway host
scp cert.pem key.pem gateway.yaml gw-host:/etc/agbridge/
ssh gw-host 'agbridge gateway --config /etc/agbridge/gateway.yaml --cert /etc/agbridge/cert.pem --key /etc/agbridge/key.pem'

# Daemon host (run as non-root)
scp daemon.yaml daemon-host:/etc/agbridge/
ssh daemon-host 'agbridge daemon --config /etc/agbridge/daemon.yaml'

# Bridge host: register with your MCP client (Claude Code etc.)
#   { "mcpServers": { "agbridge": { "command": "agbridge",
#       "args": ["bridge", "--config", "/path/to/bridge.yaml"] } } }
```

The agent will see four tools: `exec`, `read_file`, `write_file`, `port_forward`.

## Helper subcommands

`bootstrap` is the orchestrator. The individual building blocks are also
exposed for non-default setups:

```
agbridge cert gen --cn gw.example.com --out /etc/agbridge
# Writes cert.pem + key.pem; prints SHA-256 pin

agbridge keygen
# Random base64 secret + sha256 hash (used for API keys and daemon tokens)
```

Both accept `--json` for machine-readable output.

## Tools

- **exec** — params: `cmd`, `args`, `cwd`, `env`, `timeout_ms`. Runs a
  subprocess on the daemon side, streams stdout/stderr back. Returns
  `exitcode`, `duration_ms`, base64-encoded output in `_meta`.
- **read_file** — params: `path`, `max_size`. Streams a file back in 64 KB
  chunks. Returns `size`, `sha256`, `content_b64` in `_meta`. UTF-8 valid
  content is also surfaced as text in the result body.
- **write_file** — params: `path`, `content_b64`, `mode`. Atomic write via
  temp file plus rename. Defaults to `mode=0644`. Returns `bytes_written`,
  `sha256`.
- **port_forward** — params: `remote_host`, `remote_port`, `local_port`.
  Binds a local TCP listener (`local_port=0` lets the OS pick) and shuttles
  each accepted connection to `remote_host:remote_port` on the daemon
  machine over a multiplexed stream. Returns `local_host`, `local_port` in
  `_meta`.

The 10 MB cap applies to stdout/stderr per `exec` call and to total content
per `read_file` / `write_file`. `port_forward` streams are uncapped.

## Architecture

```
[AI Agent] --MCP/stdio--> [bridge] --TLS:443 WSS--> [gateway] <--TLS:443 WSS-- [daemon] --subprocess--> [your shell]
```

bridge HMAC-signs every frame; gateway verifies + audits; daemon enforces
non-root + cwd allowlist + env whitelist.

## Resilience (Phase 5)

- **Reconnect**: bridge and daemon both wrap their gateway dial in an
  exponential-backoff loop (`Base=500ms`, `Cap=30s`, ±20% jitter). After a
  WSS drop, in-flight tool calls return `network_lost` with
  `retryable=true` so the agent can retry; subsequent calls go through the
  fresh conn transparently.
- **Keepalive**: every wss.Conn ticks WebSocket-layer Ping every 30s and
  closes the conn if no Pong arrives within 90s — dead connections are
  detected within ~2 minutes instead of waiting for TCP to time out.
- **SIGHUP**: gateway re-reads its YAML config on `SIGHUP`. New agents /
  daemons / rotated hashes take effect immediately; sessions whose
  principal was removed (or whose key was rotated) are revoked surgically
  without disturbing siblings.
- **Audit rotation**: when `audit_max_bytes > 0` and `audit_max_backups > 0`,
  the JSONL writer rotates synchronously before each over-budget write:
  oldest `.N` is dropped, others shift up, active file renamed to `.1`.
  Set both to zero for legacy single-file append.

## Manual setup (without `bootstrap`)

For multi-tenant configs or custom layouts, you can build the YAMLs by
hand. The 5 fields that must line up across files:

| Source | Field | Used in |
|---|---|---|
| `agbridge cert gen` | SHA-256 pin | `daemon.yaml.cert_pin`, `bridge.yaml.cert_pin` |
| `agbridge keygen` (#1) | secret | `bridge.yaml.api_key` |
| same (#1) | hash | `gateway.yaml.agents[].api_key_hash` |
| `agbridge keygen` (#2) | secret | `daemon.yaml.registration_token` |
| same (#2) | hash | `gateway.yaml.daemons[].token_hash` |

See `agbridge {gateway,daemon,bridge} --help` for the full YAML schema.

## Known limitations

- Single-bridge-per-daemon (multiple bridges targeting the same daemon
  unsubscribe each other from the daemon proxy on connect).
- Tested in-process and via binary smoke tests; manual three-machine
  deployment not yet verified for 7×24h uptime / 30% loss tolerance.
- Per-tool ACL (`allowed_tools`) and seccomp/cgroup hardening are deferred
  to v0.2.
