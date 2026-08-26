# Друге оточення (гілка grok-1.0.0)

| | Prod (main / grok) | Це оточення (grok-1.0.0) |
|---|---|---|
| UI (Nginx) | **:84** | **:85** |
| App | **:8084** | **:8085** |
| Containers | `oncall_app_4`, `oncall_nginx_4` | `oncall_app_5`, `oncall_nginx_5` |
| Логи | `/var/log/oncall-app` | `/var/log/oncall-app-5` |
| Data | `./data` (окрема копія каталогу!) | `./data` |

## Підняти поряд із інстансом на 84

```bash
# окремий каталог, щоб не змішувати data/ і compose
mkdir -p /opt/oncall-app-5
cd /opt/oncall-app-5
git clone -b grok-1.0.0 https://github.com/OCherep/oncall-system.git .
# або: git checkout grok-1.0.0

mkdir -p data /var/log/oncall-app-5
docker compose up -d --build

# UI:  http://<host>:85/
# API: http://<host>:85/api/  → app :8085
```

Не запускайте цей compose у тому ж каталозі, що й інстанс на порту 84 — різні `container_name` і порти, але спільний `./data` перезапише БД.
