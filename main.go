package main

import (
	"database/sql"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

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
	SlackID    string `json:"slack_id,omitempty"` // Slack member ID (U…) для особистих сповіщень
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
	CreatedBy       string `json:"created_by,omitempty"`
	ReportedFor     string `json:"reported_for,omitempty"`
	ExternalID      string `json:"external_id,omitempty"` // JIRA key (OPS-123) для двостороннього sync
	ConvertedToTaskID int   `json:"converted_to_task_id,omitempty"`
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

func logAudit(user, action, ip, details string) {
	db.Exec(`INSERT INTO audit_logs (user_name, action, ip, details, timestamp) VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		user, action, ip, details)
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
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			log.Printf("schema: %v", err)
		}
	}

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
	// backfill converted_to_task_id from existing tasks
	db.Exec(`UPDATE incidents SET converted_to_task_id = (
		SELECT dt.id FROM daily_tasks dt WHERE dt.task_description LIKE '[зі звернення #' || incidents.id || ']%' LIMIT 1
	) WHERE COALESCE(converted_to_task_id,0)=0
	  AND EXISTS (SELECT 1 FROM daily_tasks dt WHERE dt.task_description LIKE '[зі звернення #' || incidents.id || ']%')`)
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

	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/data", handleGetData)
	http.HandleFunc("/api/request-absence", handleRequestAbsence)
	http.HandleFunc("/api/incidents", handleIncidents)
	http.HandleFunc("/api/daily-tasks", handleDailyTasks)

	http.HandleFunc("/api/admin/users", handleAdminUsers)
	http.HandleFunc("/api/admin/roles", handleAdminRoles)
	http.HandleFunc("/api/admin/team-roles", handleAdminRoles) // alias for admin UI
	http.HandleFunc("/api/admin/absence-types", handleAdminAbsenceTypes)
	http.HandleFunc("/api/admin/requests", handleAdminRequests)
	http.HandleFunc("/api/admin/logs", handleAdminLogs)
	http.HandleFunc("/api/admin/project/audit-logs", handleAdminLogs) // alias
	http.HandleFunc("/api/admin/tasks", handleAdminTasks)
	http.HandleFunc("/api/admin/project/unlock", handleDBUnlock)
	http.HandleFunc("/api/admin/project/db-stats", handleDBStats)
	http.HandleFunc("/api/admin/project/app-logs", handleAppLogs)
	http.HandleFunc("/api/admin/project/table", handleTableInspect)
	http.HandleFunc("/api/admin/project/query", handleReadOnlyQuery)
	http.HandleFunc("/api/admin/regenerate-shifts", handleRegenerateShifts)
	http.HandleFunc("/api/comments", handleComments)
	http.HandleFunc("/api/admin/queues", handleAdminQueues)

	// Зовнішні інтеграції: Jira / боти / Slack
	http.HandleFunc("/api/webhooks/incidents", handleWebhookIncidents)
	http.HandleFunc("/api/webhooks/health", handleWebhookHealth)

	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
