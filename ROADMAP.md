# Roadmap

Work tracked outside the issue tracker. Priorities:

- **P0** — security or correctness, blocks release
- **P1** — meaningful user-facing improvement
- **P2** — defensive / edge case, can ship without

## v0.3.0 (in progress, on `phase-c`)

AI-UX improvements + bug fixes from stress and integration testing.

- Capabilities introspection tool
- `edit_file` partial edits with auto-GC'd backups
- `read_file` offset/limit + UTF-8 perf path
- `write_file` / `exec` structured output, allowlist context in errors
- `list_directory` + `stat`
- `port_forward_list` + `port_forward_close`
- `upload` (chunks or URL) + `download`
- `exec` streaming via MCP progress notifications
- Background process management — 5 tools (`exec_start` / `_status` /
  `_logs` / `_kill` / `_list`)
- Native `search` tool (replaces `exec grep`)
- Release tarball ships statically-linked binary + helper scripts
- `agbridge cert gen` writes `pin.txt` so `gateway-setup.sh` finds it
- **F7 fix — duplicate bridge identity handled gracefully.** Gateway
  sends a `session_replaced` error frame and closes the displaced
  conn; the displaced bridge's supervisor recognises the code and
  exits with status 75 instead of reconnecting (no kick-storm).
  systemd users should set `RestartPreventExitStatus=75` to honor the
  eviction.
- **F4 partial fix — AEAD send-ordering atomicity (root cause #1).**
  `daemonState.sendInner` and `router.sendInner` used to encrypt
  outside the WSS write mutex, so two goroutines could consume Noise
  nonces N and N+1 and race to enqueue, landing on the wire reversed.
  After even one reorder the receiver's monotone recv counter
  desynced permanently and the entire session died (matched the "5/50
  round 1 → 0/50 rounds 2-5" stress pattern). Fix: both sides now
  hold a dedicated `sendMu` across encrypt + WSS send as one critical
  section. `e2e.Session` now documents "caller must serialize" as a
  contract; regression test in
  `internal/e2e/session_concurrent_test.go`. Under 50-concurrent
  stress, round-1 success goes from 5/50 to ≥48/50; with a brief idle
  gap between bursts, subsequent rounds also succeed at 80%+. A
  back-to-back burst (no gap) still degrades after round 1 — a
  separate cleanup-window issue tracked as F4b below.
- **F4b.1 defensive cleanup — bridge sends StreamClose on
  ackCtx timeout.** When the daemon never returns StreamAck within
  `streamAckTimeout`, the bridge used to return without telling the
  daemon, leaking the daemon's `handleStreamOpen` goroutine + local
  TCP socket forever. Fix: send a `StreamClose` (background ctx) on
  the ack-timeout path so the daemon can cancel its streamCtx and
  release the fd. Hygiene only — does not fully resolve the
  back-to-back burst symptom (F4b), but stops the goroutine/fd leak
  that contributes to it.
- **F6 — bridge-side progress + inline download cap.** Investigation
  found daemon already streams 1 MiB FileChunk frames; the actual
  buffer point was the bridge's `readFileHandler` and the inline
  branch of `downloadHandler`. Bridge handlers now emit MCP
  `notifications/progress` per chunk (progress = bytes received,
  total omitted because FileChunk does not carry size). `download`
  without `dest_path` gained the same 10 MB `maxBufferedOutput` cap
  `read_file` already had; the error message tells the caller to
  pass `dest_path` to stream big files to disk. No protocol or
  daemon changes — `fileproto` untouched. Smoke regressions cover
  progress emission, the inline cap, and the no-progressToken
  silent path.
- **F9 — AI-friendly tool ergonomics.** Three changes to keep
  base64 out of AI tool-call traces unless content is genuinely
  binary. (1) `write_file` accepts `content` (raw string) next to
  `content_b64`; both set is `bad_payload`, neither set is
  `bad_payload`; explicit empty-string content writes a zero-byte
  file (pointer-typed fields distinguish absent from empty). (2)
  `exec` no longer emits `stdout_b64` / `stderr_b64` when the
  corresponding text is valid UTF-8 — it emits exactly one of
  `stdout` (text) or `stdout_b64` (binary). (3) `upload` gains a
  `local_path` source mode: bridge opens a file on its own disk
  and streams 1 MiB chunks to the daemon, args carry only two
  path strings. Tool descriptions and `AGENTS.md` updated with
  the new decision tree (`edit_file` > `write_file content` >
  `Write` + `upload local_path` > `upload url` > binary
  fallbacks). Acceptance test `TestSmokeF9WriteToolBytesComparison`
  pins the byte savings on a real ~4 KB ASCII fixture: `content`
  ≈ 15% smaller than `content_b64`, `upload local_path` ≈ 95%
  smaller. No protocol or daemon changes.

## Next up

## Deferred (not blocking)

- **P2: F4b — back-to-back burst recovery on `port_forward`.** With
  the AEAD ordering fix landed, single 50-concurrent bursts now go
  through cleanly (~48/50). But firing a second 50-burst immediately
  after the first (no idle gap) still produces 0/50 success on the
  second round. The first burst recovers fully if given ~5s idle, so
  this isn't a permanent state corruption — likely a cleanup-window
  or stream-slot leak. Candidates to look at next: leaked
  handleStreamOpen goroutines holding local TCP keep-alive conns,
  HOL block at bridge `runReader` / daemon `deliverStreamData` if
  per-stream channels (cap 64) fill, and the python test server's
  default accept queue (5).

- **P2: F7 — bridge hang on duplicate identity** (in progress on `phase-c`).
  When two bridges connect with the same `(name, api_key)`, the older
  bridge's in-flight tool calls hang because the gateway re-routes the
  daemon→bridge writer to the newer one. Affects power-user setups
  where `agbridge bridge` runs detached (tmux/systemd); child-process
  supervision by an MCP client (e.g. Claude Code) is not affected
  because SIGTERM closes the conn cleanly.

  Workaround until fix lands: kill the older bridge process.

  Fix: gateway sends a `session_replaced` error frame to the old conn
  before closing it. Bridge router detects the code, sets an `evicted`
  flag, and the supervisor exits with status 75 (EX_TEMPFAIL) instead
  of reconnecting. A naive close-only approach causes a reconnect
  storm (old reconnects, kicks new, new reconnects, kicks old, …); the
  explicit signal breaks the loop.

  systemd users should set `RestartPreventExitStatus=75` to honor the
  eviction; otherwise systemd will respawn the bridge into the storm.

- **Admin UI.** No HTTP management surface yet. CLI + `state.json` +
  `audit.jsonl` cover current operational needs; revisit when
  multi-tenancy demands it.
