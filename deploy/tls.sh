#!/usr/bin/env bash
# Obtains or renews the Let's Encrypt certificate for the nginx ingress and
# hot-reloads nginx when it changes. Run by deploy/remote-deploy.sh and daily
# from the deploy user's crontab. certbot only contacts Let's Encrypt when the
# certificate is missing or within 30 days of expiry.
set -euo pipefail

DEPLOY_ROOT=/opt/logging-service
PROJECT=logging-service
CERTBOT_IMAGE=certbot/certbot:latest

# shellcheck source=/dev/null
source "$DEPLOY_ROOT/tls.conf"

docker run --rm --pull always \
  -v "$DEPLOY_ROOT/letsencrypt:/etc/letsencrypt" \
  -v "$DEPLOY_ROOT/acme:/var/www/acme" \
  "$CERTBOT_IMAGE" certonly \
  --webroot --webroot-path /var/www/acme \
  --cert-name "$TLS_DOMAIN" --domain "$TLS_DOMAIN" \
  --email "$ACME_EMAIL" --agree-tos --no-eff-email \
  --non-interactive --keep-until-expiring

# certbot's files are root-owned, so compare and copy from inside a container.
# Only the two destination files are mounted; writes happen in place to keep
# nginx's bind-mounted secrets pointing at the same inode.
changed=$(docker run --rm --entrypoint sh \
  -v "$DEPLOY_ROOT/letsencrypt:/etc/letsencrypt:ro" \
  -v "$DEPLOY_ROOT/secrets/tls_fullchain.pem:/out/fullchain.pem" \
  -v "$DEPLOY_ROOT/secrets/tls_privkey.pem:/out/privkey.pem" \
  "$CERTBOT_IMAGE" -c '
    live="/etc/letsencrypt/live/$1"
    if cmp -s "$live/fullchain.pem" /out/fullchain.pem && cmp -s "$live/privkey.pem" /out/privkey.pem; then
      echo no
    else
      cat "$live/fullchain.pem" > /out/fullchain.pem
      cat "$live/privkey.pem" > /out/privkey.pem
      echo yes
    fi' sh "$TLS_DOMAIN")

if [[ $changed == yes ]]; then
  nginx_id=$(docker ps --quiet \
    --filter "label=com.docker.compose.project=$PROJECT" \
    --filter "label=com.docker.compose.service=nginx")
  if [[ -n $nginx_id ]]; then
    docker kill --signal HUP "$nginx_id" >/dev/null
  fi
  echo "certificate for $TLS_DOMAIN installed"
else
  echo "certificate for $TLS_DOMAIN unchanged"
fi
