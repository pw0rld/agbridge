# Examples

- **[ai-agent-cli.md](ai-agent-cli.md)** — driving agbridge straight
  from an AI agent or script (no MCP): the stable `--json` contract,
  error-code reference, retry rules, connection reuse with the agent
  broker, and worked shell/Python snippets.
- **[services/](services/)** — per-user service templates so the
  daemon and the agent broker start at login: macOS `launchd`
  plists and Linux systemd user units.
- The repo root also holds **[QUICKSTART.md](../QUICKSTART.md)**
  (end-to-end setup: gateway → daemon → client → first commands) and
  **[docs/deployment.md](../docs/deployment.md)** (production gateway
  hosting, credential lifecycle, monitoring).
