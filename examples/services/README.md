# Run agbridge at login

Templates for keeping the two long-lived agbridge processes alive
across logout/reboot. One binary, two roles — pick the machine each
belongs to:

| Process | Runs on | Linux | macOS |
|---|---|---|---|
| `agbridge daemon` | the **remote box** behind NAT | auto-installed by `enroll` | `com.agbridge.daemon.plist` |
| `agbridge agent` | the **control machine** running the CLI (optional, connection reuse) | `agbridge-agent.service` | `com.agbridge.agent.plist` |

The gateway is a server: run it however you run long-lived services
(system unit, container, …) — nothing here applies to it.

## Linux

`agbridge enroll` already does the daemon for you: as root it writes
and enables a **system** unit `agbridge-daemon.service` (daemon runs
as a non-root user, `--service-user <name>` to choose which); as a
regular user it writes and enables a **user** unit. If it could not
(e.g. `--no-start-service`, or the binary was moved afterwards),
`agbridge-daemon.service` in this directory is a faithful copy of
what gets generated — sed the two placeholders and drop it in
`~/.config/systemd/user/`.

User units die at logout unless lingering is on:

```bash
sudo loginctl enable-linger $USER
```

The **agent broker** has no built-in installer; use
`agbridge-agent.service`:

```bash
mkdir -p ~/.config/systemd/user
sed -e "s|__EXE__|$(command -v agbridge)|" \
    -e "s|__STATE_DIR__|$HOME/.config/agbridge|" \
    agbridge-agent.service > ~/.config/systemd/user/agbridge-agent.service
systemctl --user daemon-reload
systemctl --user enable --now agbridge-agent.service
journalctl --user -u agbridge-agent.service -f
```

## macOS

plists accept neither `~` nor `$VARs`, so each template has two
placeholders — `__EXE__` (path to the `agbridge` binary, e.g.
`/usr/local/bin/agbridge` or `/opt/homebrew/bin/agbridge`) and
`__STATE_DIR__` (your `--state-dir`, default
`/Users/<you>/.config/agbridge`). Logs land in
`__STATE_DIR__/<name>.{out,err}.log` (the daemon and agent log to
stderr; there is no file-logging flag).

```bash
mkdir -p ~/Library/LaunchAgents
for f in com.agbridge.agent; do            # or com.agbridge.daemon
  sed -e "s|__EXE__|$(command -v agbridge)|" \
      -e "s|__STATE_DIR__|$HOME/.config/agbridge|" \
      "$f.plist" > ~/Library/LaunchAgents/"$f".plist
  launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/"$f".plist
done
```

Day-2 commands (`<label>` = `com.agbridge.agent` / `com.agbridge.daemon`):

```bash
launchctl print gui/$(id -u)/<label>          # status, last exit
launchctl kickstart -k gui/$(id -u)/<label>   # restart after editing (re-sed first)
launchctl bootout gui/$(id -u)/<label>        # unload
```

`RunAtLoad` starts it at login, `KeepAlive` respawns crashes. These
are LaunchAgents (per-user, in your GUI session): the daemon needs no
privileges beyond reading its own state dir, so no LaunchDaemon is
required. If the machine must host daemons without anyone logging in,
move the plist to `/Library/LaunchDaemons`, add a `UserName` key, and
use `gui/$(id -u)` → `system/` in the commands above.

## Agent-specific notes

- The broker's socket defaults to `agent.sock` inside your state
  dir; the CLI finds it automatically. Override with
  `AGBRIDGE_AGENT_SOCKET=/path`, disable with `AGBRIDGE_NO_AGENT=1`.
- Templates pass `--exit-after 0` (resident broker). Without it the
  agent exits after 60 min idle; `KeepAlive`/`Restart=always` will
  just respawn it, so either mode is safe under a service manager.
- The broker reads the same state dir as the CLI — run it as the
  same user, and don't share one state dir between users.
