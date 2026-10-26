#!/usr/bin/env bash
set -euo pipefail

# Print the notes of one version from a Keep a Changelog file, or fail when
# the version has no section.
#
#   hack/changelog-notes.sh CHANGELOG.md 0.1.0

file=$1 version=$2
notes=$(awk -v v="$version" '
  /^## \[/ { in_section = ($0 ~ "^## \\[" v "\\]") ; next }
  in_section { print }
' "$file")
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "error: $file has no section for version $version; add '## [$version] - YYYY-MM-DD' before releasing" >&2
  exit 1
fi
printf '%s\n' "$notes"
