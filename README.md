# oncall-system (гілка `grok`)

Об'єднана версія: **good** + інциденти + **absence-aware** розклад.

## Що нового в календарі
- Показується **вся команда** (roster chips над сіткою)
- Хто у **відпустці / Day Off / Sick Day** (approved) — **не ставиться** на primary/backup
- У клітинці дня: бейдж відсутніх, Осн/Дубл лише з доступних, рядок «В пулі»
- Деталі дня: доступні vs хто не може чергувати

## Структура
```
main.go, handlers_client.go, handlers_admin.go
static/index.html   — календар (оновлений)
static/admin.html   — адмінка (візьміть з good, якщо ще placeholder)
```

## Старт
```bash
git checkout grok
# Повна адмінка з good (якщо потрібно):
git checkout good -- static/admin.html

go mod tidy && go run .
# http://localhost:8083
```

Логіни: `admin`/`admin`, `dev1`/`1234`, `pm`/`1234`
