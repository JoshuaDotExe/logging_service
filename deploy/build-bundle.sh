#!/usr/bin/env bash
# Assembles the release bundle that .github/workflows/deploy.yml uploads to the
# OCI host. Configuration and secrets are read from environment variables and
# written only to the output directory; never echo them.
#
# Usage: deploy/build-bundle.sh <output-dir>
set -euo pipefail
umask 077

out=${1:?usage: build-bundle.sh <output-dir>}
repo_root=$(cd "$(dirname "$0")/.." && pwd)
deploy_root=/opt/logging-service

errors=0
error() {
  printf '::error::%s\n' "$1" >&2
  errors=1
}
require() {
  [[ -n $2 ]] || error "$1 is not set"
}
match() {
  [[ -z $2 || $2 =~ $3 ]] || error "$1 has an invalid format: $4"
}

: "${GITHUB_REPOSITORY:?}" "${GITHUB_SHA:?}" "${GITHUB_ACTOR:?}"
RELEASE_VERSION=${RELEASE_VERSION:-development}
RELEASE_COMMIT=${RELEASE_COMMIT:-$GITHUB_SHA}
POSTGRES_USER=${POSTGRES_USER:-logging}
POSTGRES_DB=${POSTGRES_DB:-logging}
JWT_ISSUER=${JWT_ISSUER:-logging-service}
JWT_AUDIENCE=${JWT_AUDIENCE:-account-management}
FORCE_RECREATE=${FORCE_RECREATE:-false}
TLS_CERTIFICATE=${TLS_CERTIFICATE:-}
TLS_PRIVATE_KEY=${TLS_PRIVATE_KEY:-}
ACME_EMAIL=${ACME_EMAIL:-}

require DOMAIN "${DOMAIN:-}"
require POSTGRES_PASSWORD "${POSTGRES_PASSWORD:-}"
require TOKEN_SERVICE_KEY "${TOKEN_SERVICE_KEY:-}"
require JWT_PRIVATE_KEY "${JWT_PRIVATE_KEY:-}"
require GHCR_TOKEN "${GHCR_TOKEN:-}"
require IMAGE_MANIFEST "${IMAGE_MANIFEST:-}"

match DOMAIN "${DOMAIN:-}" '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$' "lowercase DNS name, e.g. logs.example.com"
match POSTGRES_PASSWORD "${POSTGRES_PASSWORD:-}" '^[A-Za-z0-9_-]{24,}$' "at least 24 URL-safe characters (use: openssl rand -hex 32)"
match TOKEN_SERVICE_KEY "${TOKEN_SERVICE_KEY:-}" '^[A-Za-z0-9_-]{32,}$' "at least 32 URL-safe characters (use: openssl rand -hex 32)"
match POSTGRES_USER "$POSTGRES_USER" '^[a-z_][a-z0-9_]{0,62}$' "lowercase PostgreSQL identifier"
match POSTGRES_DB "$POSTGRES_DB" '^[a-z_][a-z0-9_]{0,62}$' "lowercase PostgreSQL identifier"
match JWT_ISSUER "$JWT_ISSUER" '^[A-Za-z0-9._:/-]+$' "letters, digits and ._:/-"
match JWT_AUDIENCE "$JWT_AUDIENCE" '^[A-Za-z0-9._:/-]+$' "letters, digits and ._:/-"
match FORCE_RECREATE "$FORCE_RECREATE" '^(true|false)$' "true or false"
match RELEASE_VERSION "$RELEASE_VERSION" '^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$' "1-128 safe release-tag characters"
match RELEASE_COMMIT "$RELEASE_COMMIT" '^[0-9a-f]{40}$' "40-character lowercase commit SHA"

if [[ -n $TLS_CERTIFICATE || -n $TLS_PRIVATE_KEY ]]; then
  tls_mode=provided
  require TLS_CERTIFICATE "$TLS_CERTIFICATE"
  require TLS_PRIVATE_KEY "$TLS_PRIVATE_KEY"
else
  tls_mode=letsencrypt
  require ACME_EMAIL "$ACME_EMAIL"
  match ACME_EMAIL "$ACME_EMAIL" '^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$' "email address"
fi

((errors == 0)) || exit 1

export RELEASE_VERSION RELEASE_COMMIT
bash "$repo_root/deploy/image-manifest.sh" "$IMAGE_MANIFEST"

rm -rf "$out"
mkdir -p "$out/app/deploy" "$out/runtime/secrets"

cp "$repo_root/compose.yaml" "$out/app/"
cp "$repo_root/deploy/compose.prod.yaml" "$repo_root/deploy/remote-deploy.sh" "$repo_root/deploy/tls.sh" "$out/app/deploy/"
cp -R "$repo_root/migrations" "$out/app/migrations"

