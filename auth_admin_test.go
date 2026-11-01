package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
)

var routesOnce sync.Once

func testMux(t *testing.T) http.Handler {
	t.Helper()
	routesOnce.Do(registerRoutes)
	return http.DefaultServeMux
}

func mkUser(t *testing.T, username, name, role string) User {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (username, password, name, role, is_oncall) VALUES (?,?,?,?,1)`, username, "pw-"+username, name, role)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return User{ID: int(id), Username: username, Name: name, Role: role}
}

func sessionFor(t *testing.T, u User) *http.Cookie {
	t.Helper()
	ensureSessionsTable()
	tok, _, err := createSession(u, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookieName(), Value: tok}
}

func doReq(t *testing.T, method, path, body string, c *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	testMux(t).ServeHTTP(rr, req)
	return rr
}

// adminRoutesFromSource — усі /api/admin/* маршрути з main.go (щоб новий маршрут без захисту ловився тестом).
func adminRoutesFromSource(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`HandleFunc\("(/api/admin/[^"]+)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		out = append(out, m[1])
	}
	if len(out) < 20 {
		t.Fatalf("expected many admin routes, got %d", len(out))
	}
	return out
}

func TestAdminRoutesRequireSession(t *testing.T) {
	setupTestDB(t)
	user := sessionFor(t, mkUser(t, "u1", "User One", "user"))
	for _, p := range adminRoutesFromSource(t) {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			if rr := doReq(t, m, p, `{"role":"admin","actor":"Evil"}`, nil, nil); rr.Code != http.StatusUnauthorized {
				t.Errorf("%s %s without session: want 401, got %d", m, p, rr.Code)
			}
			if rr := doReq(t, m, p, `{"role":"admin","actor":"Evil"}`, user, nil); rr.Code != http.StatusForbidden {
				t.Errorf("%s %s as non-admin: want 403, got %d", m, p, rr.Code)
			}
		}
	}
}

func TestAdminRouteAllowsAdminSession(t *testing.T) {
	setupTestDB(t)
	adm := sessionFor(t, mkUser(t, "boss", "Boss", "admin"))
	if rr := doReq(t, http.MethodGet, "/api/admin/users", "", adm, nil); rr.Code != http.StatusOK {
		t.Fatalf("admin GET /api/admin/users: want 200, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestRoleChangeAuditUsesSessionActor(t *testing.T) {
	setupTestDB(t)
	adm := sessionFor(t, mkUser(t, "boss", "Boss", "admin"))
	victim := mkUser(t, "v1", "Victim", "user")
	body := `{"id":` + itoa(victim.ID) + `,"username":"v1","name":"Victim","role":"admin","actor":"Somebody Else"}`
	if rr := doReq(t, http.MethodPut, "/api/admin/users", body, adm, nil); rr.Code != http.StatusOK {
		t.Fatalf("PUT users: %d %s", rr.Code, rr.Body.String())
	}
	var who, details string
	if err := db.QueryRow(`SELECT user_name, details FROM audit_logs WHERE action='ROLE_CHANGE' ORDER BY id DESC LIMIT 1`).Scan(&who, &details); err != nil {
		t.Fatal(err)
	}
	if who != "Boss" {
		t.Errorf("audit actor: want Boss (session), got %q", who)
	}
	if !strings.Contains(details, "user→admin") {
		t.Errorf("audit must contain old→new, got %q", details)
	}
}

func TestBodyActorAndRoleIgnored(t *testing.T) {
	setupTestDB(t)
	mkUser(t, "other", "Other", "user")
	u := sessionFor(t, mkUser(t, "u1", "User One", "user"))
	// user claims role=admin in body to file absence for someone else → must be forbidden
	rr := doReq(t, http.MethodPost, "/api/request-absence",
		`{"user_name":"Other","type":"Відпустка","start_date":"2030-01-01","end_date":"2030-01-02","actor":"Other","role":"admin","auto_approve":true}`, u, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("spoofed role in body: want 403, got %d %s", rr.Code, rr.Body.String())
	}
	// comment author comes from session, not author_name
	db.Exec(`INSERT INTO daily_tasks (id, user_name, date, task_description) VALUES (1,'User One','2030-01-01','t')`)
	rr = doReq(t, http.MethodPost, "/api/comments", `{"entity_type":"task","entity_id":1,"author_name":"CEO","body":"hi"}`, u, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("comment: %d %s", rr.Code, rr.Body.String())
	}
	var author string
	db.QueryRow(`SELECT author_name FROM comments WHERE is_system=0 ORDER BY id DESC LIMIT 1`).Scan(&author)
	if author != "User One" {
		t.Errorf("comment author: want session name, got %q", author)
	}
}

func TestIncidentDeleteRequiresAdmin(t *testing.T) {
	setupTestDB(t)
	u := sessionFor(t, mkUser(t, "u1", "User One", "user"))
	if rr := doReq(t, http.MethodDelete, "/api/incidents?id=1", "", nil, nil); rr.Code != http.StatusUnauthorized {
		t.Errorf("delete without session: want 401, got %d", rr.Code)
	}
	if rr := doReq(t, http.MethodDelete, "/api/incidents?id=1", "", u, nil); rr.Code != http.StatusForbidden {
		t.Errorf("delete as user: want 403, got %d", rr.Code)
	}
}

