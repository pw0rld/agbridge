package main

import "github.com/pw0rld/agbridge/internal/mcp"

// agbridgeVersion is the binary version string surfaced in --version, MCP
// serverInfo, and the capabilities() response. Update this when cutting a
// release.
const agbridgeVersion = "v0.0.1"

func init() { mcp.Version = agbridgeVersion }
