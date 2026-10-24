<div align="center">

# agbridge

**A one-shot CLI for AI agents to operate remote machines over outbound-only TLS.**

[![ci](https://github.com/pw0rld/agbridge/actions/workflows/ci.yml/badge.svg)](https://github.com/pw0rld/agbridge/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/pw0rld/agbridge?include_prereleases&sort=semver)](https://github.com/pw0rld/agbridge/releases)
[![license](https://img.shields.io/github/license/pw0rld/agbridge)](LICENSE)

</div>

agbridge lets AI assistants operate remote machines that are hard to
reach directly: lab workstations, cloud VMs behind firewalls, home
servers, customer VPCs, GPU boxes, CI runners, and corporate networks.

The AI does not connect to an MCP server. It runs normal shell commands
such as `agbridge ls`, `agbridge cat`, `agbridge exec`, `agbridge put`,
and `agbridge get`. Each command starts a fresh subprocess, dials the
gateway, invokes one remote operation, prints a stable result, and exits.

Both the CLI client and daemon dial out over TLS WebSocket. The gateway
is the only public listener. Inner tool payloads can be encrypted
end-to-end between the CLI client and the daemon, so the gateway acts as
a relay and control plane rather than a remote shell host.

```
AI agent
  |
  | spawns one command
  v
agbridge <subcommand>  -- outbound TLS/WSS -->  gateway  <-- outbound TLS/WSS --  daemon
       |                                                                           |
       |_____________________ Noise IK + ChaCha20-Poly1305 ________________________|
                                      inner payloads
```

## Why

SSH, VPNs, and generic tunnels are excellent once the network path and
authorization model are already solved. AI agent workflows often fail
earlier:

- The target machine cannot accept inbound connections.
- The operator does not want to expose SSH or a broad VPN route.
- The AI needs stable command output and structured errors, not an
  interactive terminal session.
- Security wants local daemon policy: explicit exec enablement,
  environment filtering, a per-target working directory, and audit logs.

agbridge is designed for that gap. It is not a VPN replacement and it is
not trying to beat OpenSSH at SSH. It provides a narrow remote operation
surface that is easy for an AI agent to call from a shell.

## Components

| Component | Runs on | Purpose |
|---|---|---|
| `agbridge gateway` | Public host or ingress | Authenticates devices, relays frames, serves `/v1/enroll`, writes audit logs |
| `agbridge daemon` | Remote machine | Executes file/command/transfer tools under daemon policy and dials the gateway outbound |
| `agbridge <subcommand>` | AI client host | One-shot CLI calls (`ls`, `cat`, `exec`, etc.) that dial the gateway, invoke one operation, and exit |

The enrollment role for the CLI client is still named `bridge` in flags
and state files for protocol compatibility. In v0.0.2, that means a CLI
client identity, not a long-running MCP stdio process.

## Features

- **CLI-first AI surface**: `status`, `targets`, `config use`, `caps`,
  `ai-help`, `ls`, `cat`, `stat`, `grep`, `write`, `edit`, `exec`,
  `put`, and `get`.
- **Outbound-only connectivity**: no inbound port on the AI client host
  or daemon host.
- **End-to-end payload encryption**: Noise IK with
  ChaCha20-Poly1305 for inner CLI<->daemon frames.
- **Gateway TLS modes**: certificate pinning by default; WebPKI mode for
  hosted TLS frontends such as Cloudflare.
- **One-shot enrollment**: gateway-issued tokens, local Noise key
  generation, and per-device `state.json`.
- **Daemon policy**: `exec_enabled`, env allowlist, non-root daemon
  startup, file size caps, typed errors.
- **Per-target working directory**: `cd` / `pwd` and relative paths
  resolve per daemon (a soft base, not a security fence).
- **AI-friendly output**: human output by default, `--json` with
  `_schema:1`, and single-line structured error JSON on stderr.

## Install

```bash
curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash
```

From source:

```bash
go install github.com/pw0rld/agbridge/cmd/agbridge@latest
```

### Windows CLI

Windows x64 and ARM64 support the CLI client. Run the remote daemon on
Linux or macOS. No local service or MCP configuration is needed.

In PowerShell, download and run the installer (no administrator required):

```powershell
Invoke-WebRequest 'https://github.com/pw0rld/agbridge/releases/latest/download/install.ps1' -OutFile "$env:TEMP\agbridge-install.ps1"
& "$env:TEMP\agbridge-install.ps1"
agbridge enroll --gateway 'wss://relay.agbridge.cloud/' --tls-mode webpki --token 'YOUR_BRIDGE_TOKEN'
agbridge status
agbridge ls /home
```

The installer verifies SHA-256, installs `agbridge.exe` to
`%LOCALAPPDATA%\agbridge\bin`, and updates the current and user PATH.
If script execution is restricted, run the reviewed script with
`powershell -NoProfile -ExecutionPolicy Bypass -File "$env:TEMP\agbridge-install.ps1"`,
then open a new terminal for the updated PATH. Existing AI clients may
need a restart to inherit the PATH. Releases also provide standalone
`agbridge-windows-amd64.exe` and `agbridge-windows-arm64.exe` files.

State and keys live in `%USERPROFILE%\.config\agbridge` by default.
Remote paths always use POSIX syntax; local transfer paths use Windows syntax:

```powershell
agbridge put 'C:\Users\me\project\config.json' /srv/app/config.json
agbridge get /srv/app/result.zip '.\result.zip'
agbridge exec --cwd /srv/app -- git status
```

Create a **bridge** device in Cloud and use its token on Windows. Daemon
enrollment and `agbridge daemon` are unsupported on Windows.

## Quickstart

If a gateway operator gives you a paste command, run it on the device
being enrolled. It installs the binary and calls `agbridge enroll` with
the right gateway URL and token.

Manual enrollment uses explicit flags:

```bash
agbridge enroll \
  --gateway wss://gw.example.com/ \
  --token et_XXXXXXXXXXXXXXXXXX

agbridge status
agbridge caps
```

For Cloudflare or another normal WebPKI TLS frontend:

```bash
agbridge enroll \
  --gateway wss://gw.example.com/ \
  --token et_XXXXXXXXXXXXXXXXXX \
  --tls-mode webpki
```

After enrollment, tell the AI to run `agbridge` commands directly. The
embedded helper is designed to be pasted into an AI session:

```bash
agbridge ai-help
```

Self-hosting operators can bootstrap the gateway with:

```yaml
listen: 0.0.0.0:443
public_url: wss://gw.example.com/
audit_path: /var/log/agbridge/audit.jsonl
agents: []
daemons: []
```

```bash
agbridge cert gen --cn gw.example.com --out /etc/agbridge

agbridge gateway \
  --config /etc/agbridge/gateway.yaml \
  --cert /etc/agbridge/cert.pem \
  --key /etc/agbridge/key.pem
```

Issue a daemon token with the file policy and optional exec capability:

```bash
agbridge issue-token \
  --config /etc/agbridge/gateway.yaml \
  --role daemon \
  --name lab01 \
  --allowed-paths /home/me/projects \
  --exec-enabled
```

Then issue a CLI-client token targeting that daemon:

```bash
agbridge issue-token \
  --config /etc/agbridge/gateway.yaml \
  --role bridge \
  --name peter-laptop \
  --target lab01
```

## Everyday Usage

Start with the connection and policy:

```bash
agbridge status
agbridge caps
agbridge targets
```

Read and inspect files:

```bash
agbridge ls /home/me/projects
agbridge ls -R --depth 2 /home/me/projects
agbridge cat /home/me/projects/app/README.md
agbridge cat --offset 0 --limit 4096 /home/me/projects/app/big.log
agbridge stat /home/me/projects/app/main.go
agbridge grep "TODO" /home/me/projects --max-results 20
```

Write, edit, and transfer:

```bash
agbridge write /home/me/projects/app/.env --content "DEBUG=1\n"
agbridge write /home/me/projects/app/config.json --from-file /tmp/config.json
agbridge edit /home/me/projects/app/main.go --old "localhost" --new "127.0.0.1"
agbridge put /tmp/build.tar.gz /home/me/projects/app/build.tar.gz
agbridge get /home/me/projects/app/result.log /tmp/result.log
```

Run a remote command when daemon policy enables exec:

```bash
agbridge exec --cwd /home/me/projects/app --exec-timeout 120000 -- go test ./...
```

Use `-t/--target` to override the default daemon for a single call:

```bash
agbridge -t lab02 status
agbridge -t lab02 cat /home/me/projects/app/README.md
```

## CLI Reference

| Command | Purpose |
|---|---|
| `agbridge status` | Dial, authenticate, run E2E handshake, and report connectivity |
| `agbridge targets` | List daemon targets known to local state |
| `agbridge config use <name>` | Set the default daemon target |
| `agbridge caps` | Show daemon policy, hostname, version, and file limits |
| `agbridge ai-help` | Print a compact AI instruction sheet |
| `agbridge ls [-R] [--depth N] <path>` | List directory entries |
| `agbridge cat [--offset N] [--limit N] <path>` | Read a file to stdout |
| `agbridge stat <path>` | Show file metadata |
| `agbridge grep <pattern> [path]` | Search file contents |
| `agbridge write <path>` | Write from `--content`, `--from-file`, or stdin |
| `agbridge edit <path> --old <s> --new <s>` | Atomic find-and-replace |
| `agbridge exec [--cwd PATH] -- <cmd> [args...]` | Run one non-interactive command |
| `agbridge put <local> <remote>` | Upload a local file to the daemon |
| `agbridge get <remote> <local>` | Download a daemon file to the CLI host |

Global flags:

- `-t, --target <name>`: override the default daemon target.
- `--json`: emit stable JSON on stdout.
- `--state-dir <dir>`: use a non-default local state directory.
- `--timeout <duration>`: per-call network/tool timeout.

Output contract:

- Success exits `0`.
- Operational failure exits `1`.
- `--json` adds top-level `_schema: 1` to stdout payloads.
- Every command failure writes exactly one JSON object to stderr:
  `{"code":"...","message":"...","retry_safe":true|false}`.
- Branch on `code`, not `message`.

## Security Model

| Layer | What it protects |
|---|---|
| TLS 1.3 | Transport encryption to the gateway |
| Cert pin or WebPKI | Gateway identity, depending on `--tls-mode` / stored state |
| HMAC-SHA256 | CLI-client-to-gateway Route frame integrity using the API key |
| Noise IK E2E | Inner CLI<->daemon payload confidentiality from the gateway |
| Daemon client ACL | Optional client public-key allowlist on the daemon |
| Protocol generation check | Gateway refuses to pair bridge and daemon across protocol generations (v0.0.4 boundary vs v0.0.5+) |
| `exec_enabled` | `agbridge exec` is denied unless explicitly enabled for the daemon |
| Env allowlist | Only configured environment variables are forwarded to remote subprocesses |
| Non-root daemon | Daemon refuses to run as root outside tests |

Important policy detail: since v0.0.4 the daemon enforces **no path
allowlist**. The working directory (`cd` / `pwd`,
`default_working_dir`) is a convenience base for resolving relative
paths — absolute paths and `..` pass through. The real access boundary
is the daemon's OS user plus the Noise client identity. Use
`agbridge caps` to see what a daemon advertises, and run the daemon
under a least-privilege account.

### E2E Modes

- `disabled`: legacy plaintext inner frames; gateway can see tool
  payloads.
- `optional`: Noise IK is required for every tool call — plaintext
  inner frames are refused, so a hostile gateway cannot inject tool
  calls — but `allowed_bridge_pubkeys` may stay empty, letting any
  gateway-authenticated CLI client complete the handshake.
- `required`: daemon requires a non-empty `allowed_bridge_pubkeys` list
  and rejects unpinned clients.

New CLI-client enrollments use `required`. New daemon enrollments
default to `optional` so the first daemon can be enrolled before any
client public key exists.

## Operator Commands

Gateway operator commands:

| Command | Purpose |
|---|---|
| `agbridge gateway --config ... --cert ... --key ...` | Run the rendezvous server |
| `agbridge issue-token --role daemon --name ...` | Mint a daemon enrollment token |
| `agbridge issue-token --role bridge --name ... --target ...` | Mint a CLI-client enrollment token |
| `agbridge gateway-list-devices --config ...` | List enrolled clients, daemons, and outstanding tokens |
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

## Architecture Details

```
L4  Inner JSON payloads
    ExecRequest / FileChunk / SearchRequest / ...

L3  Optional E2E AEAD envelope
    Noise IK session + ChaCha20-Poly1305

L2  proto.Frame
    version | type | reqid | payload

L1  WebSocket binary messages
    outbound from CLI client and daemon to gateway

L0  TLS 1.3
    cert pin or WebPKI gateway authentication
```

The gateway runs a single TLS server. WebSocket upgrade requests go to
the CLI/daemon relay. `POST /v1/enroll` goes to the enrollment handler.

## Status

Current release line: v0.0.2, CLI-first.

| Area | Status |
|---|---|
| Core CLI/gateway/daemon relay | Shipped |
| Noise IK E2E payload encryption | Shipped |
| Enrollment and `state.json` UX | Shipped |
| CLI tool surface (14 commands) | Shipped |
| MCP stdio bridge | Removed in v0.0.2 |
| Stateful tools (`port_forward`, `background_exec`, `kill_process`) | Deferred to v0.0.3 |
| Long-duration, multi-site uptime validation | Not complete |
| seccomp/cgroup sandboxing | Deferred |
| Windows daemon support | Deferred |

## Known Limitations

- `port_forward`, background process management, and remote process kill
  are not available in v0.0.2. They need a long-lived local process and
  are planned for v0.0.3.
- `agbridge exec` is non-interactive and does not provide a TTY.
- One CLI client identity currently holds one active session at a time.
  Run commands serially; concurrent calls may return
  `concurrent_not_supported`.
- E2E session keys are scoped to one WSS connection. Each one-shot CLI
  call creates a fresh connection and fresh session.
- Widening daemon client pubkey allowlists requires re-enrolling or
  updating daemon state.
- The daemon sandbox is policy-based and process-level. It is not a
  seccomp/cgroup jail yet.

## License

[Apache 2.0](LICENSE). See [NOTICE](NOTICE) for attribution.
