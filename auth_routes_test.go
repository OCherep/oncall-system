package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSlackLookupRequiresSession(t *testing.T) {
	setupTestDB(t)
	if rr := doReq(t, http.MethodGet, "/api/slack/lookup?q=@x", "", nil, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("lookup without session: want 401, got %d", rr.Code)
	}
}

func TestDataRedactedForGuest(t *testing.T) {
	setupTestDB(t)
	mkUser(t, "u1", "User One", "user")
	db.Exec(`INSERT INTO incidents (user_name, date, description) VALUES ('User One','2030-01-01','secret incident')`)
	rr := doReq(t, http.MethodGet, "/api/data?year=2030&month=1", "", nil, nil)
	if rr.Code != 200 {
		t.Fatalf("guest data: %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "secret incident") || strings.Contains(body, `"username"`) {
		t.Errorf("guest /api/data leaks incidents/usernames: %s", body)
	}
	var out map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &out)
	if out["redacted"] != true {
		t.Errorf("want redacted=true")
	}
	// із сесією — повні дані
	u := sessionFor(t, mkUser(t, "u2", "User Two", "user"))
	if rr := doReq(t, http.MethodGet, "/api/data?year=2030&month=1", "", u, nil); !strings.Contains(rr.Body.String(), `"username"`) || strings.Contains(rr.Body.String(), `"redacted"`) {
		t.Errorf("session /api/data should be full (not redacted)")
	}
	// сервісний токен
	os.Setenv("ONCALL_READ_TOKEN", "t0k-test")
	defer os.Unsetenv("ONCALL_READ_TOKEN")
	if rr := doReq(t, http.MethodGet, "/api/data?year=2030&month=1", "", nil, map[string]string{"X-Oncall-Read-Token": "wrong"}); strings.Contains(rr.Body.String(), "secret incident") {
		t.Errorf("wrong read token must be redacted")
	}
	if rr := doReq(t, http.MethodGet, "/api/data?year=2030&month=1", "", nil, map[string]string{"X-Oncall-Read-Token": "t0k-test"}); !strings.Contains(rr.Body.String(), `"username"`) {
		t.Errorf("valid read token should get full data")
	}
}

func TestSlackWebhookSignature(t *testing.T) {
	setupTestDB(t)
	os.Unsetenv("SLACK_SIGNING_SECRET")
	body := `{"text":"/brb 15m","user_name":"u1"}`
	if rr := doReq(t, http.MethodPost, "/api/webhooks/slack", body, nil, nil); rr.Code != http.StatusForbidden {
		t.Errorf("no secret: want 403 (disabled), got %d", rr.Code)
	}
	os.Setenv("SLACK_SIGNING_SECRET", "sig-test")
	defer os.Unsetenv("SLACK_SIGNING_SECRET")
	if rr := doReq(t, http.MethodPost, "/api/webhooks/slack", body, nil, nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("unsigned: want 401, got %d", rr.Code)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("sig-test"))
	mac.Write([]byte("v0:" + ts + ":" + body))
	h := map[string]string{"X-Slack-Request-Timestamp": ts, "X-Slack-Signature": "v0=" + hex.EncodeToString(mac.Sum(nil))}
	if rr := doReq(t, http.MethodPost, "/api/webhooks/slack", body, nil, h); rr.Code != http.StatusOK {
		t.Errorf("signed: want 200, got %d %s", rr.Code, rr.Body.String())
	}
}
