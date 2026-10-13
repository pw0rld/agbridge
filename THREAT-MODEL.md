# agbridge threat model

This document spells out what agbridge protects against, what it
**doesn't** protect against, and which code paths enforce each
property. Numbered invariants are pinned by regression tests in this
repository — search for the test names if you want to read the proof.

The threat model is intentionally written for two readers:

- **Operators self-hosting agbridge.** You're putting a daemon on a
  box you care about. You want to know what an attacker can do if
  they own the gateway, the network, or the AI client.
- **Security researchers.** You want a concise mental model so you
  can poke holes in it.

If you find a gap between what this document promises and what the
code does, please open an issue (or email — see SECURITY.md once
that lands).

---

## Architecture (one paragraph)

agbridge has three components. The **daemon** runs on the host you
want an AI to operate; it holds capabilities (file read/write, exec,
port-forward). The **gateway** is a relay that brokers connections
between daemons and bridges. The **bridge** runs on the host where
your AI client (Claude Code / Codex / etc.) lives; it speaks MCP to
the AI client and translates MCP calls into daemon-bound frames over
the gateway.

The interesting threat model question is: **how much do you have to
trust the gateway?** The honest answer is *less than you'd think* —
because the daemon enforces every capability locally from boot-time
config. The gateway is a routing layer; it isn't an authorization
authority.

---

## Trust assumptions

These are the things we take as given. If any of these is false in
your deployment, the rest of the model breaks.

- **The daemon process is not root** (`os.Geteuid() != 0`). The
  daemon refuses to start as root unless `AGBRIDGE_TEST_ALLOW_ROOT=1`
  is set, which is **strictly for test environments** and never for
  production. Pinned by: `sandbox.RefuseRoot`.

- **The daemon's `state.json` (or legacy YAML config) is on a
  filesystem the operator controls.** An attacker who can rewrite
  `state.json` can grant themselves anything; that's outside the
  threat model because at that point they already own the box.

- **There is no path allowlist.** The daemon's file and exec operations
  are bounded only by what the daemon's OS user can read/write/execute.
  See I6.

- **Noise static keys are generated locally and not shared.** The
  daemon's noise key is never transmitted; it's read from
  `noise_static_key_path` at boot.

---

## Attacker capabilities (what we defend against)

The threat model assumes an attacker who has **at least one** of:

1. **Compromised gateway.** They can read, modify, or inject any
   frame in transit between bridge and daemon.
2. **Network MITM** between bridge ↔ gateway or gateway ↔ daemon.
   Mitigated by TLS + cert pinning at the transport layer; further
   mitigated by E2E Noise between bridge and daemon when
   `e2e_mode=required`.
3. **Malicious bridge** that has somehow obtained a bridge
   enrollment token (e.g. by phishing the user). They can speak the
   protocol but have a specific pubkey identity.
4. **Stolen daemon enrollment token.** The token grants daemon-side
   registration, but the resulting daemon still loads its ACL from
   the local `state.json` the token writes — so an attacker with
   only the token (and no foothold on the daemon host) can't grant
   themselves anything beyond what the token's policy said.

The threat model **does not** assume:

