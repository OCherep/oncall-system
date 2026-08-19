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
    Name         string `json:"name"`
    PrimaryCount int    `json:"primary_count"`
    BackupCount  int    `json:"backup_count"`
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

    // Admin APIs (Dictionaries with full CRUD)
    // TODO: Ensure full CRUD operations (GET, POST, PUT, DELETE) are always preserved for dictionary management
    http.HandleFunc("/api/admin/users", handleAdminUsers)
    http.HandleFunc("/api/admin/team-roles", handleAdminTeamRoles)
    http.HandleFunc("/api/admin/absence-types", handleAdminAbsenceTypes)
    http.HandleFunc("/api/admin/requests", handleAdminRequests)

    // System & Monitoring APIs
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

    tables := []string{"users", "team_roles", "absence_types", "shifts", "absences", "audit_logs", "app_logs"}
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

func handleLogin(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    var req struct {
        Username string `json:"username"`
        Password string `json:"password"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    var u User
    var isOncallInt int
    err := db.QueryRow(`
        SELECT u.id, u.username, u.name, u.role, u.team_role_id, COALESCE(tr.name, ''), COALESCE(u.is_oncall, 1)
        FROM users u
        LEFT JOIN team_roles tr ON u.team_role_id = tr.id
        WHERE u.username = ? AND u.password = ?`, req.Username, req.Password).
        Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.TeamRoleID, &u.TeamRole, &isOncallInt)

    if err != nil {
        logAudit(req.Username, "LOGIN_FAILED", r.RemoteAddr, "Невдала спроба входу")
        logAppEvent("Auth Service", "WARN", fmt.Sprintf("Невдалий вхід для користувача: %s", req.Username))
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusUnauthorized)
        json.NewEncoder(w).Encode(map[string]string{"error": "Невірне ім'я користувача або пароль"})
        return
    }
    u.IsOncall = isOncallInt == 1

    logAudit(u.Username, "LOGIN_SUCCESS", r.RemoteAddr, "Успішна авторизація в системі")
    logAppEvent("Auth Service", "INFO", fmt.Sprintf("Користувач %s успішно авторизувався", u.Username))

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(u)
}

func handleGetData(w http.ResponseWriter, r *http.Request) {
    yearStr := r.URL.Query().Get("year")
    monthStr := r.URL.Query().Get("month")

    now := time.Now()
    year, month := now.Year(), int(now.Month())

    if yearStr != "" && monthStr != "" {
        fmt.Sscanf(yearStr, "%d", &year)
        fmt.Sscanf(monthStr, "%d", &month)
    }

    prefix := fmt.Sprintf("%04d-%02d", year, month)

    rows, err := db.Query("SELECT date, primary_user, backup_user FROM shifts WHERE date LIKE ?", prefix+"%")
    shifts := make(map[string]Shift)
    if err == nil {
        defer rows.Close()
        for rows.Next() {
            var s Shift
            rows.Scan(&s.Date, &s.PrimaryUser, &s.BackupUser)
            shifts[s.Date] = s
        }
    }

    absRows, err := db.Query("SELECT id, user_name, type, start_date, end_date, status FROM absences WHERE status = 'Approved'")
    var absences []AbsenceRequest
    if err == nil {
        defer absRows.Close()
        for absRows.Next() {
            var a AbsenceRequest
            absRows.Scan(&a.ID, &a.UserName, &a.Type, &a.StartDate, &a.EndDate, &a.Status)
            absences = append(absences, a)
        }
    }

    userRows, err := db.Query("SELECT name FROM users WHERE role != 'admin' AND COALESCE(is_oncall, 1) = 1")
    statsMap := make(map[string]*UserStat)
    if err == nil {
        defer userRows.Close()
        for userRows.Next() {
            var name string
            userRows.Scan(&name)
            statsMap[name] = &UserStat{Name: name, PrimaryCount: 0, BackupCount: 0}
        }
    }

    for _, s := range shifts {
        if st, ok := statsMap[s.PrimaryUser]; ok {
            st.PrimaryCount++
        }
        if st, ok := statsMap[s.BackupUser]; ok {
            st.BackupCount++
        }
    }

    var stats []UserStat
    for _, v := range statsMap {
        stats = append(stats, *v)
    }

    typesRows, _ := db.Query("SELECT id, name, code FROM absence_types")
    var absenceTypes []AbsenceType
    if typesRows != nil {
        defer typesRows.Close()
        for typesRows.Next() {
            var t AbsenceType
            typesRows.Scan(&t.ID, &t.Name, &t.Code)
            absenceTypes = append(absenceTypes, t)
        }
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "shifts":        shifts,
        "absences":      absences,
        "stats":         stats,
        "absence_types": absenceTypes,
        "year":          year,
        "month":         month,
    })
}

func handleRequestAbsence(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }
    var req struct {
        UserName  string `json:"user_name"`
        Type      string `json:"type"`
        StartDate string `json:"start_date"`
        EndDate   string `json:"end_date"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    _, err := db.Exec("INSERT INTO absences (user_name, type, start_date, end_date, status) VALUES (?, ?, ?, ?, 'Pending')",
        req.UserName, req.Type, req.StartDate, req.EndDate)
    if err != nil {
        http.Error(w, "Помилка створення заявки", http.StatusInternalServerError)
        return
    }

    logAudit(req.UserName, "CREATE_ABSENCE_REQUEST", r.RemoteAddr, fmt.Sprintf("Тип: %s, Дати: %s - %s", req.Type, req.StartDate, req.EndDate))
    logAppEvent("OnCall Core", "INFO", fmt.Sprintf("Користувач %s створив заявку на %s", req.UserName, req.Type))

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// TODO: CRUD - User Management
func handleAdminUsers(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    switch r.Method {
    case http.MethodGet:
        rows, err := db.Query(`
            SELECT u.id, u.username, u.name, u.role, u.team_role_id, COALESCE(tr.name, ''), COALESCE(u.is_oncall, 1)
            FROM users u
            LEFT JOIN team_roles tr ON u.team_role_id = tr.id
            ORDER BY u.id ASC`)
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        defer rows.Close()

        var users []User
        for rows.Next() {
            var u User
            var isOncallInt int
            rows.Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.TeamRoleID, &u.TeamRole, &isOncallInt)
            u.IsOncall = isOncallInt == 1
            users = append(users, u)
        }
        json.NewEncoder(w).Encode(users)

    case http.MethodPost:
        var u User
        json.NewDecoder(r.Body).Decode(&u)
        isOncallInt := 0
        if u.IsOncall {
            isOncallInt = 1
        }
        if u.Password == "" {
            u.Password = "1234"
        }
        res, err := db.Exec(`INSERT INTO users (username, password, name, role, team_role_id, is_oncall) VALUES (?, ?, ?, ?, ?, ?)`,
            u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, isOncallInt)
        if err != nil {
            http.Error(w, "Помилка створення", http.StatusBadRequest)
            return
        }
        id, _ := res.LastInsertId()
        u.ID = int(id)
        logAudit("Admin", "CREATE_USER", r.RemoteAddr, fmt.Sprintf("Створено користувача: %s", u.Username))
        json.NewEncoder(w).Encode(u)

    case http.MethodPut:
        var u User
        json.NewDecoder(r.Body).Decode(&u)
        isOncallInt := 0
        if u.IsOncall {
            isOncallInt = 1
        }
        if u.Password != "" {
            db.Exec(`UPDATE users SET username=?, password=?, name=?, role=?, team_role_id=?, is_oncall=? WHERE id=?`,
                u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, isOncallInt, u.ID)
        } else {
            db.Exec(`UPDATE users SET username=?, name=?, role=?, team_role_id=?, is_oncall=? WHERE id=?`,
                u.Username, u.Name, u.Role, u.TeamRoleID, isOncallInt, u.ID)
        }
        logAudit("Admin", "UPDATE_USER", r.RemoteAddr, fmt.Sprintf("Оновлено користувача ID: %d", u.ID))
        json.NewEncoder(w).Encode(u)

    case http.MethodDelete:
        idStr := r.URL.Query().Get("id")
        db.Exec("DELETE FROM users WHERE id = ?", idStr)
        logAudit("Admin", "DELETE_USER", r.RemoteAddr, fmt.Sprintf("Видалено користувача ID: %s", idStr))
        json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
    }
}

// TODO: CRUD - Team Roles Dictionary
func handleAdminTeamRoles(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    switch r.Method {
    case http.MethodGet:
        rows, _ := db.Query("SELECT id, name FROM team_roles ORDER BY id ASC")
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
        res, err := db.Exec("INSERT INTO team_roles (name) VALUES (?)", tr.Name)
        if err != nil {
            http.Error(w, "Вже існує", http.StatusBadRequest)
            return
        }
        id, _ := res.LastInsertId()
        tr.ID = int(id)
        logAudit("Admin", "CREATE_TEAM_ROLE", r.RemoteAddr, fmt.Sprintf("Додано роль: %s", tr.Name))
        json.NewEncoder(w).Encode(tr)

    case http.MethodPut:
        var tr TeamRole
        json.NewDecoder(r.Body).Decode(&tr)
        db.Exec("UPDATE team_roles SET name = ? WHERE id = ?", tr.Name, tr.ID)
        logAudit("Admin", "UPDATE_TEAM_ROLE", r.RemoteAddr, fmt.Sprintf("Оновлено роль ID: %d", tr.ID))
        json.NewEncoder(w).Encode(tr)

    case http.MethodDelete:
        idStr := r.URL.Query().Get("id")
        db.Exec("DELETE FROM team_roles WHERE id = ?", idStr)
        logAudit("Admin", "DELETE_TEAM_ROLE", r.RemoteAddr, fmt.Sprintf("Видалено роль ID: %s", idStr))
        json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
    }
}

// TODO: CRUD - Absence Types Dictionary
func handleAdminAbsenceTypes(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    switch r.Method {
    case http.MethodGet:
        rows, _ := db.Query("SELECT id, name, code FROM absence_types ORDER BY id ASC")
        defer rows.Close()
        var types []AbsenceType
        for rows.Next() {
            var t AbsenceType
            rows.Scan(&t.ID, &t.Name, &t.Code)
            types = append(types, t)
        }
        json.NewEncoder(w).Encode(types)

    case http.MethodPost:
        var t AbsenceType
        json.NewDecoder(r.Body).Decode(&t)
        res, err := db.Exec("INSERT INTO absence_types (name, code) VALUES (?, ?)", t.Name, t.Code)
        if err != nil {
            http.Error(w, "Помилка додання", http.StatusBadRequest)
            return
        }
        id, _ := res.LastInsertId()
        t.ID = int(id)
        logAudit("Admin", "CREATE_ABSENCE_TYPE", r.RemoteAddr, fmt.Sprintf("Додано тип відсутності: %s", t.Name))
        json.NewEncoder(w).Encode(t)

    case http.MethodPut:
        var t AbsenceType
        json.NewDecoder(r.Body).Decode(&t)
        db.Exec("UPDATE absence_types SET name = ?, code = ? WHERE id = ?", t.Name, t.Code, t.ID)
        logAudit("Admin", "UPDATE_ABSENCE_TYPE", r.RemoteAddr, fmt.Sprintf("Оновлено тип відсутності ID: %d", t.ID))
        json.NewEncoder(w).Encode(t)

    case http.MethodDelete:
        idStr := r.URL.Query().Get("id")
        db.Exec("DELETE FROM absence_types WHERE id = ?", idStr)
        logAudit("Admin", "DELETE_ABSENCE_TYPE", r.RemoteAddr, fmt.Sprintf("Видалено тип відсутності ID: %s", idStr))
        json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
    }
}

func handleAdminRequests(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    switch r.Method {
    case http.MethodGet:
        rows, _ := db.Query("SELECT id, user_name, type, start_date, end_date, status FROM absences ORDER BY id DESC")
        defer rows.Close()
        var list []AbsenceRequest
        for rows.Next() {
            var a AbsenceRequest
            rows.Scan(&a.ID, &a.UserName, &a.Type, &a.StartDate, &a.EndDate, &a.Status)
            list = append(list, a)
        }
        json.NewEncoder(w).Encode(list)

    case http.MethodPut:
        var req struct {
            ID     int    `json:"id"`
            Status string `json:"status"`
        }
        json.NewDecoder(r.Body).Decode(&req)
        db.Exec("UPDATE absences SET status = ? WHERE id = ?", req.Status, req.ID)
        logAudit("Admin", "UPDATE_REQUEST_STATUS", r.RemoteAddr, fmt.Sprintf("Заявка ID %d змінила статус на: %s", req.ID, req.Status))
        json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
    }
}

// --- PROJECT MONITORING HANDLERS ---
func handleDBStats(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    rows, err := db.Query("SELECT table_name, last_action, datetime(last_update, 'localtime') FROM table_tracker")
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    defer rows.Close()

    var stats []TableStat
    for rows.Next() {
        var st TableStat
        rows.Scan(&st.TableName, &st.LastAction, &st.LastUpdate)

        var count int
        db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", st.TableName)).Scan(&count)
        st.RowCount = count

        stats = append(stats, st)
    }
    json.NewEncoder(w).Encode(stats)
}

func handleReadOnlyQuery(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    if r.Method != http.MethodPost {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    var body struct {
        Query string `json:"query"`
    }
    json.NewDecoder(r.Body).Decode(&body)

    trimmed := strings.TrimSpace(strings.ToUpper(body.Query))
    if !strings.HasPrefix(trimmed, "SELECT") {
        http.Error(w, "Дозволені лише SELECT-запити", http.StatusBadRequest)
        return
    }

    rows, err := db.Query(body.Query)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    defer rows.Close()

    cols, _ := rows.Columns()
    var result []map[string]interface{}

    for rows.Next() {
        columns := make([]interface{}, len(cols))
        columnPointers := make([]interface{}, len(cols))
        for i := range columns {
            columnPointers[i] = &columns[i]
        }

        rows.Scan(columnPointers...)

        m := make(map[string]interface{})
        for i, colName := range cols {
            val := columnPointers[i].(*interface{})
            m[colName] = *val
        }
        result = append(result, m)
    }

    json.NewEncoder(w).Encode(map[string]interface{}{
        "columns": cols,
        "rows":    result,
    })
}

func handleAuditLogs(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    rows, _ := db.Query("SELECT id, datetime(timestamp, 'localtime'), user_name, action, ip, details FROM audit_logs ORDER BY id DESC LIMIT 100")
    defer rows.Close()

    var logs []AuditLog
    for rows.Next() {
        var l AuditLog
        rows.Scan(&l.ID, &l.Timestamp, &l.UserName, &l.Action, &l.IP, &l.Details)
        logs = append(logs, l)
    }
    json.NewEncoder(w).Encode(logs)
}

func handleAppLogs(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    appName := r.URL.Query().Get("app")

    var rows *sql.Rows
    if appName != "" && appName != "All" {
        rows, _ = db.Query("SELECT id, datetime(timestamp, 'localtime'), app, level, message FROM app_logs WHERE app = ? ORDER BY id DESC LIMIT 100", appName)
    } else {
        rows, _ = db.Query("SELECT id, datetime(timestamp, 'localtime'), app, level, message FROM app_logs ORDER BY id DESC LIMIT 100")
    }
    defer rows.Close()

    var logs []AppLog
    for rows.Next() {
        var l AppLog
        rows.Scan(&l.ID, &l.Timestamp, &l.App, &l.Level, &l.Message)
        logs = append(logs, l)
    }
    json.NewEncoder(w).Encode(logs)
}
