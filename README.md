# OnCall System

Система обліку чергувань (on-call), відсутностей, **звернень (інцидентів)** і **дейлі-задач** для DevOps / SRE команди.

Перший етап розробки (календар, черги, адмінка, коментарі, webhook-stub, Slack/Telegram/Jira hooks) — **завершено**.

| | |
|---|---|
| **Мова** | Go 1.21+ (CGO + SQLite) |
| **UI** | Static HTML/JS (`static/index.html`, `static/admin.html`) |
| **БД** | SQLite (`oncall.db`, WAL) |
| **Доставка** | Docker Compose + Nginx (порт **84**) |
| **Гілка** | `grok` |

---

## Можливості

### Публічний / користувацький інтерфейс (`/`)
- Календар місяця: чергування (основний / дублюючий), відсутності, маркери задач і звернень
- Колонки «Звернення сьогодні» та «Задачі з дейлі на сьогодні» по виконавцях
- Модальне вікно дня: задачі, звернення, коментарі, зміна статусів
- Картка особи (чергування, відсутності, задачі, звернення)
- Заявка на відсутність (модерація адміном)
- **Гостьове створення звернення** без авторизації
- Коментарі до задач і звернень (у т.ч. для неавторизованих у модалці дня)
- Пріоритет відсутностей: **лікарняний > відпустка > вихідний**; при накладенні відпустки на day-off — day-off відхиляється

### Адмін-панель (`/admin.html`)
- **Менеджмент**
  - Черги задач (лічильники за статусами / виконавцями / overdue)
  - Усі задачі: фільтри, inline-редагування, кілька виконавців (`task_assignees`), модальне редагування, видалення
  - Усі звернення: призначення виконавця, статуси, пріоритет, коментарі, **конвертація в задачу** (з копією коментарів, без дублів), архів
  - Заявки на відсутність (затвердження / відхилення)
- **Налаштування**
  - Користувачі, ролі команди, типи відсутностей
  - Логи (фільтр: Auth / Incidents / Tasks / Absence / Admin)
  - База й таблиці (список, схема, вміст)
  - Трафік і логіни (фільтри час / користувач / дія, реальна IP)
  - Read-only SQL (після unlock паролем)

### Інтеграції (підготовлено)
- **Webhook** прийому звернень (`POST /api/webhooks/incidents`) → incident + опційно daily_task  
  Увімкнення: `ENABLE_INCIDENT_WEBHOOK=1`
- **Slack** — командний Incoming Webhook + Bot Token (особисті DM)
- **Telegram** — дзеркало сповіщень
- **Jira** — outbound sync статусу/коментаря за `external_id` (ключ тікета)

### Службове
- Audit-лог у SQLite + дзеркало у `/var/log/oncall-app/app.log`
- Дедуплікація задач, створених зі звернення `[зі звернення #N]`
- Конвертовані звернення **не** показуються в робочих екранах (лише в архіві адміна)
- Client IP: `X-Forwarded-For` / `X-Real-IP` / `RemoteAddr`

---

## Структура проєкту

```
oncall-system/
├── main.go                 # initDB, міграції, маршрути, logAudit, seed
├── handlers_client.go      # login, data, absences, incidents, daily-tasks, comments
├── handlers_admin.go       # users, roles, requests, tasks admin, logs, DB tools, queues
├── handlers_webhook.go     # POST /api/webhooks/incidents (+ health)
├── handlers_notify.go      # Slack / Telegram helpers
├── handlers_jira.go        # outbound Jira transitions/comments
├── go.mod / go.sum
├── Dockerfile              # multi-stage: golang:1.22-alpine → alpine
├── docker-compose.yml      # app (8084) + nginx (:84)
├── nginx.conf              # static + /api/ proxy
├── .dockerignore           # виключає stub-файли (handlers.go, main_*.go, …)
├── .env                    # локальні секрети (не в git)
├── data/                   # volume SQLite (oncall.db)
├── static/
│   ├── index.html          # календар / дошки / модалки
│   └── admin.html          # адмін-панель
└── README.md
```

