#!/bin/sh
set -e
CERT_DIR=/etc/nginx/certs
mkdir -p "$CERT_DIR"

if [ ! -f "$CERT_DIR/privkey.pem" ] || [ ! -f "$CERT_DIR/fullchain.pem" ]; then
  echo "[nginx] generating self-signed TLS cert (replace with real certs in ./certs)"
  if ! command -v openssl >/dev/null 2>&1; then
    apk add --no-cache openssl >/dev/null
  fi
  CN="${TLS_CN:-www.s.ks.tv}"
  openssl req -x509 -nodes -days 825 -newkey rsa:2048 \
    -keyout "$CERT_DIR/privkey.pem" \
    -out "$CERT_DIR/fullchain.pem" \
    -subj "/CN=${CN}" \
    -addext "subjectAltName=DNS:${CN},DNS:localhost,IP:127.0.0.1"
  chmod 640 "$CERT_DIR/privkey.pem" "$CERT_DIR/fullchain.pem" || true
fi

nginx -t
exec nginx -g 'daemon off;'
