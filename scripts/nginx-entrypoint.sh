#!/bin/sh
# OnCall nginx entrypoint: ensure fullchain.pem + privkey.pem exist.
# Accepts: already-PEM, ZeroSSL flat, ZeroSSL in certs/<domain>/, or generates self-signed.
set -e
CERT_DIR=/etc/nginx/certs
mkdir -p "$CERT_DIR" /var/www/certbot
CN="${TLS_CN:-s.ks.tv}"

assemble_zerossl() {
  src="$1"
  if [ -f "${src}/certificate.crt" ] && [ -f "${src}/private.key" ]; then
    if [ -f "${src}/ca_bundle.crt" ]; then
      cat "${src}/certificate.crt" "${src}/ca_bundle.crt" > "${CERT_DIR}/fullchain.pem"
    else
      cp "${src}/certificate.crt" "${CERT_DIR}/fullchain.pem"
    fi
    cp "${src}/private.key" "${CERT_DIR}/privkey.pem"
    echo "[nginx] TLS: assembled ZeroSSL PEM from ${src}"
    return 0
  fi
  return 1
}

if [ -f "$CERT_DIR/fullchain.pem" ] && [ -f "$CERT_DIR/privkey.pem" ]; then
  echo "[nginx] TLS: using ${CERT_DIR}/fullchain.pem + privkey.pem"
elif assemble_zerossl "$CERT_DIR"; then
  :
elif assemble_zerossl "$CERT_DIR/${CN}"; then
  :
elif assemble_zerossl "$CERT_DIR/s.ks.tv"; then
  :
else
  # search one level of subdirs for certificate.crt
  found=""
  for d in "$CERT_DIR"/*/; do
    [ -d "$d" ] || continue
    if assemble_zerossl "${d%/}"; then
      found=1
      break
    fi
  done
  if [ -z "$found" ]; then
    echo "[nginx] TLS: no usable certs — generating self-signed for CN=${CN}"
    if ! command -v openssl >/dev/null 2>&1; then
      apk add --no-cache openssl >/dev/null
    fi
    openssl req -x509 -nodes -days 825 -newkey rsa:2048 \
      -keyout "$CERT_DIR/privkey.pem" \
      -out "$CERT_DIR/fullchain.pem" \
      -subj "/CN=${CN}" \
      -addext "subjectAltName=DNS:${CN},DNS:localhost,IP:127.0.0.1" 2>/dev/null \
    || openssl req -x509 -nodes -days 825 -newkey rsa:2048 \
      -keyout "$CERT_DIR/privkey.pem" \
      -out "$CERT_DIR/fullchain.pem" \
      -subj "/CN=${CN}"
  fi
fi

chmod 644 "$CERT_DIR/fullchain.pem" 2>/dev/null || true
chmod 640 "$CERT_DIR/privkey.pem" 2>/dev/null || true

nginx -t
exec nginx -g 'daemon off;'
