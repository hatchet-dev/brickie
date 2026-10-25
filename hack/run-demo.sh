#!/usr/bin/env bash
set -euo pipefail

# A guided setup from zero to an agent session. Every state-changing command
# is explained and confirmed before it runs. Safe to re-run any time: steps
# that already hold are skipped. Set BRICKIE_YES=1 to skip all prompts.

cd "$(dirname "$0")/.."

DEMO=demo
case "${1:-}" in
  "") ;;
  fs) DEMO=fs-demo ;;
  *) echo "usage: hack/run-demo.sh [fs]" >&2; exit 1 ;;
esac

TENANT_ID=707d0855-80ab-4e1f-a156-f1c4546cbf52
STATE_DIR="$HOME/.brickie"
PID_FILE="$STATE_DIR/provider.pid"
LOG_FILE="$STATE_DIR/provider.log"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; }
info() { printf '\n\033[1m› %s\033[0m\n' "$1"; }

spin_until() {
  msg=$1; timeout=$2; shift 2
  frames='|/-\'
  i=0
  while [ $i -lt $((timeout * 4)) ]; do
    if "$@" 2>/dev/null; then
      printf '\r\033[K'
      return 0
    fi
    printf '\r  %s %s' "$(printf %s "$frames" | cut -c$((i % 4 + 1)))" "$msg"
    i=$((i + 1))
    sleep 0.25
  done
  printf '\r\033[K'
  return 1
}

confirm() {
  info "$1"
  printf '  $ %s\n' "$2"
  if [ "${BRICKIE_YES:-}" = "1" ] || ! [ -t 0 ]; then
    return 0
  fi
  read -rp "  proceed? [Y/n] " ans
  case "${ans:-y}" in
    n|N|no|NO) echo "  stopped — re-run this script when ready"; exit 1 ;;
  esac
}

for bin in docker node pnpm; do
  command -v "$bin" >/dev/null || { echo "error: $bin is required" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo "error: start Docker first" >&2; exit 1; }
ok "docker is running"
mkdir -p "$STATE_DIR"


EXTERNAL_HATCHET=""
if [ -f .env ] && grep -q HATCHET_CLIENT_TOKEN .env; then
  TOKEN_IN_ENV=$(grep '^HATCHET_CLIENT_TOKEN=' .env | head -1 | cut -d= -f2-)
  if ! TOKEN_IN_ENV="$TOKEN_IN_ENV" node -e 'const c=JSON.parse(Buffer.from(process.env.TOKEN_IN_ENV.split(".")[1],"base64url").toString());process.exit(/localhost|127\.0\.0\.1/.test((c.server_url||"")+(c.grpc_broadcast_address||""))?0:1)' 2>/dev/null; then
    EXTERNAL_HATCHET=1
  fi
fi

if [ -n "$EXTERNAL_HATCHET" ]; then
  ok "using the external hatchet instance from .env (skipping the compose engine)"
elif docker compose ps hatchet 2>/dev/null | grep -q healthy; then
  ok "hatchet is running"
else
  confirm "agent sessions are durable Hatchet tasks, so we start Hatchet and Postgres in containers" \
    "docker compose up -d --wait"
  docker compose up -d --wait
fi

if [ -f .env ] && grep -q HATCHET_CLIENT_TOKEN .env; then
  ok "hatchet token is in .env"
else
  confirm "your programs and the sandbox provider authenticate to Hatchet with a token; we mint one and store it only in .env (mode 0600, git-ignored)" \
    "docker compose exec hatchet ./hatchet-admin token create ... >> .env"
  TOKEN=$(docker compose exec hatchet ./hatchet-admin token create --config /config --tenant-id "$TENANT_ID" | tr -d '[:space:]')
  umask 177
  {
    echo "HATCHET_CLIENT_TOKEN=$TOKEN"
    echo "HATCHET_CLIENT_TLS_STRATEGY=none"
  } >> .env
  umask 022
fi

REBUILT=""
if [ -x bin/brickie ] && [ -z "$(find cmd internal go.mod go.sum -newer bin/brickie 2>/dev/null | head -1)" ]; then
  ok "sandbox provider binary is up to date"
