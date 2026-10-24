# Driving agbridge from an agent or script

agbridge speaks two protocols: humans get aligned text, machines get
`--json`. This guide covers the machine side — the stable contract you
can branch on, the error codes worth retrying, and how to keep
round-trips cheap when an agent fires dozens of commands.

v0.0.2 is CLI-first — the old stdio MCP bridge is gone, and the CLI
*is* the machine interface. Feed `agbridge ai-help` into an agent's
system prompt, or wrap the commands below in your own tool layer
(including an MCP shim, if you need one).

## The `--json` contract

Every CLI verb accepts `--json`. Machine mode is guaranteed stable:

- **Success** → exit `0`, stdout holds **exactly one JSON object**
  (shape is per-command, e.g. `get` returns
  `{"remote","local","size","sha256","segments?"}`), stderr silent.
- **Failure** → exit `1`, stdout still holds **exactly one JSON
  object** (`{"ok":false,"error":"..."}`, or the command's own
  envelope — never two), and stderr carries a structured detail line
  `{"code":"...","message":"...","retry_safe":true|false}` plus a
  plain-text error. Never a usage dump.
- **Human mode** (no `--json`) → stdout is aligned text on success,
  empty on failure; one plain error line on stderr.

So `jq` can always parse stdout when you pass `--json`, on both the
success and failure paths.

### Error codes and retry policy

The structured `code` on stderr (and `_meta.error_code` inside tool
results) is one of:

| Code | Meaning | Retry? |
|---|---|---|
| `network_lost` | tunnel dropped mid-call | yes, with backoff |
| `agent_unavailable` | agent broker gone | yes — falls back to a direct dial |
| `handshake_failed` | gateway rejected the hello | no — creds/pin issue |
| `state_unreadable` / `state_unwritable` | local `state.json` problem | no — fix disk/permissions |
| `working_dir_unavailable` | stored cwd missing on daemon | no — `cd` again |
| `unknown_target` | `-t` name not in state | no — check `agbridge targets` |
| `protocol_error` | peer sent something undecodable | no — version skew |
| `not_found`, `not_a_directory`, `open_failed`, `exceeds_max_size`, `tool_forbidden` | daemon-side tool result | no — it's an answer, not a fault |
| `bad_payload`, `agent_error` | bug-class | no — report it |

`retry_safe:true` marks the transport-transient ones; treat everything
else as final for this invocation.

## Keeping round-trips cheap

A bare CLI invocation does the full cold path — TCP, TLS, WebSocket,
hello, Noise handshake — every call. Start the **agent broker** once
per machine and every invocation reuses one hot connection per
gateway+target:

```bash
agbridge agent                      # foreground; put under launchd/systemd
agbridge --state-dir ~/.config/agbridge exec -- uname -a   # auto-uses the socket
```

The CLI uses the broker automatically when its socket exists; set
`AGBRIDGE_AGENT_SOCKET=/path` to relocate or `AGBRIDGE_NO_AGENT=1` to
force a direct dial. On an LAN this cuts per-command latency several
fold; across continents it removes 5-7 RTTs. Templates for running
the broker at login live in [services/](services/).

## Concurrency

Parallel invocations are a first-class case (agents fan out tool
calls). Local state updates take a cross-process file lock, so
concurrent `cd`, `enroll`, or `config use` never interleave into
corrupted state. Per-connection request multiplexing means you can
also run many concurrent commands over one broker socket.

## Worked examples

**Bash: retry transport errors only.**

```bash
fetch() {
  for i in 1 2 3; do
    out=$(agbridge --state-dir "$STATE" -t lab01 --json get "$1" "/tmp/$(basename "$1")" 2>/tmp/err) && { echo "$out"; return 0; }
    grep -q '"retry_safe":true' /tmp/err || { cat /tmp/err >&2; return 1; }
    sleep $((i * 2))
  done
  return 1
}
```

**Python: structured dispatch.**

```python
import json, subprocess

def run(*args, state="~/.config/agbridge", target="lab01"):
    p = subprocess.run(
        ["agbridge", "--state-dir", state, "-t", target, "--json", *args],
        capture_output=True, text=True,
    )
    out = json.loads(p.stdout)          # guaranteed exactly one object
    if p.returncode == 0:
        return out
    detail = json.loads(p.stderr.splitlines()[-1] or "{}")
    if detail.get("retry_safe"):
        raise RetryLater(detail["message"])
    raise Fatal(detail.get("code", "unknown"), out.get("error", ""))

run("exec", "--", "git", "rev-parse", "HEAD")
info = run("get", "/srv/big.tar.zst", "/tmp/big.tar.zst")
print(info["size"], info.get("segments", "sequential"))  # >=16 MiB transfers are segmented automatically
```

**Discovery without guessing.** `agbridge caps --json` reports what
the paired daemon supports (protocol generation, exec policy);
`agbridge targets --json` lists enrollable targets; `agbridge doctor
--json` diagnoses enrollment/connectivity. Feed `agbridge ai-help`
output straight into an agent's system prompt for a ready-made tool
cheatsheet.

## Timeouts and sizes

- Two independent clocks bound an `exec`: the global `--timeout`
  bounds the whole CLI call end-to-end (default 60 s), while
  `exec --exec-timeout` bounds the daemon-side subprocess itself
  (milliseconds, default 60000). Raise both together for long
  builds; on expiry the daemon kills the whole process tree.
- `exec` spawns `cmd` + `args` directly — no shell — so pipelines
  and `&` need an explicit interpreter:
  `exec -- /bin/sh -c 'make -j8 > /tmp/build.log 2>&1'`. For work
  outliving any sane timeout, detach it and poll files:
  `exec -- /bin/sh -c 'nohup long.sh >/tmp/job.log 2>&1 &'`, then
  `cat`/`stat /tmp/job.log`.
- `exec --cwd DIR` pins the working directory for one call without
  touching the stored session cwd; `--env KEY=VALUE` (repeatable)
  adds environment variables.
- **Remote exit codes don't become local ones.** If the call itself
  succeeds, `exec` exits `0` even when the remote command failed —
  branch on `.exitcode` in the `exec --json` object
  (`{stdout,stderr,exitcode,duration_ms}`), not on `$?`.
- Inline results are capped: `cat` / `write` / `edit` payloads fail
  with `exceeds_max_size` beyond 10 MiB, and `exec` stdout/stderr are
  silently truncated at 1 MiB per stream. Don't `cat` big files or
  let `exec` print megabytes — move whole files with `put`/`get`,
  which chunk through the tunnel and have no such cap. `get` streams
  files of any size; at ≥16 MiB it fetches in parallel segments
  automatically and reports `segments` in its JSON result.
