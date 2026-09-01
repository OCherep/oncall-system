# OnCall `grok-1.0.0` — HTTPS :85 + Let's Encrypt

## Порти

| Хост | Контейнер | Призначення |
|------|-----------|-------------|
| **85** | 443 | **HTTPS** UI, API, Slack webhooks |
| **80** | 80 | HTTP + **ACME** (випуск/оновлення LE) |

UI: `https://www.s.ks.tv:85/`  
Slack: `https://www.s.ks.tv:85/api/webhooks/slack`

Self-signed Slack **не приймає** — потрібен Let's Encrypt (або інший довірений ланцюжок).

---

## 1. Підняти стек

```bash
cd /opt/oncall-app-5
git fetch origin && git checkout grok-1.0.0
git pull origin grok-1.0.0

mkdir -p data certs certbot/www certbot/conf /var/log/oncall-app-5
chmod +x scripts/*.sh

# Якщо :80 зайнятий іншим процесом — див. розділ «Конфлікт :80»
docker compose up -d --build
```

Перевірка контейнерів:

```bash
docker compose ps
ss -lntp | grep -E ':80|:85'
```

---

## 2. Випустити Let's Encrypt

### Передумови
1. DNS: `www.s.ks.tv` → публічний IP **цього** сервера  
2. З інтернету відкритий **TCP/80** (security group / firewall)  
3. На :80 відповідає **цей** nginx (oncall_nginx_5)

```bash
export TLS_CN=www.s.ks.tv
export LETSENCRYPT_EMAIL=you@company.com

# опційно спочатку staging (не довіряється браузерами, для перевірки шляху)
# export LETSENCRYPT_STAGING=1

./scripts/setup-tls.sh le
# те саме: ./scripts/issue-letsencrypt.sh
```

Скрипт:
- перевіряє, що nginx running і ACME webroot віддає 200;
- `certbot certonly --webroot`;
- копіює PEM у `./certs/`;
- `docker compose restart nginx`.

### Перевірка (без `-k`)

```bash
curl -sS -m 5 -o /dev/null -w "%{http_code}\n" https://www.s.ks.tv:85/
curl -sS -m 5 -X POST 'https://www.s.ks.tv:85/api/webhooks/slack' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'command=/brb&text=18:30&user_name=test&user_id=U0TEST'
```

Issuer має бути Let's Encrypt, не «self-signed».

---

## 3. Якщо сертифікат уже є на хості

```bash
export TLS_CN=www.s.ks.tv
# export HOST_LE_LIVE=/etc/letsencrypt/live/www.s.ks.tv
./scripts/setup-tls.sh copy-host
```

---

## 4. Конфлікт порту 80

```bash
ss -lntp | grep ':80'
```

- **Інший docker/nginx тримає :80**  
  - тимчасово зупиніть його, виконайте `./scripts/setup-tls.sh le`, знову запустіть; **або**  
  - у чужому nginx додайте:

```nginx
location ^~ /.well-known/acme-challenge/ {
    root /opt/oncall-app-5/certbot/www;
}
```

і випускайте certbot webroot у `/opt/oncall-app-5/certbot/www`, потім `./scripts/setup-tls.sh copy-host` або скопіюйте PEM у `./certs/`.

- **Не можете відкрити :80** — потрібен DNS-01 (окремо) або сертифікат з іншого проксі, що термінує TLS і проксує на `http://127.0.0.1:86` (тоді додайте publish `86:80` у compose).

---

## 5. Оновлення сертифіката (cron)

```bash
# раз на місяць
0 3 1 * * cd /opt/oncall-app-5 && TLS_CN=www.s.ks.tv ./scripts/issue-letsencrypt.sh >>/var/log/oncall-app-5/le-renew.log 2>&1
```

---

## 6. Slack `/brb`

Request URL (після валідного LE):

```text
https://www.s.ks.tv:85/api/webhooks/slack
```

Socket Mode для цієї команди — **OFF**.
