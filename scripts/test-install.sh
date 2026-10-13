#!/usr/bin/env bash
# Offline platform-selection checks; optionally test another rendered installer.
set -euo pipefail
script="${1:-$(cd "$(dirname "$0")" && pwd)/install.sh}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/mock"
printf '#!/usr/bin/env bash\necho fixture\n' > "$work/payload"
export FIXTURE_PAYLOAD="$work/payload"
export FIXTURE_SHA="$(sha256sum "$FIXTURE_PAYLOAD" | awk '{print $1}')"

cat > "$work/mock/uname" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
  -s) printf '%s\n' "$FIXTURE_OS" ;;
  -m) printf '%s\n' "$FIXTURE_ARCH" ;;
  *) exit 1 ;;
esac
MOCK
cat > "$work/mock/curl" <<'MOCK'
#!/usr/bin/env bash
set -eu
url=''
out=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    --connect-timeout|--speed-time|--speed-limit) shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  */SHA256SUMS) printf '%s  %s\n' "$FIXTURE_SHA" "$EXPECTED_ASSET" > "$out" ;;
  */"$EXPECTED_ASSET") cp "$FIXTURE_PAYLOAD" "$out" ;;
  *) echo "unexpected download: $url" >&2; exit 1 ;;
esac
MOCK
chmod +x "$work/mock/uname" "$work/mock/curl"

check() {
  local os="$1" arch="$2" processor="$3" native="$4" expected="$5"
  local destination="$work/install $os $arch $native"
  if env PATH="$work/mock:$PATH" VERSION=v-test INSTALL_DIR="$destination" \
      FIXTURE_OS="$os" FIXTURE_ARCH="$arch" EXPECTED_ASSET="$expected" \
      PROCESSOR_ARCHITECTURE="$processor" PROCESSOR_ARCHITEW6432="$native" \
      bash "$script" > "$work/log" 2>&1; then
    if [ "$expected" = unsupported ]; then
      echo "unexpected success for $os/$arch" >&2
      exit 1
    fi
  else
    if [ "$expected" = unsupported ]; then
      grep -q 'unsupported' "$work/log"
      echo "PASS rejected $os/$arch"
      return
    fi
    cat "$work/log" >&2
    exit 1
  fi
  local installed=agbridge
  case "$expected" in *.exe) installed=agbridge.exe ;; esac
  cmp "$FIXTURE_PAYLOAD" "$destination/$installed"
  grep -q 'SHA-256 verified' "$work/log"
  echo "PASS $os/$arch -> $expected"
}

check Linux x86_64 '' '' agbridge-linux-amd64
check Linux aarch64 '' '' agbridge-linux-arm64
check Darwin x86_64 '' '' agbridge-darwin-amd64
check Darwin arm64 '' '' agbridge-darwin-arm64
check MINGW64_NT-10.0 x86_64 AMD64 '' agbridge-windows-amd64.exe
check MINGW64_NT-10.0 x86_64 AMD64 ARM64 agbridge-windows-arm64.exe
check MINGW32_NT-10.0 i686 x86 AMD64 agbridge-windows-amd64.exe
check MSYS_NT-10.0 x86_64 '' '' agbridge-windows-amd64.exe
check CYGWIN_NT-10.0 aarch64 '' '' agbridge-windows-arm64.exe
# WSL is Linux even when Windows architecture variables are inherited.
check Linux x86_64 ARM64 ARM64 agbridge-linux-amd64
check FreeBSD x86_64 '' '' unsupported
check Linux riscv64 '' '' unsupported
check MINGW32_NT-10.0 i686 x86 '' unsupported