- Kernel-level compromise of the daemon host (we can't defend against
  that and don't try).
- Compromise of the operator's local development machine
  (used to issue enrollment tokens).
- A user-supplied AI client that's been replaced with a malicious
  binary (the trust boundary is the bridge ↔ daemon protocol, not
  the AI client ↔ bridge MCP local socket).

---

## Invariants (with proof pointers)

### I1. Daemon ACL state is immutable after boot

After the daemon process starts and validates its `state.json`, none
of these fields can be mutated at runtime by any protocol message:

- `exec_enabled`
- `allowed_bridge_pubkeys`
- `forbidden_ports`
- `env_allowlist`

**Code path proof.** `cmd/agbridge/daemon.go` loads config once in
`loadDaemonConfig`; the loaded struct is cached in
`daemonState.cfg` and `daemonState.e2e`. No code site writes to those
fields after `runDaemonSession` starts. The dispatch table in
`daemonState.dispatch` (`cmd/agbridge/daemon.go:281`) is a flat
switch over frame types — none of the cases touches ACL state.

**Tests pin this.**
- `TestDaemonDispatchHasNoConfigMutationCase`
  (`cmd/agbridge/zero_trust_test.go`) snapshots `allowedPubs` before
  dispatching every recognized frame type with empty payload, then
  verifies the slice header and contents are byte-equal afterwards.
- `TestPeerAllowedRejectsAllWhenRequiredAndEmpty` exercises the
  belt-and-suspenders runtime check that even if startup validation
  somehow let `required + empty allowlist` slip through, the peer
  check still refuses every connection.

**What this means in practice.** A compromised gateway cannot push
a "config update" to widen a daemon's ACL. There is no protocol path
for it. Adding one would require adding a new frame type and a new
dispatch case — and would trip the test above.

### I2. Unknown bridge pubkeys are rejected at handshake

When `e2e_mode=required`, the daemon performs a Noise handshake with
the bridge and refuses any peer whose static pubkey is not in
`allowed_bridge_pubkeys`. The match is byte-exact (`bytes.Equal`); no
prefix, length, or string-form comparisons.

**Code path proof.** `cmd/agbridge/daemon.go:417` (`handleNoiseInit`)
calls `peerAllowed` after `ReadMessage1` decrypts the bridge's
identity. `peerAllowed` (`cmd/agbridge/daemon.go:439`) iterates
`s.e2e.allowedPubs` with `bytes.Equal`.

**Tests pin this.**
- `TestPeerAllowedRejectsUnknownPubkey`
- `TestPeerAllowedRejectsNearMatchPubkey` (one-bit-flip rejection)
- `TestPeerAllowedNilE2E`
- `TestPeerAllowedRejectsAllWhenRequiredAndEmpty`
- `TestPeerAllowedEmptyAllowlistOptional` (documents the explicit
  exception for operator-chosen opt-out)

**What this means in practice.** Even if the gateway sends a
NoiseInit frame on behalf of an attacker, the attacker can't
complete the handshake without owning a noise private key whose
public form is in the daemon's `allowed_bridge_pubkeys`. The
allowlist is set at boot from local config — see I1.

### I6. No path scoping — containment is the non-root user + isolation

As of v0.0.4 the daemon does NOT scope file or exec operations to any path
allowlist. The earlier read/write/exec-cwd allowlists were removed: once
`exec_enabled` is on (the primary "trusted agent" use case), a path
allowlist is trivially bypassed by running a subprocess, so it added
complexity and friction without being a security boundary.

**What contains a compromised or misused session:**
- The daemon runs as a **non-root** OS user (enforced by `RefuseRoot`).
  File and exec operations are bounded by that user's ordinary OS
  permissions — the blast radius, not a path fence.
- **Strong isolation is the operator's responsibility.** Run the daemon in
  a container or VM if the workload warrants it. agbridge does not sandbox
  the daemon for you.
- Every operation is audited (cloud-side, out of scope for this file).

The CLI-side **working directory** (`agbridge cd` / `pwd`, relative-path
resolution) is a convenience default for where the AI operates. It is NOT a
security control: absolute paths and `..` reach anywhere the daemon user
can, by design.

---

## Known limitations (we don't pretend to defend against these)

- **No per-frame rate limiting.** A malicious bridge can flood the
  daemon with file/exec requests. This exhausts CPU/IO but cannot
  bypass the ACL. Tracked as a future hardening item.

- **`forbidden_ports` is a denylist, not an allowlist.** Port
  forwarding is gated by a per-port forbidden list rather than a
  per-port allowlist. Operators who care should set this explicitly
  in `state.json`.

- **Bridge ↔ AI client local socket has no authentication.** Any
  local process running under the bridge UID can connect to the
  bridge's MCP socket and issue tool calls. This is acceptable
  because if an attacker is already running code as the bridge UID,
  they don't need agbridge — they can just `ssh`. But it's worth
  knowing.

---

## Out-of-scope (deferred to future audits)

- **Side-channel attacks** (timing, cache) — out of scope for v0.0.x.
- **Quantum-resistant key exchange** — uses standard X25519/Noise;
  if you're operating in a post-quantum threat model, you need
  something else.
- **Cross-tenant isolation in the gateway.** This document covers
  the OSS daemon's local invariants. If you wrap the gateway in a
  multi-tenant frontend, that frontend has its own threat model
  which is out of scope here.

---

## How to extend this document

If you're adding a new feature that touches:

- The dispatch table in `cmd/agbridge/daemon.go:dispatch`
- `sandbox.RefuseRoot` or the CLI-side working-directory resolver
  (`cmd/agbridge/cli_workingdir.go`)
- The Noise handshake / peer authentication
- Any field on `daemonState` or `daemonE2EDeps`

...you should also update this document. Run the tests under
`cmd/agbridge/zero_trust_test.go` and `internal/sandbox/sandbox_test.go` —
they're the load-bearing evidence behind the invariants above. If a new
feature breaks one of them, the right move is usually to rethink the
feature, not to weaken the test.
