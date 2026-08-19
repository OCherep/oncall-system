package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

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
}

type TableStat struct {
	TableName  string `json:"table_name"`
	RowCount   int    `json:"row_count"`
	LastAction string `json:"last_action"`
	LastUpdate string `json:"last_update"`
}

type AuditLog struct {
	ID        int    `json:"id"`
	Timestamp string `json:"timestamp"`
	UserName  string `json:"user_name"`
	Action    string `json:"action"`
	IP        string `json:"ip"`
	Details   string `json:"details"`
}

type AppLog struct {
	ID        int    `json:"id"`
	Timestamp string `json:"timestamp"`
	App       string `json:"app"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

func main() {
	var err error
	db, err = sql.Open("sqlite3", "./oncall.db?_journal_mode=WAL")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	initDB()

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("./static"))))

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/admin", handleAdminPage)

	http.HandleFunc("/api/data", handleGetData)
	http.HandleFunc("/api/login", handleLogin)
	http.HandleFunc("/api/request-absence", handleRequestAbsence)
	http.HandleFunc("/api/incidents", handleIncidents)

	http.HandleFunc("/api/admin/users", handleAdminUsers)
	http.HandleFunc("/api/admin/team-roles", handleAdminTeamRoles)
	http.HandleFunc("/api/admin/absence-types", handleAdminAbsenceTypes)
	http.HandleFunc("/api/admin/requests", handleAdminRequests)

	http.HandleFunc("/api/admin/project/db-stats", handleDBStats)
	http.HandleFunc("/api/admin/project/query", handleReadOnlyQuery)
	http.HandleFunc("/api/admin/project/audit-logs", handleAuditLogs)
	http.HandleFunc("/api/admin/project/app-logs", handleAppLogs)

	logAppEvent("OnCall Core", "INFO", "Сервер системного адміністрування успішно запущено на порту 8083")
	fmt.Println("Server running at http://localhost:8083")
	log.Fatal(http.ListenAndServe(":8083", nil))
}

func initDB() {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            username TEXT UNIQUE,
            password TEXT,
            name TEXT,
            role TEXT,
            team_role_id INTEGER,
            is_oncall INTEGER DEFAULT 1
        );`,
		`CREATE TABLE IF NOT EXISTS team_roles (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT UNIQUE
        );`,
		`CREATE TABLE IF NOT EXISTS absence_types (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT UNIQUE,
            code TEXT UNIQUE
        );`,
		`CREATE TABLE IF NOT EXISTS shifts (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            date TEXT UNIQUE,
            primary_user TEXT,
            backup_user TEXT
        );`,
		`CREATE TABLE IF NOT EXISTS absences (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            user_name TEXT,
            type TEXT,
            start_date TEXT,
            end_date TEXT,
            status TEXT DEFAULT 'Approved'
        );`,
		`CREATE TABLE IF NOT EXISTS incidents (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            user_name TEXT NOT NULL,
            date TEXT NOT NULL,
            type TEXT NOT NULL,
            duration_minutes INTEGER NOT NULL,
            description TEXT NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
            user_name TEXT,
            action TEXT,
            ip TEXT,
            details TEXT
        );`,
		`CREATE TABLE IF NOT EXISTS app_logs (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
            app TEXT,
            level TEXT,
            message TEXT
        );`,
		`CREATE TABLE IF NOT EXISTS table_tracker (
            table_name TEXT PRIMARY KEY,
            last_action TEXT,
            last_update DATETIME DEFAULT CURRENT_TIMESTAMP
        );`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			log.Fatalf("Failed to init db: %v", err)
		}
	}

	db.Exec("ALTER TABLE absences ADD COLUMN status TEXT DEFAULT 'Approved';")

	tables := []string{"users", "team_roles", "absence_types", "shifts", "absences", "incidents", "audit_logs", "app_logs"}
	for _, t := range tables {
		db.Exec("INSERT OR IGNORE INTO table_tracker (table_name, last_action, last_update) VALUES (?, 'INIT', CURRENT_TIMESTAMP)", t)
		createTriggersForTable(t)
	}

	var absTypeCount int
	db.QueryRow("SELECT COUNT(*) FROM absence_types").Scan(&absTypeCount)
	if absTypeCount == 0 {
		db.Exec("INSERT INTO absence_types (name, code) VALUES ('Відпустка', 'Vacation')")
		db.Exec("INSERT INTO absence_types (name, code) VALUES ('Day Off', 'DayOff')")
		db.Exec("INSERT INTO absence_types (name, code) VALUES ('Sick Day', 'SickDay')")
	}

	var rolesCount int
	db.QueryRow("SELECT COUNT(*) FROM team_roles").Scan(&rolesCount)
	if rolesCount == 0 {
		db.Exec("INSERT INTO team_roles (name) VALUES ('DevOps Engineer')")
		db.Exec("INSERT INTO team_roles (name) VALUES ('Backend Developer')")
		db.Exec("INSERT INTO team_roles (name) VALUES ('Frontend Developer')")
		db.Exec("INSERT INTO team_roles (name) VALUES ('QA Engineer')")
		db.Exec("INSERT INTO team_roles (name) VALUES ('Team Lead')")
		db.Exec("INSERT INTO team_roles (name) VALUES ('Project Manager')")
	}

	var userCount int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount)
	if userCount == 0 {
		db.Exec("INSERT INTO users (username, password, name, role, team_role_id, is_oncall) VALUES ('admin', 'admin', 'Адміністратор', 'admin', 5, 0)")
		db.Exec("INSERT INTO users (username, password, name, role, team_role_id, is_oncall) VALUES ('pm', '1234', 'Олена PM', 'user', 6, 0)")
		db.Exec("INSERT INTO users (username, password, name, role, team_role_id, is_oncall) VALUES ('dev1', '1234', 'Олексій', 'user', 1, 1)")
	}
}

func createTriggersForTable(tableName string) {
	actions := []string{"INSERT", "UPDATE", "DELETE"}
	for _, act := range actions {
		trigName := fmt.Sprintf("trig_%s_%s", tableName, strings.ToLower(act))
		query := fmt.Sprintf(`
            CREATE TRIGGER IF NOT EXISTS %s AFTER %s ON %s
            BEGIN
                INSERT INTO table_tracker (table_name, last_action, last_update) 
                VALUES ('%s', '%s', CURRENT_TIMESTAMP)
                ON CONFLICT(table_name) DO UPDATE SET last_action='%s', last_update=CURRENT_TIMESTAMP;
            END;`, trigName, act, tableName, tableName, act, act)
		db.Exec(query)
	}
}

func logAudit(userName, action, ip, details string) {
	db.Exec("INSERT INTO audit_logs (user_name, action, ip, details) VALUES (?, ?, ?, ?)", userName, action, ip, details)
}

func logAppEvent(app, level, message string) {
	db.Exec("INSERT INTO app_logs (app, level, message) VALUES (?, ?, ?)", app, level, message)
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	template.Must(template.ParseFiles("static/index.html")).Execute(w, nil)
}

func handleAdminPage(w http.ResponseWriter, r *http.Request) {
	template.Must(template.ParseFiles("static/admin.html")).Execute(w, nil)
}
