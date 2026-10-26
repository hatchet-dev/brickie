#!/usr/bin/env bash
set -euo pipefail

# Fail unless a binary is linked against the CMVP-certified Go Cryptographic
# Module and enforces FIPS 140-3 mode by default.
#
#   hack/check-fips.sh dist/brickie_linux_amd64_v1/brickie

bin=$1
info=$(go version -m "$bin")
echo "$info" | grep -q 'GOFIPS140=v1.0.0-c2097c7c$' || { echo "error: $bin is not built with GOFIPS140=v1.0.0 (certified module v1.0.0-c2097c7c)" >&2; exit 1; }
echo "$info" | grep -Eq 'DefaultGODEBUG=.*fips140=on(,|$)' || { echo "error: $bin does not enable fips140=on by default" >&2; exit 1; }
echo "$bin: Go Cryptographic Module v1.0.0-c2097c7c, fips140=on"
