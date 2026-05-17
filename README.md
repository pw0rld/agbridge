<div align="center">

# agbridge

**An MCP gateway for AI agents to operate remote machines across restrictive networks.**

[![ci](https://github.com/pw0rld/agbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/pw0rld/agbridge/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/pw0rld/agbridge?include_prereleases&sort=semver)](https://github.com/pw0rld/agbridge/releases)
[![license](https://img.shields.io/github/license/pw0rld/agbridge)](LICENSE)

</div>

```
   ┌───────────────┐     ┌──────────────┐     ┌───────────────┐
   │   AI Agent    │     │              │     │               │
   │ (Claude Code, │     │   gateway    │     │    daemon     │
   │  Codex, ...)  │     │  (public IP) │     │ (lab/cloud/   │
   └───────┬───────┘     └───────┬──────┘     │  corporate)   │
           │ MCP/stdio           │            └───────┬───────┘
   ┌───────▼───────┐             │                    │
   │    bridge     │             │                    │
   └───────┬───────┘             │                    │
           │                     │                    │
           │  TLS:443 WSS  ──────►              ◄──── │   TLS:443 WSS
           │  (egress only)      │                    │   (egress only)
           └─────────────────────┴────────────────────┘
                       opaque relay (sees only metadata)
                                 │
                                 ▼
                    bridge ↔ daemon E2E encrypted
                       (Noise IK + ChaCha20-Poly1305)
```

Both bridge and daemon dial **outbound TLS:443**. The gateway is the only
listener; runs anywhere with a public IP (VPS, k8s ingress, Cloudflare
Tunnel backend). No inbound port on either lab machine or laptop.

---

## Why

You have an AI agent on your laptop (Claude Code, Codex, Cursor) and dev
resources on a machine somewhere — corporate lab, home GPU, cloud VM,
campus network. The path between them is broken by NAT, zero-trust
firewalls, or both.

agbridge gives the agent a small set of MCP tools that work over a single
egress TLS:443 connection. No VPN, no router port forward, no public sshd.

## Features

- **4 MCP tools** — `exec`, `read_file`, `write_file`, `port_forward`
- **End-to-end encryption** — Noise IK + ChaCha20-Poly1305 + BLAKE2s. Gateway sees only ciphertext
- **Cert pinning** — SHA-256 over self-signed cert, no system CA dependency
- **Daemon ACL** — pin which bridge pubkeys may connect (defense against gateway compromise)
- **Enroll flow** — paste-command onboarding, no yaml or scp on device side
- **Auto-reconnect + SIGHUP revoke + audit log + sandbox** (non-root, path/port allowlists, env whitelist)

## Install

```bash
curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash
```

Pinned version: `VERSION=v0.1.0-phase-b curl … | bash`. From source:
`go install github.com/pw0rld/agbridge/cmd/agbridge@latest`.

## Quickstart

### 1. Gateway operator — start the rendezvous server

```bash
# One-time: generate cert (or reuse an existing one)
agbridge cert gen --cn gw.example.com --out /etc/agbridge

# Start gateway. Listens TLS:443 for both WSS and POST /v1/enroll.
agbridge gateway \
  --config /etc/agbridge/gateway.yaml \
  --cert /etc/agbridge/cert.pem \
  --key /etc/agbridge/key.pem
```

Minimal `gateway.yaml`:

```yaml
listen: 0.0.0.0:443
public_url: wss://gw.example.com/
audit_path: /var/log/agbridge/audit.jsonl
agents: []   # filled by enroll
daemons: []  # filled by enroll
```

### 2. Issue tokens and onboard devices — no scp, no yaml

```bash
# Daemon side: operator mints a token and sends the paste-command.
agbridge issue-token \
  --config /etc/agbridge/gateway.yaml \
  --role=daemon --name=lab01 \
  --allowed-paths=/home/me/projects
```

Output ends with a paste-command like:

```
Paste on the daemon machine:

  curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash && \
  agbridge enroll --gateway wss://gw.example.com/ --token et_8VxR2zHKp9NmQwLs…
```

The daemon operator pastes that on their lab machine. The enroll command
runs a 7-stage diagnostic, generates a Noise keypair locally, exchanges
the token for an api_key + policy, and writes `~/.config/agbridge/state.json`.

Then start the daemon:

```bash
agbridge daemon --state-dir ~/.config/agbridge &
```

### 3. Onboard the bridge (laptop)

```bash
# Operator: mint a bridge token targeting the daemon you just enrolled.
agbridge issue-token \
  --config /etc/agbridge/gateway.yaml \
  --role=bridge --name=peter-laptop --target=lab01
```

Paste the resulting command on the laptop. Then wire agbridge into your
MCP client:

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

That's it. The agent now sees `exec`, `read_file`, `write_file`, and
`port_forward` over a Noise-encrypted TLS:443 channel. **No yaml on the
device, no cert pin to copy, no scp.**

## Two ways to use agbridge

agbridge's four tools sit on different points of the power-vs-risk axis.

### Recommended: Pure Tunnel + sshd

Let agbridge do what it's uniquely good at — egress-only firewall
traversal — and let `sshd` handle auth/audit/session management.

```
        AI agent
           │ tool call: port_forward(127.0.0.1, 22, 2222)
           ▼
        bridge ━━━━━━━━━━━ agbridge (encrypted) ━━━━━━━━━━━ daemon
                                                              │
                                                              ▼
                                                            sshd
                                          AI agent: ssh -p 2222 …
```

You get per-key authorization (`authorized_keys`, `command=`, `from=`,
`ForceCommand`), standard audit (sshd + auditd), and any tool that
composes over SSH (rsync, sshfs, scp, …).

### Advanced: full MCP RPC

For environments without sshd (audit-only filesystem, locked-down
container, corporate policy against SSH endpoints), enable `exec`,
`read_file`, `write_file`. Their security relies on agbridge's own
sandbox — narrow `--allowed-paths` at issue-token time is your defense.

## MCP tool reference

| Tool | Parameters | What it does |
|---|---|---|
| `exec` | `cmd`, `args`, `cwd`, `env`, `timeout_ms` | Spawn subprocess (own pgroup, SIGKILL on timeout), stream stdout/stderr back |
| `read_file` | `path`, `max_size` | Stream file back, sha256 in `_meta` |
| `write_file` | `path`, `content_b64`, `mode` | Atomic write via temp + rename |
| `port_forward` | `remote_host`, `remote_port`, `local_port` | Bind local listener, forward TCP to daemon |

10 MB cap per `exec` stream and per file read/write.
`port_forward` blocks `forbidden_ports` set at issue-token time.

## Security model

| Layer | What it protects |
|---|---|
| TLS 1.3 + cert pin | Wire encryption + middlebox tampering (no system CA) |
| HMAC-SHA256 | Per-frame integrity bridge → gateway (api_key in memory only) |
| Noise IK (E2E) | Inner payload invisible to gateway when `e2e_mode=required` or `optional` |
| Daemon ACL | Pinned bridge pubkeys; in `strict` mode, no bridge connects unless on the allowlist |
| Path allowlist | `exec` / `read_file` / `write_file` confined to issue-token-supplied paths |
| `forbidden_ports` | `port_forward` blocks dangerous targets |
| Non-root daemon | `sandbox.RefuseRoot` rejects root at startup unless test env flag set |
| Env whitelist | Only `PATH` / `HOME` / `LANG` passed by default |

### E2E modes

- `disabled` — pre-v0.1.0 behavior, gateway sees frames (legacy yaml fallback only)
- `optional` — Noise IK still negotiated; daemon with empty allowlist accepts any bridge under the same tenant (default for new daemon enrollments)
- `required` — daemon refuses to start unless `allowed_bridge_pubkeys` is non-empty; operator must set `Policy.Strict` via issue-token to opt in

Bridges always enroll in `required` mode; daemons default to `optional` to avoid the chicken-and-egg of enrolling before any bridge exists.

## CLI

### Gateway operator

| Command | Purpose |
|---|---|
| `agbridge gateway --config … --cert … --key …` | Run the rendezvous server |
| `agbridge issue-token --role bridge\|daemon --name …` | Mint a one-shot token (15min TTL by default) |
| `agbridge gateway-list-devices --config …` | List enrolled bridges + daemons + outstanding tokens |
| `agbridge gateway-revoke --config … --name …` | Drop an enrolled device from `gateway-state.json` (SIGHUP gateway to take effect) |

### Device

| Command | Purpose |
|---|---|
| `agbridge enroll --gateway URL --token et_…` | Onboard a fresh device (7-stage diagnostic) |
| `agbridge bridge --state-dir DIR` | Run as MCP bridge |
| `agbridge daemon --state-dir DIR` | Run as remote-operation daemon |
| `agbridge config show` | Print local state (api_key redacted) |
| `agbridge doctor` | Probe gateway connectivity from current state |
| `agbridge logout` | Wipe local state.json + keys |

### Helpers

| Command | Purpose |
|---|---|
| `agbridge keygen --type=secret` | Random 32-byte secret + sha256 hash |
| `agbridge keygen --type=noise` | X25519 keypair (manual Noise pin scenarios) |
| `agbridge cert gen --cn HOST` | Self-signed cert + SHA-256 pin |

All accept `--json` for AI-driven automation.

## Architecture detail

```
┌────────────────────────────────────────────────────────┐
│  L4  Inner Payload (JSON, type-specific)              │
│      ExecRequest / FileChunk / StreamData / ...       │
├────────────────────────────────────────────────────────┤
│  L3  E2E AEAD envelope (optional, default for v0.1.0+)│
│      Noise IK session key + ChaCha20-Poly1305         │
├────────────────────────────────────────────────────────┤
│  L2  proto.Frame (binary, length-prefixed)            │
│      version|type|reqid_len|reqid|payload_len|payload │
├────────────────────────────────────────────────────────┤
│  L1  WebSocket Binary Messages (1 frame per msg)      │
│      Ping/Pong @30s, dead conn detected @90s          │
├────────────────────────────────────────────────────────┤
│  L0  TLS 1.3 + cert pin (sha256:<hex>)                │
└────────────────────────────────────────────────────────┘
```

The gateway runs a single TLS:443 server that routes by header:
WebSocket-Upgrade requests go to the bridge/daemon multiplexer; POST
`/v1/enroll` goes to the enroll handler. Both share the same cert.

## Resilience

- **Reconnect**: exponential backoff `Base=500ms`, `Cap=30s`, ±20% jitter. In-flight calls return `network_lost` with `retryable=true`.
- **Keepalive**: WSS Ping every 30s; conn closed if no Pong within 90s.
- **SIGHUP**: gateway re-reads `gateway.yaml` AND `gateway-state.json`, revokes sessions whose principal was removed or whose key rotated.
- **Audit rotation**: synchronous JSONL rotation by `audit_max_bytes` / `audit_max_backups`.

## Status

| Milestone | Status |
|---|---|
| Phase 1-5 (MVP, 4 tools + resilience) | shipped |
| Phase A (Noise IK E2E) | shipped (`v0.1.0-phase-a`) |
| Phase B (state.json + enroll flow + zero-scp UX) | shipped (`v0.1.0-phase-b`) |
| 7×24h uptime validation | not done |
| seccomp / cgroup sandbox | deferred |
| Multi-bridge per daemon | deferred |
| macOS / Windows daemon | deferred |

## Known limitations

- Single bridge per daemon — multiple bridges targeting the same daemon unsubscribe each other from the daemon proxy on connect (multi-bridge is v0.2).
- E2E session keys are scoped to one WSS connection; rekey-while-connected is deferred (forward secrecy on reconnect is sufficient).
- Daemon `allowed_bridge_pubkeys` is static; widening requires re-enrolling the daemon with the new pubkey baked into `Policy.AllowedBridgePubkeys`.
- 7×24h uptime / 30% loss tolerance not yet validated on three real machines.

## License

[MIT](LICENSE)
