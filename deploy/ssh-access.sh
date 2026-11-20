#!/usr/bin/env bash
# Opens or closes transient SSH access to the OCI host for a GitHub Actions run
# by adding or removing a /32 ingress rule on the SSH network security group.
# Rules are tagged with a "gha-" description so stale ones from cancelled runs
# are removed too. Deploys are serialised by the workflow's concurrency group.
#
# Usage: deploy/ssh-access.sh open|close
# Requires: OCI CLI configured (~/.oci/config), jq, OCI_SSH_NSG_ID.
# Optional: RULE_DESCRIPTION (defaults to gha-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT).
set -euo pipefail

action=${1:?usage: ssh-access.sh open|close}
nsg=${OCI_SSH_NSG_ID:?OCI_SSH_NSG_ID is not set}
prefix=gha-
description=${RULE_DESCRIPTION:-$prefix${GITHUB_RUN_ID:-manual}-${GITHUB_RUN_ATTEMPT:-1}}

remove_tagged_rules() {
  local ids
  ids=$(oci network nsg rules list --nsg-id "$nsg" --all |
    jq -c --arg p "$prefix" '[.data[]? | select((.description // "") | startswith($p)) | .id]')
  ids=${ids:-[]}
  if [[ $ids != "[]" ]]; then
    oci network nsg rules remove --nsg-id "$nsg" --security-rule-ids "$ids" >/dev/null
    echo "Removed $(jq length <<<"$ids") transient SSH rule(s)."
  fi
}

runner_ip() {
  local ip url
  for url in https://api.ipify.org https://checkip.amazonaws.com https://ifconfig.me/ip; do
    ip=$(curl -fsS --max-time 10 "$url" 2>/dev/null | tr -d '[:space:]') || continue
    if [[ $ip =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
      printf '%s' "$ip"
      return 0
    fi
  done
  echo "::error::could not determine the runner's public IPv4 address" >&2
  return 1
}

case $action in
open)
  remove_tagged_rules
  ip=$(runner_ip)
  rule=$(jq -nc --arg src "$ip/32" --arg desc "$description" '[{
    direction: "INGRESS",
    protocol: "6",
    source: $src,
    sourceType: "CIDR_BLOCK",
    isStateless: false,
    description: $desc,
    tcpOptions: {destinationPortRange: {min: 22, max: 22}}
  }]')
  oci network nsg rules add --nsg-id "$nsg" --security-rules "$rule" >/dev/null
  echo "Opened SSH from the runner's address ($description)."
  ;;
close)
  remove_tagged_rules
  ;;
*)
  echo "usage: ssh-access.sh open|close" >&2
  exit 2
  ;;
esac
