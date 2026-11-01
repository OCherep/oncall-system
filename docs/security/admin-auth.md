# Авторизація адмінки та ідентичність запиту (CUT-04 AC1–AC3)

## Що змінилось

- Усі маршрути `/api/admin/*` обгорнуті `requireAuth(true, …)`: без сесії → `401 SESSION_REQUIRED`, сесія не-admin → `403 admin required` (+ запис `ADMIN_DENIED` в аудит).
- Роль і actor беруться **лише з серверної сесії** (`oncall_session` cookie або `Authorization: Bearer <session_token>`). Поля тіла `role`, `actor`, `author_name`, `created_by` ігноруються для авторизації та аудиту.
- Аудит (`audit_logs` + файл) пише користувача сесії; зміна ролі пише `ROLE_CHANGE` з `old→new`; видалення користувача — `DELETE_USER`. Час — `timestamp` рядка.
- Маршрути, які раніше брали actor/role з тіла, тепер вимагають сесію: `/api/brb`, `/api/request-absence`, `/api/daily-tasks`, `/api/comments`, `/api/task-status-log`, `/api/incidents` (GET/PUT/DELETE; гостьовий `POST` лишається, `created_by` для гостя не приймається з тіла). `DELETE /api/incidents` — лише admin.
- Звичайний користувач ставить/знімає BRB і подає відсутність лише **для себе**.

## Вхід адміністратора

1. `POST /api/login` з логіном/паролем користувача з `role=admin` → cookie `oncall_session` (HttpOnly).
2. Адмінка (`/admin.html`) і хаб (`/oncall-api/...` через edge) працюють на цій cookie (same-origin).

## Break-glass (нема жодного робочого admin-акаунта)

`DB_ADMIN_PASSWORD` — **другий фактор** для DB-інструментів (`/api/admin/project/*`) поверх admin-сесії, а не обхід логіну. Якщо адмін-вхід втрачено:

1. На хості зробити бекап БД: `cp -a /opt/ops/oncall/data/oncall.db /opt/ops/oncall/data/oncall.db.bak-$(date +%Y%m%d%H%M)` (шлях — за `DB_PATH`/volume у `docker-compose.yml`).
2. Призначити роль admin відомому користувачу: `sqlite3 <db> "UPDATE users SET role='admin' WHERE username='<логін>';"` (або скинути пароль цьому користувачу тим самим способом).
3. Увійти через `/api/login`, перевірити `GET /api/session/me` → `role: admin`.
4. Запис у журнал змін (хто/коли/чому) — break-glass робиться лише з доступом до хоста.
