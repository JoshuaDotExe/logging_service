#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_VERSION:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_OUTPUT:?}"

if [[ ! $RELEASE_VERSION =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
  echo "::error::Release tag must be 1-128 letters, digits, underscores, dots or hyphens, and start with a letter, digit or underscore"
  exit 1
fi

if ! release=$(gh api "repos/$GITHUB_REPOSITORY/releases/tags/$RELEASE_VERSION"); then
  echo "::error::Could not find published release $RELEASE_VERSION"
  exit 1
fi
if ! jq -e --arg tag "$RELEASE_VERSION" '.tag_name == $tag and .draft == false' <<<"$release" >/dev/null; then
  echo "::error::Release $RELEASE_VERSION is a draft or has an unexpected tag"
  exit 1
fi

if ! commit=$(git rev-parse --verify "refs/tags/${RELEASE_VERSION}^{commit}"); then
  echo "::error::Release tag $RELEASE_VERSION does not resolve to a commit"
  exit 1
fi
if ! git merge-base --is-ancestor "$commit" refs/remotes/origin/main; then
  echo "::error::Release commit $commit is not on main"
  exit 1
fi

if ! git cat-file -e "$commit:deploy/digest-release-v1"; then
  echo "::error::Release $RELEASE_VERSION predates support for digest-based deployments"
  exit 1
fi

{
  echo "version=$RELEASE_VERSION"
  echo "commit=$commit"
} >>"$GITHUB_OUTPUT"
echo "Selected release $RELEASE_VERSION ($commit)"
