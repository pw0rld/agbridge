# agbridge

AI agent remote operation surface over restrictive networks (TLS:443 + MCP).

See the [design spec](https://github.com/pw0rld/my-wiki/blob/main/wiki/ai-research/plan/agbridge.md) for goals.

Status: Phase 4 — all four MCP tools (`exec` / `read_file` / `write_file` /
`port_forward`) end-to-end. Resilience (reconnect, keepalive, SIGHUP reload,
audit rotation) lands in Phase 5.

## Build

```
go build ./cmd/agbridge
```

## Quickstart (single machine, manual)

1. Generate a self-signed TLS pair for the gateway:

```
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 30 \
  -subj "/CN=localhost" -keyout gw.key -out gw.crt
```

2. Compute the cert pin (paste into bridge.yaml + daemon.yaml):

```
echo "sha256:$(openssl x509 -in gw.crt -outform der | sha256sum | cut -d' ' -f1)"
```

3. Write `gateway.yaml`:

```yaml
listen: 127.0.0.1:8443
audit_path: ./audit.jsonl
agents:
  - name: claude-laptop
    api_key_hash: sha256:<sha256 hex of your api key>
    allowed_daemons: [lab01]
daemons:
  - name: lab01
    token_hash: sha256:<sha256 hex of your daemon token>
```

(Compute hashes: `printf 'api-key-1' | sha256sum`.)

4. Write `daemon.yaml`:

```yaml
gateway_url: wss://127.0.0.1:8443/
daemon_name: lab01
registration_token: <your daemon token>
cert_pin: sha256:<pin from step 2>
allowed_exec_cwds:
  - /tmp/agbridge-demo/*
allowed_read_paths:
  - /tmp/agbridge-demo/*
allowed_write_paths:
  - /tmp/agbridge-demo/*
forbidden_ports:
  - 22
  - 2375
env_allowlist:
  - PATH
  - HOME
  - LANG
```

`allowed_read_paths` / `allowed_write_paths` use the same prefix-glob
matching as `allowed_exec_cwds`. `forbidden_ports` is checked by the daemon
before dialing for `port_forward`; the bridge listener is unaffected.

5. Write `bridge.yaml`:

```yaml
gateway_url: wss://127.0.0.1:8443/
agent_name: claude-laptop
api_key: <your api key>
cert_pin: sha256:<pin from step 2>
target_daemon: lab01
```

6. Run all three (in three terminals):

```
./agbridge gateway --config gateway.yaml --cert gw.crt --key gw.key
./agbridge daemon  --config daemon.yaml
./agbridge bridge  --config bridge.yaml
```

7. In your MCP client (e.g., Claude Code), register the bridge as an MCP
   server with command `./agbridge` and args `["bridge", "--config",
   "bridge.yaml"]`. The agent will see four tools: `exec`, `read_file`,
   `write_file`, `port_forward`.

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

## Limitations (Phase 4)

- No reconnect / keepalive — a WSS drop kills the connection; the bridge
  has to be restarted to recover (Phase 5).
- No SIGHUP config reload; gateway / daemon must restart to pick up new
  agents, daemons, or allowlists (Phase 5).
- Audit log is single-file append-only with no rotation (Phase 5).
- Single-bridge-per-daemon (multiple bridges targeting the same daemon may
  race on shared state).
- Tested in-process only; manual three-machine deployment not yet verified.
