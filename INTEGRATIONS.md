# Інтеграції: Webhook звернень + Slack

## 1. Webhook прийому звернень (Jira / боти)

### Увімкнення (docker-compose / `.env`)

```bash
ENABLE_INCIDENT_WEBHOOK=1
WEBHOOK_SECRET=your-long-random-secret
```

### Endpoints

| Method | Path | Опис |
|--------|------|------|
| GET | `/api/webhooks/health` | Статус webhook + notify |
| POST | `/api/webhooks/incidents` | Створити звернення (+ опційно daily_task) |

### Авторизація

- Header `X-Webhook-Secret: <secret>`
- або `Authorization: Bearer <secret>`
- або query `?secret=<secret>`

### Приклад (нативний)

```bash
curl -sS -X POST "https://your-host/api/webhooks/incidents" \
  -H "Content-Type: application/json" \
  -H "X-Webhook-Secret: your-long-random-secret" \
  -d '{
    "source": "jira",
    "external_id": "OPS-123",
    "description": "Падіння API gateway",
    "user_name": "devops1",
    "priority": "Високий",
    "duration_minutes": 30,
    "create_daily_task": true,
    "created_by": "jira-bot"
  }'
```

Відповідь `201`:

```json
{
  "status": "ok",
  "incident_id": 42,
  "task_id": 17,
  "source": "jira",
  "external_id": "OPS-123",
  "date": "2026-08-21"
}
```

### Jira-shaped payload

Приймається також тіло з `issue.key` / `issue.fields.summary` / `assignee.displayName` / `priority.name`.

### Поведінка

1. INSERT у `incidents` (status=`Нове`, source з payload).
2. За замовчуванням — також INSERT у `daily_tasks`.
3. Системний коментар «Створено через webhook…».
4. Slack/Telegram сповіщення черговим + у командний канал.

---

## 2. Slack: командний канал + особисті DM

```bash
SLACK_WEBHOOK_URL=https://hooks.slack.com/services/T…/B…/…
SLACK_BOT_TOKEN=xoxb-…
SLACK_TEAM_CHANNEL=C0123456789
NOTIFY_ON_INCIDENT=1
```

У **Адмін → користувач** — поле **Slack Member ID** (`U…`) для DM.

---

## 3. Slack: щоденний склад @oncall

Щодня (і при заміні чергового того самого дня) система перезаписує user group `@oncall`
так, щоб у ній були **лише** сьогоднішні основний і дублюючий.

```bash
SLACK_ONCALL_GROUP_SYNC=1
SLACK_ONCALL_HANDLE=oncall
# обов'язково, якщо workspace дозволяє змінювати групи лише адмінам:
SLACK_USER_TOKEN=xoxp-…
# опційно, якщо handle нестандартний:
# SLACK_ONCALL_USERGROUP=S0123456789
```

Вимоги:

- Група `@oncall` уже існує у workspace.
- У картках членів команди заповнений `slack_id` (`U…`). Без нього людину в групу не додасть.
- Якщо на сьогодні немає жодного чергового з `slack_id`, склад не очищується.

`permission_denied` — це не баг коду. Slack не дає bot-токену змінювати user groups,
якщо у workspace «Create and edit user groups» виставлено на admins/owners.
Два варіанти:

1. Додати `SLACK_USER_TOKEN=xoxp-…` адміна з scope `usergroups:write` (рекомендовано).
2. Або в Slack: Tools & settings → Workspace settings → Permissions → User Groups → everyone.

Вимкнути без редеплою: `SLACK_ONCALL_GROUP_SYNC=0` або в `app_settings` ключ `slack_oncall_group_sync=0`.

---

## 4. Telegram-дзеркало

```bash
TELEGRAM_BOT_TOKEN=123456:ABC...
TELEGRAM_CHAT_ID=-1001234567890
```

---

## 5. Jira outbound (статус → тікет)

```bash
JIRA_ENABLED=1
JIRA_BASE_URL=https://your-domain.atlassian.net
JIRA_EMAIL=bot@company.com
JIRA_API_TOKEN=...
```

При зміні статусу звернення з `external_id` — comment + transition у Jira.

---

## 6. Деплой

```bash
git pull origin grok
docker compose build app --no-cache && docker compose up -d && docker compose restart nginx
curl -s http://127.0.0.1:84/api/webhooks/health
```

`nginx.conf` → `oncall_app_4:8084`.
