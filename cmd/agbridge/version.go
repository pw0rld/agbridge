package main

// agbridgeVersion is the binary version string surfaced in --version and
// the `agbridge status` capabilities response. Release builds override it
// via -ldflags "-X main.agbridgeVersion=<tag>" (see .github/workflows/
// release.yml), so this default only applies to local/dev builds — keep
// it at the current release tag as a sane fallback.
var agbridgeVersion = "v0.0.4"
