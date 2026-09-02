#!/bin/bash
# TLS helper for OnCall :85
#   ./scripts/setup-tls.sh le          — Let's Encrypt HTTP-01
#   ./scripts/setup-tls.sh copy-host   — з /etc/letsencrypt/live/$DOMAIN
#   ./scripts/setup-tls.sh selfsigned  — self-signed (dev only)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
DOMAIN="${TLS_CN:-s.ks.tv}"
MODE="${1:-}"
mkdir -p certs certbot/www certbot/conf

usage() {
  cat <<USAGE
Usage:
  TLS_CN=s.ks.tv LETSENCRYPT_EMAIL=you@company.com $0 le
  TLS_CN=s.ks.tv HOST_LE_LIVE=/etc/letsencrypt/live/s.ks.tv $0 copy-host
  $0 selfsigned

Env:
  TLS_CN                domain (default s.ks.tv)
  LETSENCRYPT_EMAIL     email for LE
  LETSENCRYPT_STAGING=1 test CA
  EXTRA_DOMAINS         "s.ks.tv other.example"
  HOST_LE_LIVE          path to live cert dir for copy-host
USAGE
}

case "${MODE}" in
  le|letsencrypt|certbot)
    exec "$ROOT/scripts/issue-letsencrypt.sh"
    ;;
  copy-host|host)
    SRC="${HOST_LE_LIVE:-/etc/letsencrypt/live/${DOMAIN}}"
    if [ ! -f "${SRC}/fullchain.pem" ] || [ ! -f "${SRC}/privkey.pem" ]; then
      echo "Немає ${SRC}/fullchain.pem або privkey.pem"
      exit 1
    fi
    cp -L "${SRC}/fullchain.pem" certs/fullchain.pem
    cp -L "${SRC}/privkey.pem"   certs/privkey.pem
    chmod 644 certs/fullchain.pem
    chmod 640 certs/privkey.pem || true
    docker compose restart nginx
    echo "OK: скопійовано з ${SRC} → ./certs/ і nginx перезапущено"
    echo "Перевірка: curl -sS -m 5 -o /dev/null -w '%{http_code}\\n' https://${DOMAIN}:85/"
    ;;
  selfsigned|dev)
    rm -f certs/fullchain.pem certs/privkey.pem
    docker compose restart nginx
    echo "OK: self-signed (Slack НЕ прийме цей сертифікат)"
    ;;
  ""|-h|--help)
    usage
    exit 0
    ;;
  *)
    usage
    exit 1
    ;;
esac
