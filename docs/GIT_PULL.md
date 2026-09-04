# Безпечний git pull на EC2

## OnCall (`/opt/ops/oncall`, гілка grok-1.0.0)

```bash
cd /opt/ops/oncall
git status -sb
git stash push -u -m "local-$(date +%F)" -- scripts/nginx-entrypoint.sh scripts/issue-letsencrypt.sh || true
git fetch origin
git pull --ff-only origin grok-1.0.0
# якщо nginx віддає статику з образу — перезібрати:
docker compose up -d --build
```

Адмінка: `https://s.ks.tv:85/admin.html` (не http). Hard refresh.

## Hub (`/opt/ops/hub`, гілка main)

```bash
cd /opt/ops/hub
git status -sb
git stash push -m "local-upsh" -- ops/up.sh
git pull --ff-only origin main
# розкласти модуль сертифікатів
mkdir -p /opt/ops/certs/hooks
cp -a /opt/ops/hub/ops/certs/. /opt/ops/certs/
# подивитись stash
git stash list
# відкинути локальний up.sh, якщо репо новіший:
# git stash drop
# або повернути свої правки поверх:
# git stash pop
```

## Типові конфлікти

| Файл | Чому |
|------|------|
| `ops/up.sh` | ручні правки на хості |
| `scripts/nginx-entrypoint.sh` | chmod / локальний TLS |
| `scripts/issue-letsencrypt.sh` | те саме |

Не коміть секрети (`.env`, ключі) у stash з `-u`, якщо не треба.
