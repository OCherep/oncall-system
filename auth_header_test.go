package main

import (
	"net/http"
	"os"
	"testing"
)

// X-Oncall-User не автентифікує ні з SOFT_AUTH порожнім, ні з SOFT_AUTH=1.
func TestOncallUserHeaderNeverAuthenticates(t *testing.T) {
	setupTestDB(t)
	mkUser(t, "boss", "Boss", "admin")
	mkUser(t, "u1", "User One", "user")
	for _, v := range []string{"", "1", "true"} {
		os.Setenv("SOFT_AUTH", v)
		for _, who := range []string{"boss", "Boss", "u1"} {
			h := map[string]string{"X-Oncall-User": who}
			if rr := doReq(t, http.MethodGet, "/api/admin/users", "", nil, h); rr.Code != http.StatusUnauthorized {
				t.Errorf("SOFT_AUTH=%q X-Oncall-User=%s admin route: want 401, got %d", v, who, rr.Code)
			}
			if rr := doReq(t, http.MethodGet, "/api/daily-tasks", "", nil, h); rr.Code != http.StatusUnauthorized {
				t.Errorf("SOFT_AUTH=%q X-Oncall-User=%s user route: want 401, got %d", v, who, rr.Code)
			}
		}
	}
	os.Unsetenv("SOFT_AUTH")
}

// Підроблені внутрішні заголовки X-Auth-* не впливають на роль.
func TestForgedAuthHeadersIgnored(t *testing.T) {
	setupTestDB(t)
	u := sessionFor(t, mkUser(t, "u1", "User One", "user"))
	h := map[string]string{"X-Auth-Role": "admin", "X-Auth-Username": "boss"}
	if rr := doReq(t, http.MethodGet, "/api/admin/users", "", u, h); rr.Code != http.StatusForbidden {
		t.Errorf("forged X-Auth-Role: want 403, got %d", rr.Code)
	}
}
