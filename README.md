# oncall-system

Система управління on-call чергуваннями, відсутностями та звітами про звернення.

## Гілка `grok`

Об'єднана версія на базі `good` + модуль інцидентів з `actualy-last`.

### Основні можливості
- Персистентний розклад чергувань (`shifts`)
- Заявки на відсутність з модерацією
- Звіти про звернення (інциденти) з тривалістю
- Реальне audit- та app-логування
- Тригери відстеження змін у таблицях
- Адмін-панель (CRUD користувачів, ролей, типів відсутностей)
- Моніторинг БД + read-only SQL-консоль

### Швидкий старт

```bash
# Локально
go mod tidy
go run main.go
# → http://localhost:8083

# Docker
docker compose up -d --build
# → http://localhost:83
```

### Облікові записи за замовчуванням
- `admin` / `admin` (адміністратор)
- `dev1` / `1234` (користувач on-call)
- `pm` / `1234`

### API (коротко)
- `POST /api/login`
- `GET  /api/data?year=&month=`
- `POST /api/request-absence`
- `POST /api/incidents`
- Адмін: `/api/admin/users`, `/team-roles`, `/absence-types`, `/requests`
- Моніторинг: `/api/admin/project/{db-stats,query,audit-logs,app-logs}`
