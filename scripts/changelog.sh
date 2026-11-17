#!/usr/bin/env bash
set -euo pipefail

tag=${1:?usage: changelog.sh <tag>}
file=${CHANGELOG:-CHANGELOG.md}
version=${tag#v}
repo=${GITHUB_REPOSITORY:-aapzu/drover}

if [ -f "$file" ] && grep -qF "## [$version]" "$file"; then
  exit 0
fi

release=$(gh release view "$tag" --repo "$repo" --json body,publishedAt)
published=$(jq -r '.publishedAt[:10]' <<<"$release")
body=$(jq -r '.body' <<<"$release")
compare=$(sed -n 's/^\*\*Full Changelog\*\*: //p' <<<"$body")
entries=$(sed -nE 's|^\* (.+) by @[^ ]+ in (https://[^ ]+/pull/([0-9]+))$|- \1 ([#\3](\2))|p' <<<"$body")

section="## [$version](${compare:-https://github.com/$repo/releases/tag/$tag}) - $published

$entries
"

if [ ! -f "$file" ]; then
  printf '# Changelog\n\nAdded on each release from the titles of the merged pull requests.\n' >"$file"
fi

tmp=$(mktemp)
if grep -q '^## \[' "$file"; then
  SECTION=$section awk '!done && /^## \[/ { print ENVIRON["SECTION"]; done = 1 } { print }' "$file" >"$tmp"
else
  { cat "$file"; printf '\n%s' "$section"; } >"$tmp"
fi
mv "$tmp" "$file"
