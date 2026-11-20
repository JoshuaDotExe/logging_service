#!/usr/bin/env bash
set -euo pipefail

manifest=${1:?usage: image-manifest.sh manifest.json}
: "${GITHUB_REPOSITORY:?}" "${RELEASE_VERSION:?}" "${RELEASE_COMMIT:?}"

if ! jq -e --arg repository "${GITHUB_REPOSITORY,,}" \
  --arg version "$RELEASE_VERSION" --arg commit "$RELEASE_COMMIT" '
    .schema == 1 and .repository == $repository and .version == $version and
    .commit == $commit and
    (.images | type == "object" and
      (keys == ["accountmanager", "authoriser", "logkeymanager", "logservice", "nginx", "tokenhandler"]) and
      all(.[]; type == "string" and test("^sha256:[0-9a-f]{64}$")))
  ' "$manifest" >/dev/null; then
  echo "::error::Image manifest is invalid or does not match the selected release and commit" >&2
  exit 1
fi
