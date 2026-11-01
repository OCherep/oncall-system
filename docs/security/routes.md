# Інвентар маршрутів OnCall і їх захист

Стан після PR «admin-auth» + «no-header-auth» + «route-protection» (база `grok-1.0.0`).
`IP` = `withIPAllow` (allowlist CIDR, якщо налаштовано). `S` = сесія (`oncall_session` / `Authorization: Bearer`). `A` = сесія з `role=admin`.

| Маршрут | Методи | Було | Стало | Примітка |
|---|---|---|---|---|
| `/api/login`, `/api/logout`, `/api/session/me` | POST/POST/GET | IP | IP | вхід; me → 401 без сесії |
| `/api/me/password`, `/api/me/profile` | POST/GET | IP + перевірка сесії в handler | без змін | |
| `/api/tasks/pool`, `/api/tasks/claim` | GET/POST | IP + сесія в handler | без змін | |
| **`/api/admin/*` (усі 29)** | усі | **IP (без auth)** | **IP + A** | 401 без сесії, 403 не-admin; тест читає main.go і перевіряє кожен |
| `/api/brb` | GET/POST/DELETE | IP | IP + S | user — лише свій BRB |
| `/api/request-absence` | POST | IP | IP + S | actor/role з сесії |
| `/api/incidents` | GET/PUT/DELETE | IP | IP + S (DELETE — A) | |
| `/api/incidents` | POST | IP | IP + optional S | гостьове звернення лишається; created_by/role з тіла ігноруються, Slack перевіряє сервер |
| `/api/daily-tasks` | усі | IP | IP + S | created_by з сесії |
| `/api/comments`, `/api/task-status-log` | усі | IP | IP + S | author з сесії |
| **`/api/slack/lookup`** | GET | **IP (відкритий: email/телефон/посада зі Slack)** | **IP + S** | гостьова форма: перевірка Slack на сервері під час POST |
| **`/api/data`** | GET | **IP (повні дані: звернення, задачі, статистика, логіни, ролі)** | IP + optional S | без сесії — урізаний вигляд (графік, імена, відсутності); повний — із S або `X-Oncall-Read-Token` (= `ONCALL_READ_TOKEN`) |
| `/api/teams` | GET | IP | без змін | лише назви команд |
| `/api/on-grid` | GET | IP | без змін | робочий/неробочий час; використовується healthcheck хаба |
| **`/api/webhooks/slack`** | POST | **відкритий (без IP, без підпису) — BRB/інциденти від імені будь-кого** | підпис Slack v0 (`SLACK_SIGNING_SECRET`), без секрету — 403 | |
| `/api/webhooks/incidents` | POST | `WEBHOOK_SECRET` (порожній → приймав усе) | див. PR «no-default-secrets»: порожній → вимкнено | |
| `/api/webhooks/health` | GET | відкритий | без змін | лише булеві прапорці |
| `/` (static) | GET | IP | без змін | |