else
  confirm "the sandbox provider is a Go program, but you don't need Go — we compile it inside a golang container" \
    "docker run --rm -v \$PWD:/src golang:1.27 go build -o bin/brickie ./cmd/brickie"
  docker run --rm -v "$PWD:/src" -w /src \
    -v brickie_go_cache:/go/pkg/mod -v brickie_go_build:/root/.cache/go-build \
    -e CGO_ENABLED=0 -e GOOS="$(uname -s | tr '[:upper:]' '[:lower:]')" \
    -e GOARCH="$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')" \
    golang:1.27 go build -o bin/brickie ./cmd/brickie
  REBUILT=1
fi

provider_alive() { [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null; }
provider_gone() { ! provider_alive; }

STRAY=$(pgrep -f "bin/brickie worker" | grep -vx "$(cat "$PID_FILE" 2>/dev/null || echo 0)" || true)
if [ -n "$STRAY" ]; then
  confirm "a sandbox provider from an earlier run is still running without a pid file; two providers would split one sandbox's turns, so we stop it" \
    "kill $STRAY"
  kill $STRAY 2>/dev/null || true
fi

if provider_alive && [ -z "$REBUILT" ] && [ ! .env -nt "$PID_FILE" ]; then
  ok "sandbox provider is already running"
else
  confirm "the provider runs on this machine and talks to the local Docker engine; we run it in the background" \
    "./bin/brickie worker  # logs: $LOG_FILE"
  if provider_alive; then
    kill "$(cat "$PID_FILE")" 2>/dev/null || true
    spin_until "waiting for the old provider to shut down cleanly" 15 provider_gone || kill -9 "$(cat "$PID_FILE")" 2>/dev/null || true
  fi
  unset HATCHET_CLIENT_TLS_ROOT_CA_FILE HATCHET_CLIENT_TLS_STRATEGY
  set -a
  . ./.env
  set +a
  nohup ./bin/brickie worker > "$LOG_FILE" 2>&1 &
  echo $! > "$PID_FILE"
  provider_connected() {
    provider_alive || { echo; echo "error: provider exited; see $LOG_FILE" >&2; tail -5 "$LOG_FILE" >&2; exit 1; }
    grep -q "connected as grpc operator" "$LOG_FILE" 2>/dev/null
  }
  if ! spin_until "waiting for the provider to connect to hatchet" 30 provider_connected; then
    echo "error: provider did not connect within 30s; see $LOG_FILE" >&2
    tail -5 "$LOG_FILE" >&2
    exit 1
  fi
  ok "sandbox provider is connected to hatchet"
fi

if [ -d node_modules ]; then
  ok "example dependencies are installed"
else
  confirm "the demo is a TypeScript program using the brickie SDK; we install its dependencies" \
    "pnpm install"
  pnpm install --silent
fi

cat <<EOF

  brickie is up
    dashboard   ${EXTERNAL_HATCHET:+your hatchet instance}${EXTERNAL_HATCHET:-http://localhost:8888  (admin@example.com / Admin123!!)}
    provider    tail -f $LOG_FILE   (stop: kill \$(cat $PID_FILE))
    token       .env (HATCHET_CLIENT_TOKEN, mode 0600)
    store       $STATE_DIR/store (snapshots; files over 2 MiB bypass Hatchet through it)
    sandboxes   Docker containers (alpine:3) on the local engine
                no host env vars are injected

  write your own programs: export the variables from .env and use the
  TypeScript SDK; see README.md.

EOF

if [ "$DEMO" = fs-demo ]; then
  info "everything is ready — the file demo creates one sandbox and runs each file operation in it"
else
  info "everything is ready — the demo runs one durable agent-session task through create, exec, files, fork, kill, and connect"
fi
printf '  $ %s\n' "cd examples/typescript && pnpm $DEMO"
if [ "${BRICKIE_YES:-}" != "1" ] && [ -t 0 ]; then
  read -rp "  start the demo? [Y/n] " ans
  case "${ans:-y}" in
    n|N|no|NO)
      cat <<EOF

  no problem — hatchet and the sandbox provider stay running, so you (or
  anyone on this machine) can write your own TypeScript program against them
  right now: export the variables from .env and use the SDK (see README.md).

  demo later:     hack/run-demo.sh
  file demo:      hack/run-demo.sh fs
  stop provider:  kill \$(cat $PID_FILE)
  stop hatchet:   docker compose down

EOF
      exit 0
      ;;
  esac
fi
cd examples/typescript
unset HATCHET_CLIENT_TLS_ROOT_CA_FILE HATCHET_CLIENT_TLS_STRATEGY
exec pnpm "$DEMO"
