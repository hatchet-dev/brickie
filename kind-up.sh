#!/usr/bin/env bash
set -euo pipefail

VERSION="${VERSION:-v0.5.6}"
CLUSTER="${CLUSTER:-brickie}"
M="$(go env GOMODCACHE)/sigs.k8s.io/agent-sandbox@${VERSION}"

[ -d "$M" ] || go mod download sigs.k8s.io/agent-sandbox

kind get clusters | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER"

kubectl apply -f "https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${VERSION}/sandbox-with-extensions.yaml"
kubectl -n agent-sandbox-system rollout status deploy/agent-sandbox-controller --timeout=120s

docker build -q -t sandbox-runtime:latest "$M/examples/python-runtime-sandbox"
docker build -q -t sandbox-router-go:latest -f "$M/sandbox-router/Dockerfile" "$M"
kind load docker-image sandbox-runtime:latest sandbox-router-go:latest --name "$CLUSTER"

# ponytail: no published images on registry.k8s.io yet; drop the local builds when they ship
ROUTER="$(mktemp -d)"
cp "$M"/sandbox-router/deploy/*.yaml "$ROUTER/" && chmod +w "$ROUTER"/*.yaml
sed -i '' 's|registry.k8s.io/agent-sandbox/sandbox-router-go:latest|sandbox-router-go:latest|; s|imagePullPolicy: Always|imagePullPolicy: IfNotPresent|' "$ROUTER/deployment.yaml"
kubectl apply -f "$ROUTER/"
kubectl -n default rollout status deploy/sandbox-router --timeout=120s
rm -rf "$ROUTER"

kubectl apply -f "$(dirname "$0")/warmpool.yaml"
echo "ready — go run . 'echo hello from the sandbox'"
