# AGENTS.md — agbridge tool usage for AI assistants

> **agbridge v0.0.2 — invoked as a CLI subprocess.** No MCP server, no
> daemon process on the AI host. Each call spawns `agbridge <subcommand>`,
> which dials the gateway, runs one tool, and exits.

This file is meant to be read by an AI client (Claude, Codex, Cline, …)
at session start so it knows how to use the agbridge CLI.

## What you're connected to

When you invoke `agbridge ls`, `agbridge cat`, `agbridge exec`, etc.,
they operate on a **remote machine** (the daemon host) — not the host
you were spawned on. Treat them as a thin RPC into someone else's
filesystem and shell.

- File paths in CLI args refer to the **daemon's** filesystem.
- `agbridge exec` runs in the **daemon's** shell, not yours.
- The daemon's bound is its unprivileged OS user (it refuses to run
  as root) plus `exec_enabled` + env allowlist; there is no separate
  path allowlist to stay inside.
- Run `agbridge ai-help` for a compact cheat sheet, or `agbridge caps`
  to inspect the daemon's policy / version / capabilities.

## Global flags

- `-t, --target <name>` — pick a non-default daemon for this call.
  Without it, the call goes to the target set by `agbridge config use`.
- `--json` — switch stdout to a stable structured JSON shape with
  `_schema:1`. Use this when you need to parse output reliably.
- `--state-dir <path>` — point at a non-default state directory.
- `--timeout <duration>` — per-call total deadline (default `1m0s`).

Errors are always single-line JSON on stderr regardless of `--json`:
`{"code":"...","message":"...","retry_safe":true|false}`. Exit code is
`0` on success, `1` on any error.

## The commands, by purpose

### Reading

- `agbridge cat <path> [--offset N] [--limit N]` — pull text/binary to
  stdout. Returns content + sha256 in `--json` mode. **10 MB inline
  cap.** For larger files use `--offset`+`--limit` for partial reads,
  or `agbridge get` for streaming.
- `agbridge ls <path> [-R] [--depth N]` — directory listing. Cheap.
- `agbridge stat <path>` — single-path metadata.
- `agbridge grep <pattern> [<path>] [--ignore-case] [--glob ...]` —
  pure-Go content search across files. Prefer this over
  `agbridge exec -- grep` — faster, predictable schema, no shell
  quoting.

### Writing

- `agbridge write <path> --content "..."` or
  `agbridge write <path> --from-file /local/path` — atomic write (temp
  + rename). **10 MB content cap.** Use `--content` for short literal
  text; use `--from-file` to upload bytes already on disk.
- `agbridge edit <path> --old "<s>" --new "<s>"` — partial edit. Daemon
  keeps an auto-GC'd backup, so you can iterate safely.
  `--replace-all` flips from "exactly one match" to "all occurrences".

### Transfer

- `agbridge put <local> <remote> [--mode 0644]` — bridge → daemon. The
  bytes never appear in the CLI args; only the two paths do.
- `agbridge get <remote> <local>` — daemon → bridge. Streams to disk,
  no inline content size cap on the call.

### Execution

- `agbridge exec [--cwd PATH] [--env K=V]... [--exec-timeout MS] -- <cmd> [args...]`
  — single command, blocking, 10 MB stdout/stderr cap, own process
  group, SIGKILL on timeout.

Note: long-running servers, watchers, port forwarding, and tail-followers
(`background_exec`, `port_forward`, `kill_process`) are **not available
in v0.0.2**. They need a long-lived local process and are planned for
v0.0.3 via an optional local daemon mode.

### Discovery and target selection

- `agbridge status` — quick "can I reach the gateway / handshake works?"
  check. Prints handshake latency.
- `agbridge targets` — list daemon targets known to local state.
- `agbridge config use <name>` — set the default target so later calls
  can omit `-t`.
- `agbridge caps` — show the daemon's allowed paths, env allowlist,
  forbidden ports, version. Useful before assuming a path or env var
  works.

## Key behaviours you should bake in

1. **10 MB inline cap on `cat`, `write`, and `exec` stdout/stderr.**
   If you don't know the file size, default to `agbridge get` (streams
   to disk) instead of `agbridge cat`.
2. **`--offset`/`--limit` exist on `cat`.** Use them when you only need
   part of a file — way cheaper than full read.
3. **Errors are typed**, in the stderr JSON's `code`:
   - `bad_payload` — your args are wrong (path empty, offset negative,
     missing `--old` on `edit`, …). Don't retry blindly; fix the arg.
   - `tool_forbidden` — the daemon refused the tool (e.g. `exec` when
     the daemon was enrolled without `--exec-enabled`). Don't retry;
     the operator must re-issue the token and re-enroll.
   - `not_found` — file/dir doesn't exist. Verify with
     `agbridge ls <parent>`.
   - `exceeds_max_size` — hit a 10 MB cap. Use `agbridge get` instead
     of `cat`, or split with `--offset`+`--limit`.
   - `network_lost` — transport hiccup. `retry_safe:true`. Safe to
     retry once; if it keeps failing, surface to user.
   - `handshake_failed` — couldn't reach or handshake with the gateway.
     Check `agbridge status`.
   - `timeout` — `exec` hit `--exec-timeout`. Raise it or break the job
     into smaller chunks.