> У репозиторії можуть лишатися тимчасові артефакти (`main_*.go`, `index.fixed.html` тощо) — вони **не** потрапляють у Docker-збірку завдяки `.dockerignore`.

---

## Залежності

### Runtime (Go)
| Пакет | Призначення |
|-------|-------------|
| `github.com/mattn/go-sqlite3` v1.14.22 | SQLite через CGO |
| stdlib: `net/http`, `database/sql`, `encoding/json`, … | HTTP API, JSON |

**Потрібен CGO** (`CGO_ENABLED=1`) і бібліотеки `gcc` / `musl-dev` (у Dockerfile вже є).

### Інфраструктура
| Компонент | Роль |
|-----------|------|
| **Docker** + **Compose** | Збірка і запуск |
| **Nginx (alpine)** | Статика + reverse proxy `/api/` → `oncall_app_4:8084` |
| **SQLite** | Єдина БД (`DB_PATH`) |

Зовнішні сервіси (опційно): Slack, Telegram Bot API, Atlassian Jira Cloud/Server.

---

## Швидкий старт

### Docker (рекомендовано)

```bash
git clone https://github.com/OCherep/oncall-system.git
cd oncall-system
git checkout grok

# за потреби відредагуйте .env
docker compose up -d --build

# UI
# http://<host>:84/          — календар
# http://<host>:84/admin.html — адмінка
```

Логи:

```bash
docker logs -f oncall_app_4
tail -f /var/log/oncall-app/app.log
tail -f /var/log/oncall-app/nginx_access.log
```

Оновлення з гілки `grok`:

```bash
git pull origin grok
docker compose up -d --build
# static монтується з ./static — після pull достатньо hard-refresh у браузері
```

### Локально (без Docker)

```bash
# потрібні gcc + sqlite-dev
export CGO_ENABLED=1
export PORT=8084
export DB_PATH=./data/oncall.db
go mod tidy
go run .
# → http://localhost:8084
```

---

## Змінні оточення

| Змінна | За замовч. | Опис |
|--------|------------|------|
| `PORT` | `8084` | Порт HTTP-сервера додатку |
| `DB_PATH` | `/app/data/oncall.db` | Шлях до SQLite |
| `DB_ADMIN_PASSWORD` | `db-admin-change-me` | Unlock read-only SQL / DB tools |
| `AUDIT_LOG_PATH` | `/var/log/oncall-app/app.log` | Файл-дзеркало audit |
| `ENABLE_INCIDENT_WEBHOOK` | `0` | `1` — увімкнути webhook |
| `WEBHOOK_SECRET` | — | Bearer / `X-Webhook-Secret` |
| `SLACK_WEBHOOK_URL` | — | Incoming Webhook командного каналу |
| `SLACK_BOT_TOKEN` | — | Bot token для DM |
| `SLACK_TEAM_CHANNEL` | — | Канал команди |
| `NOTIFY_ON_INCIDENT` | `1` | Сповіщати про нові звернення |
| `TELEGRAM_BOT_TOKEN` | — | Telegram bot |
| `TELEGRAM_CHAT_ID` | — | Chat / group id |
| `JIRA_ENABLED` | `0` | Outbound Jira |
| `JIRA_BASE_URL` | — | `https://xxx.atlassian.net` |
| `JIRA_EMAIL` | — | Cloud basic auth |
| `JIRA_API_TOKEN` | — | API token |
| `JIRA_STATUS_MAP` | — | JSON map внутрішній статус → transition id |

---

## API (коротко)

### Клієнт
| Метод | Шлях | Опис |
|-------|------|------|
| `POST` | `/api/login` | Авторизація |
| `GET` | `/api/data?year=&month=` | Календарні дані місяця |
| `POST` | `/api/request-absence` | Заявка на відсутність |
| `GET/POST/PUT/DELETE` | `/api/incidents` | Звернення (гість може POST) |
| `GET/POST/PUT/DELETE` | `/api/daily-tasks` | Дейлі-задачі |
| `GET/POST` | `/api/comments` | Коментарі (`entity_type` + `entity_id`) |

