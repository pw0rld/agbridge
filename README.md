# agbridge

AI agent remote operation surface over restrictive networks (TLS:443 + MCP).

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

## Two ways to use agbridge

agbridge ships four MCP tools but they sit at very different points on the
power-vs-risk axis. **Most deployments should use the Pure Tunnel mode**
and treat the other tools as opt-in escape hatches for environments that
can't run sshd.

### Recommended: Pure Tunnel + sshd

Let agbridge do what it's uniquely good at — egress-only NAT/firewall
traversal — and let `sshd` do what it's been hardened for over 25 years
(authentication, authorization, audit, session management).

1. On the daemon machine, make sure `sshd` is running with a normal
   user account that has `authorized_keys` for your laptop.
2. Use **only** `port_forward` to expose the daemon's port 22 to your
   laptop:

   ```
   port_forward(remote_host=127.0.0.1, remote_port=22, local_port=2222)
   ```
3. Your AI client (Claude Code etc.) uses its native bash tool to ssh:

   ```
   ssh -p 2222 lab-user@localhost "make test"
   scp -P 2222 -r lab-user@localhost:/home/lab-user/project ~/local-copy
   ```

You get:
- Per-key authorization (`authorized_keys` with `command=` restrictions,
  `from=` IP allowlists, `ForceCommand`, restricted shells).
- Standard audit trail (sshd logs + auditd + your existing pipeline).
- Whatever standard tool composes well over an SSH session.

What you don't get:
- A single "MCP tool" abstraction for command execution.
- Per-call HMAC + audit at the agbridge protocol layer.

For most internal-network scenarios where you control the lab machine,
this is the right trade.

### Advanced: full MCP RPC (use when you can't run sshd)

If your daemon host can't run sshd (locked-down container, audit-only
filesystem, a corporate policy that forbids opening new SSH endpoints,
etc.), enable the other three tools. They're more convenient for the AI
to call directly but their security relies on agbridge's own sandbox —
not on Unix's user model.

- **exec** — params: `cmd`, `args`, `cwd`, `env`, `timeout_ms`. Runs a
  subprocess on the daemon side, streams stdout/stderr back. Returns
  `exitcode`, `duration_ms`, base64-encoded output in `_meta`.
- **read_file** — params: `path`, `max_size`. Streams a file back in
  64 KB chunks. Returns `size`, `sha256`, `content_b64` in `_meta`.
  UTF-8 valid content is also surfaced as text in the result body.
- **write_file** — params: `path`, `content_b64`, `mode`. Atomic write via
  temp file plus rename. Defaults to `mode=0644`. Returns `bytes_written`,
  `sha256`.

The 10 MB cap applies to stdout/stderr per `exec` call and to total content
per `read_file` / `write_file`.

When using these tools, keep `allowed_exec_cwds` / `allowed_read_paths` /
`allowed_write_paths` as narrow as your workflow allows — they are your
only line of defense against AI misbehavior.

### Always available

- **port_forward** — params: `remote_host`, `remote_port`, `local_port`.
  Binds a local TCP listener (`local_port=0` lets the OS pick) and shuttles
  each accepted connection to `remote_host:remote_port` on the daemon
  machine over a multiplexed stream. Returns `local_host`, `local_port` in
  `_meta`. `forbidden_ports` in daemon.yaml blocks dangerous targets
  (default suggestion: include 22 only if you're NOT using the tunnel
  pattern above).

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

- **No per-tool toggle yet**. The Pure Tunnel recommendation is currently
  documentation-only — the daemon enables all four tools as long as their
  allowed_*_paths / forbidden_ports are configured. Per-tool
  `enabled_tools` config and a `strict_mode: port_forward only` are
  planned for the next milestone. Until then, the most defensive thing
  you can do is leave `allowed_exec_cwds` / `allowed_read_paths` /
  `allowed_write_paths` empty in daemon.yaml — that effectively disables
  exec / read_file / write_file by always-deny.
- Single-bridge-per-daemon (multiple bridges targeting the same daemon
  unsubscribe each other from the daemon proxy on connect).
- Tested in-process and via binary smoke tests; manual three-machine
  deployment self-test passed (2026-05-15) but 7×24h uptime / 30% loss
  tolerance not yet validated.
- Per-agent ACL (`allowed_tools` on the gateway side) and seccomp/cgroup
  hardening are deferred to v0.2.