## Token-saving priorities

When choosing how to write or transfer content, pick the cheapest
option first:

1. **Editing an existing file** → `agbridge edit <path> --old "..." --new "..."`.
   Smallest diff, lowest token cost. Use this over read+write whenever
   the change is local.
2. **New file, content < ~8 KB** → `agbridge write <path> --content "..."`.
   Inline literal.
3. **New file, content > ~8 KB OR you want to iterate locally** →
   write to a local path (e.g. `/tmp/<name>`), then
   `agbridge put /tmp/<name> /remote/...`. The agbridge CLI call carries
   only the two paths; the bytes never appear in the tool-call trace.
4. **Large file read** → `agbridge get <remote> /tmp/<name>`. Streams
   to disk, then read locally if you need to inspect.
5. **Don't** double-roundtrip with `cat` then `write` for a small
   change — `edit` does it in one call and leaves a daemon-side backup.
6. **Don't** prefer `--from-file` over `--content` for short strings —
   `--content` avoids touching the local disk and is shorter.
7. **`exec` stdout is plain text when UTF-8** — read it directly. For
   binary output, prefer redirecting to a daemon-side file and `get`-ing
   it back, rather than passing through `exec`.

## Common recipes

### "Read all the source files in a project"

```
agbridge ls /some/project           # see the layout
agbridge grep "TODO" /some/project  # find what's interesting
agbridge cat /some/project/foo.go   # pull the ones you need
```

Don't open every file blindly. Use `grep` + `ls` to narrow first.

### "Edit a config file"

```
agbridge cat /etc/foo.conf
# look at what's there
agbridge edit /etc/foo.conf \
  --old "bind = 127.0.0.1" \
  --new "bind = 0.0.0.0"
```

Prefer `edit` over `cat` + `write` for partial edits — the daemon keeps
an auto-GC'd backup, and you don't accidentally rewrite the whole file.

### "Run a build remotely"

```
agbridge exec --cwd /path/to/repo --exec-timeout 120000 -- go build ./...
```

Raise `--exec-timeout` (ms) if the build is slow. Default is 60000.

### "Get a 50 MB binary back to my machine"

```
agbridge get /path/to/big.bin /tmp/big.bin
```

Use `get` (streaming) for anything you suspect exceeds 10 MB. Avoid
`cat` for large files.

### "Push a small config up"

```
agbridge put /tmp/nginx.conf /etc/nginx/nginx.conf --mode 0644
```

## Coordination with other tools

If both `agbridge` and your local shell can touch files, they overlap.
Rule of thumb:

- Local fs / local shell → files on the **same host as the AI client**.
- `agbridge` commands → files on the **daemon host** (remote dev box,
  prod debugging shell, etc.).

When the user says "the project", clarify which side they mean — or
just ask. Don't silently pick.

## Things that will surprise you

- **`exec` in default mode passes remote stdout/stderr through to your
  local stdout/stderr but does NOT propagate the remote exit code.** If
  you care about exit status, use `--json` and read the `exitcode`
  field.
- **Empty `grep` matches return `matches: []`, not `null`** in `--json`
  mode — safe to iterate.
- **`edit` defaults to requiring exactly one match** of `--old`. Pass
  `--replace-all` if the substring appears multiple times.
- **`agbridge` itself is one-shot.** Every call re-dials and
  re-handshakes. There's no persistent session to manage; just call
  what you need.

## When agbridge is the wrong tool

- The file is **small** and **already on the AI client's host**. Use
  your local fs tool — saves a round-trip and a TLS handshake.
- You need to run something **interactive** (REPL, ncurses, etc.).
  `exec` is non-interactive only; a v0.0.3 stateful tool is planned.
- You need to **keep a long-running process alive across calls**.
  Not supported in v0.0.2 (deferred to v0.0.3).
- You need to **forward a TCP port to the daemon's network**. Not
  supported in v0.0.2 (deferred to v0.0.3).

## If something goes wrong

- Daemon side: error responses include enough detail to debug (the
  per-tool error code, the literal exit code in `exec` failures).
  Read them carefully before retrying.
- Transport side: `network_lost` and `handshake_failed` are recoverable
  (`retry_safe:true`); everything else means you need to change your
  call.
- If `agbridge status` fails, check the gateway URL in `agbridge config
  show` and run `agbridge doctor`.
