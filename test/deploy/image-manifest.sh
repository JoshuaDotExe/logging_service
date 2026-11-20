#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

export GITHUB_REPOSITORY=Example/Repo RELEASE_VERSION=v1.2.3
export RELEASE_COMMIT=0123456789abcdef0123456789abcdef01234567
digest="sha256:$(printf '%064d' 0)"
jq -n --arg digest "$digest" --arg commit "$RELEASE_COMMIT" \
  '{schema: 1, repository: "example/repo", version: "v1.2.3", commit: $commit,
    images: {accountmanager: $digest, authoriser: $digest, logkeymanager: $digest,
             logservice: $digest, nginx: $digest, tokenhandler: $digest}}' >"$tmp/valid.json"
bash "$root/deploy/image-manifest.sh" "$tmp/valid.json"

for filter in \
  '.version = "v2"' \
  '.commit = "wrong"' \
  '.repository = "other/repo"' \
  '.schema = 2' \
  'del(.images.nginx)' \
  '.images.extra = .images.nginx' \
  '.images.nginx = "sha256:bad"' \
  '.images.nginx = 42'; do
  jq "$filter" "$tmp/valid.json" >"$tmp/invalid.json"
  if bash "$root/deploy/image-manifest.sh" "$tmp/invalid.json" >"$tmp/log" 2>&1; then
    echo "Invalid manifest accepted: $filter" >&2
    exit 1
  fi
done
echo "Image manifest validation tests passed."
