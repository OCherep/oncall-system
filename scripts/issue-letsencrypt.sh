#!/bin/bash
# Випуск / оновлення сертифіката Let's Encrypt для www.s.ks.tv
# Потрібно: DNS A-запис на цей сервер; порт 80 відкритий з інтернету.
set -euo pipefail
cd "$(dirname "$0")/.."
DOMAIN="${TLS_CN:-www.s.ks.tv}"
EMAIL="${LETSENCRYPT_EMAIL:-admin@${DOMAIN}}"
mkdir -p certs certbot/www certbot/conf

echo "==> Переконайтесь що docker compose піднятий і http://${DOMAIN}/.well-known/ доступний"

docker run --rm \
  -v "$(pwd)/certbot/www:/var/www/certbot" \
  -v "$(pwd)/certbot/conf:/etc/letsencrypt" \
  certbot/certbot certonly --webroot \
  -w /var/www/certbot \
  -d "$DOMAIN" \
  --email "$EMAIL" \
  --agree-tos --no-eff-email --non-interactive

# Скопіювати в ./certs для nginx
live="certbot/conf/live/${DOMAIN}"
if [ -f "${live}/fullchain.pem" ]; then
  cp -L "${live}/fullchain.pem" certs/fullchain.pem
  cp -L "${live}/privkey.pem" certs/privkey.pem
  chmod 640 certs/privkey.pem certs/fullchain.pem || true
  echo "==> Сертифікати в ./certs — перезапуск nginx"
  docker compose restart nginx
  echo "OK: https://${DOMAIN}:85/"
else
  echo "ERROR: не знайдено ${live}/fullchain.pem"
  exit 1
fi
