package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`SELECT u.id, u.username, u.name, u.role, u.team_role_id, COALESCE(tr.name, ''), COALESCE(u.is_oncall, 1) FROM users u LEFT JOIN team_roles tr ON u.team_role_id = tr.id ORDER BY u.id ASC`)
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
		isOn := 0
		if u.IsOncall {
			isOn = 1
		}
		if u.Password == "" {
			u.Password = "1234"
		}
		res, err := db.Exec(`INSERT INTO users (username, password, name, role, team_role_id, is_oncall) VALUES (?, ?, ?, ?, ?, ?)`, u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, isOn)
		if err != nil {
			http.Error(w, "Помилка створення", http.StatusBadRequest)
			return
		}
		id, _ := res.LastInsertId()
		u.ID = int(id)
		json.NewEncoder(w).Encode(u)
	case http.MethodPut:
		var u User
		json.NewDecoder(r.Body).Decode(&u)
		isOn := 0
		if u.IsOncall {
			isOn = 1
		}
		if u.Password != "" {
			db.Exec(`UPDATE users SET username=?, password=?, name=?, role=?, team_role_id=?, is_oncall=? WHERE id=?`, u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, isOn, u.ID)
		} else {
			db.Exec(`UPDATE users SET username=?, name=?, role=?, team_role_id=?, is_oncall=? WHERE id=?`, u.Username, u.Name, u.Role, u.TeamRoleID, isOn, u.ID)
		}
		json.NewEncoder(w).Encode(u)
	case http.MethodDelete:
		db.Exec("DELETE FROM users WHERE id = ?", r.URL.Query().Get("id"))
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
		json.NewEncoder(w).Encode(tr)
	case http.MethodPut:
		var tr TeamRole
		json.NewDecoder(r.Body).Decode(&tr)
		db.Exec("UPDATE team_roles SET name = ? WHERE id = ?", tr.Name, tr.ID)
		json.NewEncoder(w).Encode(tr)
	case http.MethodDelete:
		db.Exec("DELETE FROM team_roles WHERE id = ?", r.URL.Query().Get("id"))
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
			http.Error(w, "Помилка", http.StatusBadRequest)
			return
		}
		id, _ := res.LastInsertId()
		t.ID = int(id)
		json.NewEncoder(w).Encode(t)
	case http.MethodPut:
		var t AbsenceType
		json.NewDecoder(r.Body).Decode(&t)
		db.Exec("UPDATE absence_types SET name = ?, code = ? WHERE id = ?", t.Name, t.Code, t.ID)
		json.NewEncoder(w).Encode(t)
	case http.MethodDelete:
		db.Exec("DELETE FROM absence_types WHERE id = ?", r.URL.Query().Get("id"))
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
		json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
	}
}

func handleAdminTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		q := `SELECT id, user_name, date, task_description, COALESCE(status,'Нова'), COALESCE(priority,'Базова'), work_started_at, COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),'') FROM daily_tasks WHERE 1=1`
		args := []interface{}{}
		if v := r.URL.Query().Get("user"); v != "" {
			q += " AND user_name = ?"
			args = append(args, v)
		}
		if v := r.URL.Query().Get("status"); v != "" {
			q += " AND COALESCE(status,'Нова') = ?"
			args = append(args, v)
		}
		if v := r.URL.Query().Get("priority"); v != "" {
			q += " AND COALESCE(priority,'Базова') = ?"
			args = append(args, v)
		}
		if v := r.URL.Query().Get("date"); v != "" {
			q += " AND date = ?"
			args = append(args, v)
		}
		if v := r.URL.Query().Get("date_from"); v != "" {
			q += " AND date >= ?"
			args = append(args, v)
		}
		if v := r.URL.Query().Get("date_to"); v != "" {
			q += " AND date <= ?"
			args = append(args, v)
		}
		q += " ORDER BY date DESC, id DESC LIMIT 500"
		rows, err := db.Query(q, args...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var list []DailyTask
		for rows.Next() {
			var t DailyTask
			var ws, ca sql.NullString
			rows.Scan(&t.ID, &t.UserName, &t.Date, &t.TaskDescription, &t.Status, &t.Priority, &ws, &t.TotalMinutes, &ca)
			if ws.Valid {
				t.WorkStartedAt = ws.String
			}
			if ca.Valid {
				t.CreatedAt = ca.String
			}
			list = append(list, t)
		}
		if list == nil {
			list = []DailyTask{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPut:
		var t DailyTask
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		var cur DailyTask
		var ws sql.NullString
		err := db.QueryRow(`SELECT id, user_name, date, task_description, COALESCE(status,'Нова'), COALESCE(priority,'Базова'), work_started_at, COALESCE(total_minutes,0) FROM daily_tasks WHERE id=?`, t.ID).Scan(&cur.ID, &cur.UserName, &cur.Date, &cur.TaskDescription, &cur.Status, &cur.Priority, &ws, &cur.TotalMinutes)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if ws.Valid {
			cur.WorkStartedAt = ws.String
		}
		if t.Status == "" {
			t.Status = cur.Status
		}
		if t.Priority == "" {
			t.Priority = cur.Priority
		}
		if t.TaskDescription == "" {
			t.TaskDescription = cur.TaskDescription
		}
		if t.UserName == "" {
			t.UserName = cur.UserName
		}
		if t.Date == "" {
			t.Date = cur.Date
		}
		if t.Status == "Перевідкрита" {
			t.Status = "Нова"
		}
		total := cur.TotalMinutes
		workStarted := cur.WorkStartedAt
		if cur.Status == "У роботі" && t.Status != "У роботі" {
			tmp := cur
			accumulateWorkTime(&tmp)
			total = tmp.TotalMinutes
			workStarted = ""
		}
		if t.Status == "У роботі" && cur.Status != "У роботі" {
			workStarted = time.Now().Format(time.RFC3339)
		}
		var workArg interface{}
		if workStarted == "" {
			workArg = nil
		} else {
			workArg = workStarted
		}
		db.Exec(`UPDATE daily_tasks SET user_name=?, date=?, task_description=?, status=?, priority=?, work_started_at=?, total_minutes=? WHERE id=?`, t.UserName, t.Date, t.TaskDescription, t.Status, t.Priority, workArg, total, t.ID)
		t.TotalMinutes = total
		t.WorkStartedAt = workStarted
		json.NewEncoder(w).Encode(t)
	case http.MethodDelete:
		db.Exec("DELETE FROM daily_tasks WHERE id=?", r.URL.Query().Get("id"))
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	var body struct{ Query string `json:"query"` }
	json.NewDecoder(r.Body).Decode(&body)
	trimmed := strings.TrimSpace(strings.ToUpper(body.Query))
	if !strings.HasPrefix(trimmed, "SELECT") {
		http.Error(w, "SELECT only", http.StatusBadRequest)
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
		ptrs := make([]interface{}, len(cols))
		for i := range columns {
			ptrs[i] = &columns[i]
		}
		rows.Scan(ptrs...)
		m := make(map[string]interface{})
		for i, col := range cols {
			m[col] = *ptrs[i].(*interface{})
		}
		result = append(result, m)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"columns": cols, "rows": result})
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
