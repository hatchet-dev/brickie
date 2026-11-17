#!/usr/bin/env bash
# Prints the sandboxd image brickie expects. When that tag is not on GHCR yet,
# builds it locally for this machine's architecture so pull requests that bump
# agent-sandbox can run the demos without publishing anything.
set -euo pipefail
cd "$(dirname "$0")/.."
ref=$(sed -n 's/^var Image = "\(.*\)"$/\1/p' internal/sandboxd/sandboxd.go)
[ -n "$ref" ] || { echo "error: no sandboxd image in internal/sandboxd/sandboxd.go" >&2; exit 1; }
if docker manifest inspect "$ref" >/dev/null 2>&1; then
  echo "present $ref"
else
  docker build -q --target sandboxd --build-arg "VERSION=${ref##*:}" -t "$ref" -f hack/agent-sandbox-fips.Dockerfile hack >/dev/null
  echo "built $ref"
fi
