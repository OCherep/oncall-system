# Changelog

## 1.1.0 — 2026-09-04

### Infrastructure
- Host **port 80 removed** from docker-compose (reserved for [devops-hub](https://github.com/OCherep/devops-hub)). UI/API only on **https://s.ks.tv:85**.

### Multi-team
- Table `teams`; users/incidents/tasks have `team_id`.
- Admin: CRUD команд, призначення користувача в команду, колонки «Роль у команді» / «Команда» / «У roster».
- Default team **DevOps**.

### Incidents
- Field **Кому направлено**: конкретний виконавець або **«До команди»** (черга; не auto «перший вільний»).
- `directed_to` / `directed_scope` + `team_id` on create.

### Other (1.0.x → 1.1)
- Fair weekend on-call rotation; productivity special days; dispatchers setting; hierarchy issue_type foundation; BRB/productivity anchors; session/logout fixes.

### Deferred
- Project Manager / Team Lead cross-team transfers and project pool.
- Full Jira hierarchy sync (Epic/Story parent links in UI).
