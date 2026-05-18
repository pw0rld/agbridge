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

## Next up

- **P1: F6 — large file streaming for `read_file` / `download`.**
  Both currently buffer the whole file daemon-side before responding,
  risking OOM on >100 MB files. Plan: reuse the S1 MCP-progress
  infrastructure (`mcp.EmitterFromContext`) to forward `FileChunk`
  frames as progress notifications instead of buffering.

## Deferred (not blocking)

- **P2: F4 — `port_forward` concurrent timeout.** Stress test shows
  7–20 of 20 parallel HTTP requests through one tunnel timing out.
  Root cause not yet investigated; possibly head-of-line blocking on
  the multiplexed Noise stream, or a daemon-side connection cap.

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
