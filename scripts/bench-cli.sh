#!/usr/bin/env bash
# agbridge end-to-end CLI benchmark.
#
# Stands up a throwaway localhost deployment (gateway + one daemon +
# bridge state), then measures what the T1 performance track changed:
#
#   1. exec round-trip latency, cold (one-shot dial per invocation)
#   2. exec round-trip latency, hot (agent connection-reuse broker)
#   3. put throughput (streamed upload)
#   4. get throughput (parallel segmented download for size >= 16 MiB)
#   5. gateway /metrics snapshot for cross-checking counters
#
# Everything runs under a mktemp dir and is torn down on exit; nothing
# outside the work dir is touched. Requires: go, python3 (free-port
# picking + JSON field extraction), curl, /usr/bin/time.
#
# Usage:
#   scripts/bench-cli.sh [--size-mb N] [--iters N] [--keep]
set -euo pipefail

SIZE_MB=32
ITERS=5
KEEP=false
while [[ $# -gt 0 ]]; do
  case "$1" in
    --size-mb) SIZE_MB="$2"; shift 2 ;;
    --iters) ITERS="$2"; shift 2 ;;
    --keep) KEEP=true; shift ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    *) echo "bench-cli: unknown arg: $1" >&2; exit 2 ;;
  esac
done

REPO="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/agbridge-bench.XXXXXX")"
PIDS=()

