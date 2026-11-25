#!/usr/bin/env bash
set -euo pipefail

tag=${1:?usage: changelog.sh <tag>}
version=${tag#v}

if grep -qF "## [$version]" CHANGELOG.md; then
  echo "CHANGELOG.md already has $tag"
  exit 0
fi

grep -qx '## Unreleased' CHANGELOG.md || { echo "CHANGELOG.md has no '## Unreleased' section" >&2; exit 1; }

release=$(gh release view "$tag" --json body,publishedAt)
published=$(jq -er '.publishedAt[:10]' <<<"$release")
body=$(jq -er '.body' <<<"$release" | tr -d '\r')
compare=$(sed -n 's/^\*\*Full Changelog\*\*: //p' <<<"$body")

: "${compare:?no Full Changelog line in the release notes of $tag}"

entries=$(awk '/^## Unreleased$/ { in_section = 1; next } /^## / { in_section = 0 } in_section && NF' CHANGELOG.md)
if [ -z "$entries" ]; then
  entries=$(sed -nE 's|^\* (.+) by @[^ ]+ in (https://[^ ]+/pull/([0-9]+))$|- \1 ([#\3](\2))|p' <<<"$body")
fi
: "${entries:?no Unreleased entries and no pull requests found in the release notes of $tag}"

section="## [$version]($compare) - $published

$entries
"

updated=$(SECTION=$section awk '
  /^## Unreleased$/ { print; print ""; print ENVIRON["SECTION"]; skipping = 1; next }
  /^## / { skipping = 0 }
  !skipping { print }
' CHANGELOG.md)
printf '%s\n' "$updated" >CHANGELOG.md
