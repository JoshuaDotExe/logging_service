#!/usr/bin/env bash
# Runs on the OCI host as the `deploy` user. .github/workflows/deploy.yml
# uploads a bundle built by deploy/build-bundle.sh to $DEPLOY_ROOT/incoming and
# then executes this script from it. Safe to re-run; it never deletes data.
set -euo pipefail
umask 077

DEPLOY_ROOT=/opt/logging-service
INCOMING="$DEPLOY_ROOT/incoming"
DATA_MOUNT=/srv/logging-service
PROJECT=logging-service
APP_SERVICES=(authoriser tokenhandler logkeymanager logservice accountmanager)

log() { printf '[deploy] %s\n' "$*"; }
fail() {
  printf '::error::%s\n' "$*"
  exit 1
}

[[ -d $INCOMING/app && -d $INCOMING/runtime ]] || fail "no release bundle found in $INCOMING"

cleanup() {
  docker logout ghcr.io >/dev/null 2>&1 || true
  rm -rf "$INCOMING"
}
trap cleanup EXIT

# shellcheck source=/dev/null
source "$INCOMING/runtime/deploy.conf"

log "waiting for host bootstrap"
timeout 900 cloud-init status --wait >/dev/null 2>&1 || true
[[ -f /var/lib/logging-service-bootstrap.done ]] || fail "host bootstrap incomplete; inspect /var/log/cloud-init-output.log"
mountpoint -q "$DATA_MOUNT" || fail "$DATA_MOUNT is not mounted; refusing to start PostgreSQL on the boot disk"

# Overwrite in place so the inode stays the same: Compose secrets are bind
# mounts, and running containers keep seeing the file they were started with.
replace_in_place() {
  local src=$1 dst=$2 mode=$3
  touch "$dst"
  chmod "$mode" "$dst"
  cat "$src" >"$dst"
}

install -d -m 0700 "$DEPLOY_ROOT/secrets"
install -d -m 0755 "$DEPLOY_ROOT/acme" "$DEPLOY_ROOT/letsencrypt"

replace_in_place "$INCOMING/runtime/env" "$DEPLOY_ROOT/.env" 0600
# 0644 lets the non-root users inside the containers read the keys; the 0700
# parent directory keeps every other host user out.
replace_in_place "$INCOMING/runtime/secrets/jwt_private.pem" "$DEPLOY_ROOT/secrets/jwt_private.pem" 0644
replace_in_place "$INCOMING/runtime/secrets/jwt_public.pem" "$DEPLOY_ROOT/secrets/jwt_public.pem" 0644

if [[ $TLS_MODE == provided ]]; then
  replace_in_place "$INCOMING/runtime/secrets/tls_fullchain.pem" "$DEPLOY_ROOT/secrets/tls_fullchain.pem" 0644
  replace_in_place "$INCOMING/runtime/secrets/tls_privkey.pem" "$DEPLOY_ROOT/secrets/tls_privkey.pem" 0600
elif [[ ! -s $DEPLOY_ROOT/secrets/tls_fullchain.pem ]]; then
  log "creating a temporary self-signed certificate until Let's Encrypt issues one"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 7 \
    -subj "/CN=$TLS_DOMAIN" \
    -keyout "$DEPLOY_ROOT/secrets/tls_privkey.pem" \
    -out "$DEPLOY_ROOT/secrets/tls_fullchain.pem" 2>/dev/null
  chmod 0644 "$DEPLOY_ROOT/secrets/tls_fullchain.pem"
  chmod 0600 "$DEPLOY_ROOT/secrets/tls_privkey.pem"
fi

rm -rf "$DEPLOY_ROOT/app.previous"
if [[ -d $DEPLOY_ROOT/app ]]; then
  mv "$DEPLOY_ROOT/app" "$DEPLOY_ROOT/app.previous"
fi
mv "$INCOMING/app" "$DEPLOY_ROOT/app"
chmod -R go+rX "$DEPLOY_ROOT/app/migrations"

compose() {
  docker compose \
    --project-name "$PROJECT" \
    --project-directory "$DEPLOY_ROOT/app" \
    --env-file "$DEPLOY_ROOT/.env" \
    -f "$DEPLOY_ROOT/app/compose.yaml" \
    -f "$DEPLOY_ROOT/app/deploy/compose.prod.yaml" \
    "$@"
}

compose config --quiet

log "pulling images"
docker login ghcr.io --username "$GHCR_USER" --password-stdin <"$INCOMING/runtime/ghcr_token" >/dev/null
compose pull --quiet "${APP_SERVICES[@]}" nginx postgres postgres_migrate
docker logout ghcr.io >/dev/null

log "starting data services"
compose up --detach --no-build --wait --wait-timeout 180 postgres

log "running migrations"
compose run --rm --no-deps postgres_migrate

recreate=()
if [[ $FORCE_RECREATE == true ]]; then
  recreate=(--force-recreate)
fi

log "starting application services"
compose up --detach --no-build --remove-orphans --wait --wait-timeout 300 "${recreate[@]}" "${APP_SERVICES[@]}" nginx

if [[ $TLS_MODE == letsencrypt ]]; then
  printf 'TLS_DOMAIN=%q\nACME_EMAIL=%q\n' "$TLS_DOMAIN" "$ACME_EMAIL" >"$DEPLOY_ROOT/tls.conf"
  log "ensuring Let's Encrypt certificate for $TLS_DOMAIN"
  "$DEPLOY_ROOT/app/deploy/tls.sh" || fail "certificate issuance failed; check that DNS for $TLS_DOMAIN points at this host and port 80 is reachable"
  cron_line="17 3 * * * $DEPLOY_ROOT/app/deploy/tls.sh >>$DEPLOY_ROOT/tls.log 2>&1"
  { crontab -l 2>/dev/null | grep -v 'deploy/tls.sh' || true; echo "$cron_line"; } | crontab -
else
  { crontab -l 2>/dev/null | grep -v 'deploy/tls.sh' || true; } | crontab -
fi

docker image prune --all --force --filter "until=168h" >/dev/null || true

compose ps
log "deployed $(grep '^IMAGE_TAG=' "$DEPLOY_ROOT/.env" | cut -d"'" -f2)"
