#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

NAMESPACE="${1:-${NAMESPACE:-brickie}}"
CLUSTER="brickie"
RELEASE="brickie"

[ -n "${HATCHET_CLIENT_TOKEN:-}" ] || {
  echo "HATCHET_CLIENT_TOKEN is required: export it or run HATCHET_CLIENT_TOKEN=... $0"
  exit 1
}

say() { printf '\n==> %s\n' "$*"; }

say "Checking tools"
command -v docker >/dev/null || { echo "Docker is required"; exit 1; }
docker info >/dev/null 2>&1 || { echo "Start Docker first"; exit 1; }
command -v helm >/dev/null || { echo "helm is required (brew install helm)"; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl is required (brew install kubectl)"; exit 1; }
if ! command -v kind >/dev/null; then
  command -v brew >/dev/null || { echo "kind is required (https://kind.sigs.k8s.io)"; exit 1; }
  brew install kind
fi

say "Kind cluster '${CLUSTER}'"
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER"
kubectl config use-context "kind-${CLUSTER}"

say "Building worker image"
docker build -t hatchet-sandbox-worker:latest ts/
kind load docker-image hatchet-sandbox-worker:latest --name "$CLUSTER"

say "Installing chart into namespace '${NAMESPACE}'"
AGENT_SANDBOX_ENABLED=true
if kubectl get crd sandboxes.agents.x-k8s.io >/dev/null 2>&1 &&
  ! helm status "$RELEASE" -n "$NAMESPACE" >/dev/null 2>&1; then
  echo "agent-sandbox already installed outside helm; skipping subchart"
  AGENT_SANDBOX_ENABLED=false
fi

HELM_ARGS=(
  --namespace "$NAMESPACE" --create-namespace
  --set "hatchet.token=${HATCHET_CLIENT_TOKEN}"
  --set "agent-sandbox.enabled=${AGENT_SANDBOX_ENABLED}"
)
if [ -n "${ANTHROPIC_API_KEY:-}" ]; then
  HELM_ARGS+=(--set "sandbox.credentials.ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY}")
fi
if [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
  HELM_ARGS+=(--set "sandbox.credentials.CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN}")
fi

helm upgrade --install "$RELEASE" helm/hatchet-sandbox "${HELM_ARGS[@]}"

say "Waiting for the provider worker"
kubectl -n "$NAMESPACE" rollout status "deploy/${RELEASE}-hatchet-sandbox" --timeout=180s

say "Done. Sandbox turns will run in namespace '${NAMESPACE}' on kind-${CLUSTER}."
