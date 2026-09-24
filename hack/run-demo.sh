#!/usr/bin/env bash
set -euo pipefail

# One command from zero to an agent session. Hatchet runs in docker compose;
# the sandbox provider is built inside a golang container (no Go toolchain
# needed on this machine) and runs here on the host, because Docker Sandboxes
# micro-VMs are managed by a host daemon. Safe to re-run any time.

cd "$(dirname "$0")/.."

TENANT_ID=707d0855-80ab-4e1f-a156-f1c4546cbf52
STATE_DIR="$HOME/.brickie"
PID_FILE="$STATE_DIR/provider.pid"
LOG_FILE="$STATE_DIR/provider.log"

for bin in docker node pnpm; do
  command -v "$bin" >/dev/null || { echo "error: $bin is required" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo "error: start Docker Desktop first" >&2; exit 1; }
mkdir -p "$STATE_DIR"

if ! command -v sbx >/dev/null; then
  if [ "$(uname -s)" != "Darwin" ]; then
    echo "error: Docker Sandboxes (sbx) is required: https://docs.docker.com/ai/sandboxes/" >&2
    exit 1
  fi
  command -v brew >/dev/null || { echo "error: Homebrew is required to install sbx: https://brew.sh" >&2; exit 1; }
  echo "==> installing sbx (Docker Sandboxes CLI)"
  brew trust docker/tap
  brew install docker/tap/sbx
fi

if ! sbx ls >/dev/null 2>&1; then
  echo "==> signing in to Docker Sandboxes (opens a browser)"
  sbx login
fi

if ! sbx policy ls 2>/dev/null | grep -q "balanced\|local-policy"; then
  echo "==> initializing the sandbox network policy"
  sbx policy init balanced
fi

if [ ! -f "$STATE_DIR/claude-authed" ]; then
  echo "==> authenticating Claude inside sandboxes (opens a browser)"
  sbx run claude -- auth login
  touch "$STATE_DIR/claude-authed"
fi

echo "==> starting hatchet"
docker compose up -d --wait

if [ ! -f .env ] || ! grep -q HATCHET_CLIENT_TOKEN .env; then
  echo "==> minting a hatchet token"
  TOKEN=$(docker compose exec hatchet ./hatchet-admin token create --config /config --tenant-id "$TENANT_ID" | tr -d '[:space:]')
  umask 177
  {
    echo "HATCHET_CLIENT_TOKEN=$TOKEN"
    echo "HATCHET_CLIENT_TLS_STRATEGY=none"
  } >> .env
  umask 022
fi

if [ ! -x bin/brickie ] || [ -n "$(find cmd internal go.mod go.sum -newer bin/brickie 2>/dev/null | head -1)" ]; then
  echo "==> building the sandbox provider (inside a golang container)"
  docker run --rm -v "$PWD:/src" -w /src \
    -v brickie_go_cache:/go/pkg/mod -v brickie_go_build:/root/.cache/go-build \
    -e CGO_ENABLED=0 -e GOOS="$(uname -s | tr '[:upper:]' '[:lower:]')" \
    -e GOARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')" \
    golang:1.27 go build -o bin/brickie ./cmd/brickie
fi

if [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; then
  kill "$(cat "$PID_FILE")" 2>/dev/null || true
  sleep 1
fi
echo "==> starting the sandbox provider"
set -a
. ./.env
set +a
nohup ./bin/brickie worker > "$LOG_FILE" 2>&1 &
echo $! > "$PID_FILE"

for _ in $(seq 1 20); do
  grep -q "connected as grpc operator" "$LOG_FILE" 2>/dev/null && break
  kill -0 "$(cat "$PID_FILE")" 2>/dev/null || { echo "error: provider exited; see $LOG_FILE" >&2; tail -5 "$LOG_FILE" >&2; exit 1; }
  sleep 1
done

[ -d node_modules ] || { echo "==> installing example dependencies"; pnpm install --silent; }

cat <<EOF

  brickie is up
    dashboard   http://localhost:8888  (admin@example.com / Admin123!!)
    provider    tail -f $LOG_FILE   (stop: kill \$(cat $PID_FILE))
    token       .env (HATCHET_CLIENT_TOKEN, mode 0600)
    sandboxes   Docker Sandboxes micro-VMs; no host env vars are injected

  write your own programs: export the variables from .env and use the
  TypeScript SDK; see README.md.

EOF

cd examples/typescript
exec pnpm demo
