# OnCall System — паспорт API

> **Гілка:** `grok-1.0.0`  
> **Оновлювати** цей файл при додаванні/зміні маршрутів, тіл запитів, правил маршрутизації звернень або env.  
> Базовий URL застосунку (приклади): `http://host:8085` (app) або через nginx `http://host:85`.

---

## 1. Загальні положення

| Тема | Опис |
|------|------|
| Формат | JSON (`Content-Type: application/json`), UTF-8 |
| Час / дати | Дати календаря: `YYYY-MM-DD`. Timestamps SQLite / RFC3339 |
| CORS | Не налаштований окремо; UI same-origin |
| IP allowlist | Усі `/api/*` обгорнуті в `withIPAllow`. Порожній довідник `allowed_ips` → доступ усім; інакше лише CIDR/IP (+ loopback) |
| Security headers | `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `X-XSS-Protection` на частині маршрутів (login, session, settings, daily-board, jira import) |
| Сесія | Cookie `oncall_session` (HttpOnly), TTL ~14 год, sliding. Альтернатива: `Authorization: Bearer <token>` або `X-Session-Token` |
| Soft auth | `SOFT_AUTH=1` (default): admin API без жорсткого `requireAuth` (перехідний режим). Планується `SOFT_AUTH=0` |

### Коди відповіді (типові)

| Код | Значення |
|-----|----------|
| 200 | OK |
| 201 | Created |
| 400 | Bad request / валідація |
| 401 | Unauthorized (логін, webhook, session) |
| 403 | Forbidden (роль, IP, DB unlock) |
| 404 | Not found / webhook disabled |
| 405 | Method not allowed |
| 429 | Login rate limit |
| 500 / 502 | Server / upstream (Jira) |

---

## 2. Автентифікація та сесія

### `POST /api/login`

Тіло:
```json
{ "username": "admin", "password": "…" }
```

Успіх (200): користувач + сесія.
```json
{
  "id": 1,
  "username": "admin",
  "name": "Admin",
  "role": "admin",
  "team_role_id": null,
  "team_role": "",
  "is_oncall": false,
  "session_token": "<hex>",
  "session_expires": "2026-09-01T09:00:00Z"
}
```

Cookies: `oncall_session` (HttpOnly), legacy `oncall_user` / `oncall_name` / `oncall_role`.

Помилки: 401, 429 (ліміт ~20 спроб / 15 хв з IP).

### `POST /api/logout`

Інвалідація сесії + очищення cookies. Відповідь: `{ "status": "ok" }`.

### `GET /api/session/me`

Поточна сесія. 200:
```json
{ "id": 1, "username": "admin", "name": "Admin", "role": "admin" }
```
401 — немає/прострочена сесія.

---

## 3. Публічний режим on-grid

### `GET /api/on-grid`

Без авторизації (лише IP allowlist). Поточний режим розподілу:

```json
{
  "on_grid": true,
  "mode": "on-grid",
  "label": "робочий час",
  "on_grid_start": "09:00",
  "on_grid_end": "18:00",
  "on_grid_timezone": "Europe/Kyiv",
  "on_grid_weekdays": "1,2,3,4,5",
  "_on_grid_now": "1"
}
```

Також вкладено в `GET /api/data` як поле `on_grid`.

## 3b. Календар і дані UI

### `GET /api/data`

Query: `year`, `month`, опційно `viewer`.

Повертає зведення місяця: користувачі on-call, shifts, absences, incidents, daily_tasks, пріоритети, тощо (для `index.html`).

### `POST /api/request-absence`

Тіло: `{ "user_name", "type", "start_date", "end_date" }`.  
Створює заявку зі статусом Pending (модерація адміном).

---

## 4. Звернення (incidents)

### `GET /api/incidents`

Query:

| Параметр | Опис |
|----------|------|
| `limit` | Макс. кількість (напр. 200) |
| `status` | Фільтр статусу |
| `user` | Фільтр `user_name` |
| `date` | (якщо підтримується клієнтом) |

Масив об’єктів incident (id, user_name, date, description, status, priority, source, duration_minutes, total_minutes, fact_minutes, reporter_*, external_id, converted_to_task_id, …).

### `POST /api/incidents`

Створення звернення (користувач / guest / admin).

Тіло (основне):
```json
{
  "user_name": "",
  "date": "2026-08-31",
  "type": "…",
  "duration_minutes": 15,
  "description": "текст",
  "priority": "Звичайний",
  "source": "guest",
  "role": "guest",
  "created_by": "Ім'я",
  "reported_for": "",
  "reporter_name": "Ім'я",
  "reporter_email": "a@b.c",
  "reporter_slack": ""
}
```

- Guest: обов’язкові `reporter_email`, ім’я; `source=guest`, `user_name` порожній до routing.  
- Admin (`source=manual`): може одразу вказати виконавця.  
- Залогінений user: `source=self` (routing скидає self-assign і застосовує правила).

**Після валідації** викликається `applyIncidentRouting` (див. §8).

Відповідь 201:
```json
{
  "status": "ok",
  "id": 42,
  "as_task": false,
  "assignee": "",
  "notified": true,
  "on_grid": true
}
```
Якщо дата в майбутньому — можлива авто-конвертація в задачу (`as_task`, `task_id`).

### `PUT /api/incidents`

Тіло: `{ "id", "role", "actor", … }`.

| Поле | Дія |
|------|-----|
| `status` | Зміна статусу (+ system comment, опційно Jira sync за `external_id`) |
| `user_name` | Призначення виконавця (**role=admin**) |
| `priority` | Зміна пріоритету |
| `convert_to_task: true` | Конвертація в `daily_task`, копія коментарів, `converted_to_task_id` |

### `DELETE /api/incidents?id=`

Видалення звернення (і пов’язаних коментарів за логікою хендлера).

---

## 5. Задачі (daily_tasks)

### `GET /api/daily-tasks`

Список задач (фільтри залежать від реалізації клієнтського хендлера).

### `POST /api/daily-tasks`

Створення задачі: `user_name`, `date`, `task_description`, `priority`, `status`, `created_by`, `due_date`, `responsible`, `estimated_minutes`, …

### `PUT /api/daily-tasks`

Оновлення: `id`, `role`, `actor`, `status`, `user_name`, `priority`, `due_date`, …  
Переходи статусів обмежені FSM (`isStatusAllowed`). Non-admin — лише свої задачі.

### `DELETE /api/daily-tasks?id=`

Видалення задачі.

### `GET /api/admin/tasks`

Адмін-список з фільтрами: `user`, `status`, `priority`, `date_from`, `date_to`.  
Включає `assignees` (після закриття rows).

---

## 6. Коментарі та статус-лог

### `GET /api/comments?entity_type=task|incident&entity_id=`

Масив коментарів (author, body, is_system, created_at).

### `POST /api/comments`

```json
{ "entity_type": "task", "entity_id": 1, "author_name": "…", "body": "…" }
```

### `GET /api/task-status-log`

Query: `task_id` | `ids=1,2,3` | `date=YYYY-MM-DD` — історія перебування в статусах і хвилини.

---

## 7. Адмін API

| Метод | Шлях | Опис |
|-------|-----|------|
| GET/POST/PUT/DELETE | `/api/admin/users` | Користувачі (slack_id, email, phone, role, is_oncall) |
| GET/POST/… | `/api/admin/roles`, `/api/admin/team-roles` | Ролі команди |
| GET/POST/… | `/api/admin/absence-types` | Типи відсутностей |
| GET/PUT | `/api/admin/requests` | Заявки на відсутність (approve/reject) |
| GET | `/api/admin/logs`, `/api/admin/project/audit-logs` | Audit |
| GET | `/api/admin/project/app-logs` | App logs |
| GET | `/api/admin/project/db-stats` | Статистика таблиць |
| GET | `/api/admin/project/table` | Інспекція таблиці |
| POST | `/api/admin/project/query` | Read-only SQL (після unlock) |
| POST | `/api/admin/project/unlock` | `{ "password" }` → DB tools |
| POST | `/api/admin/regenerate-shifts` | Перегенерація змін місяця |
| GET | `/api/admin/queues?date=` | Черги: by status/assignee, overdue, incidents by day |
| GET | `/api/admin/badges` | Крапки меню: open incidents, unassigned, overdue, approval |
| GET | `/api/admin/daily-board?date=` | Дашборд дейлі: tasks, incidents, shift, by_status |
| GET/PUT | `/api/admin/settings` | On-grid та інші `app_settings` |
| POST | `/api/admin/jira/import` | Імпорт issue → daily_tasks |
| GET/POST/PUT/DELETE | `/api/admin/allowed-ips` | IP allowlist |

### `GET /api/admin/settings`

```json
{
  "on_grid_start": "09:00",
  "on_grid_end": "18:00",
  "on_grid_timezone": "Europe/Kyiv",
  "on_grid_weekdays": "1,2,3,4,5",
  "_on_grid_now": "1"
}
```

### `PUT /api/admin/settings`

Тіло: будь-які з ключів `on_grid_*`.

### `POST /api/admin/jira/import`

```json
{ "jql": "project = X AND labels = devops AND statusCategory != Done", "max": 50 }
```

Відповідь: `{ "found", "created", "updated", "skipped", "jql" }`.  
Потрібно: `JIRA_ENABLED=1`, `JIRA_BASE_URL`, `JIRA_EMAIL`, `JIRA_API_TOKEN`.  
Upsert за `daily_tasks.external_id` = issue key.

### `GET /api/admin/daily-board?date=YYYY-MM-DD`

```json
{
  "date": "…",
  "primary": "devops4",
  "backup": "devops5",
  "tasks": [ { "id", "user_name", "task_description", "status", "priority", "due_date", "external_id", "source": "jira|oncall|incident", … } ],
  "incidents": [ … ],
  "by_status": { "Нова": 7 },
  "unassigned": 0,
  "jira_linked": 0
}
```

### `GET /api/admin/badges`

```json
{
  "open_incidents": 0,
  "unassigned": 0,
  "incidents_today": 0,
  "approval": 0,
  "overdue": 0,
  "open_tasks": 0,
  "dot_queues": false,
  "dot_incidents": false,
  "dot_tasks": false,
  "dot_approval": false
}
```

---

## 8. Маршрутизація нових звернень (`applyIncidentRouting`)

Викликається на **`POST /api/incidents`** перед INSERT.

### Джерела (`source`)

| source | Поведінка |
|--------|-----------|
| `manual` / `admin` | Якщо `user_name` задано — **не перетирати** (явний розподіл адміном) |
| `guest`, `self`, порожній, `webhook`, `jira` | Застосувати правила on-grid/off-grid (`user_name` спочатку очищається) |

### Час on-grid

З `app_settings`: `on_grid_start`, `on_grid_end`, `on_grid_timezone`, `on_grid_weekdays` (1=Пн … 7=Нд).  
`isOnGridNow()` — поточний час у TZ ∈ [start, end) і день у списку.

### Пріоритети (ранг)

| Ранг | Приклади назв |
|------|----------------|
| 0 | Звичайний, Базова |
| 1 | Підвищений |
| 2 | Високий |
| 3 | Критичний |
| 4 | Терміновий / Термінова |
| 5 | Надкритична |

- **Hot** (критичний+): `priorityRank >= 3`  
- **Off-grid notify**: `priorityRank >= 2` (вище за «підвищений»)

### Матриця

| Режим | Пріоритет | `user_name` | Сповіщення (`notified`) |
|--------|-----------|-------------|-------------------------|
| On-grid | звичайний / підвищений | порожній | так → admin + канал |
| On-grid | критичний / терміновий / надкрит. | primary з `shifts` | так → admin + пара чергових |
| Off-grid | ≥ високий | primary | так |
| Off-grid | нижче високого | порожній | **ні** |

Чергові: `shifts.primary_user` / `backup_user` на `inc.date`.  
System comments: «Режим: on-grid|off-grid», авто-призначення / очікує розподілу.  
Логи сервера: рядки `routing: …`.

### Ручний розподіл після створення

- `PUT /api/incidents` з `{ "id", "user_name", "role": "admin", "actor" }`  
- UI: дашборд дейлі, усі звернення, модалка дня  

---

## 9. Webhooks

### `GET /api/webhooks/health`

Статус webhook (увімкнено / секрет).

### `POST /api/webhooks/incidents`

Умови: `ENABLE_INCIDENT_WEBHOOK=1`, auth через `WEBHOOK_SECRET` (header або query).

Приймає:

1. Плоский JSON (source, external_id, description, priority, …)  
2. Jira-shaped (`webhookEvent`, `issue.key`, `issue.fields`)

Поведінка:

- Upsert incident за `external_id`  
- Опційно `daily_task` з тим самим `external_id`  
- Мапінг статусу Jira → локальний  
- Notify (якщо не вимкнено логікою)

---

## 10. FSM статусів (актуально)

### Задачі (`daily_tasks`)

| Поточний | → далі (user) | → далі (admin) |
|----------|---------------|----------------|
| **Нерозподілена** | (залишити) | Нова, Архів |
| **Нова** | У роботі | У роботі, Архів, Нерозподілена |
| **У роботі** | На паузі, До перевірки | На паузі, До перевірки, Виконана, Архів |
| **На паузі** | У роботі | У роботі, До перевірки, Архів |
| **До перевірки** | Виконана | У роботі, Виконана, Архів |
| **Виконана** | — | Перевідкрита, Архів |
| **Перевідкрита** | У роботі | Нова, Нерозподілена, У роботі, Архів |
| **Архів** | — | Перевідкрита, Нова, Нерозподілена |

- **Нерозподілена** — лише адмін-інтерфейси / дейлі; не в публічних чіпсах і сітці.
- Конвертація звернення→задача: зберігає виконавця; статус мапиться (В роботі→У роботі, без виконавця→Нерозподілена).
- Системні коментарі на кожен перехід + `task_status_log` для обліку часу в статусі.

### Звернення (`incidents`)

| Поточний | → user | → admin |
|----------|--------|---------|
| **Нове** | В роботі | В роботі, Архів |
| **В роботі** | На паузі, Вирішено | На паузі, Вирішено, У задачу, Архів |
| **На паузі** | В роботі | В роботі, Архів |
| **Вирішено** | — | Архів, Нове (admin reopen) |
| **У задачу** | — | Архів, Нове (admin) |
| **Архів** | — | обмежено |

- Після `convert` статус звернення = **У задачу** (еквівалент закритого для лічильників).
- `converted_to_task_id` > 0 → не показується як відкрите звернення на публічній дошці.

### Зв’язок convert

```
Incident (Виконавець + статус) --convert--> Task (той самий виконавець, map status)
Incident.status := «У задачу»
```

### Чергування / BRB / графік

| API | Призначення |
|-----|-------------|
| `GET/PUT/POST /api/admin/shifts` | Перегляд місяця, bulk-правка днів, **перерахунок лише вперед** від поточної пари |
| `GET/POST/PUT /api/admin/shift-relief` | Тимчасова заміна; PUT = «Я знову на місці» |
| `POST /api/webhooks/slack` `/brb HH:MM` | BRB без IP allowlist |
| `GET/POST/DELETE /api/brb` | BRB напряму |

Перерахунок: `from_date` + `current_primary`[/backup] + опційно previous pair; дати **&lt; from_date** не змінюються.


## 11. Змінні середовища (API-релевантні)

| Змінна | Призначення |
|--------|-------------|
| `PORT` | Порт app (8085) |
| `DB_PATH` | SQLite |
| `DB_ADMIN_PASSWORD` | Unlock DB tools |
| `SOFT_AUTH` | М’який режим auth |
| `SESSION_SECURE` | Secure cookie |
| `ENABLE_INCIDENT_WEBHOOK` | Webhook on/off |
| `WEBHOOK_SECRET` | Auth webhook |
| `SLACK_BOT_TOKEN`, `SLACK_TEAM_CHANNEL`, `SLACK_WEBHOOK_URL` | Сповіщення |
| `NOTIFY_ON_INCIDENT` | 0 = вимкнути notify |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | Дзеркало |
| `APP_PUBLIC_URL` | Deep-link у повідомленнях |
| `JIRA_ENABLED`, `JIRA_BASE_URL`, `JIRA_EMAIL`, `JIRA_API_TOKEN` | Jira API |
| `JIRA_STATUS_MAP` | Мапінг статусів |
| `JIRA_JQL_FILTER` | Дефолтний JQL імпорту |

---

## 12. Статичні сторінки (не JSON API)

| Шлях | Опис |
|------|------|
| `/`, `/index.html` | Календар / користувач |
| `/admin.html` | Адмін-панель |
| `/status-fix.js` | Допоміжний JS |

Віддає nginx з `./static` (volume).

---

## 13. Історія змін паспорта

| Дата | Зміна |
|------|--------|
| 2026-08-31 | `GET /api/on-grid`, `on_grid` у `/api/data`, чіпи на UI |
| 2026-09-02 | FSM таблиці, convert keep assignee, shifts forward recalc, BRB HTTPS, queue task modal comments-on-click |
| 2026-08-31 | Початкова версія: повний реєстр маршрутів гілки `grok-1.0.0`, матриця on-grid routing, session, Jira import, webhooks |



## 14. Статус задач «Нерозподілена» (2026-08-31)

- **Нерозподілена**: Jira import / створення без виконавця; конвертація зі звернення **без** виконавця. Якщо у звернення був виконавець — статус мапиться (не завжди Нерозподілена).
- **Не** потрапляють у публічний `GET /api/data` / календар / колонки.
- Видимі в адмінці: Усі задачі, Дашборд дейлі, Черги.
- Перехід: admin **Нерозподілена → Нова** (під час дейлі).

## 15. BRB і тимчасова заміна чергових

| Метод | Шлях | Опис |
|-------|------|------|
| GET/POST/DELETE | `/api/brb` | Список / встановити `{user_name, until}` / зняти `?user=` |
| GET/POST/PUT | `/api/admin/shift-relief` | Тимчасова заміна primary/backup; PUT = «Я знову на місці» |
| POST | `/api/webhooks/slack` | Slash/text `/brb 16:00` |

`GET /api/data` → поле `brb: { "Name": "2026-08-31 16:00:00" }`.  
Login → `needs_resume: true` якщо зняли зі зміни.


## 16. Імпорт користувачів зі Slack

| Метод | Шлях | Опис |
|-------|------|------|
| GET | `/api/admin/slack/users` | `users.list` → members (без ботів), прапорець already_link |
| POST | `/api/admin/slack/users` | `{ members:[{slack_id,name,username,email,phone,…}], default_password }` |

Scopes бота: `users:read`, `users:read.email`. Env: `SLACK_BOT_TOKEN`, опційно `SLACK_IMPORT_DEFAULT_PASSWORD`.


## 16. Продуктивність / BRB intervals (2026-09-03)

| Метод | Шлях | Опис |
|-------|------|------|
| GET | `/api/admin/productivity?from=&to=` або `?date=` | Підсумок днів + інтервали |
| POST | `/api/admin/productivity` | `{action:"close_brb", user_name}` або `anchor` |

Таблиці: `work_day_anchor` (перша подія дня), `presence_intervals` (brb start/end).

**Повернення з BRB:**
1. Дочекатись `until` — чіп зникне з active map (інтервал лишається до clear).
2. Або явно: Slack повторний сценарій / admin «Зняти BRB» / `DELETE /api/brb?user=Name`.
3. Явний clear ставить `ended_at` і може створити work anchor `brb_end` якщо логіну ще не було.

**Формула (v1):** `productive_minutes = span(first_event → eod/now) − brb_minutes − away_minutes`.
Slack reactions/posts — пізніше. Колонка в «Статистика» на головній — пізніше.


## 17. Ієрархія задач (Jira-like) + диспетчери (2026-09-03)

### Типи (`daily_tasks.issue_type`)
`Epic` | `Story` | `Task` | `Sub-task` | `Bug`  
Поля: `parent_id`, `epic_id`, `external_id`, `source`.

Правила: Sub-task потребує parent; Epic без parent; батько не Sub-task.  
Convert звернення→задача: за замовчуванням `Task` (критичні пріоритети → `Bug`), `source=incident`.

### Диспетчери
`app_settings.dispatchers` = імена через кому. Якщо порожньо — усі `role=admin`.  
Нове звернення **без виконавця**: team channel + DM диспетчерам (+ чергові).  
З виконавцем: DM виконавцю; чергові — лише при priority ≥ «високий».  
Дедуп: один notify на `inc-new-{id}` протягом 2 хв.
