package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Session TTL з довідника безпеки (app_settings session_ttl_hours), default 14h.
func sessionTTL() time.Duration {
	h := 14
	if v := strings.TrimSpace(getSetting("session_ttl_hours", "")); v != "" {
		if n, err := time.ParseDuration(v + "h"); err == nil && n > 0 {
			return n
		}
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			h = n
		}
	}
	return time.Duration(h) * time.Hour
}

func sessionCookieName() string { return "oncall_session" }

func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func ensureUserExtraColumns() {
	if db == nil {
		return
	}
	for _, c := range []string{
		"email TEXT DEFAULT ''",
		"phone TEXT DEFAULT ''",
		"slack_id TEXT DEFAULT ''",
		"is_oncall INTEGER DEFAULT 1",
		"show_in_roster INTEGER DEFAULT 1",
		"needs_resume INTEGER DEFAULT 0",
	} {
		db.Exec("ALTER TABLE users ADD COLUMN " + c)
	}
}

func ensureSessionsTable() {
	db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		token_hash TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		username TEXT NOT NULL,
		role TEXT NOT NULL,
		name TEXT NOT NULL,
		ip TEXT DEFAULT '',
		user_agent TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL,
		last_seen DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_exp ON sessions(expires_at)`)
}

func createSession(u User, ip, ua string) (token string, expires time.Time, err error) {
	token, err = newSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires = time.Now().Add(sessionTTL())
	_, err = db.Exec(`INSERT INTO sessions (token_hash, user_id, username, role, name, ip, user_agent, expires_at, last_seen)
		VALUES (?,?,?,?,?,?,?,?,datetime('now','localtime'))`,
		hashToken(token), u.ID, u.Username, u.Role, u.Name, ip, truncateStr(ua, 200), expires.Format("2006-01-02 15:04:05"))
	return token, expires, err
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type sessionInfo struct {
	UserID   int
	Username string
	Role     string
	Name     string
}

func lookupSession(token string) (*sessionInfo, bool) {
	if token == "" {
		return nil, false
	}
	var s sessionInfo
	var expStr string
	err := db.QueryRow(`SELECT user_id, username, role, name, expires_at FROM sessions WHERE token_hash=?`, hashToken(token)).
		Scan(&s.UserID, &s.Username, &s.Role, &s.Name, &expStr)
	if err != nil {
		return nil, false
	}
	exp, err := time.ParseInLocation("2006-01-02 15:04:05", expStr, time.Local)
	if err != nil {
		exp, _ = time.Parse(time.RFC3339, expStr)
	}
	if time.Now().After(exp) {
		db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
		return nil, false
	}
	// sliding expiration
	newExp := time.Now().Add(sessionTTL())
	db.Exec(`UPDATE sessions SET last_seen=datetime('now','localtime'), expires_at=? WHERE token_hash=?`,
		newExp.Format("2006-01-02 15:04:05"), hashToken(token))
	return &s, true
}

func sessionTokenFromRequest(r *http.Request) string {
	if c, err := r.Cookie(sessionCookieName()); err == nil && c.Value != "" {
		return c.Value
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if t := r.Header.Get("X-Session-Token"); t != "" {
		return t
	}
	return ""
}

func setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(),
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure: true when behind HTTPS — optional via env
		Secure: strings.EqualFold(os.Getenv("SESSION_SECURE"), "1") || strings.EqualFold(os.Getenv("SESSION_SECURE"), "true") ||
			getSetting("session_secure", "") == "1" || getSetting("session_secure", "") == "true",
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func destroySession(token string) {
	if token == "" {
		return
	}
	db.Exec(`DELETE FROM sessions WHERE token_hash=?`, hashToken(token))
}

// requireAuth wraps handlers that need a logged-in user. Admin-only if needAdmin.
func requireAuth(needAdmin bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := sessionTokenFromRequest(r)
		s, ok := lookupSession(tok)
		if !ok {
			// fallback: soft mode for gradual rollout — allow if SOFT_AUTH=1 and legacy header
			if softAuthEnabled() {
				if u := strings.TrimSpace(r.Header.Get("X-Oncall-User")); u != "" {
					var role, name string
					var id int
					err := db.QueryRow(`SELECT id, role, name FROM users WHERE username=? OR name=? LIMIT 1`, u, u).Scan(&id, &role, &name)
					if err == nil {
						if needAdmin && role != "admin" {
							http.Error(w, `{"error":"admin required"}`, http.StatusForbidden)
							return
						}
						r.Header.Set("X-Auth-Username", u)
						r.Header.Set("X-Auth-Role", role)
						r.Header.Set("X-Auth-Name", name)
						next(w, r)
						return
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"unauthorized","code":"SESSION_REQUIRED"}`, http.StatusUnauthorized)
			return
		}
		if needAdmin && s.Role != "admin" {
			http.Error(w, `{"error":"admin required"}`, http.StatusForbidden)
			return
		}
		r.Header.Set("X-Auth-Username", s.Username)
		r.Header.Set("X-Auth-Role", s.Role)
		r.Header.Set("X-Auth-Name", s.Name)
		next(w, r)
	}
}

func softAuthEnabled() bool {
	v := strings.TrimSpace(os.Getenv("SOFT_AUTH"))
	if v == "" {
		return true // default soft during transition
	}
	return v == "1" || strings.EqualFold(v, "true")
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	tok := sessionTokenFromRequest(r)
	destroySession(tok)
	clearSessionCookie(w)
	// clear legacy cookies
	for _, n := range []string{"oncall_user", "oncall_name", "oncall_role"} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleSessionMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	tok := sessionTokenFromRequest(r)
	s, ok := lookupSession(tok)
	if !ok {
		http.Error(w, `{"error":"no session"}`, http.StatusUnauthorized)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":       s.UserID,
		"username": s.Username,
		"name":     s.Name,
		"role":     s.Role,
	})
}

// --- simple login rate limit (in-memory) ---
var loginAttempts = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: make(map[string][]time.Time)}

func loginRateLimited(ip string) bool {
	loginAttempts.Lock()
	defer loginAttempts.Unlock()
	now := time.Now()
	cut := now.Add(-15 * time.Minute)
	arr := loginAttempts.m[ip]
	var kept []time.Time
	for _, t := range arr {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	loginAttempts.m[ip] = kept
	return len(kept) >= 20 // 20 / 15 хв з однієї IP
}

func recordLoginAttempt(ip string) {
	loginAttempts.Lock()
	defer loginAttempts.Unlock()
	loginAttempts.m[ip] = append(loginAttempts.m[ip], time.Now())
}

func securityHeaders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		next(w, r)
	}
}

// purgeExpiredSessions — викликати періодично
func purgeExpiredSessions() {
	res, err := db.Exec(`DELETE FROM sessions WHERE expires_at < datetime('now','localtime')`)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Printf("sessions: purged %d expired", n)
		}
	}
}

// ensure daily_tasks.external_id for Jira link
func ensureTaskExternalID() {
	db.Exec(`ALTER TABLE daily_tasks ADD COLUMN external_id TEXT DEFAULT ''`)
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_external ON daily_tasks(external_id) WHERE external_id != '' AND external_id IS NOT NULL`)
}

// silent ignore if index fails on older sqlite
var _ = sql.ErrNoRows
