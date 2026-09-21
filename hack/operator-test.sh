#!/usr/bin/env bash
set -euo pipefail

# End-to-end test of the gRPC operator provider against a running Hatchet
# instance. The tenant must have SERVER_GRPC_OPERATORS_ENABLED=true.
# Usage: HATCHET_CLIENT_TOKEN=... hack/operator-test.sh [--full]
# (--full also runs the Claude Agent SDK harness example; needs sbx claude auth)
# Against a local TLS-less engine, also set HATCHET_CLIENT_TLS_STRATEGY=none.

cd "$(dirname "$0")/.."
ROOT=$(pwd)
WORKDIR=$(mktemp -d /tmp/brickie-operator-test.XXXXXX)
FULL=${1:-}

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
GOPROXY=direct GOSUMDB=off go build -o "$WORKDIR/brickie" ./cmd/brickie

echo "==> starting provider in operator mode (local driver)"
BRICKIE_OTEL=false "$WORKDIR/brickie" worker --driver local --operator --slots 5 \
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

cd examples/typescript
[ -d node_modules ] || pnpm install --silent

echo "==> running command example"
BRICKIE_OTEL=false npx tsx command.ts > "$WORKDIR/command.log" 2>&1 || {
  echo "error: command example failed" >&2; tail -15 "$WORKDIR/command.log" >&2; exit 1; }
grep -q "e2e ok" "$WORKDIR/command.log"
echo "    e2e ok"

SNAPSHOT_ID=$(grep -o '"snapshotId": "[a-f0-9-]*"' "$WORKDIR/command.log" | head -1 | cut -d'"' -f4)
echo "==> running resume example (snapshot $SNAPSHOT_ID)"
BRICKIE_OTEL=false SNAPSHOT_ID="$SNAPSHOT_ID" npx tsx resume.ts > "$WORKDIR/resume.log" 2>&1 || {
  echo "error: resume example failed" >&2; tail -15 "$WORKDIR/resume.log" >&2; exit 1; }
grep -q "restore ok" "$WORKDIR/resume.log"
echo "    restore ok"

if [ "$FULL" = "--full" ]; then
  echo "==> running agent harness example (this takes a few minutes)"
  BRICKIE_OTEL=false npx tsx harness.ts > "$WORKDIR/harness.log" 2>&1 || {
    echo "error: harness example failed" >&2; tail -15 "$WORKDIR/harness.log" >&2; exit 1; }
  grep -q "e2e ok" "$WORKDIR/harness.log"
  echo "    e2e ok"
fi

echo
echo "PASS: unchanged SDK examples served by the gRPC operator ($ROOT)"