cleanup() {
  for p in "${PIDS[@]:-}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  if [[ "$KEEP" == true ]]; then
    echo "[*] work dir kept: $WORK"
  else
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

note() { printf '\n== %s ==\n' "$*"; }

# Median-free simplicity: run each timed command ITERS times, report
# every sample plus min and mean (ms). /usr/bin/time -p keeps the
# timing process itself out of the measured span.
time_ms() {
  # /usr/bin/time only has 10 ms resolution, which collapses localhost
  # round trips to "0". python3's perf_counter around subprocess.run
  # gives sub-ms resolution while still counting the agbridge process
  # startup (the interpreter's own start precedes t0). A failing
  # command aborts the bench with its stderr, instead of silently
  # timing ~0 ms.
  python3 - "$@" <<'PYEOF'
import subprocess, sys, time
t0 = time.perf_counter()
r = subprocess.run(sys.argv[1:], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
dt = time.perf_counter() - t0
if r.returncode != 0:
    sys.stderr.write(r.stderr.decode(errors="replace")[-2000:] + "\n")
    sys.exit(1)
print(int(dt * 1000))
PYEOF
}

stats() { # samples in $@ (ms)
  printf '%s\n' "$@" | awk '
    { a[NR]=$1; sum+=$1; if (min==""||$1<min) min=$1 }
    END { printf "  samples:"; for (i=1;i<=NR;i++) printf " %d", a[i];
          printf "\n  min: %d ms   mean: %d ms   n: %d\n", min, sum/NR, NR }'
}

pick_port() {
  python3 - <<'EOF'
import socket
s = socket.socket(); s.bind(("127.0.0.1", 0))
print(s.getsockname()[1]); s.close()
EOF
}

wait_for() { # desc, seconds, cmd...
  local desc="$1" deadline=$(( $(date +%s) + $2 )); shift 2
  until "$@" >/dev/null 2>&1; do
    if (( $(date +%s) >= deadline )); then
      echo "bench-cli: timeout waiting for $desc" >&2
      echo "---- gateway.log ----" >&2; tail -20 "$WORK/gateway.log" >&2 || true
      echo "---- daemon.log ----" >&2; tail -20 "$WORK/daemon.log" >&2 || true
      exit 1
    fi
    sleep 0.2
  done
}

extract() { # json-field
  python3 -c "import sys,json;print(json.load(sys.stdin)['$1'])"
}

note "build"
BIN="$WORK/agbridge"
(cd "$REPO" && go build -o "$BIN" ./cmd/agbridge)
echo "[*] binary: $BIN"

note "cert + configs"
"$BIN" cert gen --cn localhost --out "$WORK/certs" >/dev/null
GW_PORT="$(pick_port)"; MET_PORT="$(pick_port)"
cat > "$WORK/gateway.yaml" <<EOF
listen: 127.0.0.1:$GW_PORT
audit_path: $WORK/audit.jsonl
metrics_listen: 127.0.0.1:$MET_PORT
agents: []
daemons: []
EOF
METRICS_URL="http://127.0.0.1:$MET_PORT/metrics"

note "start gateway"
"$BIN" gateway --config "$WORK/gateway.yaml" \
  --cert "$WORK/certs/cert.pem" --key "$WORK/certs/key.pem" \
  >"$WORK/gateway.log" 2>&1 &
PIDS+=($!)
wait_for "gateway /metrics" 15 curl -sf "$METRICS_URL"

jtok() { # role name extra...
  local role="$1" name="$2"; shift 2
  "$BIN" issue-token --config "$WORK/gateway.yaml" \
    --role "$role" --name "$name" "$@" --json
}

note "onboard daemon (benchd)"
DTOK="$(jtok daemon benchd --exec-enabled --default-working-dir "$WORK" | extract token)"
"$BIN" enroll --gateway "wss://127.0.0.1:$GW_PORT/" --token "$DTOK" \
  --state-dir "$WORK/dstate" --no-start-service >"$WORK/enroll-d.log" 2>&1
"$BIN" daemon --state-dir "$WORK/dstate" >"$WORK/daemon.log" 2>&1 &
PIDS+=($!)
wait_for "daemon online (metrics gauge)" 20 \
  sh -c "curl -sf '$METRICS_URL' | grep -q '^agbridge_daemon_connections_active 1'"

note "onboard bridge (benchb)"
BTOK="$(jtok bridge benchb --target benchd | extract token)"
"$BIN" enroll --gateway "wss://127.0.0.1:$GW_PORT/" --token "$BTOK" \
  --state-dir "$WORK/bstate" >"$WORK/enroll-b.log" 2>&1
CLI=( "$BIN" --state-dir "$WORK/bstate" -t benchd )

note "exec latency, cold (one-shot dial, ${ITERS}x)"
cold=()
for (( i=0; i<ITERS; i++ )); do
  cold+=( "$(AGBRIDGE_NO_AGENT=1 time_ms "${CLI[@]}" exec -- /bin/echo bench)" )
done
stats "${cold[@]}"

note "start agent broker"
"$BIN" agent --socket "$WORK/agent.sock" --idle-timeout 5m >"$WORK/agent.log" 2>&1 &
PIDS+=($!)
wait_for "agent socket" 10 test -S "$WORK/agent.sock"

note "exec latency, hot (agent reuse, ${ITERS}x)"
hot=()
for (( i=0; i<ITERS; i++ )); do
  hot+=( "$(AGBRIDGE_AGENT_SOCKET="$WORK/agent.sock" time_ms "${CLI[@]}" exec -- /bin/echo bench)" )
done
stats "${hot[@]}"

note "put ${SIZE_MB} MiB (streamed upload)"
dd if=/dev/urandom of="$WORK/blob.bin" bs=1048576 count="$SIZE_MB" 2>/dev/null
put_ms="$(AGBRIDGE_NO_AGENT=1 time_ms "${CLI[@]}" put "$WORK/blob.bin" "$WORK/blob.remote.bin")"
printf '  put: %d ms  (%d MiB/s)\n' "$put_ms" \
  "$(python3 -c "print(max(1, int($SIZE_MB*1000/$put_ms)))")"

note "get ${SIZE_MB} MiB (expect parallel segments: size >= 16 MiB)"
GET_JSON="$WORK/get.json"
get_ms="$(AGBRIDGE_NO_AGENT=1 python3 - "$GET_JSON" "${CLI[@]}" --json get "$WORK/blob.remote.bin" "$WORK/blob.copy.bin" <<'PYEOF'
import subprocess, sys, time
out_path, cmd = sys.argv[1], sys.argv[2:]
t0 = time.perf_counter()
with open(out_path, "wb") as out:
    r = subprocess.run(cmd, stdout=out, stderr=subprocess.PIPE)
dt = time.perf_counter() - t0
if r.returncode != 0:
    sys.stderr.write(r.stderr.decode(errors="replace")[-2000:] + "\n")
    sys.exit(1)
print(int(dt * 1000))
PYEOF
)"
printf '  get: %d ms  (%d MiB/s)\n' "$get_ms" \
  "$(python3 -c "print(max(1, int($SIZE_MB*1000/$get_ms)))")"
cmp -s "$WORK/blob.bin" "$WORK/blob.copy.bin" \
  && echo "  content: OK" || { echo "  content: MISMATCH" >&2; exit 1; }
python3 - "$GET_JSON" <<'EOF' || true
import json, sys
d = json.load(open(sys.argv[1]))
if "segments" in d:
    print(f"  parallel segments: {d['segments']}  sha256={d.get('sha256','?')[:12]}…")
else:
    print("  sequential path (no segments field)")
EOF

note "gateway /metrics snapshot"
curl -sf "$METRICS_URL" | grep '^agbridge'

echo
echo "[*] bench done."
