<div align="center">

# agbridge

**A one-shot CLI for AI agents to operate remote machines over outbound-only TLS.**

[![ci](https://github.com/pw0rld/agbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/pw0rld/agbridge/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/pw0rld/agbridge?include_prereleases&sort=semver)](https://github.com/pw0rld/agbridge/releases)
[![license](https://img.shields.io/github/license/pw0rld/agbridge)](LICENSE)

</div>

agbridge gives AI assistants a structured CLI for operating machines
that are hard to reach directly: lab workstations, cloud VMs behind
firewalls, home servers, customer VPCs, GPU boxes, CI runners, and
corporate networks. Each AI invocation spawns a fresh `agbridge <cmd>`
subprocess that dials the gateway, runs one tool, and exits.

Both sides dial out over TLS WebSocket. The gateway is the only public
listener. Tool payloads can be end-to-end encrypted between the CLI
client and the remote daemon, so the gateway acts as a relay and control
plane rather than a remote shell host.

```
   ┌───────────────┐     ┌──────────────┐     ┌───────────────┐
   │   AI Agent    │     │              │     │               │
   │ (Codex,       │     │   gateway    │     │    daemon     │
   │  Claude, ...) │     │  (public IP) │     │ (lab/cloud/   │
   └───────┬───────┘     └───────┬──────┘     │  corporate)   │
           │ spawns subprocess   │            └───────┬───────┘
   ┌───────▼───────┐             │                    │
   │ agbridge <cmd>│             │                    │
   │  (one-shot)   │             │                    │
   └───────┬───────┘             │                    │
           │                     │                    │
           │  TLS:443 WSS  ──────►              ◄──── │   TLS:443 WSS
           │  outbound only      │                    │   outbound only
           └─────────────────────┴────────────────────┘
                       relay + enrollment + audit
                                 │
                                 ▼
                    CLI <-> daemon E2E payloads
                       Noise IK + ChaCha20-Poly1305
```

## Why

SSH, VPNs, and generic tunnels are excellent once the network path and
authorization model are already solved. AI agent workflows often fail
earlier:

- The target machine cannot accept inbound connections.
- The operator does not want to expose SSH or a broad VPN route.
- The AI client needs structured tool results instead of terminal text.
- Security wants path, port, environment, and audit boundaries around
  what the agent can do.

agbridge is designed for that gap. It is not a VPN replacement and it is
not trying to beat OpenSSH at SSH. It provides an AI-native remote
operation plane: bounded tools, typed errors, discoverable capabilities,
file streaming, and optional TCP forwarding.

## Components

| Component | Runs on | Purpose |
|---|---|---|
| `agbridge gateway` | Public host or ingress | Authenticates devices, relays frames, serves `/v1/enroll`, writes audit logs |
| `agbridge daemon` | Remote machine | Executes tools under sandbox policy and dials the gateway outbound |
| `agbridge <subcommand>` (CLI on AI client host) | AI client host | One-shot subcommands (`ls`, `cat`, `exec`, etc.) that dial the gateway, invoke one tool, and exit. AI agents drive these via subprocess spawning. |

The CLI client and daemon can be behind NAT or restrictive firewalls as
long as they can make outbound TLS connections to the gateway.

## Features

- **CLI tool surface**: `agbridge ls / cat / exec / write / edit / search / put / get / stat / caps / status / targets / config use / ai-help` covering remote filesystem, command execution, and file transfer.
- **Outbound-only networking**: no inbound port on the AI client host or
  daemon host.
- **End-to-end payload encryption**: Noise IK with ChaCha20-Poly1305;
  the gateway relays encrypted inner frames when E2E mode is enabled.
- **Cert pinning**: SHA-256 pin over the gateway certificate, no system
  CA trust required.
- **Enrollment flow**: one-shot tokens and paste-command onboarding.
  Devices generate keys locally and write `state.json`.
- **Daemon sandbox**: non-root startup check, path allowlists, port
  denylist, environment allowlist, file size caps, and typed errors.
- **Operational resilience**: reconnect with backoff, WebSocket
  keepalive, SIGHUP revocation, and rotating JSONL audit logs.

## Install

```bash
curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash
```

From source:

```bash
go install github.com/pw0rld/agbridge/cmd/agbridge@latest
```

## Quickstart

Install the binary (see Install above), then enroll your device with a
one-shot token from your gateway operator:

```bash
agbridge enroll <token>
agbridge status   # verify the connection
```

Use from your AI:

- Tell the AI to run `agbridge` commands directly — no MCP setup required.
- Run `agbridge ai-help` to print a cheat sheet you can paste into the
  AI's system prompt.

For operators self-hosting the gateway, see [QUICKSTART.md](QUICKSTART.md)
for the full walkthrough (gateway setup, token issuance, daemon enrollment).

## Recommended Use

agbridge has two useful modes. They can be mixed.

### Direct CLI

Use the CLI directly when SSH is not available, when you want tighter
tool-level control, or when AI agents benefit from structured results.

Examples:

- `agbridge grep <pattern> <path>` instead of `ssh grep`
- `agbridge cat <path>` with `--offset` / `--limit` instead of `ssh cat`
- `agbridge edit <path> --old <s> --new <s>` with an exact old string
  instead of remote shell edits
- `agbridge get <remote> <local>` for downloading files

### Tunnel to SSH

When `agbridge` ships TCP forwarding (planned v0.0.3 via local daemon
mode), the recommended pattern for interactive shells is to use the CLI
for egress-only reachability and let `sshd` handle accounts, authorized
keys, and mature audit controls.

## CLI Reference

See `agbridge --help` for the full command list. v0.0.2 surface:

| Command | Purpose |
|---|---|
| `agbridge status` | Connection check |
| `agbridge targets` | List known daemons |
| `agbridge config use <name>` | Set default target |
| `agbridge caps` | Show daemon sandbox / version |
| `agbridge ai-help` | Cheat sheet for AI system prompts |
| `agbridge ls <path>` | List directory |
| `agbridge cat <path>` | Read file |
| `agbridge stat <path>` | File metadata |
| `agbridge grep <pattern> [path]` | Search file contents |
| `agbridge write <path>` | Write file (`--content` or `--from-file`) |
| `agbridge edit <path> --old <s> --new <s>` | Find-and-replace |
| `agbridge exec -- <cmd>` | Run command remotely |
| `agbridge put <local> <remote>` | Upload |
| `agbridge get <remote> <local>` | Download |

Global flags: `-t/--target`, `--json`, `--state-dir`, `--timeout`.

Output: human-readable by default; `--json` switches to stable structured
shape with top-level `_schema:1`.

Errors: exit `0` on success, `1` on error. Stderr carries single-line
JSON `{"code":"...","message":"...","retry_safe":bool}` on failure.

## Security Model

| Layer | What it protects |
|---|---|
| TLS 1.3 + cert pin | Transport encryption and gateway identity without system CA trust |
| HMAC-SHA256 | Bridge-to-gateway Route frame integrity using the bridge API key |
| Noise IK E2E | Inner tool payload confidentiality from the gateway |
| Daemon bridge ACL | Optional bridge public-key allowlist on the daemon |
| Path allowlists | `exec`, read, write, edit, search, upload, and download are scoped to enrolled paths |
| Port denylist | `port_forward` refuses configured remote ports |
| Non-root daemon | Daemon refuses to run as root outside tests |
| Environment allowlist | Only configured environment variables are forwarded to subprocesses |

### E2E Modes

- `disabled`: legacy plaintext inner frames; gateway can see tool
  payloads.
- `optional`: Noise IK is negotiated; an empty daemon bridge allowlist
  accepts authenticated bridges for the same tenant.
- `required`: daemon requires a non-empty `allowed_bridge_pubkeys` list
  and rejects unpinned bridges.

New bridge enrollments use `required`. New daemon enrollments default to
`optional` so the first daemon can be enrolled before any bridge public
key exists.

## Operator Commands

Gateway operator commands:

| Command | Purpose |
|---|---|
| `agbridge gateway --config ... --cert ... --key ...` | Run the rendezvous server |
| `agbridge issue-token --role bridge|daemon --name ...` | Mint one-shot enrollment tokens |
| `agbridge gateway-list-devices --config ...` | List enrolled devices and outstanding tokens |
| `agbridge gateway-revoke --config ... --name ...` | Remove a device from gateway state |

Device commands:

| Command | Purpose |
|---|---|
| `agbridge enroll --gateway URL --token et_...` | Enroll a CLI client or daemon with diagnostics |
| `agbridge daemon --state-dir DIR` | Run the remote daemon |
| `agbridge config show` | Print local state with secrets redacted |
| `agbridge doctor` | Probe gateway reachability from local state |
| `agbridge logout` | Remove local state and key files |

Helpers:

| Command | Purpose |
|---|---|
| `agbridge cert gen --cn HOST` | Generate a self-signed TLS cert and SHA-256 pin |
| `agbridge keygen --type secret` | Generate a random secret and hash |
| `agbridge keygen --type noise` | Generate an X25519 keypair |

Most operational commands support `--json` for automation.

## Architecture Details

```
┌────────────────────────────────────────────────────────┐
│  L4  Inner payload (JSON, type-specific)              │
│      ExecRequest / FileChunk / StreamData / ...       │
├────────────────────────────────────────────────────────┤
│  L3  E2E AEAD envelope                                │
│      Noise IK session + ChaCha20-Poly1305             │
├────────────────────────────────────────────────────────┤
│  L2  proto.Frame                                      │
│      version|type|reqid_len|reqid|payload_len|payload │
├────────────────────────────────────────────────────────┤
│  L1  WebSocket binary messages                        │
│      Ping every 30s, dead connection after 90s        │
├────────────────────────────────────────────────────────┤
│  L0  TLS 1.3 + cert pin                               │
└────────────────────────────────────────────────────────┘
```

The gateway runs a single TLS server. WebSocket upgrade requests go to
the bridge/daemon relay. `POST /v1/enroll` goes to the enrollment
handler. Both share the same certificate and pin.

## Resilience

- Reconnect uses exponential backoff with jitter.
- In-flight tool calls fail as `network_lost` with retry metadata.
- WebSocket keepalive closes stale connections.
- Gateway SIGHUP reloads config and persisted gateway state, then
  revokes sessions whose credentials were removed or rotated.
- Duplicate bridge identity is handled by sending `session_replaced` to
  the older bridge and exiting it with status 75.
- Audit logs are JSONL and rotate by size and backup count.

## Status

Current release: v0.0.2 (CLI-first).

| Area | Status |
|---|---|
| Core CLI/gateway/daemon relay | Shipped |
| Noise IK E2E payload encryption | Shipped |
| Enrollment and `state.json` UX | Shipped |
| CLI tool surface (14 commands) | Shipped |
| MCP stdio bridge | Removed in v0.0.2 (replaced by CLI) |
| Stateful tools (`port_forward`, `background_exec`, `kill_process`) | Deferred to v0.0.3 (optional local daemon mode) |
| Long-duration, multi-site uptime validation | Not complete |
| seccomp/cgroup sandboxing | Deferred |
| Windows daemon support | Deferred |

## Known Limitations

- `port_forward`, `background_exec`, and `kill_process` are temporarily
  unavailable in v0.0.2; planned for v0.0.3 via an optional local daemon
  mode.
- `exec` is non-interactive. A real TTY needs the v0.0.3 stateful tool
  path.
- One daemon currently has one active CLI writer at a time. A newer
  client with the same identity replaces the older session cleanly, but
  true multi-client fanout is deferred.
- E2E session keys are scoped to a WSS connection. Rekey while connected
  is deferred; reconnecting creates a fresh session.
- Widening daemon `allowed_bridge_pubkeys` requires re-enrolling or
  otherwise updating daemon state.
- The daemon sandbox is policy-based and process-level. It is not a
  seccomp/cgroup jail yet.

## License

[Apache 2.0](LICENSE). See [NOTICE](NOTICE) for attribution.
