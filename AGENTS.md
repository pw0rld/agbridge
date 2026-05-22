# AGENTS.md — agbridge tool usage for AI assistants

This file is meant to be read by an AI client (Claude, Codex, Cline, …)
at session start so it knows how to use the agbridge MCP tools.

## What you're connected to

When you see tool names like `read_file`, `write_file`, `exec`,
`download`, etc. coming from an MCP server named **agbridge**, they
operate on a **remote machine** (the daemon host) — not the host you
were spawned on. Treat them as a thin RPC into someone else's
filesystem and shell.

- File paths in tool args refer to the **daemon's** filesystem.
- `exec` runs in the **daemon's** shell, not yours.
- The daemon may have a path allowlist; calls outside it return
  `path_forbidden` immediately — don't retry without a different path.

## The tools, by purpose

### Reading

- `read_file path [offset] [limit]` — pull text/binary. Returns content
  + sha256 in `_meta`. **10 MB inline cap.** For larger files use
  `offset`+`limit` for partial reads, or `download` with `dest_path`.
- `list_directory path` — immediate children. Cheap.
- `stat path` — single-path metadata.
- `search` — pure-Go content search across files. Prefer this over
  `exec grep` — faster, predictable output schema, no shell quoting.

### Writing

- `write_file path content_b64 [mode]` — atomic write (temp + rename).
  **10 MB content cap.** Encode bytes with base64 before sending.
- `edit_file path old_string new_string` — partial edit. Daemon keeps
  an auto-GC'd backup, so you can iterate safely.

### Transfer

- `upload` — bridge → daemon. Two source modes: inline bytes or fetch
  from URL.
- `download src_path [dest_path] [offset] [limit]` — daemon → bridge.
  - **With `dest_path`**: streams chunks straight to disk on the
    bridge host, no inline content returned, no size cap. Use this
    for anything you suspect is > 1 MB.
  - **Without `dest_path`**: inline content in the result, **10 MB
    cap**. If it returns `exceeds_max_size`, retry with `dest_path`.

### Execution

- `exec cmd args [cwd] [env] [timeout_ms]` — single command, blocking,
  10 MB stdout/stderr cap, own process group, SIGKILL on timeout. Don't
  use for long-running servers — use `exec_start` family.
- `exec_start cmd args …` → returns a handle. Then `exec_status` /
  `exec_logs` / `exec_kill` / `exec_list` to manage it. Use for `npm
  run dev`, watchers, tail-followers, anything that doesn't exit on
  its own.

### Network forwarding

- `port_forward remote_host remote_port [local_port]` — opens a local
  TCP listener on the bridge host that tunnels to the daemon side.
- `port_forward_list` / `port_forward_close` — manage active forwards.
- `forbidden_ports` is enforced at the gateway based on the daemon's
  enrollment policy.

## Key behaviours you should bake in

1. **10 MB inline cap on `read_file`, `write_file`, `download`-without-
   `dest_path`, and `exec` stdout/stderr.** If you don't know the file
   size, default to `download dest_path=<local-path>` to avoid wasting
   a round-trip on `exceeds_max_size`.
2. **`offset`/`limit` exist on `read_file` and `download`.** Use them
   when you only need part of a file — way cheaper than full read.
3. **MCP progress notifications** (`notifications/progress`) fire from
   `read_file`, `download`, and `exec` when you pass
   `_meta.progressToken`. They report bytes-received so far. Useful
   for showing the user "10 MiB / 30 MiB transferred" but not required
   — clients without a token get the same final result.
4. **Errors are typed**, in `_meta.error_code`:
   - `bad_payload` — your args are wrong (path empty, offset negative,
     …). Don't retry blindly; fix the arg.
   - `path_forbidden` — outside the daemon's allowlist. Don't retry
     with the same path; ask the user where the file actually lives or
     pick a path inside the allowlist (the daemon hints which paths in
     the error message).
   - `not_found` — file/dir doesn't exist. Verify the path; consider
     `list_directory` of the parent.
   - `exceeds_max_size` — hit a 10 MB cap. Retry with `dest_path` (for
     download) or `offset`+`limit` (for read_file).
   - `network_lost` — transport hiccup. Safe to retry once; if it keeps
     failing, surface to user.
   - `timeout` — `exec` hit `timeout_ms`. Either raise the timeout or
     switch to `exec_start` if it's a long job.

## Token-saving priorities

When choosing how to write or transfer content, pick the cheapest
option first:

1. **Editing an existing file** → `edit_file old_string=… new_string=…`.
   Smallest diff, lowest token cost.
