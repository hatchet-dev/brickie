#!/usr/bin/env bash
set -euo pipefail

# End-to-end check of the Kubernetes driver on a local kind cluster: Hatchet
# runs on the host with docker compose, the provider runs in the cluster, and
# both demos run from this machine. Needs Docker Desktop, kind, kubectl, node,
# and pnpm. Safe to re-run; steps that already hold are skipped.
#
#   hack/e2e-kind.sh          both demos
#   hack/e2e-kind.sh fs       the file demo only
#   hack/e2e-kind.sh down     delete the cluster

cd "$(dirname "$0")/.."

CLUSTER=${KIND_CLUSTER:-brickie}
NS=brickie
TENANT_ID=707d0855-80ab-4e1f-a156-f1c4546cbf52
AGENT_SANDBOX=https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v1.0.8/sandbox-with-extensions.yaml

ok() { printf '  \033[32m✓\033[0m %s\n' "$1"; }
step() { printf '\n\033[1m› %s\033[0m\n' "$1"; }

if [ "${1:-}" = down ]; then
  kind delete cluster --name "$CLUSTER"
  exit 0
fi

for bin in docker kind kubectl node pnpm; do
  command -v "$bin" >/dev/null || { echo "error: $bin is required" >&2; exit 1; }
done
docker info >/dev/null 2>&1 || { echo "error: start Docker first" >&2; exit 1; }

step "hatchet on the host"
docker compose up -d --wait
if ! grep -q '^HATCHET_CLIENT_TOKEN=' .env 2>/dev/null; then
  TOKEN=$(docker compose exec hatchet ./hatchet-admin token create --config /config --tenant-id "$TENANT_ID" | tr -d '[:space:]')
  umask 177
  printf 'HATCHET_CLIENT_TOKEN=%s\nHATCHET_CLIENT_TLS_STRATEGY=none\n' "$TOKEN" >> .env
  umask 022
fi
TOKEN=$(grep '^HATCHET_CLIENT_TOKEN=' .env | head -1 | cut -d= -f2-)
ok "token is in .env"

step "kind cluster $CLUSTER"
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 120s
kubectl config use-context "kind-$CLUSTER" >/dev/null
ok "cluster is up"

step "agent-sandbox controller v1.0.8"
kubectl apply --server-side -f "$AGENT_SANDBOX" >/dev/null
kubectl -n agent-sandbox-system rollout status deployment/agent-sandbox-controller --timeout=180s
ok "controller is ready"

step "provider image"
docker build -q -t brickie:e2e . >/dev/null
kind load docker-image brickie:e2e --name "$CLUSTER" >/dev/null
ok "brickie:e2e is on the node"

step "provider deployment"
HOST_IP=$(docker run --rm --network kind alpine:3 getent ahostsv4 host.docker.internal | awk 'NR==1{print $1}')
[ -n "$HOST_IP" ] || { echo "error: cannot resolve host.docker.internal from the kind network (Docker Desktop only)" >&2; exit 1; }
kubectl apply -f hack/e2e-kind/brickie.yaml >/dev/null
kubectl -n "$NS" create secret generic brickie-hatchet \
  --from-literal=HATCHET_CLIENT_TOKEN="$TOKEN" \
  --from-literal=HATCHET_CLIENT_HOST_PORT="$HOST_IP:7077" \
  --from-literal=HATCHET_CLIENT_TLS_STRATEGY=none \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "$NS" rollout restart deployment/brickie >/dev/null
kubectl -n "$NS" rollout status deployment/brickie --timeout=180s
for _ in $(seq 1 60); do
  if kubectl -n "$NS" logs deployment/brickie 2>/dev/null | grep -q "connected as grpc operator"; then
    break
  fi
  sleep 1
done
kubectl -n "$NS" logs deployment/brickie | grep -q "connected as grpc operator" || {
  echo "error: provider did not connect to hatchet; logs:" >&2
  kubectl -n "$NS" logs deployment/brickie --tail=20 >&2
  exit 1
}
ok "provider is connected to hatchet (kubectl -n $NS logs -f deployment/brickie)"

step "demos"
set -a
. ./.env
set +a
unset HATCHET_CLIENT_TLS_ROOT_CA_FILE
cd examples/typescript
[ -d node_modules ] || pnpm install --silent
export DEMO_BIG_MB=1
case "${1:-}" in
  fs) pnpm fs-demo ;;
  "") pnpm fs-demo && pnpm demo ;;
  *) echo "usage: hack/e2e-kind.sh [fs|down]" >&2; exit 1 ;;
esac
echo
echo "  sandbox pods:   kubectl -n $NS get pods,sandboxclaims"
echo "  tear down:      hack/e2e-kind.sh down"
