package main

import (
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "strconv"
    "strings"
    "time"

    _ "github.com/mattn/go-sqlite3"
)

// --- СТРУКТУРИ ДАНИХ ---

type User struct {
    ID         int    `json:"id"`
    Name       string `json:"name"`
    Username   string `json:"username"`
    Password   string `json:"password,omitempty"`
    Role       string `json:"role"`
    TeamRoleID *int   `json:"team_role_id"`
    TeamRole   string `json:"team_role,omitempty"`
    IsOnCall   bool   `json:"is_oncall"`
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
    UserID    int    `json:"user_id,omitempty"`
    UserName  string `json:"user_name"`
    Type      string `json:"type"`
    StartDate string `json:"start_date"`
    EndDate   string `json:"end_date"`
    Status    string `json:"status"`
}

type IncidentReport struct {
    ID              int    `json:"id,omitempty"`
    UserName        string `json:"user_name"`
    Date            string `json:"date"`
    Type            string `json:"type"`
    DurationMinutes int    `json:"duration_minutes"`
    Description     string `json:"description"`
}

type Shift struct {
    PrimaryUser string `json:"primary_user"`
    BackupUser  string `json:"backup_user"`
}

type UserStat struct {
    Name            string `json:"name"`
    PrimaryCount    int    `json:"primary_count"`
    BackupCount     int    `json:"backup_count"`
    IncidentMinutes int    `json:"incident_minutes"`
}

type DailyTask struct {
    UserName        string `json:"user_name"`
    TaskDescription string `json:"task_description"`
}

type AppLog struct {
    Timestamp string `json:"timestamp"`
    App       string `json:"app"`
    Level     string `json:"level"`
    Message   string `json:"message"`
}

type DBStat struct {
    TableName  string `json:"table_name"`
    RowCount   int    `json:"row_count"`
    LastAction string `json:"last_action"`
    LastUpdate string `json:"last_update"`
}

type AuditLog struct {
    Timestamp string `json:"timestamp"`
    UserName  string `json:"user_name"`
    Action    string `json:"action"`
    IP        string `json:"ip"`
    Details   string `json:"details"`
}

type QueryRequest struct {
    Query string `json:"query"`
}

var db *sql.DB

func main() {
    var err error
    db, err = sql.Open("sqlite3", "./oncall.db")
    if err != nil {
        log.Fatalf("Помилка відкриття БД: %v", err)
    }
    defer db.Close()

    initDB()

    // Статичні файли
    fs := http.FileServer(http.Dir("./static"))
    http.Handle("/static/", http.StripPrefix("/static/", fs))

    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/" {
            http.ServeFile(w, r, "./static/index.html")
            return
        }
        if r.URL.Path == "/admin" {
            http.ServeFile(w, r, "./static/admin.html")
            return
        }
        fs.ServeHTTP(w, r)
    })

    // Публічні та Користувацькі API
    http.HandleFunc("/api/login", handleLogin)
    http.HandleFunc("/api/data", handleClientData)
    http.HandleFunc("/api/incidents", handleIncidents)
    http.HandleFunc("/api/request-absence", handleRequestAbsence)

    // Адмінські API (CRUD)
    http.HandleFunc("/api/admin/users", handleUsers)
    http.HandleFunc("/api/admin/team-roles", handleTeamRoles)
    http.HandleFunc("/api/admin/absence-types", handleAbsenceTypes)
    http.HandleFunc("/api/admin/requests", handleAdminRequests)

    // Системні API Моніторингу
    http.HandleFunc("/api/admin/project/app-logs", handleAppLogs)
    http.HandleFunc("/api/admin/project/db-stats", handleDBStats)
    http.HandleFunc("/api/admin/project/query", handleSqlQuery)
    http.HandleFunc("/api/admin/project/audit-logs", handleAuditLogs)

    log.Println("Сервер успішно запущено на http://localhost:8083")
    if err := http.ListenAndServe(":8083", nil); err != nil {
        log.Fatalf("Помилка запуска сервера: %v", err)
    }
}

// --- ІНІЦІАЛІЗАЦІЯ БД ---

