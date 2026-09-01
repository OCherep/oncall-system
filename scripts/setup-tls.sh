#!/bin/bash
# Універсальне підключення TLS для OnCall (порт 85).
# Режими:
#   ./scripts/setup-tls.sh le          — Let's Encrypt HTTP-01
#   ./scripts/setup-tls.sh copy-host   — скопіювати з /etc/letsencrypt/live/$DOMAIN
#   ./scripts/setup-tls.sh selfsigned  — self-signed (лише dev; Slack НЕ прийме)
set -euo pipefail
cd "$(dirname "$0")/.."
DOMAIN="${TLS_CN:-www.s.ks.tv}"
MODE="${1:-le}"
mkdir -p certs certbot/www certbot/conf

case "$MODE" in
  le|letsencrypt)
    exec ./scripts/issue-letsencrypt.sh
    ;;
  copy-host|host)
    SRC="${HOST_LE_LIVE:-/etc/letsencrypt/live/${DOMAIN}}"
    if [ ! -f "${SRC}/fullchain.pem" ]; then
      echo "Немає ${SRC}/fullchain.pem"
      echo "Вкажіть HOST_LE_LIVE=/path/to/live/domain"
      exit 1
    fi
    cp -L "${SRC}/fullchain.pem" certs/fullchain.pem
    cp -L "${SRC}/privkey.pem"   certs/privkey.pem
    chmod 644 certs/fullchain.pem
    chmod 640 certs/privkey.pem || true
    docker compose restart nginx
    echo "OK: скопійовано з ${SRC}"
    ;;
  selfsigned|dev)
    rm -f certs/fullchain.pem certs/privkey.pem
    docker compose restart nginx
    echo "OK: entrypoint згенерує self-signed (Slack/браузери будуть скаржитись)"
    ;;
  *)
    echo "Usage: $0 le|copy-host|selfsigned"
    exit 1
    ;;
esac
