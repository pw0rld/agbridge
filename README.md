# agbridge

AI agent remote operation surface over restrictive networks (TLS:443 + MCP).

See the [design spec](https://github.com/pw0rld/my-wiki/blob/main/wiki/ai-research/plan/agbridge.md) for goals.

Status: Phase 3 — first MCP tool (`exec`) end-to-end. Other 3 tools
(read_file, write_file, port_forward) + reconnect + keepalive land in Phase 4.

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
env_allowlist:
  - PATH
  - HOME
  - LANG
```

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
   "bridge.yaml"]`. The agent will see one tool: `exec`.

## Architecture

```
[AI Agent] --MCP/stdio--> [bridge] --TLS:443 WSS--> [gateway] <--TLS:443 WSS-- [daemon] --subprocess--> [your shell]
```

bridge HMAC-signs every frame; gateway verifies + audits; daemon enforces
non-root + cwd allowlist + env whitelist.

## Limitations (Phase 3)

- Only one tool: `exec`. read_file/write_file/port_forward in Phase 4.
- 1 MB stdout/stderr truncation cap (raises in Phase 4).
- No reconnect/keepalive — WSS drop kills the connection.
- Single-bridge-per-daemon (multiple bridges targeting the same daemon may race).
- Tested in-process only; manual three-machine deployment not yet verified.
