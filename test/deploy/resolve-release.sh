#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

mkdir -p "$tmp/bin" "$tmp/repo"
cat >"$tmp/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ ${GH_FAIL:-false} == false ]] || exit 1
jq -n --arg tag "$RELEASE_VERSION" --argjson draft "${GH_DRAFT:-false}" \
  '{tag_name: $tag, draft: $draft, prerelease: false}'
EOF
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH" GITHUB_REPOSITORY=example/repo GITHUB_OUTPUT="$tmp/output"

git -C "$tmp/repo" init -q -b main
git -C "$tmp/repo" config user.email test@example.com
git -C "$tmp/repo" config user.name test
git -C "$tmp/repo" commit -q --allow-empty -m initial
git -C "$tmp/repo" tag v0.1.0
mkdir -p "$tmp/repo/deploy"
printf 'versioned deployment\n' >"$tmp/repo/deploy/versioned-release-v1"
git -C "$tmp/repo" add deploy/versioned-release-v1
git -C "$tmp/repo" commit -q -m versioned
commit=$(git -C "$tmp/repo" rev-parse HEAD)
git -C "$tmp/repo" tag -a v1.2.3 -m release
git -C "$tmp/repo" tag v1.2.4
git -C "$tmp/repo" update-ref refs/remotes/origin/main "$commit"
git -C "$tmp/repo" switch -q --detach v0.1.0
git -C "$tmp/repo" commit -q --allow-empty -m unrelated
git -C "$tmp/repo" tag v9.0.0
cd "$tmp/repo"

check_success() {
  export RELEASE_VERSION=$1
  : >"$GITHUB_OUTPUT"
  bash "$root/deploy/resolve-release.sh"
  grep -Fx "version=$RELEASE_VERSION" "$GITHUB_OUTPUT"
  grep -Fx "commit=$commit" "$GITHUB_OUTPUT"
}

check_failure() {
  export RELEASE_VERSION=$1
  : >"$GITHUB_OUTPUT"
  if bash "$root/deploy/resolve-release.sh" >"$tmp/log" 2>&1; then
    echo "Expected release $RELEASE_VERSION to be rejected" >&2
    exit 1
  fi
  if [[ -s $GITHUB_OUTPUT ]]; then
    echo "Rejected release produced outputs" >&2
    exit 1
  fi
  if ! grep -Fq "$2" "$tmp/log"; then
    echo "Unexpected error for $RELEASE_VERSION:" >&2
    cat "$tmp/log" >&2
    exit 1
  fi
}

check_success v1.2.3
check_success v1.2.4
check_failure v0.1.0 'predates support for versioned deployments'
check_failure v9.0.0 'is not on main'
check_failure missing 'does not resolve to a commit'
check_failure '../invalid' 'Release tag must be'
export GH_DRAFT=true
check_failure v1.2.3 'is a draft or has an unexpected tag'
unset GH_DRAFT
export GH_FAIL=true
check_failure v1.2.3 'Could not find published release'
echo "Release resolution tests passed."
