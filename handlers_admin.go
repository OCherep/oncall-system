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
		rows, err := db.Query(`SELECT u.id, u.username, u.name, u.role, u.team_role_id, COALESCE(tr.name, ''), COALESCE(u.is_oncall, 1)
			FROM users u LEFT JOIN team_roles tr ON u.team_role_id = tr.id ORDER BY u.name`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var list []User
		for rows.Next() {
			var u User
			var isOn int
			rows.Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.TeamRoleID, &u.TeamRole, &isOn)
			u.IsOncall = isOn == 1
			list = append(list, u)
		}
		if list == nil {
			list = []User{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var u User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if u.Username == "" || u.Name == "" {
			http.Error(w, "username and name required", http.StatusBadRequest)
			return
		}
		if u.Password == "" {
			u.Password = "password"
		}
		if u.Role == "" {
			u.Role = "user"
		}
		isOn := 0
		if u.IsOncall {
			isOn = 1
		}
		res, err := db.Exec(`INSERT INTO users (username, password, name, role, team_role_id, is_oncall) VALUES (?, ?, ?, ?, ?, ?)`,
			u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, isOn)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		u.ID = int(id)
		logAudit("Admin", "CREATE_USER", r.RemoteAddr, u.Username)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(u)
	case http.MethodPut:
		var u User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil || u.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		isOn := 0
		if u.IsOncall {
			isOn = 1
		}
		if u.Password != "" {
			db.Exec(`UPDATE users SET username=?, password=?, name=?, role=?, team_role_id=?, is_oncall=? WHERE id=?`,
				u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, isOn, u.ID)
		} else {
			db.Exec(`UPDATE users SET username=?, name=?, role=?, team_role_id=?, is_oncall=? WHERE id=?`,
				u.Username, u.Name, u.Role, u.TeamRoleID, isOn, u.ID)
		}
		logAudit("Admin", "UPDATE_USER", r.RemoteAddr, u.Username)
		json.NewEncoder(w).Encode(u)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		db.Exec("DELETE FROM users WHERE id=?", id)
		logAudit("Admin", "DELETE_USER", r.RemoteAddr, id)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleAdminRoles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, _ := db.Query("SELECT id, name FROM team_roles ORDER BY name")
		var list []TeamRole
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var t TeamRole
				rows.Scan(&t.ID, &t.Name)
				list = append(list, t)
			}
		}
		if list == nil {
			list = []TeamRole{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var t TeamRole
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		res, err := db.Exec("INSERT INTO team_roles (name) VALUES (?)", t.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		t.ID = int(id)
		logAudit("Admin", "CREATE_ROLE", r.RemoteAddr, t.Name)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(t)
	case http.MethodPut:
		var t TeamRole
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		db.Exec("UPDATE team_roles SET name=? WHERE id=?", t.Name, t.ID)
		logAudit("Admin", "UPDATE_ROLE", r.RemoteAddr, t.Name)
		json.NewEncoder(w).Encode(t)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		db.Exec("DELETE FROM team_roles WHERE id=?", id)
		logAudit("Admin", "DELETE_ROLE", r.RemoteAddr, id)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleAdminAbsenceTypes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, _ := db.Query("SELECT id, name, code FROM absence_types ORDER BY name")
		var list []AbsenceType
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var t AbsenceType
				rows.Scan(&t.ID, &t.Name, &t.Code)
				list = append(list, t)
			}
		}
		if list == nil {
			list = []AbsenceType{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var t AbsenceType
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if t.Code == "" {
			t.Code = t.Name
		}
		res, err := db.Exec("INSERT INTO absence_types (name, code) VALUES (?, ?)", t.Name, t.Code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		t.ID = int(id)
		logAudit("Admin", "CREATE_ABSENCE_TYPE", r.RemoteAddr, t.Name)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(t)
	case http.MethodPut:
		var t AbsenceType
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		db.Exec("UPDATE absence_types SET name=?, code=? WHERE id=?", t.Name, t.Code, t.ID)
		logAudit("Admin", "UPDATE_ABSENCE_TYPE", r.RemoteAddr, t.Name)
		json.NewEncoder(w).Encode(t)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		db.Exec("DELETE FROM absence_types WHERE id=?", id)
		logAudit("Admin", "DELETE_ABSENCE_TYPE", r.RemoteAddr, id)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleAdminRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, _ := db.Query(`SELECT id, user_name, type, start_date, end_date, status FROM absences ORDER BY id DESC`)
		var list []AbsenceRequest
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var a AbsenceRequest
				rows.Scan(&a.ID, &a.UserName, &a.Type, &a.StartDate, &a.EndDate, &a.Status)
				list = append(list, a)
			}
		}
		if list == nil {
			list = []AbsenceRequest{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPut:
		var req struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		db.Exec("UPDATE absences SET status=? WHERE id=?", req.Status, req.ID)
		logAudit("Admin", "UPDATE_REQUEST", r.RemoteAddr, fmt.Sprintf("id=%d %s", req.ID, req.Status))
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, _ := db.Query(`SELECT id, user_name, action, ip, details, datetime(timestamp,'localtime') FROM audit_logs ORDER BY id DESC LIMIT 200`)
	var logs []AuditLog
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var l AuditLog
			rows.Scan(&l.ID, &l.UserName, &l.Action, &l.IP, &l.Details, &l.Timestamp)
			logs = append(logs, l)
		}
	}
	if logs == nil {
		logs = []AuditLog{}
	}
	json.NewEncoder(w).Encode(logs)
}

func handleAdminTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		q := `SELECT id, user_name, date, task_description,
			COALESCE(status,'Нова'), COALESCE(priority,'Базова'), work_started_at,
			COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),'')
			FROM daily_tasks WHERE 1=1`
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
		err := db.QueryRow(`SELECT id, user_name, date, task_description, COALESCE(status,'Нова'), COALESCE(priority,'Базова'),
			work_started_at, COALESCE(total_minutes,0) FROM daily_tasks WHERE id=?`, t.ID).
			Scan(&cur.ID, &cur.UserName, &cur.Date, &cur.TaskDescription, &cur.Status, &cur.Priority, &ws, &cur.TotalMinutes)
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
		db.Exec(`UPDATE daily_tasks SET user_name=?, date=?, task_description=?, status=?, priority=?,
			work_started_at=?, total_minutes=? WHERE id=?`,
			t.UserName, t.Date, t.TaskDescription, t.Status, t.Priority, workArg, total, t.ID)
		t.TotalMinutes = total
		t.WorkStartedAt = workStarted
		logAudit("Admin", "UPDATE_TASK", r.RemoteAddr, fmt.Sprintf("id=%d %s %s", t.ID, t.Status, t.Priority))
		json.NewEncoder(w).Encode(t)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		db.Exec("DELETE FROM daily_tasks WHERE id=?", id)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleDBStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, err := db.Query("SELECT table_name, last_action, datetime(last_update, 'localtime') FROM table_tracker ORDER BY table_name")
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
	if stats == nil {
		stats = []TableStat{}
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
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
		ptrs := make([]interface{}, len(cols))
		for i := range columns {
			ptrs[i] = &columns[i]
		}
		rows.Scan(ptrs...)
		m := make(map[string]interface{})
		for i, col := range cols {
			val := *(ptrs[i].(*interface{}))
			switch v := val.(type) {
			case []byte:
				m[col] = string(v)
			default:
				m[col] = v
			}
		}
		result = append(result, m)
	}
	if result == nil {
		result = []map[string]interface{}{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"columns": cols, "rows": result})
}