2. **New file, content < ~8 KB** → `write_file path=… content="…"`.
   `content` is raw text (recommended); use `content_b64` only when
   the file is genuinely binary.
3. **New file, content > ~8 KB OR you want to iterate locally** →
   use your native Write tool to put the content at a local path
   (e.g. `/tmp/<name>`), then `upload source="local_path"
   local_path="/tmp/<name>" dest_path="/remote/…"`. The agbridge call
   args carry only the two path strings; the bytes never appear in
   the tool-call trace.
4. **Content already reachable by URL** → `upload source="url"
   url="…" dest_path="…"`. The daemon fetches; the AI carries no
   bytes at all.
5. **Genuinely binary content** → `download dest_path=…` for reads,
   `upload source="chunks" chunks_b64=[…]` only when the bytes are
   small and must travel inline (rare).

Things to NOT do:

- Don't use `write_file content_b64="…"` for text. It's the old
  path; you'd burn ~33% extra tokens on base64.
- Don't expect `exec` to put stdout in both `stdout` and
  `stdout_b64`. As of F9, exactly one is set: text when UTF-8,
  b64 when binary.
- Don't write a Python wrapper that spawns the agbridge binary
  directly. The bridge is an MCP server — use its tools.

## Common recipes

### "Read all the source files in a project"

```
list_directory path=/some/project       # see the layout
search query="TODO" path=/some/project  # find what's interesting
read_file path=/some/project/foo.go     # pull the ones you need
```

Don't open every file blindly. Use `search` + `list_directory` to
narrow first.

### "Edit a config file"

```
read_file path=/etc/foo.conf
# look at what's there
edit_file path=/etc/foo.conf \
  old_string="bind = 127.0.0.1" \
  new_string="bind = 0.0.0.0"
```

Prefer `edit_file` over `read_file` + `write_file` for partial edits —
the daemon keeps an auto-GC'd backup, and you don't accidentally
rewrite the whole file.

### "Run a build remotely"

```
exec cmd=go args=["build","./..."] cwd=/path/to/repo timeout_ms=120000
```

Increase `timeout_ms` if the build is slow. Default is short.

### "Tail logs from a long-running server"

```
exec_start cmd=npm args=["run","dev"] cwd=/app
# → returns {handle: "..."}
exec_logs handle=... offset=0          # initial dump
# poll with increasing offset for streaming
exec_status handle=...                  # is it still running?
exec_kill handle=...                    # stop it
```

### "Get a 50 MB binary back to my machine"

```
download src_path=/path/to/big.bin dest_path=/tmp/big.bin
```

Never use inline `download` (no `dest_path`) for files you suspect
exceed 10 MB.

### "Reach a service on the daemon's network"

```
port_forward remote_host=internal-db.local remote_port=5432
# → returns {local_port: 54321}
# now connect to 127.0.0.1:54321 from the bridge host
```

When you're done:

```
port_forward_close local_port=54321
```

## Coordination with other MCP tools

If both `agbridge` and a local-filesystem MCP server are configured,
they overlap. Rule of thumb:

- Local fs tools → files on the **same host as the AI client**.
- `agbridge` tools → files on the **daemon host** (remote dev box,
  prod debugging shell, etc.).

When the user says "the project", clarify which side they mean — or
just ask. Don't silently pick.

## Things that will surprise you

- **`exec` stdout/stderr return as plain text when UTF-8** —
  read `_meta.stdout` / `_meta.stderr` directly. For binary output
  (non-UTF-8 bytes) the result has `stdout_b64` / `stderr_b64`
  instead; exactly one of text or b64 will be present.
- **Empty `search` matches return `[]`, not `null`** — safe to iterate.
- **`port_forward` keeps holding the local port** until you call
  `port_forward_close` or the bridge process exits.
- **Progress notifications fire per chunk (1 MiB)** — under no
  throttling. A 1 GiB download will emit ~1000 events. Don't render
  one toast per event; aggregate.

## When agbridge is the wrong tool

- The file is **small** and **already on the AI client's host**. Use
  the local fs tool — saves a round-trip.
- You need to run something **interactive** (REPL, ncurses, etc.).
  `exec` is non-interactive only; use `port_forward` + a local client.
- You need to **edit a file repeatedly with small diffs**. Use
  `edit_file`, not `read_file` + `write_file` every time.

## If something goes wrong

- Daemon side: error responses include enough detail to debug (allowed
  paths in `path_forbidden`, the literal exit code in `exec`
  failures). Read them carefully before retrying.
- Transport side: `network_lost` is recoverable; everything else means
  you need to change your call.
- If the bridge itself can't be reached, restarting the AI client
  re-spawns it cleanly.
