#!/usr/bin/env bash
# deploy.sh — деплой OnCall за один захід з автовідкатом.
# Запуск на прод-хості з каталогу репозиторію (напр. /opt/oncall-app-5):
#   ./deploy.sh [REF] [--dry-run] [--no-announce-today] [--skip-tests]
# REF — гілка/тег/коміт (за замовчуванням grok-1.0.0).
set -euo pipefail

# --- захист: git checkout може змінити цей файл під час виконання — працюємо з копії
if [[ -z "${ONCALL_DEPLOY_REEXEC:-}" ]]; then
  tmp="$(mktemp /tmp/oncall-deploy.XXXXXX.sh)"
  cp "$0" "$tmp"; chmod +x "$tmp"
  ONCALL_DEPLOY_REEXEC=1 ONCALL_REPO_DIR="${ONCALL_REPO_DIR:-$(cd "$(dirname "$0")" && pwd)}" exec "$tmp" "$@"
fi

REPO_DIR="${ONCALL_REPO_DIR:?}"
cd "$REPO_DIR"

REF="grok-1.0.0"; DRY_RUN=0; NO_ANNOUNCE=0; SKIP_TESTS=0
for a in "$@"; do
  case "$a" in
    --dry-run) DRY_RUN=1 ;;
    --no-announce-today) NO_ANNOUNCE=1 ;;
    --skip-tests) SKIP_TESTS=1 ;;
    -h|--help) sed -n 2,6p "$0"; exit 0 ;;
    -*) echo "Невідомий параметр: $a" >&2; exit 2 ;;
    *) REF="$a" ;;
  esac
done

SERVICE="app"
CONTAINER="${ONCALL_CONTAINER:-oncall_app_5}"
DB_FILE="$REPO_DIR/data/oncall.db"
BACKUP_DIR="${ONCALL_BACKUP_DIR:-$REPO_DIR/../oncall-backups}"
MIN_FREE_MB="${ONCALL_MIN_FREE_MB:-2048}"
HEALTH_TIMEOUT="${ONCALL_HEALTH_TIMEOUT:-90}"
TS="$(date +%Y%m%d-%H%M%S)"
LOG_FILE="$REPO_DIR/deploy-$TS.log"
OVERRIDE="$REPO_DIR/.deploy-override.yml"
KYIV_TODAY="$(TZ=Europe/Kyiv date +%F)"

exec > >(tee -a "$LOG_FILE") 2>&1

log()  { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }
ok()   { log "✅ $*"; }
warn() { log "⚠️  $*"; }
die()  { log "❌ $*"; exit 1; }
run()  { if (( DRY_RUN )); then log "[dry-run] $*"; else "$@"; fi; }

# sqlite3: на хості або одноразовим контейнером
SQLITE_IMG="alpine:3.20"
sq() {
  if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "$@"
  else
    docker run --rm -v "$REPO_DIR/data:$REPO_DIR/data" -v "$BACKUP_DIR:$BACKUP_DIR" "$SQLITE_IMG" \
      sh -c 'apk add -q --no-cache sqlite >/dev/null && sqlite3 "$@"' sh "$@"
  fi
}

today_pair() {
  sq -readonly "$DB_FILE" "SELECT COALESCE(primary_user,'')||' / '||COALESCE(backup_user,'') FROM shifts WHERE date='$KYIV_TODAY';"
}

log "=== Деплой OnCall: ref=$REF dry-run=$DRY_RUN no-announce-today=$NO_ANNOUNCE · лог: $LOG_FILE"

