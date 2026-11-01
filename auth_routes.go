package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// readTokenOK — сервісний read-only токен для /api/data (напр. DevOps Hub mentions,
// серверний виклик без браузерної сесії). Заголовок X-Oncall-Read-Token, env ONCALL_READ_TOKEN.
// Якщо env не задано — сервісний доступ вимкнено (fail closed).
func readTokenOK(r *http.Request) bool {
	want := strings.TrimSpace(os.Getenv("ONCALL_READ_TOKEN"))
	got := strings.TrimSpace(r.Header.Get("X-Oncall-Read-Token"))
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// redactPublicData — що бачить гість (без сесії) у /api/data: календар чергувань без
// персональних/робочих даних (звернення, задачі, статистика, BRB, логіни, ролі).
func redactPublicData(out map[string]interface{}) {
	// порожні значення того ж типу, щоб UI гостя не падав
	out["incidents"] = map[string][]IncidentReport{}
	out["daily_tasks"] = map[string][]DailyTask{}
	out["stats"] = []UserStat{}
	out["brb"] = map[string]interface{}{}
	if team, ok := out["team_members"].([]User); ok {
		pub := make([]map[string]interface{}, 0, len(team))
		for _, u := range team {
			pub = append(pub, map[string]interface{}{"id": u.ID, "name": u.Name, "team_role": u.TeamRole, "is_oncall": u.IsOncall})
		}
		out["team_members"] = pub
	}
	out["redacted"] = true
}

var slackSigWarnOnce sync.Once

// verifySlackSignature — перевірка X-Slack-Signature (v0) з SLACK_SIGNING_SECRET.
// Без секрету endpoint вимкнено (fail closed) з гучним логом. Тіло відновлюється для handler'а.
func verifySlackSignature(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := strings.TrimSpace(os.Getenv("SLACK_SIGNING_SECRET"))
		if secret == "" {
			slackSigWarnOnce.Do(func() {
				log.Printf("SECURITY: SLACK_SIGNING_SECRET не задано — /api/webhooks/slack вимкнено (403)")
			})
			http.Error(w, `{"error":"slack webhook disabled: SLACK_SIGNING_SECRET not set"}`, http.StatusForbidden)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		ts := r.Header.Get("X-Slack-Request-Timestamp")
		sig := r.Header.Get("X-Slack-Signature")
		n, err := strconv.ParseInt(ts, 10, 64)
		if err != nil || time.Since(time.Unix(n, 0)).Abs() > 5*time.Minute || sig == "" {
			http.Error(w, `{"error":"bad slack signature"}`, http.StatusUnauthorized)
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte("v0:" + ts + ":"))
		mac.Write(body)
		want := "v0=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(want), []byte(sig)) {
			http.Error(w, `{"error":"bad slack signature"}`, http.StatusUnauthorized)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next(w, r)
	}
}
