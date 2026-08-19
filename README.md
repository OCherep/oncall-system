# oncall-system (гілка `grok`)

Об'єднана версія: **good** (база) + модуль інцидентів з **actualy-last**.

## Структура backend

```
main.go              — types, initDB (incidents table), triggers, routing
handlers_client.go   — login, /api/data, absences, incidents
handlers_admin.go    — CRUD users/roles/types/requests + monitoring
```

## Швидкий старт

```bash
git clone https://github.com/OCherep/oncall-system.git
cd oncall-system
git checkout grok

# Підтягнути повний frontend з good (UI вже підтримує incidents)
git checkout good -- static/

go mod tidy
go run .
# → http://localhost:8083

# Docker
docker compose up -d --build
# → http://localhost:83
```

## Облікові записи
- `admin` / `admin`
- `dev1` / `1234`
- `pm` / `1234`

## API
- `POST /api/login`
- `GET  /api/data?year=&month=` (shifts, absences, **incidents**, stats)
- `POST /api/request-absence`
- `POST /api/incidents`
- Admin CRUD: `/api/admin/users`, `/team-roles`, `/absence-types`, `/requests`
- Monitoring: `/api/admin/project/{db-stats,query,audit-logs,app-logs}`

## Що включено
- Персистентні shifts, реальні audit/app logs, table triggers (з good)
- Таблиця incidents + API + stats.incident_minutes (з actualy-last)
- Frontend з good уже має повний UI інцидентів
