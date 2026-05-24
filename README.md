<div align="center">

# agbridge

**An outbound-only MCP gateway for AI agents to operate remote machines safely.**

[![ci](https://github.com/pw0rld/agbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/pw0rld/agbridge/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/pw0rld/agbridge?include_prereleases&sort=semver)](https://github.com/pw0rld/agbridge/releases)
[![license](https://img.shields.io/github/license/pw0rld/agbridge)](LICENSE)

</div>

agbridge gives AI assistants a structured MCP tool surface for machines
that are hard to reach directly: lab workstations, cloud VMs behind
firewalls, home servers, customer VPCs, GPU boxes, CI runners, and
corporate networks.

Both sides dial out over TLS WebSocket. The gateway is the only public
listener. Tool payloads can be end-to-end encrypted between the local
bridge and the remote daemon, so the gateway acts as a relay and control
plane rather than a remote shell host.

```
   ┌───────────────┐     ┌──────────────┐     ┌───────────────┐
   │   AI Agent    │     │              │     │               │
   │ (Codex,       │     │   gateway    │     │    daemon     │
   │  Claude, ...) │     │  (public IP) │     │ (lab/cloud/   │
   └───────┬───────┘     └───────┬──────┘     │  corporate)   │
           │ MCP/stdio           │            └───────┬───────┘
   ┌───────▼───────┐             │                    │
   │    bridge     │             │                    │
   └───────┬───────┘             │                    │
           │                     │                    │
           │  TLS:443 WSS  ──────►              ◄──── │   TLS:443 WSS
           │  outbound only      │                    │   outbound only
           └─────────────────────┴────────────────────┘
                       relay + enrollment + audit
                                 │
                                 ▼
                    bridge <-> daemon E2E payloads
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
| `agbridge bridge` | AI client host | Exposes stdio MCP tools and dials the gateway outbound |

The bridge and daemon can be behind NAT or restrictive firewalls as long
as they can make outbound TLS connections to the gateway.

## Features

- **MCP-native tool surface**: `exec`, file read/write/edit, search,
  upload/download, directory metadata, background processes, TCP
  forwarding, and policy introspection.
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

Pinned release:

```bash
VERSION=v0.1.0-phase-b \
  curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash
```

From source:

```bash
go install github.com/pw0rld/agbridge/cmd/agbridge@latest
```

## Quickstart

See [QUICKSTART.md](QUICKSTART.md) for the full walkthrough. The short
version is:

1. Start a gateway with a TLS certificate.
2. Issue a daemon token with the paths and ports the daemon should be
   allowed to touch.
3. Enroll and start the daemon on the remote machine.
4. Issue a bridge token targeting that daemon.
5. Enroll the bridge on the AI client host and register it as an MCP
   server.

Minimal gateway config:

```yaml
listen: 0.0.0.0:443
public_url: wss://gw.example.com/
audit_path: /var/log/agbridge/audit.jsonl
agents: []
daemons: []
```

Start the gateway:

```bash
agbridge cert gen --cn gw.example.com --out /etc/agbridge

agbridge gateway \
  --config /etc/agbridge/gateway.yaml \
  --cert /etc/agbridge/cert.pem \
  --key /etc/agbridge/key.pem
```

Issue and use a daemon enrollment token:

```bash
agbridge issue-token \
  --config /etc/agbridge/gateway.yaml \
  --role daemon \
  --name lab01 \
  --allowed-paths /home/me/projects
```

The command prints a paste-command for the daemon host. After enrollment,
start the daemon:

```bash
agbridge daemon --state-dir ~/.config/agbridge
```

Then issue a bridge token:

```bash
agbridge issue-token \
  --config /etc/agbridge/gateway.yaml \
  --role bridge \
  --name peter-laptop \
  --target lab01
```

After bridge enrollment, configure your MCP client to run:

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

## Recommended Use

agbridge has two useful modes. They can be mixed.

### Tunnel to SSH

Use agbridge for egress-only reachability, then let `sshd` handle
interactive shells, user accounts, authorized keys, and mature audit
controls.

```
AI agent -> port_forward(127.0.0.1, 22, 2222)

local ssh -p 2222 user@127.0.0.1
```

This is usually the safest operational default when SSH is allowed on the
daemon host.

### Direct MCP Tools

Use direct tools when SSH is not available, when you want tighter
tool-level control, or when AI agents benefit from structured results.

Examples:

- `search` instead of `ssh grep`
- `read_file` with `offset` and `limit` instead of `ssh cat`
- `edit_file` with an exact `old_string` instead of remote shell edits
- `download` with `dest_path` for large files
- `exec_start` for long-running servers and watchers

## MCP Tools

| Tool | Use it for | Notes |
|---|---|---|
| `capabilities` | Discover daemon policy and limits | Lists allowed paths, forbidden ports, hostname, version |
| `exec` | Run one command | Non-interactive; stdout/stderr stream back; process group killed on timeout |
| `exec_start` / `_status` / `_logs` / `_kill` / `_list` | Manage background processes | For dev servers, watchers, long-running tasks |
| `read_file` | Read a remote file | Inline cap 10 MiB; supports `offset` and `limit`; returns sha256 |
| `write_file` | Write a small text or binary file | Use `content` for text, `content_b64` for binary; cap 10 MiB |
| `edit_file` | Replace text in an existing file | Atomic rewrite with backup retention; avoids rewriting large files |
| `search` | Search file contents | Pure Go, structured matches, skips common build/cache directories |
| `list_directory` / `stat` | Inspect filesystem metadata | Non-recursive directory listing and single-path metadata |
| `upload` | Send a file to the daemon | `local_path`, `url`, or inline `chunks`; cap 1 GiB |
| `download` | Bring a daemon file back to the bridge host | Use `dest_path` for large files; inline downloads cap at 10 MiB |
| `port_forward` / `_list` / `_close` | Reach TCP services from the bridge host | Blocks `forbidden_ports` set during daemon enrollment |

The tools return structured MCP results with human-readable content and
machine-readable `_meta`. Failures include `_meta.error_code`, category,
and retryability where possible.

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

## CLI Reference

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
| `agbridge enroll --gateway URL --token et_...` | Enroll a bridge or daemon with diagnostics |
| `agbridge bridge --state-dir DIR` | Run the local MCP bridge |
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

Current development branch: `phase-c`.

| Area | Status |
|---|---|
| Core bridge/gateway/daemon relay | Shipped |
| Noise IK E2E payload encryption | Shipped |
| Enrollment and `state.json` UX | Shipped |
| Expanded MCP tool surface | In progress for v0.3.0 |
| Long-duration, multi-site uptime validation | Not complete |
| Multi-bridge per daemon | Deferred |
| seccomp/cgroup sandboxing | Deferred |
| Windows daemon support | Deferred |

## Known Limitations

- One daemon currently has one active bridge writer at a time. A newer
  bridge with the same identity replaces the older bridge cleanly, but
  true multi-bridge fanout is deferred.
- `exec` is non-interactive. Use `port_forward` to SSH or another remote
  service when you need a real TTY.
- E2E session keys are scoped to a WSS connection. Rekey while connected
  is deferred; reconnecting creates a fresh session.
- Widening daemon `allowed_bridge_pubkeys` requires re-enrolling or
  otherwise updating daemon state.
- The daemon sandbox is policy-based and process-level. It is not a
  seccomp/cgroup jail yet.

## License

[Apache 2.0](LICENSE). See [NOTICE](NOTICE) for attribution.