func handleRegenerateShifts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Year  int `json:"year"`
		Month int `json:"month"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.Year == 0 {
		body.Year = time.Now().Year()
	}
	if body.Month == 0 {
		body.Month = int(time.Now().Month())
	}
	rows, _ := db.Query(`SELECT u.name FROM users u WHERE COALESCE(u.is_oncall, 1) = 1 AND u.role != 'admin' ORDER BY u.name`)
	var oncall []string
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var n string
			rows.Scan(&n)
			oncall = append(oncall, n)
		}
	}
	absRows, _ := db.Query(`SELECT id, user_name, type, start_date, end_date, status FROM absences WHERE status = 'Approved'`)
	var absences []AbsenceRequest
	if absRows != nil {
		defer absRows.Close()
		for absRows.Next() {
			var a AbsenceRequest
			absRows.Scan(&a.ID, &a.UserName, &a.Type, &a.StartDate, &a.EndDate, &a.Status)
			absences = append(absences, a)
		}
	}
	db.Exec(`DELETE FROM shifts WHERE date LIKE ?`, fmt.Sprintf("%04d-%02d-%%", body.Year, body.Month))
	shifts := generateShifts(body.Year, body.Month, oncall, absences)
	for _, s := range shifts {
		db.Exec(`INSERT INTO shifts (date, primary_user, backup_user) VALUES (?, ?, ?)`, s.Date, s.PrimaryUser, s.BackupUser)
	}
	logAudit("Admin", "REGENERATE_SHIFTS", r.RemoteAddr, fmt.Sprintf("%04d-%02d count=%d", body.Year, body.Month, len(shifts)))
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "shifts": len(shifts), "oncall": oncall})
}
