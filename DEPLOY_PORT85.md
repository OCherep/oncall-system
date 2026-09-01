# OnCall `grok-1.0.0` — HTTPS :85 + Let's Encrypt

## Порти

| Хост | Контейнер | Призначення |
|------|-----------|-------------|
| **85** | 443 | HTTPS — UI, API, Slack `/brb`, webhooks |
| **80** | 80 | HTTP — ACME challenge Let's Encrypt |

Slack **вимагає** валідний TLS. Self-signed → `dispatch_unknown_error` / timeout.

---

## 1. Підняти стек

```bash
cd /opt/oncall-app-5
git fetch origin && git checkout grok-1.0.0
git pull origin grok-1.0.0

# якщо git скаржиться на локальні файли:
# git checkout -- scripts/nginx-entrypoint.sh

mkdir -p data certs certbot/www certbot/conf /var/log/oncall-app-5
chmod +x scripts/*.sh
docker compose up -d --build
```

Перевірка HTTP ACME (має бути **200**):

```bash
echo test > certbot/www/ping
curl -sS -o /dev/null -w "%{http_code}\n" http://127.0.0.1/.well-known/acme-challenge/ping
rm certbot/www/ping
```

Якщо не 200 — на :80 відповідає **інший** процес (`ss -lntp | grep :80`).

---

## 2. Випуск Let's Encrypt

**Умови:** DNS `www.s.ks.tv` → IP цього сервера; TCP/80 відкритий з інтернету.

```bash
export TLS_CN=www.s.ks.tv
export LETSENCRYPT_EMAIL=you@company.com
# export EXTRA_DOMAINS="s.ks.tv"   # опційно

./scripts/setup-tls.sh le
# або: ./scripts/issue-letsencrypt.sh
```

Скрипт перевіряє webroot → certbot → копіює PEM у `./certs/` → `restart nginx`.

**Перевірка без `-k`:**

```bash
curl -sS -m 5 -o /dev/null -w "%{http_code}\n" https://www.s.ks.tv:85/
echo | openssl s_client -connect www.s.ks.tv:85 -servername www.s.ks.tv 2>/dev/null \
  | openssl x509 -noout -issuer -subject -dates
```

Issuer: `Let's Encrypt`, не self-signed.

---

## 3. Сертифікат уже є на хості

```bash
export TLS_CN=www.s.ks.tv
export HOST_LE_LIVE=/etc/letsencrypt/live/www.s.ks.tv
./scripts/setup-tls.sh copy-host
```

---

## 4. Конфлікт порту 80

```bash
ss -lntp | grep ':80'
```

- Зупиніть чужий сервіс на час `./scripts/setup-tls.sh le`, **або**
- У чужому nginx:

```nginx
location ^~ /.well-known/acme-challenge/ {
    root /opt/oncall-app-5/certbot/www;
}
```

потім знову `./scripts/setup-tls.sh le` (certbot пише в `./certbot/www`).

---

## 5. Slack `/brb`

```text
https://www.s.ks.tv:85/api/webhooks/slack
```

Socket Mode для slash command — **OFF**.

```bash
curl -sS -m 5 -X POST 'https://www.s.ks.tv:85/api/webhooks/slack' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'command=/brb&text=18:30&user_name=test&user_id=U0TEST'
```

---

## 6. Оновлення (cron)

```bash
0 3 1 * * cd /opt/oncall-app-5 && TLS_CN=www.s.ks.tv LETSENCRYPT_EMAIL=you@company.com ./scripts/issue-letsencrypt.sh >>/var/log/oncall-app-5/le-renew.log 2>&1
```
