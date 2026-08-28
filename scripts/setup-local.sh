#!/usr/bin/env bash
set -euo pipefail

say() { printf '\n==> %s\n' "$*"; }

say "Checking Docker Desktop"
command -v docker >/dev/null || {
  echo "Install Docker Desktop first: https://www.docker.com/products/docker-desktop/"
  exit 1
}
docker info >/dev/null 2>&1 || {
  echo "Start Docker Desktop, then re-run this script"
  exit 1
}

say "Installing sbx (Docker Sandboxes CLI)"
if command -v sbx >/dev/null; then
  echo "already installed: $(sbx version)"
else
  command -v brew >/dev/null || {
    echo "Homebrew is required to install sbx: https://brew.sh"
    exit 1
  }
  brew trust docker/tap
  brew install docker/tap/sbx
fi

say "Docker sign-in"
if sbx ls >/dev/null 2>&1; then
  echo "already signed in"
else
  sbx login
fi

say "Global sandbox network policy"
if sbx policy ls 2>/dev/null | grep -q '^local-policy'; then
  echo "already initialized"
else
  sbx policy init balanced
fi

say "Claude auth inside sandboxes (opens a browser)"
sbx run claude -- auth login

say "Node dependencies"
cd "$(dirname "$0")/../ts"
command -v pnpm >/dev/null || corepack enable pnpm 2>/dev/null || npm install -g pnpm
pnpm install

say "Done. Run the example from the brickie folder with: pnpm --dir ts example"
