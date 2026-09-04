# OnCall System

Система обліку чергувань (on-call), відсутностей, **звернень (інцидентів)** і **дейлі-задач** для DevOps / SRE команди.

> **Part of [DevOps Hub](https://github.com/OCherep/devops-hub)** — єдина точка входу разом із [KSTV Tech Radar](https://github.com/OCherep/kstv-tech_radar).

Перший етап розробки (календар, черги, адмінка, коментарі, webhook-stub, Slack/Telegram/Jira hooks) — **завершено**.

| | |
|---|---|
| **Мова** | Go 1.21+ (CGO + SQLite) |
| **UI** | Static HTML/JS (`static/index.html`, `static/admin.html`) |
| **БД** | SQLite (`oncall.db`, WAL) |
| **Доставка** | Docker Compose + Nginx (порт **84**) |
| **Гілка** | `grok-1.0.0` / `grok` |

---

## Паспорт API

Повний опис REST-ендпоінтів, правил **on-grid / off-grid** маршрутизації звернень, webhook і Jira:

→ **[docs/API.md](docs/API.md)**

Документ потрібно **оновлювати** при зміні маршрутів або логіки routing.

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

## Пов’язані проєкти (DevOps Hub)

| Проєкт | Роль |
|--------|------|
| [DevOps Hub](https://github.com/OCherep/devops-hub) | Центральний портал інструментів |
| [KSTV Tech Radar](https://github.com/OCherep/kstv-tech_radar) | Tech portfolio платформи |

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
├── .dockerignore
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
git checkout grok-1.0.0

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

### Локально (без Docker)

```bash
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

## Облікові записи за замовчуванням

| Логін | Пароль | Роль |
|-------|--------|------|
| `admin` | `admin` | адміністратор |

**Змініть пароль admin** після першого входу.

---

## Ліцензія

Див. файл `LICENSE` у корені репозиторію.
