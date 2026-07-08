#!/usr/bin/env bash
# agbridge install script.
#
# Usage:
#   curl -sSL https://github.com/pw0rld/agbridge/raw/main/scripts/install.sh | bash
#
# Installs into ~/bin by default — no sudo, no root needed. Make sure
# ~/bin is on your PATH; the script prints a one-liner to add it if it
# isn't.
#
# Environment overrides:
#   VERSION=v0.1.0-phase-b   pin a specific release (default: latest)
#   INSTALL_DIR=/opt/bin     install path (default: $HOME/bin)
#   REPO=user/fork           target a fork (default: pw0rld/agbridge)
#   AGBRIDGE_GITHUB_PROXIES  whitespace-separated proxy prefixes
#                            (default: several public GitHub proxies)
#   AGBRIDGE_DOWNLOAD_TIMEOUT seconds before falling back when curl gets no response/data
set -euo pipefail

REPO="${REPO:-pw0rld/agbridge}"
VERSION="${VERSION:-}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/bin}"
AGBRIDGE_DOWNLOAD_TIMEOUT="${AGBRIDGE_DOWNLOAD_TIMEOUT:-20}"
AGBRIDGE_GITHUB_PROXIES="${AGBRIDGE_GITHUB_PROXIES:-${GITHUB_PROXIES:-${GITHUB_PROXY_URL:-}}}"
if [ -z "$AGBRIDGE_GITHUB_PROXIES" ]; then
  AGBRIDGE_GITHUB_PROXIES="https://gh.llkk.cc/ https://ghfast.top/ https://ghproxy.net/"
fi

curl_download() {
  local url="$1"
  local out="$2"
  curl -fsSL \
    --connect-timeout "$AGBRIDGE_DOWNLOAD_TIMEOUT" \
    --speed-time "$AGBRIDGE_DOWNLOAD_TIMEOUT" \
    --speed-limit 1 \
    "$url" -o "$out"
}

proxied_url() {
  local proxy="$1"
  local url="$2"
  if [ -z "$proxy" ]; then
    printf '%s\n' "$url"
    return
  fi
  printf '%s/%s\n' "${proxy%/}" "$url"
}

download_with_fallback() {
  local url="$1"
  local out="$2"
  local label="$3"
  local candidate
  local proxy

  echo "agbridge: downloading $label from $url"
  if curl_download "$url" "$out"; then
    return 0
  fi

  for proxy in $AGBRIDGE_GITHUB_PROXIES; do
    candidate="$(proxied_url "$proxy" "$url")"
    echo "agbridge: primary download failed; trying proxy $proxy"
    if curl_download "$candidate" "$out"; then
      echo "agbridge: downloaded $label via proxy $proxy"
      return 0
    fi
  done

  echo "agbridge: failed to download $label from GitHub or configured proxies" >&2
  return 1
}

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

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

if [ -z "$VERSION" ]; then
  echo "agbridge: detecting latest release for $REPO …"
  LATEST_JSON="$TMPDIR/latest.json"
  if ! download_with_fallback "https://api.github.com/repos/${REPO}/releases/latest" "$LATEST_JSON" "latest release metadata"; then
    echo "agbridge: could not detect latest release; pass VERSION=…" >&2
    exit 1
  fi
  VERSION="$(grep '"tag_name":' "$LATEST_JSON" | head -1 | sed -E 's/.*"([^"]+)".*/\1/')"
  if [ -z "$VERSION" ]; then
    echo "agbridge: could not detect latest release; pass VERSION=…" >&2
    exit 1
  fi
fi

BIN="agbridge-${OS}-${ARCH}"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${BIN}"
SUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS"

TMP="$TMPDIR/$BIN"
SUMS="$TMPDIR/SHA256SUMS"

if ! download_with_fallback "$URL" "$TMP" "$BIN"; then
  exit 1
fi

# Verify checksum if SHA256SUMS exists alongside the binary.
if download_with_fallback "$SUMS_URL" "$SUMS" "SHA256SUMS" 2>/dev/null; then
  EXPECTED="$(grep " $BIN\$" "$SUMS" | awk '{print $1}' || true)"
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

mkdir -p "$INSTALL_DIR"
if [ ! -w "$INSTALL_DIR" ]; then
  echo "agbridge: $INSTALL_DIR is not writable" >&2
  echo "agbridge: pick a writable path, e.g. INSTALL_DIR=\$HOME/bin or INSTALL_DIR=/tmp" >&2
  exit 1
fi
install -m 0755 "$TMP" "$INSTALL_DIR/agbridge"

echo "agbridge: installed to $INSTALL_DIR/agbridge"

case ":${PATH}:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo
    echo "agbridge: $INSTALL_DIR is not on your PATH. Add this line to your shell rc:"
    echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
    echo
    ;;
esac

"$INSTALL_DIR/agbridge" --help 2>&1 | head -3 || true
