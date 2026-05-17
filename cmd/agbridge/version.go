package main

import "github.com/pw0rld/agbridge/internal/mcp"

// agbridgeVersion is the binary version string surfaced in MCP serverInfo
// and the capabilities() response. Update this when cutting a release.
const agbridgeVersion = "0.2.0-dev"

func init() { mcp.Version = agbridgeVersion }
