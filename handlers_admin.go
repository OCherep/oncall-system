package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// dbAdminPassword returns the secondary password required for DB tools.
// Set via env DB_ADMIN_PASSWORD (default: "db-admin-change-me").
func dbAdminPassword() string {
	p := os.Getenv("DB_ADMIN_PASSWORD")
	if p == "" {
		return "db-admin-change-me"
	}
	return p
}

func checkDBAdminPassword(r *http.Request) bool {
	p := r.Header.Get("X-DB-Admin-Password")
	if p == "" {
		p = r.URL.Query().Get("db_password")
	}
	return p != "" && p == dbAdminPassword()
}

func handleDBUnlock(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Password != dbAdminPassword() {
		logAudit("admin", "DB_UNLOCK_FAILED", r.RemoteAddr, "wrong password")
		http.Error(w, "Невірний пароль доступу до бази", 403)
		return
	}
	logAudit("admin", "DB_UNLOCK_OK", r.RemoteAddr, "db tools unlocked")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`SELECT u.id, u.username, u.name, u.role, u.team_role_id, COALESCE(tr.name,''), COALESCE(u.is_oncall,1)
			FROM users u LEFT JOIN team_roles tr ON u.team_role_id=tr.id ORDER BY u.id`)
		if err != nil {
			http.Error(w, err.Error(), 500)
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
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var u User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		on := 0
		if u.IsOncall {
			on = 1
		}
		res, err := db.Exec(`INSERT INTO users (username, password, name, role, team_role_id, is_oncall) VALUES (?,?,?,?,?,?)`,
			u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, on)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		id, _ := res.LastInsertId()
		u.ID = int(id)
		logAudit("admin", "CREATE_USER", r.RemoteAddr, u.Username)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(u)
	case http.MethodPut:
		var u User
		if err := json.NewDecoder(r.Body).Decode(&u); err != nil || u.ID == 0 {
			http.Error(w, "id required", 400)
			return
		}
		on := 0
		if u.IsOncall {
			on = 1
		}
		if u.Password != "" {
			db.Exec(`UPDATE users SET username=?, password=?, name=?, role=?, team_role_id=?, is_oncall=? WHERE id=?`,
				u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, on, u.ID)
		} else {
			db.Exec(`UPDATE users SET username=?, name=?, role=?, team_role_id=?, is_oncall=? WHERE id=?`,
				u.Username, u.Name, u.Role, u.TeamRoleID, on, u.ID)
		}
		logAudit("admin", "UPDATE_USER", r.RemoteAddr, fmt.Sprintf("id=%d", u.ID))
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		db.Exec("DELETE FROM users WHERE id=?", id)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

func handleAdminRoles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, _ := db.Query(`SELECT id, name FROM team_roles ORDER BY id`)
		var list []TeamRole
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var t TeamRole
				rows.Scan(&t.ID, &t.Name)
				list = append(list, t)
			}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var t TeamRole
		json.NewDecoder(r.Body).Decode(&t)
		res, err := db.Exec(`INSERT INTO team_roles (name) VALUES (?)`, t.Name)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		id, _ := res.LastInsertId()
		t.ID = int(id)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(t)
	case http.MethodPut:
		var t TeamRole
		json.NewDecoder(r.Body).Decode(&t)
		db.Exec(`UPDATE team_roles SET name=? WHERE id=?`, t.Name, t.ID)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		db.Exec("DELETE FROM team_roles WHERE id=?", r.URL.Query().Get("id"))
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