func initDB() {
    queries := []string{
        `CREATE TABLE IF NOT EXISTS team_roles (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT UNIQUE NOT NULL
        );`,
        `CREATE TABLE IF NOT EXISTS absence_types (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            code TEXT UNIQUE NOT NULL
        );`,
        `CREATE TABLE IF NOT EXISTS users (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            name TEXT NOT NULL,
            username TEXT UNIQUE NOT NULL,
            password TEXT NOT NULL,
            role TEXT NOT NULL DEFAULT 'user',
            team_role_id INTEGER,
            is_oncall BOOLEAN NOT NULL DEFAULT 1,
            FOREIGN KEY(team_role_id) REFERENCES team_roles(id)
        );`,
        `CREATE TABLE IF NOT EXISTS absence_requests (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            user_id INTEGER,
            user_name TEXT NOT NULL,
            type TEXT NOT NULL,
            start_date TEXT NOT NULL,
            end_date TEXT NOT NULL,
            status TEXT NOT NULL DEFAULT 'Approved'
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
    }

    for _, q := range queries {
        if _, err := db.Exec(q); err != nil {
            log.Fatalf("Помилка ініціалізації БД: %v", err)
        }
    }

    // Дефолтні дані для старту
    var count int
    db.QueryRow("SELECT COUNT(*) FROM team_roles").Scan(&count)
    if count == 0 {
        db.Exec("INSERT INTO team_roles (name) VALUES ('Team Lead'), ('Project Manager'), ('DevOps'), ('Backend Engineer'), ('QA')")
    }

    db.QueryRow("SELECT COUNT(*) FROM absence_types").Scan(&count)
    if count == 0 {
        db.Exec("INSERT INTO absence_types (name, code) VALUES ('Відпустка', 'vacation'), ('DayOff', 'dayoff'), ('SickDay', 'sickday')")
    }

    db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
    if count == 0 {
        db.Exec("INSERT INTO users (name, username, password, role, is_oncall) VALUES ('Administrator', 'admin', '1234', 'admin', 1)")
        db.Exec("INSERT INTO users (name, username, password, role, is_oncall) VALUES ('Dev User 1', 'dev1', '1234', 'user', 1)")
        db.Exec("INSERT INTO users (name, username, password, role, is_oncall) VALUES ('Project Manager', 'pm', '1234', 'user', 0)")
    }
}

// --- КОРИСТУВАЦЬКІ ЕНДПОЇНТИ ---

func handleLogin(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", 405)
        return
    }
    var req struct {
        Username string `json:"username"`
        Password string `json:"password"`
    }
    json.NewDecoder(r.Body).Decode(&req)

    var u User
    var teamRole sql.NullString
    err := db.QueryRow(`
        SELECT u.id, u.name, u.username, u.role, u.team_role_id, tr.name, u.is_oncall 
        FROM users u 
        LEFT JOIN team_roles tr ON u.team_role_id = tr.id 
        WHERE u.username = ? AND u.password = ?`, req.Username, req.Password).
        Scan(&u.ID, &u.Name, &u.Username, &u.Role, &u.TeamRoleID, &teamRole, &u.IsOnCall)

    if err != nil {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusUnauthorized)
        json.NewEncoder(w).Encode(map[string]string{"error": "Невірні облікові дані"})
        return
    }

    if teamRole.Valid {
        u.TeamRole = teamRole.String
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(u)
}

func handleClientData(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    yearStr := r.URL.Query().Get("year")
    monthStr := r.URL.Query().Get("month")

    year, _ := strconv.Atoi(yearStr)
    month, _ := strconv.Atoi(monthStr)

    if year == 0 {
        year = time.Now().Year()
    }
    if month == 0 {
        month = int(time.Now().Month())
    }

    // Типи відсутностей
    rowsType, _ := db.Query("SELECT id, name, code FROM absence_types")
    var absenceTypes []AbsenceType
    if rowsType != nil {
        for rowsType.Next() {
            var at AbsenceType
            rowsType.Scan(&at.ID, &at.Name, &at.Code)
            absenceTypes = append(absenceTypes, at)
        }
        rowsType.Close()
    }

    // Затверджені відсутності
    rowsAbs, _ := db.Query("SELECT id, user_name, type, start_date, end_date, status FROM absence_requests WHERE status = 'Approved'")
    var absences []AbsenceRequest
    if rowsAbs != nil {
        for rowsAbs.Next() {
            var a AbsenceRequest
            rowsAbs.Scan(&a.ID, &a.UserName, &a.Type, &a.StartDate, &a.EndDate, &a.Status)
            absences = append(absences, a)
        }
        rowsAbs.Close()
    }

    // Отримання інцидентів за місяць
    monthPattern := fmt.Sprintf("%04d-%02d-%%", year, month)
    rowsInc, _ := db.Query("SELECT user_name, date, type, duration_minutes, description FROM incidents WHERE date LIKE ?", monthPattern)
    incidents := make(map[string][]IncidentReport)
    statsMap := make(map[string]*UserStat)

    if rowsInc != nil {
        for rowsInc.Next() {
            var inc IncidentReport
            rowsInc.Scan(&inc.UserName, &inc.Date, &inc.Type, &inc.DurationMinutes, &inc.Description)
            incidents[inc.Date] = append(incidents[inc.Date], inc)

            if _, exists := statsMap[inc.UserName]; !exists {
                statsMap[inc.UserName] = &UserStat{Name: inc.UserName}
            }
            statsMap[inc.UserName].IncidentMinutes += inc.DurationMinutes
        }
        rowsInc.Close()
    }

    // Генерація тестового розкладу чергувань
    rowsUsers, _ := db.Query("SELECT name FROM users WHERE is_oncall = 1")
    var oncallUsers []string
    if rowsUsers != nil {
        for rowsUsers.Next() {
            var name string
            rowsUsers.Scan(&name)
            oncallUsers = append(oncallUsers, name)
        }
        rowsUsers.Close()
    }

    shifts := make(map[string]Shift)
    daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()

    if len(oncallUsers) > 0 {
        for d := 1; d <= daysInMonth; d++ {
            dateStr := fmt.Sprintf("%04d-%02d-%02d", year, month, d)
            pIdx := (d - 1) % len(oncallUsers)
            bIdx := d % len(oncallUsers)

            pUser := oncallUsers[pIdx]
            bUser := oncallUsers[bIdx]
            if pIdx == bIdx && len(oncallUsers) > 1 {
                bUser = oncallUsers[(bIdx+1)%len(oncallUsers)]
            }

            shifts[dateStr] = Shift{PrimaryUser: pUser, BackupUser: bUser}

            if _, exists := statsMap[pUser]; !exists {
                statsMap[pUser] = &UserStat{Name: pUser}
            }
            statsMap[pUser].PrimaryCount++

            if _, exists := statsMap[bUser]; !exists {
                statsMap[bUser] = &UserStat{Name: bUser}
            }
            statsMap[bUser].BackupCount++
        }
    }

    var stats []UserStat
    for _, st := range statsMap {
        stats = append(stats, *st)
    }

    response := map[string]interface{}{
        "year":          year,
        "month":         month,
        "absence_types": absenceTypes,
        "absences":      absences,
        "shifts":        shifts,
        "incidents":     incidents,
        "daily_tasks":   map[string][]DailyTask{},
        "stats":         stats,
    }

    json.NewEncoder(w).Encode(response)
}

func handleIncidents(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", 405)
        return
    }
    var inc IncidentReport
    json.NewDecoder(r.Body).Decode(&inc)

    _, err := db.Exec("INSERT INTO incidents (user_name, date, type, duration_minutes, description) VALUES (?, ?, ?, ?, ?)",
        inc.UserName, inc.Date, inc.Type, inc.DurationMinutes, inc.Description)

    if err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    w.WriteHeader(http.StatusCreated)
}

func handleRequestAbsence(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", 405)
        return
    }
    var req AbsenceRequest
    json.NewDecoder(r.Body).Decode(&req)

    _, err := db.Exec("INSERT INTO absence_requests (user_name, type, start_date, end_date, status) VALUES (?, ?, ?, ?, 'Pending')",
        req.UserName, req.Type, req.StartDate, req.EndDate)

    if err != nil {
        http.Error(w, err.Error(), 500)
        return
    }
    w.WriteHeader(http.StatusCreated)
}

// --- АДМІНІСТРАТИВНІ ЕНДПОЇНТИ (CRUD) ---

func handleUsers(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    switch r.Method {
    case http.MethodGet:
        rows, err := db.Query(`
            SELECT u.id, u.name, u.username, u.role, u.team_role_id, COALESCE(tr.name, ''), u.is_oncall 
            FROM users u 
            LEFT JOIN team_roles tr ON u.team_role_id = tr.id`)
        if err != nil {
            http.Error(w, err.Error(), 500)
            return
        }
        defer rows.Close()

        var users []User
        for rows.Next() {
            var u User
            rows.Scan(&u.ID, &u.Name, &u.Username, &u.Role, &u.TeamRoleID, &u.TeamRole, &u.IsOnCall)
            users = append(users, u)
        }
        json.NewEncoder(w).Encode(users)

    case http.MethodPost:
        var u User
        json.NewDecoder(r.Body).Decode(&u)
        _, err := db.Exec("INSERT INTO users (name, username, password, role, team_role_id, is_oncall) VALUES (?, ?, ?, ?, ?, ?)",
            u.Name, u.Username, u.Password, u.Role, u.TeamRoleID, u.IsOnCall)
        if err != nil {
            http.Error(w, err.Error(), 400)
            return
        }
        w.WriteHeader(http.StatusCreated)

    case http.MethodPut:
        var u User
        json.NewDecoder(r.Body).Decode(&u)
        if u.Password != "" {
            db.Exec("UPDATE users SET name=?, username=?, password=?, role=?, team_role_id=?, is_oncall=? WHERE id=?",
                u.Name, u.Username, u.Password, u.Role, u.TeamRoleID, u.IsOnCall, u.ID)
        } else {
            db.Exec("UPDATE users SET name=?, username=?, role=?, team_role_id=?, is_oncall=? WHERE id=?",
                u.Name, u.Username, u.Role, u.TeamRoleID, u.IsOnCall, u.ID)
        }
        w.WriteHeader(http.StatusOK)

    case http.MethodDelete:
        id := r.URL.Query().Get("id")
        db.Exec("DELETE FROM users WHERE id = ?", id)
        w.WriteHeader(http.StatusOK)
    }
}

func handleTeamRoles(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    switch r.Method {
    case http.MethodGet:
        rows, _ := db.Query("SELECT id, name FROM team_roles")
        defer rows.Close()
        var roles []TeamRole
        for rows.Next() {
            var tr TeamRole
            rows.Scan(&tr.ID, &tr.Name)
            roles = append(roles, tr)
        }
        json.NewEncoder(w).Encode(roles)

    case http.MethodPost:
        var tr TeamRole
        json.NewDecoder(r.Body).Decode(&tr)
        db.Exec("INSERT INTO team_roles (name) VALUES (?)", tr.Name)
        w.WriteHeader(http.StatusCreated)

    case http.MethodPut:
        var tr TeamRole
        json.NewDecoder(r.Body).Decode(&tr)
        db.Exec("UPDATE team_roles SET name = ? WHERE id = ?", tr.Name, tr.ID)
        w.WriteHeader(http.StatusOK)

    case http.MethodDelete:
        id := r.URL.Query().Get("id")
        db.Exec("DELETE FROM team_roles WHERE id = ?", id)
        w.WriteHeader(http.StatusOK)
    }
}

func handleAbsenceTypes(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    switch r.Method {
    case http.MethodGet:
        rows, _ := db.Query("SELECT id, name, code FROM absence_types")
        defer rows.Close()
        var types []AbsenceType
        for rows.Next() {
            var at AbsenceType
            rows.Scan(&at.ID, &at.Name, &at.Code)
            types = append(types, at)
        }
        json.NewEncoder(w).Encode(types)

    case http.MethodPost:
        var at AbsenceType
        json.NewDecoder(r.Body).Decode(&at)
        db.Exec("INSERT INTO absence_types (name, code) VALUES (?, ?)", at.Name, at.Code)
        w.WriteHeader(http.StatusCreated)

    case http.MethodPut:
        var at AbsenceType
        json.NewDecoder(r.Body).Decode(&at)
        db.Exec("UPDATE absence_types SET name = ?, code = ? WHERE id = ?", at.Name, at.Code, at.ID)
        w.WriteHeader(http.StatusOK)

    case http.MethodDelete:
        id := r.URL.Query().Get("id")
        db.Exec("DELETE FROM absence_types WHERE id = ?", id)
        w.WriteHeader(http.StatusOK)
    }
}

func handleAdminRequests(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    switch r.Method {
    case http.MethodGet:
        rows, err := db.Query("SELECT id, user_name, type, start_date, end_date, status FROM absence_requests")
        if err != nil {
            json.NewEncoder(w).Encode([]AbsenceRequest{})
            return
        }
        defer rows.Close()

        var reqs []AbsenceRequest
        for rows.Next() {
            var req AbsenceRequest
            rows.Scan(&req.ID, &req.UserName, &req.Type, &req.StartDate, &req.EndDate, &req.Status)
            reqs = append(reqs, req)
        }
        json.NewEncoder(w).Encode(reqs)

    case http.MethodPut:
        var payload struct {
            ID     int    `json:"id"`
            Status string `json:"status"`
        }
        json.NewDecoder(r.Body).Decode(&payload)
        db.Exec("UPDATE absence_requests SET status = ? WHERE id = ?", payload.Status, payload.ID)
        w.WriteHeader(http.StatusOK)
    }
}

// --- СИСТЕМНІ ЕНДПОЇНТИ ТА МОНІТОРИНГ ---

func handleAppLogs(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    appFilter := r.URL.Query().Get("app")

    logs := []AppLog{
        {Timestamp: time.Now().Add(-10 * time.Minute).Format("2006-01-02 15:04:05"), App: "Auth Service", Level: "INFO", Message: "User 'admin' successfully logged in"},
        {Timestamp: time.Now().Add(-5 * time.Minute).Format("2006-01-02 15:04:05"), App: "OnCall Core", Level: "INFO", Message: "Shift schedule generated for current week"},
        {Timestamp: time.Now().Add(-2 * time.Minute).Format("2006-01-02 15:04:05"), App: "Admin Panel", Level: "WARN", Message: "Unauthorized access attempt to /admin route blocked"},
        {Timestamp: time.Now().Format("2006-01-02 15:04:05"), App: "Nginx Proxy", Level: "INFO", Message: "GET /api/admin/users HTTP/1.1 200 OK"},
    }

    if appFilter != "" && appFilter != "All" {
        var filtered []AppLog
        for _, l := range logs {
            if l.App == appFilter {
                filtered = append(filtered, l)
            }
        }
        logs = filtered
    }

    json.NewEncoder(w).Encode(logs)
}

func handleDBStats(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    tables := []string{"users", "team_roles", "absence_types", "absence_requests", "incidents", "audit_logs"}
    var stats []DBStat

    for _, t := range tables {
        var count int
        db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", t)).Scan(&count)
        stats = append(stats, DBStat{
            TableName:  t,
            RowCount:   count,
            LastAction: "UPDATE/INSERT",
            LastUpdate: time.Now().Format("2006-01-02 15:04:05"),
        })
    }

    json.NewEncoder(w).Encode(stats)
}

func handleSqlQuery(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", 405)
        return
    }

    var req QueryRequest
    json.NewDecoder(r.Body).Decode(&req)

    q := strings.TrimSpace(req.Query)
    if !strings.HasPrefix(strings.ToUpper(q), "SELECT") {
        http.Error(w, "Дозволені тільки SELECT-запити (Read-Only)", http.StatusBadRequest)
        return
    }

    rows, err := db.Query(q)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    defer rows.Close()

    cols, err := rows.Columns()
    if err != nil {
        http.Error(w, err.Error(), 500)
        return
    }

    var result []map[string]interface{}
    for rows.Next() {
        columns := make([]interface{}, len(cols))
        columnPointers := make([]interface{}, len(cols))
        for i := range columns {
            columnPointers[i] = &columns[i]
        }

        if err := rows.Scan(columnPointers...); err != nil {
            http.Error(w, err.Error(), 500)
            return
        }

        m := make(map[string]interface{})
        for i, colName := range cols {
            val := columnPointers[i].(*interface{})
            m[colName] = *val
        }
        result = append(result, m)
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "columns": cols,
        "rows":    result,
    })
}

func handleAuditLogs(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    logs := []AuditLog{
        {Timestamp: time.Now().Add(-15 * time.Minute).Format("2006-01-02 15:04:05"), UserName: "admin", Action: "LOGIN", IP: "127.0.0.1", Details: "Вхід у систему успішний"},
        {Timestamp: time.Now().Add(-10 * time.Minute).Format("2006-01-02 15:04:05"), UserName: "admin", Action: "UPDATE_USER", IP: "127.0.0.1", Details: "Змінено роль користувача"},
        {Timestamp: time.Now().Add(-5 * time.Minute).Format("2006-01-02 15:04:05"), UserName: "system", Action: "BACKUP", IP: "127.0.0.1", Details: "Автоматичне створення резервної копії БД"},
    }
    json.NewEncoder(w).Encode(logs)
}
