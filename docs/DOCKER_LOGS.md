# Моніторинг логів Docker (ops + OnCall)

```bash
docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'

# хвіст
docker logs --tail=80 -f oncall_app_5
docker logs --tail=80 -f oncall_nginx_5
docker logs --tail=80 -f ops_edge
docker logs --tail=40 ops_postgres
docker logs --tail=40 ops_certs_ui

# помилки nginx OnCall
tail -f /var/log/oncall-app-5/nginx_error.log
tail -f /var/log/oncall-app-5/app.log

# SQLite lock
docker logs oncall_app_5 2>&1 | grep -iE 'lock|busy|productivity'
```

Фільтр за хвилину: `docker logs --since 2m oncall_app_5`.
