# Друге оточення (гілка grok-1.0.0) + HTTPS

| | Prod (84) | Це оточення |
|---|---|---|
| **HTTPS UI/API** | за вашим проксі | **https://host:85/** |
| HTTP fallback | — | **http://host:86/** |
| App (внутрішній) | :8084 | :8085 |
| Containers | oncall_app_4 / nginx_4 | oncall_app_5 / nginx_5 |

## Підняти

```bash
cd /opt/oncall-app-5
git pull origin grok-1.0.0
mkdir -p data certs /var/log/oncall-app-5
chmod +x scripts/nginx-entrypoint.sh

# Опційно валідний сертифікат:
# cp /path/fullchain.pem /path/privkey.pem ./certs/
# інакше entrypoint згенерує self-signed (CN=www.s.ks.tv)

docker compose up -d --build
```

## Slack /brb Request URL

```text
https://www.s.ks.tv:85/api/webhooks/slack
```

Перевірка:

```bash
curl -sk -m 5 -X POST 'https://127.0.0.1:85/api/webhooks/slack' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'command=/brb&text=01:30&user_name=test&user_id=U0TEST'
```

Self-signed Slack може відхилити — для production покладіть валідний PEM у `./certs/`.

Відкрийте TCP 85 у firewall. SESSION_SECURE=1 у compose за замовчуванням.
