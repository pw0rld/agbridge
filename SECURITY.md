# Security policy

## Reporting a vulnerability

If you think you've found a security issue in agbridge, please report
it privately — **do not** open a public GitHub issue.

The fastest channel:

- GitHub Security Advisories — open a private advisory at
  https://github.com/pw0rld/agbridge/security/advisories/new

Alternative:

- Email: `security@agbridge.cloud` <!-- TODO: replace with actual address before launch -->

Include:

- A description of the issue and why you believe it's a security
  problem (not just a bug).
- Steps to reproduce, ideally a minimal proof of concept.
- The commit SHA or release tag you tested against.
- Any thoughts on severity and likely impact.

You don't need a CVE-quality writeup — a clear repro is enough. The
maintainers will handle the rest.

## What you can expect

- **Acknowledgement within 48 hours** that we received the report.
- **Initial assessment within 7 days** of whether it's a security
  issue, and a rough severity rating.
- **Fix timeline** depends on severity. Critical issues (remote code
  execution, allowlist bypass, authentication bypass) get a fix in
  days. Less severe issues land in the next regular release.
- **Coordinated disclosure**: we publish a CVE and a security
  advisory once a fix is available. Default embargo is 90 days from
  initial report, shorter if active exploitation is observed, longer
  if you ask for more time to coordinate disclosure with another
  affected project.

## What's in scope

In scope (please report):

- **Bypass of `allowed_read_paths` / `allowed_write_paths` /
  `allowed_exec_cwds`.** Any way to make the daemon access a file
  or directory outside its configured allowlist.
- **Bypass of `allowed_bridge_pubkeys`.** Any way to make a daemon
  accept a Noise handshake from a pubkey not on its list.
- **Bypass of `forbidden_ports`.** Any way to make port-forward
  reach a denied port.
- **Authentication bypass** on enrollment, daemon-gateway handshake,
  or bridge-gateway handshake.
- **Code injection** via daemon inputs (file paths, exec args,
  search patterns, edit content).
- **Cryptographic flaws** in the Noise channel or TLS setup.
- **Denial of service** that requires only a valid bridge identity
  (a malicious bridge crashing the daemon is in scope; arbitrary
  packets crashing the public gateway is also in scope).

Out of scope:

- Vulnerabilities in dependencies (Go stdlib, `nhooyr.io/websocket`,
  etc.) that haven't been disclosed upstream. Please report those to
  the upstream project first; we'll track and update.
- Issues that require kernel-level compromise of the daemon host
  (the threat model explicitly excludes this — see
  [THREAT-MODEL.md](THREAT-MODEL.md)).
- Issues that require an attacker to already have shell access as
  the daemon user (same reason).
- Bugs without a security impact (please open a regular issue).
- Social engineering or physical attacks.

## Hardening commitments

The maintainers commit to:

- A documented threat model — see [THREAT-MODEL.md](THREAT-MODEL.md).
- Regression tests for every fixed vulnerability, kept in the
  test suite for the life of the repo.
- A `SECURITY.md` (this file) that stays current with the contact
  channels and process.

## Hall of fame

We'll list researchers who report valid issues here, with their
consent. (Empty for now; we'd rather not have entries than need them.)
