#!/usr/bin/env bash
set -euo pipefail

# End-to-end check: the demo session runs one shell turn through a running
# Hatchet instance (docker compose up -d) and the local sandbox provider.
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

echo "==> starting provider in operator mode"
BRICKIE_OTEL=false "$WORKDIR/brickie" worker --slots 5 \
  > "$WORKDIR/provider.log" 2>&1 &
PROVIDER_PID=$!

for _ in $(seq 1 30); do
  grep -q "connected as grpc operator" "$WORKDIR/provider.log" && break
  kill -0 "$PROVIDER_PID" 2>/dev/null || break
  sleep 1
done
if ! grep -q "connected as grpc operator" "$WORKDIR/provider.log"; then
  echo "error: provider did not register as an operator; last log lines:" >&2
  tail -5 "$WORKDIR/provider.log" >&2
  echo "hint: the tenant needs SERVER_GRPC_OPERATORS_ENABLED=true" >&2
  exit 1
fi
echo "    $(grep 'connected as grpc operator' "$WORKDIR/provider.log")"

[ -d node_modules ] || pnpm install --silent
cd examples/typescript

echo "==> running one demo session turn"
printf '!echo e2e ok\n/exit\n' | BRICKIE_OTEL=false npx tsx demo.ts > "$WORKDIR/demo.log" 2>&1 || {
  echo "error: demo session failed" >&2; tail -15 "$WORKDIR/demo.log" >&2; exit 1; }
grep -q "e2e ok" "$WORKDIR/demo.log"
grep -q "session ended after 1 turns" "$WORKDIR/demo.log"
echo "    e2e ok"

echo
echo "PASS"