### Адмін
| Шлях | Опис |
|------|------|
| `/api/admin/users` | CRUD користувачів |
| `/api/admin/roles`, `/team-roles` | Ролі команди |
| `/api/admin/absence-types` | Типи відсутностей |
| `/api/admin/requests` | Модерація заявок |
| `/api/admin/tasks` | Адмін задач (PUT з assignees) |
| `/api/admin/queues` | Лічильники черг |
| `/api/admin/logs`, `/project/audit-logs` | Audit |
| `/api/admin/project/app-logs?app=` | Логи з фільтром категорії |
| `/api/admin/project/db-stats` | Список таблиць |
| `/api/admin/project/table?name=` | Схема + рядки |
| `/api/admin/project/query` | Read-only SQL |
| `/api/admin/project/unlock` | Розблокування DB tools |
| `/api/admin/regenerate-shifts` | Перегенерація чергувань |

### Webhooks
| Метод | Шлях | Опис |
|-------|------|------|
| `POST` | `/api/webhooks/incidents` | Створення звернення (+ опційно задача) |
| `GET` | `/api/webhooks/health` | Healthcheck webhook |

Приклад webhook:

```bash
curl -X POST http://host:84/api/webhooks/incidents \
  -H "Content-Type: application/json" \
  -H "X-Webhook-Secret: $WEBHOOK_SECRET" \
  -d '{
    "description": "Алерт з моніторингу",
    "priority": "Високий",
    "source": "bot",
    "external_id": "OPS-123",
    "create_task": true
  }'
```

---

## Модель даних (основні таблиці)

| Таблиця | Призначення |
|---------|-------------|
| `users` | Користувачі, ролі, on-call flag |
| `team_roles` | Довідник ролей (DevOps, TL, PM, …) |
| `absence_types` | Типи відсутностей + пріоритет |
| `shifts` | Чергування по датах |
| `absences` | Заявки / затверджені відсутності |
| `incidents` | Звернення; `converted_to_task_id`, `external_id` |
| `daily_tasks` | Дейлі-задачі; статус, пріоритет, due, responsible |
| `task_assignees` | Кілька виконавців + час на кожного |
| `comments` / `incident_comments` | Коментарі (у т.ч. системні) |
| `audit_logs` | Дії користувачів |
| `app_logs` | Додаткові сервісні логи (опційно) |

Статуси **задач**: Нова → У роботі → На паузі → До перевірки → Виконана / Перевідкрита / Архів  

Статуси **звернень**: Нове → В роботі → На паузі → Вирішено → Архів  

---

## Облікові записи за замовчуванням

Після першого старту (seed):

| Логін | Пароль | Роль |
|-------|--------|------|
| `admin` | `admin` | адміністратор |

Інші користувачі створюються в адмінці (**Налаштування → Користувачі**).  
**Змініть пароль admin** після першого входу.

---

## Типові операції на EC2

```bash
cd /opt/oncall-app-4/oncall-system   # або ваш шлях
git pull origin grok
docker compose down
docker compose up -d --build

# перевірка API
curl -sS -m 5 "http://127.0.0.1:8084/api/data?year=2026&month=8" | head -c 200
```

Volume:
- `./data` → SQLite
- `/var/log/oncall-app` → `app.log`, nginx access/error

---

## Відомі обмеження (етап 1)

- Авторизація — session-less (роль/актор у тілі запиту; немає JWT)
- Двосторонній Jira sync — outbound готовий, inbound через webhook
- Gorilla Mux / окремий auth-шар — не впроваджено (net/http)
- Повний UI обліку часу по кожному з кількох виконавців — базовий (хвилини в `task_assignees`)

---

## Ліцензія

Див. файл `LICENSE` у корені репозиторію.
