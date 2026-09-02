#!/bin/bash
# =============================================================================
# Let's Encrypt (HTTP-01) для OnCall grok-1.0.0
# Публічний URL: https://$TLS_CN:85/
# ACME challenge: http://$TLS_CN/.well-known/acme-challenge/  (хост :80)
# =============================================================================
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DOMAIN="${TLS_CN:-s.ks.tv}"
EMAIL="${LETSENCRYPT_EMAIL:-admin@${DOMAIN}}"
STAGING="${LETSENCRYPT_STAGING:-0}"
# додаткові імена через пробіл, напр. EXTRA_DOMAINS="s.ks.tv"
EXTRA_DOMAINS="${EXTRA_DOMAINS:-}"

mkdir -p certs certbot/www certbot/conf
chmod -R a+rX certbot/www 2>/dev/null || true

echo "=============================================="
echo " Let's Encrypt for OnCall"
echo " Domain:  ${DOMAIN}"
echo " Email:   ${EMAIL}"
echo " Staging: ${STAGING}"
echo "=============================================="

if ! command -v docker >/dev/null 2>&1; then
  echo "ERROR: docker не знайдено"; exit 1
fi

# --- 1. nginx running ---
if ! docker compose ps 2>/dev/null | grep -E 'oncall_nginx_5' | grep -qiE 'up|running'; then
  echo "!! Контейнер oncall_nginx_5 не запущений."
  echo "   Спочатку:  docker compose up -d"
  exit 1
fi

# --- 2. host :80 listening ---
if command -v ss >/dev/null 2>&1; then
  if ! ss -lntp 2>/dev/null | grep -qE ':80\s'; then
    echo "!! На хості ніхто не слухає TCP/80."
    echo "   У docker-compose.yml має бути: ports: - \"80:80\""
    echo "   І firewall: allow 80/tcp з інтернету."
    exit 1
  fi
  echo "==> :80 слухається на хості"
  ss -lntp 2>/dev/null | grep -E ':80\s' || true
fi

# --- 3. ACME webroot preflight (local) ---
PROBE="le-ok-$(date +%s)"
echo "ok-${PROBE}" > "certbot/www/${PROBE}"
# nginx mounts ./certbot/www → /var/www/certbot
CODE_LOCAL=$(curl -sS -o /tmp/le-body -w "%{http_code}" --max-time 5 \
  "http://127.0.0.1/.well-known/acme-challenge/${PROBE}" || echo "000")
BODY_LOCAL=$(cat /tmp/le-body 2>/dev/null || true)
rm -f "certbot/www/${PROBE}" /tmp/le-body

if [ "$CODE_LOCAL" != "200" ]; then
  echo "!! ACME webroot НЕ працює локально (HTTP ${CODE_LOCAL})."
  echo "   Очікувалось: GET http://127.0.0.1/.well-known/acme-challenge/<file> → 200"
  echo ""
  echo "   Можливі причини:"
  echo "   1) На :80 відповідає НЕ oncall_nginx_5 (інший nginx/apache)."
  echo "      Перевірте: curl -sI http://127.0.0.1/ | head -5"
  echo "      docker port oncall_nginx_5"
  echo "   2) volume certbot/www не підключений до nginx."
  echo "   3) у nginx.conf немає location ^~ /.well-known/acme-challenge/"
  exit 1
fi
echo "==> ACME webroot OK (local HTTP 200)"

# probe with Host: domain (як LE ззовні)
echo "ok-host" > "certbot/www/host-probe"
CODE_HOST=$(curl -sS -o /dev/null -w "%{http_code}" --max-time 5 \
  -H "Host: ${DOMAIN}" "http://127.0.0.1/.well-known/acme-challenge/host-probe" || echo "000")
rm -f certbot/www/host-probe
echo "==> ACME with Host: ${DOMAIN} → HTTP ${CODE_HOST}"

# --- 4. certbot ---
DOMAIN_ARGS=(-d "$DOMAIN")
for d in $EXTRA_DOMAINS; do
  [ -n "$d" ] && DOMAIN_ARGS+=(-d "$d")
done

STAGING_ARGS=()
if [ "$STAGING" = "1" ]; then
  STAGING_ARGS=(--staging)
  echo "==> STAGING (тестовий CA, браузери НЕ довірятимуть)"
fi

echo "==> Запуск certbot/certbot ..."
docker run --rm \
  -v "${ROOT}/certbot/www:/var/www/certbot" \
  -v "${ROOT}/certbot/conf:/etc/letsencrypt" \
  certbot/certbot certonly --webroot \
  -w /var/www/certbot \
  "${DOMAIN_ARGS[@]}" \
  --email "$EMAIL" \
  --agree-tos --no-eff-email --non-interactive \
  --keep-until-expiring \
  --cert-name "$DOMAIN" \
  ${STAGING_ARGS[@]+"${STAGING_ARGS[@]}"}

live="${ROOT}/certbot/conf/live/${DOMAIN}"
if [ ! -f "${live}/fullchain.pem" ] || [ ! -f "${live}/privkey.pem" ]; then
  echo "ERROR: після certbot немає ${live}/fullchain.pem"
  echo "Дивіться логи вище (часто: DNS не вказує сюди, :80 закритий ззовні, rate limit)."
  exit 1
fi

cp -L "${live}/fullchain.pem" "${ROOT}/certs/fullchain.pem"
cp -L "${live}/privkey.pem"   "${ROOT}/certs/privkey.pem"
chmod 644 "${ROOT}/certs/fullchain.pem"
chmod 640 "${ROOT}/certs/privkey.pem" || true

echo "==> Сертифікати скопійовано в ./certs/"
docker compose restart nginx

sleep 1
echo ""
echo "========== ГОТОВО =========="
echo "Перевірка (без -k):"
echo "  curl -sS -m 5 -o /dev/null -w '%{http_code}\\n' https://${DOMAIN}:85/"
echo "  echo | openssl s_client -connect ${DOMAIN}:85 -servername ${DOMAIN} 2>/dev/null | openssl x509 -noout -issuer -subject -dates"
echo ""
echo "Slack Request URL:"
echo "  https://${DOMAIN}:85/api/webhooks/slack"
echo "============================"
