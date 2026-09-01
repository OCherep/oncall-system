#!/bin/bash
# Випуск / оновлення Let's Encrypt (HTTP-01 webroot) для OnCall :85
# Вимоги: DNS A/AAAA домену → цей сервер; з інтернету відкритий TCP/80.
set -euo pipefail
cd "$(dirname "$0")/.."

DOMAIN="${TLS_CN:-www.s.ks.tv}"
EMAIL="${LETSENCRYPT_EMAIL:-admin@${DOMAIN}}"
STAGING="${LETSENCRYPT_STAGING:-0}"

mkdir -p certs certbot/www certbot/conf
chmod -R a+rX certbot/www 2>/dev/null || true

echo "==> Domain: ${DOMAIN}"
echo "==> Email:  ${EMAIL}"
echo "==> Preflight: HTTP ACME path must answer on port 80"

# 1) compose піднятий?
if ! docker compose ps --status running 2>/dev/null | grep -q oncall_nginx_5; then
  echo "!! nginx-контейнер не running. Спочатку: docker compose up -d"
  exit 1
fi

# 2) порт 80 слухає наш nginx?
if ! ss -lntp 2>/dev/null | grep -qE ':80\s'; then
  echo "!! На хості ніхто не слухає :80 — LE HTTP-01 не спрацює."
  echo "   Перевірте ports у docker-compose (має бути \"80:80\") і firewall."
  exit 1
fi

# 3) локальний probe webroot
PROBE="le-ok-$(date +%s)"
echo ok > "certbot/www/${PROBE}"
HTTP_CODE=$(curl -sS -o /dev/null -w "%{http_code}" --max-time 5 "http://127.0.0.1/.well-known/acme-challenge/${PROBE}" || true)
rm -f "certbot/www/${PROBE}"
if [ "$HTTP_CODE" != "200" ]; then
  echo "!! ACME webroot не віддається (HTTP ${HTTP_CODE})."
  echo "   Очікувалось: http://127.0.0.1/.well-known/acme-challenge/ → 200"
  echo "   Якщо на :80 інший nginx — додайте location /.well-known/acme-challenge/"
  echo "   proxy на цей контейнер або зупиніть чужий сервіс на час випуску."
  exit 1
fi
echo "==> ACME webroot OK (local HTTP 200)"

STAGING_ARGS=()
if [ "$STAGING" = "1" ]; then
  STAGING_ARGS+=(--staging)
  echo "==> STAGING mode (тестові сертифікати LE)"
fi

echo "==> certbot certonly --webroot"
docker run --rm \
  -v "$(pwd)/certbot/www:/var/www/certbot" \
  -v "$(pwd)/certbot/conf:/etc/letsencrypt" \
  certbot/certbot certonly --webroot \
  -w /var/www/certbot \
  -d "$DOMAIN" \
  --email "$EMAIL" \
  --agree-tos --no-eff-email --non-interactive \
  --keep-until-expiring \
  "${STAGING_ARGS[@]+"${STAGING_ARGS[@]}"}"

live="certbot/conf/live/${DOMAIN}"
if [ ! -f "${live}/fullchain.pem" ] || [ ! -f "${live}/privkey.pem" ]; then
  echo "ERROR: немає ${live}/fullchain.pem після certbot"
  exit 1
fi

cp -L "${live}/fullchain.pem" certs/fullchain.pem
cp -L "${live}/privkey.pem"   certs/privkey.pem
chmod 644 certs/fullchain.pem
chmod 640 certs/privkey.pem || true

echo "==> ./certs оновлено — restart nginx"
docker compose restart nginx

echo ""
echo "OK. Перевірте БЕЗ -k:"
echo "  curl -sS -m 5 -o /dev/null -w '%{http_code}\\n' https://${DOMAIN}:85/"
echo "  openssl s_client -connect ${DOMAIN}:85 -servername ${DOMAIN} </dev/null 2>/dev/null | openssl x509 -noout -issuer -dates"
echo ""
echo "Slack Request URL:"
echo "  https://${DOMAIN}:85/api/webhooks/slack"
