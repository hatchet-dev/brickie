#!/usr/bin/env bash
set -euo pipefail

# End-to-end check of the provider against a running Hatchet instance: one
# agent session with a shell turn, a Claude turn, and a fork from the first
# snapshot, driven through the demo on stdin.
# Usage: HATCHET_CLIENT_TOKEN=... hack/test.sh
# Against a local TLS-less engine, also set HATCHET_CLIENT_TLS_STRATEGY=none.

cd "$(dirname "$0")/.."
WORKDIR=$(mktemp -d /tmp/brickie-test.XXXXXX)

for bin in go pnpm sbx node; do
  command -v "$bin" >/dev/null || { echo "error: $bin is required" >&2; exit 1; }
done

if [ -z "${HATCHET_CLIENT_TOKEN:-}" ]; then
  printf "Hatchet client token (input hidden): "
  read -rs HATCHET_CLIENT_TOKEN
  echo
  export HATCHET_CLIENT_TOKEN
fi
[ -n "$HATCHET_CLIENT_TOKEN" ] || { echo "error: empty token" >&2; exit 1; }

cleanup() {
  [ -n "${PROVIDER_PID:-}" ] && kill "$PROVIDER_PID" 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

echo "==> building brickie"
go build -o "$WORKDIR/brickie" ./cmd/brickie

echo "==> starting provider (local driver)"
BRICKIE_OTEL=false "$WORKDIR/brickie" worker --driver local --slots 5 > "$WORKDIR/provider.log" 2>&1 &
PROVIDER_PID=$!
for _ in $(seq 1 30); do
  grep -q "connected as grpc operator\|sandbox provider started" "$WORKDIR/provider.log" && break
  kill -0 "$PROVIDER_PID" 2>/dev/null || break
  sleep 1
done
grep -q "connected as grpc operator\|sandbox provider started" "$WORKDIR/provider.log" || {
  echo "error: provider did not start; last log lines:" >&2; tail -5 "$WORKDIR/provider.log" >&2; exit 1; }

cd examples/typescript
[ -d node_modules ] || pnpm install --silent

echo "==> running one session: shell turn, claude turn, fork"
printf '!echo e2e ok > out.txt && cat out.txt\nreply with the single word pong\n' | BRICKIE_OTEL=false pnpm --silent demo > "$WORKDIR/demo.log" 2>&1 || {
  echo "error: demo failed" >&2; tail -20 "$WORKDIR/demo.log" >&2; exit 1; }
grep -q "e2e ok" "$WORKDIR/demo.log" || { echo "error: shell turn output missing" >&2; tail -20 "$WORKDIR/demo.log" >&2; exit 1; }
SNAPSHOT_ID=$(grep -o 'snapshot [a-f0-9-]*' "$WORKDIR/demo.log" | head -1 | cut -d' ' -f2)
[ -n "$SNAPSHOT_ID" ] || { echo "error: no snapshot id in demo output" >&2; exit 1; }
printf '/restore %s\n!cat out.txt\n' "$SNAPSHOT_ID" | BRICKIE_OTEL=false pnpm --silent demo > "$WORKDIR/fork.log" 2>&1 || {
  echo "error: fork failed" >&2; tail -20 "$WORKDIR/fork.log" >&2; exit 1; }
grep -q "e2e ok" "$WORKDIR/fork.log" || { echo "error: fork did not carry out.txt" >&2; tail -20 "$WORKDIR/fork.log" >&2; exit 1; }

echo
echo "PASS: session, snapshot, and fork served by the provider"
