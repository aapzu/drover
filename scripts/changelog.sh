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

entries=$(git show "$tag:CHANGELOG.md" 2>/dev/null | awk '/^## Unreleased$/ { in_section = 1; next } /^## / { in_section = 0 } in_section && NF' || true)
if [ -z "$entries" ]; then
  entries=$(sed -nE 's|^\* (.+) by @[^ ]+ in (https://[^ ]+/pull/([0-9]+))$|- \1 ([#\3](\2))|p' <<<"$body")
fi
: "${entries:?no Unreleased entries and no pull requests found in the release notes of $tag}"

section="## [$version]($compare) - $published

$entries
"

updated=$(SECTION=$section ENTRIES=$entries awk '
  BEGIN { n = split(ENVIRON["ENTRIES"], released, "\n"); for (i = 1; i <= n; i++) drop[released[i]] = 1 }
  function flush(    i) {
    print "## Unreleased"; print ""
    for (i = 1; i <= kept; i++) print rest[i]
    if (kept) print ""
    print ENVIRON["SECTION"]
  }
  /^## Unreleased$/ { in_unreleased = 1; next }
  in_unreleased && /^## / { flush(); in_unreleased = 0 }
  in_unreleased { if (NF && !($0 in drop)) rest[++kept] = $0; next }
  { print }
' CHANGELOG.md)
printf '%s\n' "$updated" >CHANGELOG.md