# ---------- 1. Префлайт
log "1/9 Префлайт"
command -v docker >/dev/null || die "docker не знайдено"
docker compose version >/dev/null 2>&1 || die "docker compose (v2) не знайдено"
command -v git >/dev/null || die "git не знайдено"
[[ -f docker-compose.yml ]] || die "docker-compose.yml не знайдено у $REPO_DIR"
[[ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" == "true" ]] || die "контейнер $CONTAINER не запущений"
[[ -f "$DB_FILE" ]] || die "БД не знайдена: $DB_FILE"
git diff --quiet HEAD -- || die "у репозиторії є незакомічені зміни tracked-файлів — приберіть їх перед деплоєм (git status)"
free_mb=$(df -Pm "$REPO_DIR" | awk 'NR==2{print $4}')
(( free_mb >= MIN_FREE_MB )) || die "мало місця: ${free_mb}MB < ${MIN_FREE_MB}MB"
ok "місце: ${free_mb}MB"
mkdir -p "$BACKUP_DIR"

envval() { # значення з .env або з поточного оточення
  local k="$1" v="${!1:-}"
  if [[ -z "$v" && -f .env ]]; then v="$(grep -E "^${k}=" .env | tail -1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//')"; fi
  printf '%s' "$v"
}
[[ -n "$(envval SLACK_BOT_TOKEN)" ]] || die "SLACK_BOT_TOKEN не задано (.env) — оголошення/DM не працюватимуть"
# Секрети без дефолтів: деплой ЗУПИНЯЄТЬСЯ, якщо не задано/плейсхолдер (значення не друкуються)
for k in DB_ADMIN_PASSWORD WEBHOOK_SECRET; do
  v="$(envval "$k")"
  if [[ -z "$v" || "${v,,}" == *change-me* || "${v,,}" == *changeme* || "$v" == "your-long-random-secret" ]]; then
    die "$k не задано або дефолтний/плейсхолдер — задайте у .env (openssl rand -hex 24)"
  fi
  (( ${#v} >= 16 )) || die "$k закороткий (<16 символів)"
  ok "$k: задано (${#v} симв., значення приховано)"
done
for k in SLACK_SIGNING_SECRET ONCALL_READ_TOKEN; do
  [[ -n "$(envval "$k")" ]] || warn "$k не задано — відповідна функція вимкнена (fail closed): SLACK_SIGNING_SECRET → /api/webhooks/slack; ONCALL_READ_TOKEN → повний /api/data для DevOps Hub mentions"
done
SA="$(envval SOFT_AUTH)"; [[ -z "$SA" || "$SA" == "0" ]] || warn "SOFT_AUTH=$SA більше не діє (X-Oncall-User не приймається) — приберіть з .env"
CH="$(envval DUTY_ANNOUNCE_CHANNELS)"
if [[ -z "$CH" || "$CH" == *"#"* ]]; then
  warn "DUTY_ANNOUNCE_CHANNELS='${CH:-<дефолт #devops-pm-direct,#devops_team>}' — краще ID каналів (C…), бот має бути запрошений"
fi
ANN_EN="$(envval DUTY_ANNOUNCE_ENABLED)"; ANN_EN="${ANN_EN:-1}"
ANN_TIME="$(envval DUTY_ANNOUNCE_TIME)"; ANN_TIME="${ANN_TIME:-09:00}"
now_hm="$(TZ=Europe/Kyiv date +%H:%M)"
if [[ "$ANN_EN" != "0" && ! "$now_hm" < "$ANN_TIME" && $NO_ANNOUNCE -eq 0 ]]; then
  warn "зараз $now_hm (Київ) ≥ $ANN_TIME: після старту одразу піде оголошення в канали. Щоб його не було сьогодні — --no-announce-today"
fi
PREV_COMMIT="$(git rev-parse HEAD)"
PREV_IMAGE_ID="$(docker inspect -f '{{.Image}}' "$CONTAINER")"
ok "поточний коміт $PREV_COMMIT, образ $PREV_IMAGE_ID"

# ---------- 2. Бекап БД
log "2/9 Бекап SQLite"
BACKUP="$BACKUP_DIR/oncall-$TS.db"
if (( DRY_RUN )); then log "[dry-run] sqlite .backup → $BACKUP"; else
  sq "$DB_FILE" ".backup '$BACKUP'"
  [[ "$(sq "$BACKUP" 'PRAGMA integrity_check;')" == "ok" ]] || die "integrity_check бекапу не ok"
  n_live="$(sq -readonly "$DB_FILE" 'SELECT count(*) FROM shifts;')"
  n_bak="$(sq -readonly "$BACKUP" 'SELECT count(*) FROM shifts;')"
  [[ "$n_live" == "$n_bak" ]] || die "кількість shifts у бекапі ($n_bak) ≠ живій ($n_live)"
  sha256sum "$BACKUP" > "$BACKUP.sha256"
  ok "бекап $BACKUP (shifts=$n_bak, integrity ok)"
fi

# ---------- 3. Тег відкату
log "3/9 Тег поточного образу для відкату"
ROLLBACK_TAG="oncall-app:rollback-$TS"
run docker tag "$PREV_IMAGE_ID" "$ROLLBACK_TAG"
printf '%s\n' "PREV_COMMIT=$PREV_COMMIT" "PREV_IMAGE_ID=$PREV_IMAGE_ID" "ROLLBACK_TAG=$ROLLBACK_TAG" "BACKUP=$BACKUP" > "$BACKUP_DIR/rollback-$TS.env"
ok "відкат: $ROLLBACK_TAG (записано в $BACKUP_DIR/rollback-$TS.env)"

# ---------- 4. Код
log "4/9 git fetch + checkout $REF"
run git fetch --prune origin
if git rev-parse --verify -q "origin/$REF^{commit}" >/dev/null; then TARGET="origin/$REF"; else TARGET="$REF"; fi
NEW_COMMIT="$(git rev-parse --verify "$TARGET^{commit}")" || die "ref $REF не знайдено"
SHORT="$(git rev-parse --short=12 "$NEW_COMMIT")"
NEW_TAG="oncall-app:$SHORT"
ok "цільовий коміт $SHORT ($(git log -1 --format=%s "$NEW_COMMIT"))"

# Тести і збірка — з чистого дерева в тимчасовому каталозі, прод ще не чіпаємо
BUILD_DIR="$(mktemp -d /tmp/oncall-build.XXXXXX)"
trap 'rm -rf "$BUILD_DIR"' EXIT
git archive "$NEW_COMMIT" | tar -x -C "$BUILD_DIR"

# ---------- 5. go test
log "5/9 go vet + go test у контейнері"
if (( SKIP_TESTS )); then warn "тести пропущено (--skip-tests)"; else
  run docker run --rm -v "$BUILD_DIR:/src" -w /src -e CGO_ENABLED=1 golang:1.22-alpine \
    sh -c 'apk add -q --no-cache gcc musl-dev tzdata >/dev/null && go mod tidy && go vet ./... && go test -count=1 ./...' \
    || die "тести впали — прод не змінено"
  ok "тести зелені"
fi

# ---------- 6. Збірка
log "6/9 docker build $NEW_TAG"
run docker build -t "$NEW_TAG" "$BUILD_DIR" || die "збірка впала — прод не змінено"
ok "образ $NEW_TAG"

# ---------- 7. Пара на сьогодні
log "7/9 Сьогоднішня пара ($KYIV_TODAY, Київ)"
PAIR_BEFORE="$(today_pair)"
ok "до деплою: '${PAIR_BEFORE:-<порожньо>}'"

if (( DRY_RUN )); then
  ok "dry-run завершено: прод не змінено (коміт $SHORT, образ не перезапускався)"; exit 0
fi

# ---------- 8. Рестарт (далі — з автовідкатом)
rollback() {
  local rc=$?
  trap - ERR
  log "❌ Помилка після рестарту (код $rc) — АВТОВІДКАТ"
  set +e
  docker compose -f docker-compose.yml -f "$OVERRIDE" stop "$SERVICE"
  cp -a "$BACKUP" "$DB_FILE" && rm -f "$DB_FILE-wal" "$DB_FILE-shm" && log "БД відновлено з $BACKUP"
  git checkout -q "$PREV_COMMIT" && log "код повернуто на $PREV_COMMIT"
  printf 'services:\n  %s:\n    image: %s\n' "$SERVICE" "$ROLLBACK_TAG" > "$OVERRIDE"
  docker compose -f docker-compose.yml -f "$OVERRIDE" up -d --no-build --no-deps "$SERVICE"
  sleep 5
  if [[ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" == "true" ]]; then
    log "↩️  Відкат виконано: образ $ROLLBACK_TAG, БД з бекапу. Перевірте UI."
  else
    log "🔥 ВІДКАТ НЕ ПІДНЯВ КОНТЕЙНЕР — ручне втручання! Див. ранбук, розділ «Відкат вручну». Бекап: $BACKUP"
  fi
  exit 1
}

fail() { log "❌ $*"; false; rollback; }
log "8/9 Перемикання на $NEW_TAG"
git checkout -q --detach "$NEW_COMMIT"
printf 'services:\n  %s:\n    image: %s\n' "$SERVICE" "$NEW_TAG" > "$OVERRIDE"
trap rollback ERR

if (( NO_ANNOUNCE )); then
  # Позначаємо сьогоднішнє оголошення як «вже відправлене» (схема як у handlers_duty_announce.go)
  IFS=',' read -ra chs <<< "${CH:-#devops-pm-direct,#devops_team}"
  sql="CREATE TABLE IF NOT EXISTS duty_announce_sent (day TEXT NOT NULL, channel TEXT NOT NULL, sent_at TEXT NOT NULL, PRIMARY KEY (day, channel));"
  for c in "${chs[@]}"; do c="$(echo "$c" | xargs)"; [[ -n "$c" ]] && sql+="INSERT OR IGNORE INTO duty_announce_sent VALUES ('$KYIV_TODAY','${c//\'/}','suppressed-by-deploy');"; done
  sq "$DB_FILE" "$sql"
  ok "оголошення на $KYIV_TODAY придушено (${#chs[@]} канал(и))"
fi

docker compose -f docker-compose.yml -f "$OVERRIDE" up -d --no-build --no-deps "$SERVICE"

# ---------- 9. Перевірки
log "9/9 Healthcheck (до ${HEALTH_TIMEOUT}s)"
deadline=$(( SECONDS + HEALTH_TIMEOUT )); healthy=0
while (( SECONDS < deadline )); do
  if [[ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" == "true" ]] \
     && docker exec "$CONTAINER" sh -c 'wget -S -q -O /dev/null http://127.0.0.1:8085/ 2>&1 | grep -q "HTTP/"'; then
    healthy=1; break
  fi
  sleep 3
done
(( healthy )) || fail "застосунок не відповів за ${HEALTH_TIMEOUT}s"
[[ "$(docker inspect -f '{{.Config.Image}}' "$CONTAINER")" == "$NEW_TAG" ]] || fail "контейнер запущено не з $NEW_TAG"
ok "застосунок відповідає по HTTP"
sleep 10   # міграції + horizon-планувальник
docker logs --since 2m "$CONTAINER" 2>&1 | grep -qiE 'panic|fatal' && fail "у логах panic/fatal"
sq -readonly "$DB_FILE" "PRAGMA table_info(shifts);" | grep -q is_manual || fail "колонка is_manual не з'явилась"
PAIR_AFTER="$(today_pair)"
if [[ -z "$PAIR_BEFORE" ]]; then
  warn "до деплою на сьогодні не було рядка; планувальник заповнив: '${PAIR_AFTER:-<порожньо>}'"
else
  [[ "$PAIR_AFTER" == "$PAIR_BEFORE" ]] || fail "пара змінилась: '$PAIR_BEFORE' → '$PAIR_AFTER'"
fi
ok "сьогоднішня пара без змін: '${PAIR_AFTER:-<порожньо>}'"
trap - ERR
docker logs --since 2m "$CONTAINER" 2>&1 | grep -E 'shifts plan|duty announce|sqlite:' || true
ok "=== ДЕПЛОЙ УСПІШНИЙ: $SHORT ($NEW_TAG). Відкат вручну: ROLLBACK_TAG=$ROLLBACK_TAG, BACKUP=$BACKUP"
