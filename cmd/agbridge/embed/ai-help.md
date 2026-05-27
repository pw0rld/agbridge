# agbridge for AI agents

You're an AI assistant with shell access. Use these commands to operate the user's remote machine.

## Common commands

- `agbridge status` — verify connection (call this first)
- `agbridge ls <path>` — list directory
- `agbridge cat <path>` — read file
- `agbridge exec -- <command> [args]` — run a command
- `agbridge edit <path> "<old>" "<new>"` — replace exact-one occurrence
- `agbridge write <path> --content "<text>"` — write file
- `agbridge grep <pattern> [path]` — search

Multi-target: `agbridge -t <name> <command>` overrides the default daemon.

## Token-saving priorities

- Prefer `edit` over `cat`+`write` for small changes
- Use `--content` (raw string) over `--stdin` for inline text — saves base64 overhead
- `grep` with `--max-results 10` if you only need a few hits
- `cat` with `--offset/--limit` to avoid loading large files

## Output

- Default stdout is human-readable
- Add `--json` to any command for stable structured output (top-level `_schema:1`)
- On error: exit 1, stderr has single-line JSON `{"code":"...","message":"...","retry_safe":true|false}`
- `code` is stable; branch on `code`, not `message`

## Common error codes

- `not_found` — path missing on daemon
- `path_traversal` — denied by sandbox
- `exec_timeout` — process exceeded timeout
- `network_lost` — transport-level disconnect (`retry_safe: true`)
- `unknown_target` — `-t <name>` referred to a daemon not in state
- `ambiguous_match` — `edit` old_string occurred multiple times (use `--replace-all`)
- `exceeds_max_size` — file/output too large (use `cat --offset/--limit` or `get`)
