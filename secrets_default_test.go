package main

import (
	"net/http"
	"os"
	"testing"
)

func TestNoDefaultDBAdminPassword(t *testing.T) {
	setupTestDB(t)
	adm := sessionFor(t, mkUser(t, "boss", "Boss", "admin"))
	for _, v := range []string{"", "db-admin-change-me", "change-me"} {
		os.Setenv("DB_ADMIN_PASSWORD", v)
		if rr := doReq(t, http.MethodPost, "/api/admin/project/unlock", `{"password":"db-admin-change-me"}`, adm, nil); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("DB_ADMIN_PASSWORD=%q: unlock want 503 (disabled), got %d", v, rr.Code)
		}
		h := map[string]string{"X-DB-Admin-Password": "db-admin-change-me"}
		if rr := doReq(t, http.MethodPost, "/api/admin/project/query", `{"sql":"select 1"}`, adm, h); rr.Code != http.StatusForbidden {
			t.Errorf("DB_ADMIN_PASSWORD=%q: query want 403, got %d", v, rr.Code)
		}
	}
	os.Setenv("DB_ADMIN_PASSWORD", "real-strong-test-pass")
	defer os.Unsetenv("DB_ADMIN_PASSWORD")
	if rr := doReq(t, http.MethodPost, "/api/admin/project/unlock", `{"password":"real-strong-test-pass"}`, adm, nil); rr.Code != http.StatusOK {
		t.Errorf("valid unlock: want 200, got %d", rr.Code)
	}
}

func TestWebhookFailsClosedWithoutSecret(t *testing.T) {
	setupTestDB(t)
	os.Setenv("ENABLE_INCIDENT_WEBHOOK", "1")
	defer os.Unsetenv("ENABLE_INCIDENT_WEBHOOK")
	for _, v := range []string{"", "change-me-webhook-secret", "your-long-random-secret"} {
		os.Setenv("WEBHOOK_SECRET", v)
		h := map[string]string{"X-Webhook-Secret": v}
		rr := doReq(t, http.MethodPost, "/api/webhooks/incidents", `{"source":"jira","description":"x"}`, nil, h)
		if rr.Code < 400 {
			t.Errorf("WEBHOOK_SECRET=%q must be rejected, got %d", v, rr.Code)
		}
	}
	os.Unsetenv("WEBHOOK_SECRET")
}

func TestNoDefaultAdminSeed(t *testing.T) {
	os.Unsetenv("ONCALL_BOOTSTRAP_ADMIN_PASSWORD")
	setupTestDB(t)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE username='admin' AND password='admin'`).Scan(&n)
	if n != 0 {
		t.Fatalf("admin/admin must not be seeded")
	}
}
