#!/bin/sh
# Fetch the Ludus plugin SDK into .ci-deps/ludus and print its directory.
#
# usage: [LUDUS_PROJECT=group/project] scripts/fetch-sdk.sh [REF]
#
# REF (or LUDUS_SDK_REF) is a branch, tag, or commit of the Ludus repository;
# use it to match a specific server. Without one, this takes the repository's
# default branch, or, while that does not carry ludus-plugin-sdk, the newest
# version-named branch or tag (X.Y.Z or vX.Y.Z) that does.
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
url="https://gitlab.com/${LUDUS_PROJECT:-badsectorlabs/ludus}.git"
checkout="$repo_root/.ci-deps/ludus"
ref=${1:-${LUDUS_SDK_REF:-}}

rm -rf "$checkout"
git init --quiet "$checkout"
has_sdk() {
  git -C "$checkout" fetch --quiet --depth=1 "$url" "$1" &&
    git -C "$checkout" cat-file -e FETCH_HEAD:ludus-plugin-sdk/go.mod 2>/dev/null
}

if [ -n "$ref" ]; then
  has_sdk "$ref" || { echo "$url at $ref has no ludus-plugin-sdk" >&2; exit 1; }
else
  candidates="HEAD $(git ls-remote --heads --tags "$url" |
    awk '{ name = $2; sub(/^refs\/(heads|tags)\//, "", name)
           if (name ~ /^v?[0-9]+\.[0-9]+\.[0-9]+$/) { key = name; sub(/^v/, "", key); print key, $2 } }' |
    sort -V -r | awk '{ print $2 }')"
  found=
  for candidate in $candidates; do
    if has_sdk "$candidate"; then found=$candidate; break; fi
  done
  [ -n "$found" ] || { echo "No branch or tag of $url carries ludus-plugin-sdk" >&2; exit 1; }
  ref=$found
fi

git -C "$checkout" checkout --quiet --detach FETCH_HEAD
echo "Ludus SDK: $url $ref $(git -C "$checkout" rev-parse --short HEAD)" >&2
echo "$checkout/ludus-plugin-sdk"
