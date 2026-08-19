package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"

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
}

// Status: Нова | У роботі | До перевірки | Виконана | Архів | Перевідкрита
// Priority: Надкритична | Термінова | Базова | Техборг | У шухляду
type DailyTask struct {
	ID              int    `json:"id,omitempty"`
	UserName        string `json:"user_name"`
	Date            string `json:"date"`
	TaskDescription string `json:"task_description"`
	Status          string `json:"status"`
	Priority        string `json:"priority"`
	WorkStartedAt   string `json:"work_started_at,omitempty"`
	TotalMinutes    int    `json:"total_minutes"`
	CreatedAt       string `json:"created_at,omitempty"`
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

func initDB() {
	var err error
	db, err = sql.Open("sqlite3", "./oncall.db?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		log.Fatal(err)
	}
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
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
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
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			log.Printf("schema: %v", err)
		}
	}

	db.Exec("ALTER TABLE incidents ADD COLUMN created_at DATETIME DEFAULT CURRENT_TIMESTAMP")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN status TEXT DEFAULT 'Нова'")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN priority TEXT DEFAULT 'Базова'")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN work_started_at TEXT")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN total_minutes INTEGER DEFAULT 0")
	db.Exec("ALTER TABLE daily_tasks ADD COLUMN created_at DATETIME DEFAULT CURRENT_TIMESTAMP")
	db.Exec("ALTER TABLE users ADD COLUMN is_oncall INTEGER DEFAULT 1")

	tables := []string{"users", "team_roles", "absence_types", "shifts", "absences", "incidents", "daily_tasks", "audit_logs", "app_logs"}
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
	http.HandleFunc("/api/admin/absence-types", handleAdminAbsenceTypes)
	http.HandleFunc("/api/admin/requests", handleAdminRequests)
	http.HandleFunc("/api/admin/logs", handleAdminLogs)
	http.HandleFunc("/api/admin/tasks", handleAdminTasks)
	http.HandleFunc("/api/admin/project/db-stats", handleDBStats)
	http.HandleFunc("/api/admin/project/query", handleReadOnlyQuery)
	http.HandleFunc("/api/admin/regenerate-shifts", handleRegenerateShifts)

	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/", fs)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
