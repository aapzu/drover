#!/usr/bin/env bash
# Usage: next-version.sh <latest-tag-or-empty> <comma-separated PR labels>
# Prints the next vX.Y.Z: patch by default, minor/major when labelled.
set -euo pipefail

latest=${1:-}
labels=",${2:-},"

if [ -z "$latest" ]; then
  echo v0.1.0
  exit 0
fi

IFS=. read -r major minor patch <<<"${latest#v}"
case "$labels" in
  *,major,*) echo "v$((major + 1)).0.0" ;;
  *,minor,*) echo "v${major}.$((minor + 1)).0" ;;
  *) echo "v${major}.${minor}.$((patch + 1))" ;;
esac
