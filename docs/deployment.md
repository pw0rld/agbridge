# Deploying a production gateway

This guide covers running an agbridge gateway for real workloads: TLS
choices, firewall, state files, credential lifecycle, and monitoring.
For the five-minute tour of all three components see
[QUICKSTART.md](../QUICKSTART.md).

## Topology

```
                      public Internet
   laptop / CI / AI host          server behind NAT / laptop / IoT
   ┌───────────────┐              ┌────────────────┐
   │ bridge (CLI   │   outbound   │    daemon      │
   │ client)       │──── WSS ───► │  (outbound-    │
   └──────┬────────┘    only      │   only WSS)    │
          │                       └───────▲────────┘
          ▼                               │ same WSS tunnels,
   ┌──────────────┐                       │ both sides dial out
   │   gateway    │───────────────────────┘
   │ (only public │
   │  listener)   │
   └──────────────┘
```

Only the gateway accepts inbound connections. Daemons and bridges make
outbound WSS connections, so neither needs a public IP, port forward,
or VPN. The gateway relays opaque encrypted frames; end-to-end Noise
IK + ChaCha20-Poly1305 keeps payloads (and even most metadata) away
from it.

## Gateway host setup

The supported one-shot path is `scripts/gateway-setup.sh`:

```bash
curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/gateway-setup.sh \
  | sudo bash -s -- --domain agbridge.example.com
```

It generates a self-signed cert (unless you bring your own), writes
`/etc/agbridge/gateway.yaml`, and installs a hardened systemd unit
(`DynamicUser`, `ProtectSystem=strict`, capability-limited port bind).
Manual setup is the same three steps: cert, YAML, service.

### TLS: pin vs WebPKI

Two client-side verification modes (`enroll --tls-mode`):

- **`pin`** (default) — the gateway presents any cert (self-signed is
  fine); clients pin its SHA-256 SPKI fingerprint, which `agbridge
  cert gen` writes to `pin.txt` and enrollment propagates
  automatically. No CA, no domain validation needed. Pin rotates with
  the cert: when you regenerate the cert, daemons must re-enroll or
  update their stored pin.
- **`webpki`** — the gateway uses a normal CA cert (Let's Encrypt, or
  an origin cert behind Cloudflare). Clients verify through the system
  trust store, which is the right choice when the gateway sits behind
  a CDN/mTLS terminator or you want cert rotation invisible to
  clients.

Firewall: open only the gateway port (443, or `--port`). Daemons
never listen; consider also restricting the gateway's own outbound
(nothing is required — connections are stateful-return).

## State files and backups

On the gateway host under the config dir (default `/etc/agbridge`):

| File | Contains | Notes |
|---|---|---|
| `gateway.yaml` | static config, agent + daemon entries | hashes only, safe-ish to keep in git |
| `gateway-state.json` | enrollments added at runtime | **secret material (hashes)** — 0600, back up |
| `tokens.json` | issued one-shot enrollment tokens | auto-GC'd after expiry; lock sidecar `.lock` |
| `cert.pem` / `key.pem` | TLS material | key is 0600 |
| `/var/log/agbridge/audit.jsonl` | audit ledger | rotated via `audit_max_bytes` |

Back up the config dir; losing it means re-enrolling every device.
Enrollments survive restarts (`gateway-state.json` is hydrated at boot
and merged on SIGHUP, add-only — runtime enrollments never vanish
because `gateway.yaml` lacks them).

## Credential lifecycle

- **Issuing:** `agbridge issue-token --config gateway.yaml --role
  daemon --name lab01 …` prints a one-shot `et_…` token (default TTL
  15 min). Daemon policy (exec, working dir, forbidden ports) is
  baked into the token at issue time and cannot change after enroll —
  rotate the daemon if policy must change.
- **Revoking:** remove the entry from `gateway.yaml`, then `systemctl
  reload agbridge` (SIGHUP). Reload re-reads config, drops credentials
  no longer listed, and tears down the affected live sessions.
- **Rotating an agent API key:** issue a replacement entry, SIGHUP,
  re-enroll the bridge. Old sessions die on reload.

`scripts/gateway-setup.sh` writes nothing secret to stdout beyond the
enrollment tokens you explicitly issue.

## Monitoring

The gateway exposes two operator surfaces:

1. **Audit log** (`/var/log/agbridge/audit.jsonl`) — one JSON event
   per auth/authz/route/failure, a complete connect/disconnect
   ledger. Size-capped, self-rotating.
2. **Prometheus endpoint** — set `metrics_listen` in
   `gateway.yaml` to start an **unauthenticated plain-HTTP**
   `/metrics` (Prometheus text format). Bind it to localhost or a
   management interface only:

   ```yaml
   metrics_listen: 127.0.0.1:9464
   ```

   Scrape it with node_exporter's companion or a local Prometheus:

   ```yaml
   - job_name: agbridge
     static_configs: [{ targets: ["127.0.0.1:9464"] }]
   ```

   Signals worth alerting on:

   | Metric | Meaning | Alert idea |
   |---|---|---|
   | `agbridge_daemon_connections_active` | online daemons | drop below expected fleet |
   | `agbridge_auth_failures_total` | bad tokens/keys | rate > 0 sustained |
   | `agbridge_mac_failures_total` | E2E frame tamper | any increase |
   | `agbridge_bridge_queue_overflows_total` | bridge too slow, session severed | sustained increase |
   | `agbridge_bridge_queue_bytes` | fan-out backlog | sustained high |

## Upgrades and version skew

Frame and handshake negotiation is generation-based: the gateway
refuses to pair a bridge and daemon from different protocol
generations (`protocol_mismatch`) instead of silently misparsing.
Upgrading the gateway is a binary swap + SIGHUP-free restart: daemons
and bridges reconnect automatically (their supervisors retry with
backoff). Upgrade daemons before bridges when adopting a new
generation, then mixed old/new pairs still work among themselves.

## Common failure patterns

- **Daemons never connect after cert regen** — their pinned
  fingerprint no longer matches. Re-distribute the pin (or move to
  `webpki` mode before rotating).
- **`address already in use` at start** — another listener on the
  port; the gateway fails fast rather than degrading.
- **Enrollments vanish** — impossible by design now (`gateway-state.json`
  ⊕ `gateway.yaml` merge on reload), but hand-editing
  `gateway-state.json` while the gateway runs gets clobbered; edit
  `gateway.yaml` instead.
- **High `bridge_queue_overflows_total`** — a bridge client stops
  reading fast enough (e.g. a wedged CLI process behind the WSS
  connection); the gateway severs the session rather than dropping
  frames, because dropped frames would corrupt
  E2E nonce ordering. Fix the consumer, or move big transfers to
  `put`/`get` instead of `cat`/`exec`-with-huge-output.
