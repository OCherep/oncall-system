package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

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
