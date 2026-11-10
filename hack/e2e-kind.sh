#!/usr/bin/env bash
set -euo pipefail

# End-to-end check of the Kubernetes driver on a local kind cluster: Hatchet
# runs on the host with docker compose, the provider runs in the cluster, and
# both demos run from this machine. The cluster has a worker that is labelled
# and tainted for sandboxes, so sandbox pods must carry the provider's node
# selector and toleration to schedule at all, while the provider and the
# controller stay on the untainted nodes. Needs Docker, kind, kubectl, node,
# and pnpm. Safe to re-run; steps that already hold are skipped.
#
# The cluster reaches Hatchet through host.docker.internal on Docker Desktop.
# On Docker Engine (Linux, CI) that name does not resolve, so run with
# BRICKIE_HATCHET_BIND=0.0.0.0 and the cluster uses the Docker bridge instead.
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
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --config hack/e2e-kind/kind.yaml --wait 120s
kubectl config use-context "kind-$CLUSTER" >/dev/null
SANDBOX_NODE=$(kubectl get nodes -l brickie.hatchet.run/sandbox-node=true -o name | head -1)
[ -n "$SANDBOX_NODE" ] || { echo "error: the cluster has no sandbox worker; it was created by an older run, so run 'hack/e2e-kind.sh down' first" >&2; exit 1; }
kubectl taint node "${SANDBOX_NODE#node/}" brickie.hatchet.run/sandbox=true:NoSchedule --overwrite >/dev/null
ok "cluster is up; ${SANDBOX_NODE#node/} is labelled and tainted for sandboxes only"

step "agent-sandbox controller v1.0.8"
kubectl apply --server-side -f "$AGENT_SANDBOX" >/dev/null
kubectl -n agent-sandbox-system rollout status deployment/agent-sandbox-controller --timeout=180s
ok "controller is ready"

step "provider image"
docker build -q -t brickie:e2e . >/dev/null
kind load docker-image brickie:e2e --name "$CLUSTER" >/dev/null
ok "brickie:e2e is on the node"

step "provider deployment"
HOST_IP=$(docker run --rm --network kind alpine:3 getent ahostsv4 host.docker.internal 2>/dev/null | awk 'NR==1{print $1}' || true)
if [ -z "$HOST_IP" ]; then
  if [ "${BRICKIE_HATCHET_BIND:-127.0.0.1}" != 0.0.0.0 ]; then
    echo "error: host.docker.internal does not resolve from the kind network; re-run with BRICKIE_HATCHET_BIND=0.0.0.0 so the cluster can reach Hatchet through the Docker bridge" >&2
    exit 1
  fi
  HOST_IP=$(docker run --rm --network kind alpine:3 ip route | awk '/^default/{print $3}')
fi
[ -n "$HOST_IP" ] || { echo "error: cannot find the host's address from the kind network" >&2; exit 1; }
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

step "warm pool"
warm_ready() {
  kubectl -n "$NS" get pods -l agents.x-k8s.io/warm-pool-sandbox -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' 2>/dev/null | grep -q True
}
for _ in $(seq 1 150); do
  warm_ready && break
  sleep 2
done
warm_ready || {
  echo "error: no warm sandbox pod became ready within 5 minutes:" >&2
  kubectl -n "$NS" get pods -l agents.x-k8s.io/warm-pool-sandbox >&2
  kubectl -n "$NS" describe pods -l agents.x-k8s.io/warm-pool-sandbox | sed -n '/^Events:/,$p' >&2
  exit 1
}
WARM_NODE=$(kubectl -n "$NS" get pods -l agents.x-k8s.io/warm-pool-sandbox -o jsonpath='{.items[0].spec.nodeName}')
[ "node/$WARM_NODE" = "$SANDBOX_NODE" ] || { echo "error: warm sandbox pod landed on $WARM_NODE, not the tainted sandbox node" >&2; exit 1; }
ok "a warm sandbox pod is ready on $WARM_NODE (sandboxd behind brickie sandbox-tls)"

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
  "") pnpm fs-demo && pnpm demo && pnpm test ;;
  *) echo "usage: hack/e2e-kind.sh [fs|down]" >&2; exit 1 ;;
esac
echo
echo "  sandbox pods:   kubectl -n $NS get pods,sandboxclaims"
echo "  tear down:      hack/e2e-kind.sh down"
