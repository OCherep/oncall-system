#!/bin/bash
# Встановлення ZeroSSL / PEM у ./certs для nginx (порт 85).
# Використання:
#   ./scripts/install-zerossl.sh /path/to/dir
#   де dir містить: certificate.crt, ca_bundle.crt, private.key
#   або: fullchain.pem + privkey.pem
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
SRC="${1:-.}"
mkdir -p certs

if [ -f "${SRC}/fullchain.pem" ] && [ -f "${SRC}/privkey.pem" ]; then
  cp -L "${SRC}/fullchain.pem" certs/fullchain.pem
  cp -L "${SRC}/privkey.pem"   certs/privkey.pem
elif [ -f "${SRC}/certificate.crt" ] && [ -f "${SRC}/private.key" ]; then
  if [ -f "${SRC}/ca_bundle.crt" ]; then
    cat "${SRC}/certificate.crt" "${SRC}/ca_bundle.crt" > certs/fullchain.pem
  else
    cp "${SRC}/certificate.crt" certs/fullchain.pem
  fi
  cp "${SRC}/private.key" certs/privkey.pem
else
  echo "Потрібні файли в ${SRC}:"
  echo "  certificate.crt + ca_bundle.crt + private.key"
  echo "  або fullchain.pem + privkey.pem"
  exit 1
fi

chmod 644 certs/fullchain.pem
chmod 600 certs/privkey.pem

echo "==> Certificate subject / SAN / dates:"
openssl x509 -in certs/fullchain.pem -noout -subject -issuer -dates -ext subjectAltName 2>/dev/null || \
  openssl x509 -in certs/fullchain.pem -noout -subject -issuer -dates

CN=$(openssl x509 -in certs/fullchain.pem -noout -subject 2>/dev/null | sed -n 's/.*CN *= *//p' | head -1)
echo ""
echo "==> Рекомендовано в .env:"
echo "  TLS_CN=${CN:-s.ks.tv}"
echo "  APP_PUBLIC_URL=https://${CN:-s.ks.tv}:85"
echo "  SESSION_SECURE=1"

if docker compose ps 2>/dev/null | grep -qi oncall_nginx; then
  docker compose restart nginx
  echo "==> nginx restarted"
else
  echo "==> Підніміть стек: docker compose up -d"
fi

echo ""
echo "Перевірка (без -k):"
echo "  curl -sS -m 5 -o /dev/null -w '%{http_code}\\n' https://${CN:-s.ks.tv}:85/"
echo "  curl -sS -m 5 -X POST 'https://${CN:-s.ks.tv}:85/api/webhooks/slack' \\"
echo "    -H 'Content-Type: application/x-www-form-urlencoded' \\"
echo "    -d 'command=/brb&text=18:30&user_name=test&user_id=U0TEST'"
