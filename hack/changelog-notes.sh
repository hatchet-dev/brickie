#!/usr/bin/env bash
set -euo pipefail

file=$1 version=$2
notes=$(awk -v v="$version" '
  /^## \[/ { in_section = (index($0, "## [" v "]") == 1) ; next }
  in_section { print }
' "$file")
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "error: $file has no section for version $version; add '## [$version] - YYYY-MM-DD' before releasing" >&2
  exit 1
fi
printf '%s\n' "$notes"
