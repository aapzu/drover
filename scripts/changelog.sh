#!/usr/bin/env bash
set -euo pipefail

tag=${1:?usage: changelog.sh <tag>}
version=${tag#v}

if grep -qF "## [$version]" CHANGELOG.md; then
  echo "CHANGELOG.md already has $tag"
  exit 0
fi

release=$(gh release view "$tag" --json body,publishedAt)
published=$(jq -er '.publishedAt[:10]' <<<"$release")
body=$(jq -er '.body' <<<"$release" | tr -d '\r')
compare=$(sed -n 's/^\*\*Full Changelog\*\*: //p' <<<"$body")
entries=$(sed -nE 's|^\* (.+) by @[^ ]+ in (https://[^ ]+/pull/([0-9]+))$|- \1 ([#\3](\2))|p' <<<"$body")

: "${compare:?no Full Changelog line in the release notes of $tag}"
: "${entries:?no pull requests found in the release notes of $tag}"

section="## [$version]($compare) - $published

$entries
"

tmp=$(mktemp)
SECTION=$section awk '!done && /^## \[/ { print ENVIRON["SECTION"]; done = 1 } { print } END { if (!done) print ENVIRON["SECTION"] }' CHANGELOG.md >"$tmp"
cat "$tmp" >CHANGELOG.md
rm "$tmp"
