# OnCall grok-1.0.0 — порт 85 + Let's Encrypt

| Порт хоста | Призначення |
|---|---|
| **85** | HTTPS UI/API (`https://www.s.ks.tv:85/`) |
| **80** | HTTP + ACME challenge Let's Encrypt |

## Деплой

```bash
cd /opt/oncall-app-5
git pull origin grok-1.0.0
mkdir -p data certs certbot/www certbot/conf /var/log/oncall-app-5
chmod +x scripts/*.sh
docker compose up -d --build
```

## Let's Encrypt (одноразово)

1. DNS `www.s.ks.tv` → IP сервера  
2. Порт **80** відкритий з інтернету  
3. Якщо на хості вже зайнятий :80 — зупиніть конфліктний сервіс на час випуску або використайте DNS-01  

```bash
export LETSENCRYPT_EMAIL=you@company.com
export TLS_CN=www.s.ks.tv
./scripts/issue-letsencrypt.sh
```

Скрипт кладе `fullchain.pem` + `privkey.pem` у `./certs/` і робить `docker compose restart nginx`.

Оновлення (cron раз на 2 місяці):

```bash
cd /opt/oncall-app-5 && ./scripts/issue-letsencrypt.sh
```

## Slack `/brb`

```text
https://www.s.ks.tv:85/api/webhooks/slack
```

## Перевірка

```bash
curl -sS -m 5 -X POST 'https://www.s.ks.tv:85/api/webhooks/slack' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'command=/brb&text=14:00&user_name=test&user_id=U0TEST'
```
