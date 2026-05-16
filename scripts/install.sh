#!/usr/bin/env bash
# agbridge install script.
#
# Usage:
#   curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash
#
# Environment overrides:
#   VERSION=v0.1.0-phase-b   pin a specific release (default: latest)
#   INSTALL_DIR=/opt/bin     install path (default: /usr/local/bin)
#   REPO=user/fork           target a fork (default: pw0rld/agbridge)
set -euo pipefail

REPO="${REPO:-pw0rld/agbridge}"
VERSION="${VERSION:-}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux|darwin) ;;
  *) echo "agbridge: unsupported OS: $OS" >&2; exit 1 ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "agbridge: unsupported arch: $ARCH" >&2; exit 1 ;;
esac

if [ -z "$VERSION" ]; then
  echo "agbridge: detecting latest release for $REPO …"
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name":' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')"
  if [ -z "$VERSION" ]; then
    echo "agbridge: could not detect latest release; pass VERSION=…" >&2
    exit 1
  fi
fi

BIN="agbridge-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${BIN}"
SUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS"

TMP="$(mktemp)"
trap 'rm -f "$TMP" "$TMP.sums"' EXIT

echo "agbridge: downloading $URL"
if ! curl -fsSL "$URL" -o "$TMP"; then
  echo "agbridge: download failed" >&2
  exit 1
fi

# Verify checksum if SHA256SUMS exists alongside the binary.
if curl -fsSL "$SUMS_URL" -o "$TMP.sums" 2>/dev/null; then
  EXPECTED="$(grep " $BIN\$" "$TMP.sums" | awk '{print $1}' || true)"
  if [ -n "$EXPECTED" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      ACTUAL="$(sha256sum "$TMP" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
      ACTUAL="$(shasum -a 256 "$TMP" | awk '{print $1}')"
    else
      echo "agbridge: no sha256sum/shasum found; skipping checksum verification" >&2
      ACTUAL="$EXPECTED"
    fi
    if [ "$EXPECTED" != "$ACTUAL" ]; then
      echo "agbridge: checksum mismatch! expected $EXPECTED got $ACTUAL" >&2
      exit 1
    fi
    echo "agbridge: SHA-256 verified ($ACTUAL)"
  fi
fi

if [ -w "$INSTALL_DIR" ]; then
  install -m 0755 "$TMP" "$INSTALL_DIR/agbridge"
else
  echo "agbridge: installing to $INSTALL_DIR (sudo)"
  sudo install -m 0755 "$TMP" "$INSTALL_DIR/agbridge"
fi

echo "agbridge: installed to $INSTALL_DIR/agbridge"
"$INSTALL_DIR/agbridge" --help 2>&1 | head -3 || true