func handleAdminAbsenceTypes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, _ := db.Query(`SELECT id, name, code FROM absence_types ORDER BY id`)
		var list []AbsenceType
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var t AbsenceType
				rows.Scan(&t.ID, &t.Name, &t.Code)
				list = append(list, t)
			}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var t AbsenceType
		json.NewDecoder(r.Body).Decode(&t)
		res, err := db.Exec(`INSERT INTO absence_types (name, code) VALUES (?,?)`, t.Name, t.Code)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		id, _ := res.LastInsertId()
		t.ID = int(id)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(t)
	case http.MethodPut:
		var t AbsenceType
		json.NewDecoder(r.Body).Decode(&t)
		db.Exec(`UPDATE absence_types SET name=?, code=? WHERE id=?`, t.Name, t.Code, t.ID)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		db.Exec("DELETE FROM absence_types WHERE id=?", r.URL.Query().Get("id"))
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", 405)
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
		json.NewEncoder(w).Encode(list)
	case http.MethodPut:
		var req struct {
			ID     int    `json:"id"`
			Status string `json:"status"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		db.Exec(`UPDATE absences SET status=? WHERE id=?`, req.Status, req.ID)
		logAudit("admin", "UPDATE_REQUEST", r.RemoteAddr, fmt.Sprintf("id=%d status=%s", req.ID, req.Status))
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

func handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, _ := db.Query(`SELECT id, user_name, action, ip, details, COALESCE(datetime(timestamp,'localtime'),'') FROM audit_logs ORDER BY id DESC LIMIT 200`)
	var list []AuditLog
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var a AuditLog
			rows.Scan(&a.ID, &a.UserName, &a.Action, &a.IP, &a.Details, &a.Timestamp)
			list = append(list, a)
		}
	}
	json.NewEncoder(w).Encode(list)
}

func handleAdminTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		q := `SELECT id, user_name, date, task_description,
			COALESCE(status,'Нова'), COALESCE(priority,'Базова'), work_started_at,
			COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),''),
			COALESCE(visible_from,''), COALESCE(due_date,''), COALESCE(created_by,''), COALESCE(responsible,'')
			FROM daily_tasks WHERE 1=1`
		args := []interface{}{}
		if v := r.URL.Query().Get("user"); v != "" {
			q += " AND user_name = ?"
			args = append(args, v)
		}
		if v := r.URL.Query().Get("status"); v != "" {
			q += " AND status = ?"
			args = append(args, v)
		}
		if v := r.URL.Query().Get("priority"); v != "" {
			q += " AND priority = ?"
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
		q += " ORDER BY date DESC, id DESC"
		rows, err := db.Query(q, args...)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var list []DailyTask
		for rows.Next() {
			var t DailyTask
			var ws, ca, vis, due, cby, resp sql.NullString
			rows.Scan(&t.ID, &t.UserName, &t.Date, &t.TaskDescription, &t.Status, &t.Priority, &ws, &t.TotalMinutes, &ca, &vis, &due, &cby, &resp)
			if ws.Valid {
				t.WorkStartedAt = ws.String
			}
			if ca.Valid {
				t.CreatedAt = ca.String
			}
			if vis.Valid {
				t.VisibleFrom = vis.String
			}
			if due.Valid {
				t.DueDate = due.String
			}
			if cby.Valid {
				t.CreatedBy = cby.String
			}
			if resp.Valid {
				t.Responsible = resp.String
			}
			list = append(list, t)
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPut:
		var raw map[string]interface{}
		json.NewDecoder(r.Body).Decode(&raw)
		b, _ := json.Marshal(raw)
		var t DailyTask
		json.Unmarshal(b, &t)
		if t.ID == 0 {
			http.Error(w, "id required", 400)
			return
		}
		var cur DailyTask
		var ws, ca, visN, dueN, respN sql.NullString
		err := db.QueryRow(`SELECT id, user_name, date, task_description, COALESCE(status,'Нова'), COALESCE(priority,'Базова'),
			work_started_at, COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),''),
			COALESCE(visible_from,''), COALESCE(due_date,''), COALESCE(responsible,'')
			FROM daily_tasks WHERE id=?`, t.ID).
			Scan(&cur.ID, &cur.UserName, &cur.Date, &cur.TaskDescription, &cur.Status, &cur.Priority, &ws, &cur.TotalMinutes, &ca, &visN, &dueN, &respN)
		if err != nil {
			http.Error(w, "not found", 404)
			return
		}
		if ws.Valid {
			cur.WorkStartedAt = ws.String
		}
		newStatus := t.Status
		if newStatus == "" {
			newStatus = cur.Status
		}
		newPriority := t.Priority
		if newPriority == "" {
			newPriority = cur.Priority
		}
		if !isStatusAllowed(cur.Status, newStatus, "admin") {
			http.Error(w, "Недозволений перехід: "+cur.Status+" → "+newStatus, 400)
			return
		}
		total := cur.TotalMinutes
		workStarted := cur.WorkStartedAt
		if cur.Status == "У роботі" && newStatus != "У роботі" {
			tmp := cur
			accumulateWorkTime(&tmp)
			total = tmp.TotalMinutes
			workStarted = ""
		}
		var workArg interface{}
		if workStarted == "" {
			workArg = nil
		} else {
			workArg = workStarted
		}
		userName := t.UserName
		if userName == "" {
			userName = cur.UserName
		}
		db.Exec(`UPDATE daily_tasks SET status=?, priority=?, work_started_at=?, total_minutes=?, user_name=? WHERE id=?`,
			newStatus, newPriority, workArg, total, userName, t.ID)
		_ = b
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		db.Exec("DELETE FROM daily_tasks WHERE id=?", r.URL.Query().Get("id"))
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

func handleDBStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !checkDBAdminPassword(r) {
		http.Error(w, "Потрібен пароль доступу до бази (заголовок X-DB-Admin-Password)", 403)
		return
	}
	rows, _ := db.Query(`SELECT table_name, COALESCE(row_count,0), COALESCE(last_action,''), COALESCE(datetime(last_update,'localtime'),'') FROM table_tracker`)
	var list []TableStat
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var t TableStat
			rows.Scan(&t.TableName, &t.RowCount, &t.LastAction, &t.LastUpdate)
			var cnt int
			db.QueryRow("SELECT COUNT(*) FROM " + t.TableName).Scan(&cnt)
			t.RowCount = cnt
			list = append(list, t)
		}
	}
	json.NewEncoder(w).Encode(list)
}

func handleReadOnlyQuery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	if !checkDBAdminPassword(r) {
		http.Error(w, "Потрібен пароль доступу до бази (заголовок X-DB-Admin-Password)", 403)
		return
	}
	var req struct {
		SQL string `json:"sql"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s := strings.TrimSpace(req.SQL)
	up := strings.ToUpper(s)
	if !strings.HasPrefix(up, "SELECT") || strings.Contains(up, "INSERT") || strings.Contains(up, "UPDATE") ||
		strings.Contains(up, "DELETE") || strings.Contains(up, "DROP") || strings.Contains(up, "ALTER") {
		http.Error(w, "only SELECT allowed", 400)
		return
	}
	rows, err := db.Query(s)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var result []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		row := map[string]interface{}{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = vals[i]
			}
		}
		result = append(result, row)
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"columns": cols, "rows": result})
}

func handleRegenerateShifts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	if !checkDBAdminPassword(r) {
		http.Error(w, "Потрібен пароль доступу до бази", 403)
		return
	}
	db.Exec(`DELETE FROM shifts`)
	json.NewEncoder(w).Encode(map[string]string{"status": "shifts cleared — reload calendar to regenerate"})
}