secrets="$out/runtime/secrets"
printf '%s\n' "$JWT_PRIVATE_KEY" >"$secrets/jwt_private.pem"
if ! openssl pkey -in "$secrets/jwt_private.pem" -noout -text 2>/dev/null | head -n1 | grep -q 'ED25519'; then
  error "JWT_PRIVATE_KEY must be an Ed25519 PKCS#8 PEM key (openssl genpkey -algorithm ed25519)"
  exit 1
fi
# The public key is derived so it can never drift from the signing key.
openssl pkey -in "$secrets/jwt_private.pem" -pubout -out "$secrets/jwt_public.pem"

if [[ $tls_mode == provided ]]; then
  printf '%s\n' "$TLS_CERTIFICATE" >"$secrets/tls_fullchain.pem"
  printf '%s\n' "$TLS_PRIVATE_KEY" >"$secrets/tls_privkey.pem"
  cert_pub=$(openssl x509 -in "$secrets/tls_fullchain.pem" -noout -pubkey 2>/dev/null) || {
    error "TLS_CERTIFICATE is not a PEM certificate"
    exit 1
  }
  key_pub=$(openssl pkey -in "$secrets/tls_privkey.pem" -pubout 2>/dev/null) || {
    error "TLS_PRIVATE_KEY is not a PEM private key"
    exit 1
  }
  [[ $cert_pub == "$key_pub" ]] || {
    error "TLS_PRIVATE_KEY does not match TLS_CERTIFICATE"
    exit 1
  }
fi

pg_url="postgres://$POSTGRES_USER:$POSTGRES_PASSWORD@postgres:5432/$POSTGRES_DB?sslmode=disable"

# Single quotes stop Compose from interpolating inside values; the validation
# above guarantees no value contains a quote or newline.
env_line() { printf "%s='%s'\n" "$1" "$2"; }
{
  env_line IMAGE_REGISTRY "ghcr.io/${GITHUB_REPOSITORY,,}"
  for service in accountmanager authoriser logkeymanager logservice nginx tokenhandler; do
    env_line "${service^^}_IMAGE_DIGEST" "$(jq -r --arg service "$service" '.images[$service]' "$IMAGE_MANIFEST")"
  done
  env_line RELEASE_VERSION "$RELEASE_VERSION"
  env_line RELEASE_COMMIT "$RELEASE_COMMIT"
  env_line DEPLOY_ROOT "$deploy_root"
  env_line POSTGRES_DATA_DIR /srv/logging-service/postgres
  env_line POSTGRES_USER "$POSTGRES_USER"
  env_line POSTGRES_PASSWORD "$POSTGRES_PASSWORD"
  env_line POSTGRES_DB "$POSTGRES_DB"
  env_line POSTGRES_URL "$pg_url"
  env_line POSTGRES_MIGRATION_URL "$pg_url"
  env_line TOKEN_SERVICE_KEY "$TOKEN_SERVICE_KEY"
  env_line JWT_ISSUER "$JWT_ISSUER"
  env_line JWT_AUDIENCE "$JWT_AUDIENCE"
  env_line JWT_PRIVATE_KEY_FILE "$deploy_root/secrets/jwt_private.pem"
  env_line JWT_PUBLIC_KEY_FILE "$deploy_root/secrets/jwt_public.pem"
  env_line NGINX_SERVER_NAME "$DOMAIN"
  env_line NGINX_TLS_CERT_FILE "$deploy_root/secrets/tls_fullchain.pem"
  env_line NGINX_TLS_KEY_FILE "$deploy_root/secrets/tls_privkey.pem"
  env_line LOGSERVICE_API_PORT 8080
  env_line ACCOUNTMANAGER_API_PORT 8081
  env_line LOGKEYMANAGER_API_PORT 8082
  env_line AUTHORISER_API_PORT 8083
  env_line TOKENHANDLER_API_PORT 8084
  env_line LOGSERVICE_ENV production
  env_line LOGSERVICE_AUTH_MODE authoriser
} >"$out/runtime/env"

{
  printf 'TLS_MODE=%q\n' "$tls_mode"
  printf 'TLS_DOMAIN=%q\n' "$DOMAIN"
  printf 'ACME_EMAIL=%q\n' "$ACME_EMAIL"
  printf 'FORCE_RECREATE=%q\n' "$FORCE_RECREATE"
  printf 'GHCR_USER=%q\n' "$GITHUB_ACTOR"
} >"$out/runtime/deploy.conf"

printf '%s' "$GHCR_TOKEN" >"$out/runtime/ghcr_token"

echo "Release bundle for ${RELEASE_VERSION} (${RELEASE_COMMIT}) written (TLS mode: $tls_mode)."
