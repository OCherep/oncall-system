package main

import (
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3")

var db *sql.DB

type User struct {
	ID         int    `json:"id"`
	Username   string `json:"username"`
	Password   string `json:"password,omitempty"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	TeamRoleID *int   `json:"team_role_id"`
	TeamRole   string `json:"team_role"`
	IsOncall   bool   `json:"is_oncall"`
	SlackID    string `json:"slack_id,omitempty"`
	Email      string `json:"email,omitempty"`
	Phone      string `json:"phone,omitempty"`
}

type TeamRole struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type AbsenceType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

type AbsenceRequest struct {
	ID        int    `json:"id"`
	UserName  string `json:"user_name"`
	Type      string `json:"type"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Status    string `json:"status"`
}

type Shift struct {
	Date        string `json:"date"`
	PrimaryUser string `json:"primary_user"`
	BackupUser  string `json:"backup_user"`
}

type UserStat struct {
	Name            string `json:"name"`
	PrimaryCount    int    `json:"primary_count"`
	BackupCount     int    `json:"backup_count"`
	IncidentMinutes int    `json:"incident_minutes"`
}

type IncidentReport struct {
	ID              int    `json:"id,omitempty"`
	UserName        string `json:"user_name"`
	Date            string `json:"date"`
	Type            string `json:"type"`
	DurationMinutes int    `json:"duration_minutes"`
	Description     string `json:"description"`
	CreatedAt       string `json:"created_at,omitempty"`
	Role            string `json:"role,omitempty"`
	Status          string `json:"status,omitempty"`   // Нове | В роботі | На паузі | Вирішено | Архів
	Priority        string `json:"priority,omitempty"` // Звичайний / ...
	Source          string `json:"source,omitempty"`   // self | team_lead | jira | bot
	TotalMinutes    int    `json:"total_minutes,omitempty"`
	CreatedBy         string `json:"created_by,omitempty"`
	ReportedFor       string `json:"reported_for,omitempty"`
	ExternalID        string `json:"external_id,omitempty"`
	ConvertedToTaskID int    `json:"converted_to_task_id,omitempty"`
	ReporterName      string `json:"reporter_name,omitempty"`
	ReporterEmail     string `json:"reporter_email,omitempty"`
	ReporterSlack     string `json:"reporter_slack,omitempty"`
	AssignedAt        string `json:"assigned_at,omitempty"`
	FactMinutes       int    `json:"fact_minutes,omitempty"` // обчислений факт (хв)
}

// TaskAssignee — виконавець задачі з окремим обліком часу


// Status: Нова | У роботі | На паузі | До перевірки | Виконана | Перевідкрита | Архів
// User flow: Нова → У роботі ⇄ На паузі → До перевірки → Виконана
// Unassigned executor (empty user_name) = «на розгляді» — only admin can change status
type DailyTask struct {
	ID              int    `json:"id,omitempty"`
	UserName        string `json:"user_name"` // виконавець; порожнє = «на розгляді»
	Date            string `json:"date"`
	TaskDescription string `json:"task_description"`
	Status          string `json:"status"`
	Priority        string `json:"priority"`
	WorkStartedAt   string `json:"work_started_at,omitempty"`
	TotalMinutes    int    `json:"total_minutes"`
	CreatedAt       string `json:"created_at,omitempty"`
	VisibleFrom     string `json:"visible_from,omitempty"`
	DueDate         string `json:"due_date,omitempty"`
	CreatedBy       string `json:"created_by,omitempty"`
	Responsible     string         `json:"responsible,omitempty"` // відповідальна особа
	Assignees       []TaskAssignee `json:"assignees,omitempty"`
}

// TaskAssignee — виконавець з окремим обліком часу
type TaskAssignee struct {
	UserName      string `json:"user_name"`
	TotalMinutes  int    `json:"total_minutes"`
	WorkStartedAt string `json:"work_started_at,omitempty"`
}

type TableStat struct {
	TableName  string `json:"table_name"`
	RowCount   int    `json:"row_count"`
	LastAction string `json:"last_action"`
	LastUpdate string `json:"last_update"`
}

type AuditLog struct {
	ID        int    `json:"id"`
	UserName  string `json:"user_name"`
	Action    string `json:"action"`
	IP        string `json:"ip"`
	Details   string `json:"details"`
	Timestamp string `json:"timestamp"`
}

var (
	auditFileMu   sync.Mutex
	auditLogPath  = envOr("AUDIT_LOG_PATH", "/var/log/oncall-app/app.log")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func logAudit(user, action, ip, details string) {
	db.Exec(`INSERT INTO audit_logs (user_name, action, ip, details, timestamp) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		user, action, ip, details)
	appendAuditFile(user, action, ip, details)
}

// appendAuditFile дублює audit_logs у файл на хості (volume /var/log/oncall-app)
func appendAuditFile(user, action, ip, details string) {
	auditFileMu.Lock()
	defer auditFileMu.Unlock()
	path := auditLogPath
	if i := strings.LastIndex(path, "/"); i > 0 {
		_ = os.MkdirAll(path[:i], 0755)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	ts := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(f, "%s | user=%s | action=%s | ip=%s | %s\n", ts, user, action, ip, details)
}

// Real client IP behind nginx: X-Forwarded-For / X-Real-IP, else RemoteAddr host
func clientIP(r *http.Request) string {
	if x := r.Header.Get("X-Forwarded-For"); x != "" {
		return strings.TrimSpace(strings.Split(x, ",")[0])
	}
	if x := r.Header.Get("X-Real-IP"); x != "" {
		return strings.TrimSpace(x)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}


// ipAllowed: empty allowlist = allow all (bootstrap). Always allow loopback.
func ipAllowed(ip string) bool {
	ip = strings.TrimSpace(ip)
	if ip == "" || ip == "127.0.0.1" || ip == "::1" || ip == "localhost" {
		return true
	}
	// strip port if present
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM allowed_ips WHERE enabled=1`).Scan(&n)
	if n == 0 {
		return true // no rules → open
	}
	// exact match or simple prefix (CIDR without full parser: store exact IP or "x.x.x.x/32")
	rows, err := db.Query(`SELECT cidr FROM allowed_ips WHERE enabled=1`)
	if err != nil {
		return true
	}
	defer rows.Close()
	for rows.Next() {
		var cidr string
		rows.Scan(&cidr)
		cidr = strings.TrimSpace(cidr)
		if cidr == ip {
			return true
		}
		// support trailing .* wildcard e.g. 217.24.169.*
		if strings.HasSuffix(cidr, ".*") {
			pref := strings.TrimSuffix(cidr, ".*")
			if strings.HasPrefix(ip, pref) {
				return true
			}
		}
		// support /24 style by prefix of 3 octets: 1.2.3.0/24
		if strings.Contains(cidr, "/") {
			parts := strings.SplitN(cidr, "/", 2)
			base := parts[0]
			oct := strings.Split(base, ".")
			ipo := strings.Split(ip, ".")
			if len(oct) == 4 && len(ipo) == 4 {
				if parts[1] == "24" && oct[0] == ipo[0] && oct[1] == ipo[1] && oct[2] == ipo[2] {
					return true
				}
				if parts[1] == "16" && oct[0] == ipo[0] && oct[1] == ipo[1] {
					return true
				}
				if parts[1] == "8" && oct[0] == ipo[0] {
					return true
				}
				if parts[1] == "32" && base == ip {
					return true
				}
			}
		}
	}
	return false
}

func withIPAllow(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !ipAllowed(ip) {
			logAudit("-", "IP_BLOCKED", ip, r.URL.Path)
			http.Error(w, "Access denied: IP not in allowlist", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}


func cleanupDuplicateIncidentTasks() {
	rows, err := db.Query(`SELECT id, task_description FROM daily_tasks WHERE task_description LIKE '[зі звернення #%]' ORDER BY id ASC`)
	if err != nil || rows == nil {
		return
	}
	keep := map[int]int{} // incidentID -> first task id
	var drop []int
	for rows.Next() {
		var tid int
		var desc string
		if rows.Scan(&tid, &desc) != nil {
			continue
		}
		var iid int
		if _, err := fmt.Sscanf(desc, "[зі звернення #%d]", &iid); err != nil || iid <= 0 {
			continue
		}
		if first, ok := keep[iid]; ok {
			if tid != first {
				drop = append(drop, tid)
			}
		} else {
			keep[iid] = tid
		}
	}
	rows.Close()
	for _, tid := range drop {
		db.Exec(`DELETE FROM comments WHERE entity_type='task' AND entity_id=?`, tid)
		db.Exec(`DELETE FROM task_assignees WHERE task_id=?`, tid)
		db.Exec(`DELETE FROM daily_tasks WHERE id=?`, tid)
		log.Printf("cleanup: removed duplicate task #%d (same incident source)", tid)
	}
	// ensure incidents point at kept task
	for iid, tid := range keep {
		db.Exec(`UPDATE incidents SET converted_to_task_id=?, status=CASE WHEN status IN ('Вирішено','Архів') THEN status ELSE 'Вирішено' END WHERE id=?`, tid, iid)
	}
}

func backfillConvertedTaskIDs() {
	rows, err := db.Query(`SELECT id, task_description, COALESCE(status,'Нова') FROM daily_tasks WHERE task_description LIKE '[зі звернення #%]' ORDER BY id ASC`)
	if err != nil || rows == nil {
		return
	}
	type pair struct {
		tid, iid int
		status   string
	}
	var pairs []pair
	for rows.Next() {
		var tid int
		var desc, st string
		if rows.Scan(&tid, &desc, &st) != nil {
			continue
		}
		var iid int
		if _, err := fmt.Sscanf(desc, "[зі звернення #%d]", &iid); err == nil && iid > 0 {
			pairs = append(pairs, pair{tid, iid, st})
		}
	}
	rows.Close()
	// group by incident: keep lowest task id, archive the rest
	first := map[int]int{}
	for _, p := range pairs {
		if keep, ok := first[p.iid]; !ok {
			first[p.iid] = p.tid
			db.Exec(`UPDATE incidents SET converted_to_task_id=?, status=CASE WHEN status IN ('Архів') THEN status ELSE 'Вирішено' END WHERE id=?`, p.tid, p.iid)
		} else if p.tid != keep {
			// duplicate task from same incident — archive (hide from boards)
			if p.status != "Архів" {
				db.Exec(`UPDATE daily_tasks SET status='Архів' WHERE id=?`, p.tid)
				addSystemComment("task", p.tid, fmt.Sprintf("Дублікат звернення #%d; основна задача #%d", p.iid, keep))
			}
		}
	}
}

func initDB() {
	var err error
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data/oncall.db"
	}
	// ensure directory exists so volume mount works across rebuilds
	if i := strings.LastIndex(dbPath, "/"); i > 0 {
		_ = os.MkdirAll(dbPath[:i], 0755)
	}
	db, err = sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=15000&_txlock=immediate")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sqlite: %s", dbPath)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL,
			name TEXT NOT NULL,
			role TEXT DEFAULT 'user',
			team_role_id INTEGER,
			is_oncall INTEGER DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS team_roles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS absence_types (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL,
			code TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS shifts (
			date TEXT PRIMARY KEY,
			primary_user TEXT,
			backup_user TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS absences (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_name TEXT,
			type TEXT,
			start_date TEXT,
			end_date TEXT,
			status TEXT DEFAULT 'Pending'
		)`,
		`CREATE TABLE IF NOT EXISTS incidents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_name TEXT,
			date TEXT,
			type TEXT,
			duration_minutes INTEGER,
			description TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS daily_tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_name TEXT,
			date TEXT,
			task_description TEXT,
			status TEXT DEFAULT 'Нова',
			priority TEXT DEFAULT 'Базова',
			work_started_at TEXT,
			total_minutes INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			visible_from TEXT,
			due_date TEXT,
			created_by TEXT,
			responsible TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_name TEXT,
			action TEXT,
			ip TEXT,
			details TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS app_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			level TEXT,
			message TEXT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS table_tracker (
			table_name TEXT PRIMARY KEY,
			row_count INTEGER,
			last_action TEXT,
			last_update DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS comments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			entity_type TEXT NOT NULL,
			entity_id INTEGER NOT NULL,
			author_name TEXT,
			body TEXT NOT NULL,
			is_system INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS task_status_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL,
			status TEXT NOT NULL,
			started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			ended_at DATETIME,
			minutes INTEGER DEFAULT 0,
			changed_by TEXT DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS allowed_ips (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			cidr TEXT NOT NULL UNIQUE,
			label TEXT DEFAULT '',
			enabled INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			log.Printf("schema: %v", err)
		}
	}
	db.Exec(`ALTER TABLE users ADD COLUMN email TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE users ADD COLUMN phone TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE users ADD COLUMN slack_id TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE incidents ADD COLUMN reporter_name TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE incidents ADD COLUMN reporter_email TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE incidents ADD COLUMN reporter_slack TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE incidents ADD COLUMN assigned_at TEXT DEFAULT ''`)


	db.Exec("ALTER TABLE incidents ADD COLUMN created_at DATETIME DEFAULT CURRENT_TIMESTAMP")
	db.Exec("ALTER TABLE incidents ADD COLUMN status TEXT DEFAULT 'Нове'")
	db.Exec("ALTER TABLE incidents ADD COLUMN priority TEXT DEFAULT 'Звичайний'")
	db.Exec("ALTER TABLE incidents ADD COLUMN source TEXT DEFAULT 'self'")
	db.Exec("ALTER TABLE incidents ADD COLUMN total_minutes INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE incidents ADD COLUMN created_by TEXT")
	db.Exec("ALTER TABLE incidents ADD COLUMN reported_for TEXT")
	db.Exec("ALTER TABLE incidents ADD COLUMN external_id TEXT DEFAULT ''")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN status TEXT DEFAULT 'Нова'")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN priority TEXT DEFAULT 'Базова'")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN work_started_at TEXT")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN total_minutes INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN created_at DATETIME DEFAULT CURRENT_TIMESTAMP")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN visible_from TEXT")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN due_date TEXT")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN created_by TEXT")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN responsible TEXT")
	db.Exec("ALTER TABLE users ADD COLUMN is_oncall INTEGER DEFAULT 1")
	db.Exec("ALTER TABLE users ADD COLUMN slack_id TEXT DEFAULT ''")
		db.Exec("ALTER TABLE incidents ADD COLUMN converted_to_task_id INTEGER DEFAULT 0")
	db.Exec(`CREATE TABLE IF NOT EXISTS task_assignees (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id INTEGER NOT NULL,
		user_name TEXT NOT NULL,
		total_minutes INTEGER DEFAULT 0,
		work_started_at TEXT,
		UNIQUE(task_id, user_name)
	)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_task_assignees_task ON task_assignees(task_id)")
	// backfill converted_to_task_id from existing tasks (cursor closed before UPDATE)
	backfillConvertedTaskIDs()
	ensureSessionsTable()
	ensureAppSettingsTable()
	ensureTaskExternalID()
	go func() {
		for {
			time.Sleep(1 * time.Hour)
			purgeExpiredSessions()
		}
	}()
db.Exec("CREATE INDEX IF NOT EXISTS idx_comments_entity ON comments(entity_type, entity_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_incidents_source ON incidents(source)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_incidents_date ON incidents(date)")

	tables := []string{"users", "team_roles", "absence_types", "shifts", "absences", "incidents", "daily_tasks", "task_assignees", "audit_logs", "app_logs", "comments"}
	for _, t := range tables {
		db.Exec("INSERT OR IGNORE INTO table_tracker (table_name, last_action, last_update) VALUES (?, 'INIT', CURRENT_TIMESTAMP)", t)
		for _, act := range []string{"INSERT", "UPDATE", "DELETE"} {
			trig := "trig_" + t + "_" + act
			q := "CREATE TRIGGER IF NOT EXISTS " + trig + " AFTER " + act + " ON " + t +
				" BEGIN INSERT INTO table_tracker (table_name, last_action, last_update) VALUES ('" + t + "', '" + act +
				"', CURRENT_TIMESTAMP) ON CONFLICT(table_name) DO UPDATE SET last_action='" + act + "', last_update=CURRENT_TIMESTAMP; END;"
			db.Exec(q)
		}
	}

	var cnt int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&cnt)
	if cnt == 0 {
		db.Exec(`INSERT INTO users (username, password, name, role, is_oncall) VALUES ('admin', 'admin', 'Admin', 'admin', 0)`)
		db.Exec(`INSERT INTO absence_types (name, code) VALUES ('Відпустка', 'vacation'), ('Лікарняний', 'sick'), ('Вихідний', 'dayoff')`)
		log.Println("seeded default admin / admin")
	}
}

func main() {
	initDB()
	defer db.Close()

	http.HandleFunc("/api/login", withIPAllow(securityHeaders(handleLogin)))
	http.HandleFunc("/api/logout", withIPAllow(securityHeaders(handleLogout)))
	http.HandleFunc("/api/session/me", withIPAllow(securityHeaders(handleSessionMe)))
	http.HandleFunc("/api/admin/jira/import", withIPAllow(securityHeaders(handleJiraImport)))
	http.HandleFunc("/api/on-grid", withIPAllow(securityHeaders(handleOnGridPublic)))
	http.HandleFunc("/api/admin/settings", withIPAllow(securityHeaders(handleAppSettings)))
	http.HandleFunc("/api/admin/daily-board", withIPAllow(securityHeaders(handleDailyBoard)))
	http.HandleFunc("/api/data", withIPAllow(handleGetData))
	http.HandleFunc("/api/request-absence", withIPAllow(handleRequestAbsence))
	http.HandleFunc("/api/incidents", withIPAllow(handleIncidents))
	http.HandleFunc("/api/daily-tasks", withIPAllow(handleDailyTasks))

	http.HandleFunc("/api/admin/users", withIPAllow(handleAdminUsers))
	http.HandleFunc("/api/admin/roles", withIPAllow(handleAdminRoles))
	http.HandleFunc("/api/admin/team-roles", withIPAllow(handleAdminRoles))
	http.HandleFunc("/api/admin/absence-types", withIPAllow(handleAdminAbsenceTypes))
	http.HandleFunc("/api/admin/requests", withIPAllow(handleAdminRequests))
	http.HandleFunc("/api/admin/logs", withIPAllow(handleAdminLogs))
	http.HandleFunc("/api/admin/project/audit-logs", withIPAllow(handleAdminLogs))
	http.HandleFunc("/api/admin/tasks", withIPAllow(handleAdminTasks))
	http.HandleFunc("/api/admin/project/unlock", withIPAllow(handleDBUnlock))
	http.HandleFunc("/api/admin/project/db-stats", withIPAllow(handleDBStats))
	http.HandleFunc("/api/admin/project/app-logs", withIPAllow(handleAppLogs))
	http.HandleFunc("/api/admin/project/table", withIPAllow(handleTableInspect))
	http.HandleFunc("/api/admin/project/query", withIPAllow(handleReadOnlyQuery))
	http.HandleFunc("/api/admin/regenerate-shifts", withIPAllow(handleRegenerateShifts))
	http.HandleFunc("/api/comments", withIPAllow(handleComments))
	http.HandleFunc("/api/task-status-log", withIPAllow(handleTaskStatusLog))
	http.HandleFunc("/api/admin/queues", withIPAllow(handleAdminQueues))
	http.HandleFunc("/api/admin/badges", withIPAllow(handleAdminBadges))
	http.HandleFunc("/api/admin/allowed-ips", withIPAllow(handleAdminAllowedIPs))

	// Зовнішні інтеграції: Jira / боти / Slack
	http.HandleFunc("/api/webhooks/incidents", withIPAllow(handleWebhookIncidents))
	http.HandleFunc("/api/webhooks/health", withIPAllow(handleWebhookHealth))

	fs := http.FileServer(http.Dir("./static"))
	http.HandleFunc("/", withIPAllow(func(w http.ResponseWriter, r *http.Request) {
		fs.ServeHTTP(w, r)
	}))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
