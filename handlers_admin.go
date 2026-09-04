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
	if p == "" && r.Method == http.MethodPost {
		// try form / json body field without consuming body — use header primarily
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
		logAudit("admin", "DB_UNLOCK_FAILED", clientIP(r), "wrong password")
		http.Error(w, "Невірний пароль доступу до бази", 403)
		return
	}
	logAudit("admin", "DB_UNLOCK_OK", clientIP(r), "db tools unlocked")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	ensureUserExtraColumns()
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		ensureTeamsSchema()
		rows, err := db.Query(`SELECT u.id, u.username, u.name, u.role, u.team_role_id, COALESCE(tr.name,''), COALESCE(u.is_oncall,1),
			COALESCE(u.slack_id,''), COALESCE(u.email,''), COALESCE(u.phone,''), COALESCE(u.show_in_roster, 1),
			COALESCE(u.team_id,0), COALESCE(t.name,'')
			FROM users u
			LEFT JOIN team_roles tr ON u.team_role_id=tr.id
			LEFT JOIN teams t ON u.team_id=t.id
			ORDER BY u.id`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var list []User
		for rows.Next() {
			var u User
			var isOn, showR int
			rows.Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.TeamRoleID, &u.TeamRole, &isOn, &u.SlackID, &u.Email, &u.Phone, &showR, &u.TeamID, &u.TeamName)
			u.IsOncall = isOn == 1
			u.ShowInRoster = showR == 1
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
		show := 0
		if u.ShowInRoster {
			show = 1
		}
		if !u.ShowInRoster && u.IsOncall {
			// default: oncall visible unless explicitly false — ShowInRoster false stays 0
		}
		if u.TeamID <= 0 {
			u.TeamID = defaultTeamID()
		}
		res, err := db.Exec(`INSERT INTO users (username, password, name, role, team_role_id, is_oncall, slack_id, email, phone, show_in_roster, team_id) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, on, u.SlackID, u.Email, u.Phone, show, u.TeamID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		id, _ := res.LastInsertId()
		u.ID = int(id)
		logAudit("admin", "CREATE_USER", clientIP(r), u.Username)
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
		show := 0
		if u.ShowInRoster {
			show = 1
		}
		if u.TeamID <= 0 {
			u.TeamID = defaultTeamID()
		}
		if u.Password != "" {
			db.Exec(`UPDATE users SET username=?, password=?, name=?, role=?, team_role_id=?, is_oncall=?, slack_id=?, email=?, phone=?, show_in_roster=?, team_id=? WHERE id=?`,
				u.Username, u.Password, u.Name, u.Role, u.TeamRoleID, on, u.SlackID, u.Email, u.Phone, show, u.TeamID, u.ID)
		} else {
			db.Exec(`UPDATE users SET username=?, name=?, role=?, team_role_id=?, is_oncall=?, slack_id=?, email=?, phone=?, show_in_roster=?, team_id=? WHERE id=?`,
				u.Username, u.Name, u.Role, u.TeamRoleID, on, u.SlackID, u.Email, u.Phone, show, u.TeamID, u.ID)
		}
		logAudit("admin", "UPDATE_USER", clientIP(r), fmt.Sprintf("id=%d slack_id=%s", u.ID, u.SlackID))
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
		rows, err := db.Query(`SELECT id, name, code FROM absence_types ORDER BY id`)
		list := []AbsenceType{}
		if err == nil && rows != nil {
			defer rows.Close()
			for rows.Next() {
				var t AbsenceType
				rows.Scan(&t.ID, &t.Name, &t.Code)
				list = append(list, t)
			}
		}
		if len(list) == 0 {
			db.Exec(`INSERT OR IGNORE INTO absence_types (name, code) VALUES
				('Вихідний','dayoff'),('Відпустка','vacation'),('Командировка','trip'),('Лікарняний','sick')`)
			// absence_types may not have UNIQUE on name — plain insert if still empty
			rows2, _ := db.Query(`SELECT id, name, code FROM absence_types ORDER BY id`)
			list = []AbsenceType{}
			if rows2 != nil {
				defer rows2.Close()
				for rows2.Next() {
					var t AbsenceType
					rows2.Scan(&t.ID, &t.Name, &t.Code)
					list = append(list, t)
				}
			}
			if len(list) == 0 {
				for _, pair := range [][2]string{{"Вихідний","dayoff"},{"Відпустка","vacation"},{"Командировка","trip"},{"Лікарняний","sick"}} {
					db.Exec(`INSERT INTO absence_types (name, code) VALUES (?,?)`, pair[0], pair[1])
				}
				rows3, _ := db.Query(`SELECT id, name, code FROM absence_types ORDER BY id`)
				if rows3 != nil {
					defer rows3.Close()
					for rows3.Next() {
						var t AbsenceType
						rows3.Scan(&t.ID, &t.Name, &t.Code)
						list = append(list, t)
					}
				}
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

// absTypeRank: Лікарняний (здоров'я) > Відпустка > Вихідний
func absTypeRank(typeName string) int {
	n := strings.ToLower(typeName)
	if strings.Contains(n, "ікарн") || strings.Contains(n, "sick") {
		return 30
	}
	if strings.Contains(n, "ідпуст") || strings.Contains(n, "vacation") {
		return 20
	}
	if strings.Contains(n, "ихідн") || strings.Contains(n, "dayoff") {
		return 10
	}
	return 5
}

func datesOverlap(aStart, aEnd, bStart, bEnd string) bool {
	return aStart <= bEnd && bStart <= aEnd
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
		if req.ID == 0 || req.Status == "" {
			http.Error(w, "id and status required", 400)
			return
		}
		var cur AbsenceRequest
		err := db.QueryRow(`SELECT id, user_name, type, start_date, end_date, status FROM absences WHERE id=?`, req.ID).
			Scan(&cur.ID, &cur.UserName, &cur.Type, &cur.StartDate, &cur.EndDate, &cur.Status)
		if err != nil {
			http.Error(w, "not found", 404)
			return
		}
		db.Exec(`UPDATE absences SET status=? WHERE id=?`, req.Status, req.ID)
		go notifyAbsenceDecision(cur, req.Status, "admin")
		rejected := 0
		// При approve вищої пріоритетності — авто-відхилити нижчі перетинаючі заявки того ж користувача
		if req.Status == "Approved" {
			rank := absTypeRank(cur.Type)
			rows, _ := db.Query(`SELECT id, type, start_date, end_date, status FROM absences
				WHERE user_name=? AND id!=? AND status IN ('Pending','Approved')`, cur.UserName, cur.ID)
			if rows != nil {
				defer rows.Close()
				for rows.Next() {
					var oid int
					var otype, os, oe, ost string
					rows.Scan(&oid, &otype, &os, &oe, &ost)
					if absTypeRank(otype) >= rank {
						continue
					}
					if !datesOverlap(cur.StartDate, cur.EndDate, os, oe) {
						continue
					}
					db.Exec(`UPDATE absences SET status='Rejected' WHERE id=?`, oid)
					go notifyAbsenceDecision(AbsenceRequest{ID: oid, UserName: cur.UserName, Type: otype, StartDate: os, EndDate: oe, Status: "Rejected"}, "Rejected", "auto")
					logAudit("admin", "AUTO_REJECT_ABSENCE", clientIP(r),
						fmt.Sprintf("id=%d type=%s rejected due to higher %s id=%d", oid, otype, cur.Type, cur.ID))
					rejected++
				}
			}
		}
		logAudit("admin", "UPDATE_REQUEST", clientIP(r), fmt.Sprintf("id=%d status=%s auto_rejected=%d", req.ID, req.Status, rejected))
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "auto_rejected": rejected})
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

func handleAdminLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := `SELECT id, user_name, action, ip, details, COALESCE(datetime(timestamp,'localtime'),'') FROM audit_logs WHERE 1=1`
	args := []interface{}{}
	if v := r.URL.Query().Get("user"); v != "" {
		q += " AND user_name LIKE ?"
		args = append(args, "%"+v+"%")
	}
	if v := r.URL.Query().Get("action"); v != "" {
		q += " AND action LIKE ?"
		args = append(args, "%"+v+"%")
	}
	if v := r.URL.Query().Get("from"); v != "" {
		q += " AND date(timestamp) >= date(?)"
		args = append(args, v)
	}
	if v := r.URL.Query().Get("to"); v != "" {
		q += " AND date(timestamp) <= date(?)"
		args = append(args, v)
	}
	if v := r.URL.Query().Get("ip"); v != "" {
		q += " AND ip LIKE ?"
		args = append(args, "%"+v+"%")
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
		if limit <= 0 || limit > 1000 {
			limit = 200
		}
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	var list []AuditLog
	if err == nil && rows != nil {
		defer rows.Close()
		for rows.Next() {
			var a AuditLog
			rows.Scan(&a.ID, &a.UserName, &a.Action, &a.IP, &a.Details, &a.Timestamp)
			list = append(list, a)
		}
	}
	if list == nil {
		list = []AuditLog{}
	}
	json.NewEncoder(w).Encode(list)
}

// handleAppLogs — app_logs або fallback audit_logs з фільтром за категорією
func handleAppLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	filter := strings.TrimSpace(r.URL.Query().Get("app"))
	if filter == "" {
		filter = "All"
	}
	type line struct {
		Time    string `json:"time"`
		Level   string `json:"level"`
		Service string `json:"service"`
		Message string `json:"message"`
	}
	var list []line

	// 1) app_logs table
	q := `SELECT COALESCE(datetime(created_at,'localtime'),''), COALESCE(level,'info'), COALESCE(service,''), COALESCE(message,'')
		FROM app_logs WHERE 1=1`
	args := []interface{}{}
	if filter != "All" && filter != "all" {
		q += " AND (service LIKE ? OR message LIKE ?)"
		args = append(args, "%"+filter+"%", "%"+filter+"%")
	}
	q += " ORDER BY id DESC LIMIT 200"
	rows, err := db.Query(q, args...)
	if err == nil && rows != nil {
		for rows.Next() {
			var L line
			rows.Scan(&L.Time, &L.Level, &L.Service, &L.Message)
			list = append(list, L)
		}
		rows.Close()
	}

	// 2) fallback: audit_logs, map filter → action patterns
	if len(list) == 0 {
		aq := `SELECT COALESCE(datetime(timestamp,'localtime'),''), user_name, action, ip, details FROM audit_logs WHERE 1=1`
		aargs := []interface{}{}
		switch strings.ToLower(filter) {
		case "all", "":
			// no extra filter
		case "auth", "auth service", "login":
			aq += " AND (action LIKE ? OR action LIKE ? OR action LIKE ?)"
			aargs = append(aargs, "%LOGIN%", "%AUTH%", "%LOGOUT%")
		case "incidents", "incident", "звернення":
			aq += " AND (action LIKE ? OR action LIKE ?)"
			aargs = append(aargs, "%INCIDENT%", "%CONVERT%")
		case "tasks", "task", "задачі", "admin tasks":
			aq += " AND (action LIKE ? OR action LIKE ?)"
			aargs = append(aargs, "%TASK%", "%DAILY%")
		case "admin", "admin panel":
			aq += " AND (action LIKE ? OR action LIKE ? OR action LIKE ?)"
			aargs = append(aargs, "%ADMIN%", "%UPDATE_USER%", "%UPDATE_ROLE%")
		case "absence", "відсутності":
			aq += " AND (action LIKE ? OR action LIKE ?)"
			aargs = append(aargs, "%ABSENCE%", "%REQUEST%")
		case "oncall core", "oncall":
			// all audit = core
		default:
			// free-text match on action or details
			aq += " AND (action LIKE ? OR details LIKE ? OR user_name LIKE ?)"
			aargs = append(aargs, "%"+filter+"%", "%"+filter+"%", "%"+filter+"%")
		}
		aq += " ORDER BY id DESC LIMIT 200"
		arows, _ := db.Query(aq, aargs...)
		if arows != nil {
			for arows.Next() {
				var ts, user, action, ip, details string
				arows.Scan(&ts, &user, &action, &ip, &details)
				svc := "OnCall Core"
				al := strings.ToUpper(action)
				switch {
				case strings.Contains(al, "LOGIN") || strings.Contains(al, "AUTH"):
					svc = "Auth"
				case strings.Contains(al, "INCIDENT") || strings.Contains(al, "CONVERT"):
					svc = "Incidents"
				case strings.Contains(al, "TASK") || strings.Contains(al, "DAILY"):
					svc = "Tasks"
				case strings.Contains(al, "ABSENCE") || strings.Contains(al, "REQUEST"):
					svc = "Absence"
				case strings.Contains(al, "ADMIN") || strings.Contains(al, "USER") || strings.Contains(al, "ROLE"):
					svc = "Admin"
				}
				list = append(list, line{
					Time: ts, Level: "info", Service: svc,
					Message: fmt.Sprintf("[%s] %s @ %s — %s", action, user, ip, details),
				})
			}
			arows.Close()
		}
	}
	if list == nil {
		list = []line{}
	}
	json.NewEncoder(w).Encode(list)
}

// handleTableInspect — schema + rows for one table
func handleTableInspect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}
	// whitelist tables
	allowed := map[string]bool{}
	trows, _ := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if trows != nil {
		for trows.Next() {
			var n string
			trows.Scan(&n)
			allowed[n] = true
		}
		trows.Close()
	}
	if !allowed[name] {
		http.Error(w, "unknown table", 404)
		return
	}
	// schema
	var schema []map[string]interface{}
	cols, _ := db.Query(`PRAGMA table_info(` + name + `)`)
	if cols != nil {
		for cols.Next() {
			var cid, notnull, pk int
			var cname, ctype string
			var dflt interface{}
			cols.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk)
			schema = append(schema, map[string]interface{}{
				"cid": cid, "name": cname, "type": ctype, "notnull": notnull, "pk": pk, "dflt": dflt,
			})
		}
		cols.Close()
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
		if limit <= 0 || limit > 500 {
			limit = 100
		}
	}
	rows, err := db.Query(`SELECT * FROM ` + name + ` LIMIT ?`, limit)
	var data []map[string]interface{}
	var colnames []string
	if err == nil && rows != nil {
		defer rows.Close()
		colnames, _ = rows.Columns()
		for rows.Next() {
			raw := make([]interface{}, len(colnames))
			ptrs := make([]interface{}, len(colnames))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			rows.Scan(ptrs...)
			m := map[string]interface{}{}
			for i, c := range colnames {
				switch v := raw[i].(type) {
				case []byte:
					m[c] = string(v)
				default:
					m[c] = v
				}
			}
			data = append(data, m)
		}
	}
	if data == nil {
		data = []map[string]interface{}{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"table": name, "schema": schema, "columns": colnames, "rows": data, "limit": limit,
	})
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
		rows.Close()
		// Assignees AFTER rows closed (MaxOpenConns=1 — no nested queries)
		for i := range list {
			list[i].Assignees = loadTaskAssignees(list[i].ID)
		}
		if list == nil {
			list = []DailyTask{}
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
		// Admin path: reuse client handler logic by calling through HTTP is overkill;
		// forward fields into client-style update via direct SQL + status rules
		raw["role"] = "admin"
		body, _ := json.Marshal(raw)
		// Simulate client PUT by reusing same DB rules: minimal update path
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
		if newStatus == "У роботі" && cur.Status != "У роботі" {
			workStarted = ""
			// set later if needed
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
		// Full admin edit fields
		date := cur.Date
		if t.Date != "" {
			date = t.Date
		}
		desc := cur.TaskDescription
		if t.TaskDescription != "" {
			desc = t.TaskDescription
		}
		resp := cur.Responsible
		if _, ok := raw["responsible"]; ok {
			resp = t.Responsible
		}
		vis := cur.VisibleFrom
		if t.VisibleFrom != "" {
			vis = t.VisibleFrom
		}
		due := cur.DueDate
		if t.DueDate != "" {
			due = t.DueDate
		}
		db.Exec(`UPDATE daily_tasks SET status=?, priority=?, work_started_at=?, total_minutes=?, user_name=?,
			date=?, task_description=?, responsible=?, visible_from=?, due_date=? WHERE id=?`,
			newStatus, newPriority, workArg, total, userName, date, desc, resp, vis, due, t.ID)
		if newStatus != cur.Status {
			openTaskStatusLog(t.ID, newStatus, "admin")
		}
		// Multi-assignees
		if arr, ok := raw["assignees"].([]interface{}); ok {
			names := []string{}
			for _, x := range arr {
				if s, ok := x.(string); ok {
					names = append(names, s)
				}
			}
			setTaskAssignees(t.ID, names)
			if userName == "" && len(names) > 0 {
				db.Exec(`UPDATE daily_tasks SET user_name=? WHERE id=?`, names[0], t.ID)
			}
		} else if userName != "" {
			// ensure primary is in assignees
			db.Exec(`INSERT OR IGNORE INTO task_assignees (task_id, user_name, total_minutes) VALUES (?,?,0)`, t.ID, userName)
		}
		_ = body
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "assignees": loadTaskAssignees(t.ID)})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		db.Exec("DELETE FROM task_assignees WHERE task_id=?", id)
		db.Exec("DELETE FROM daily_tasks WHERE id=?", id)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

func handleDBStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	// Повний перелік таблиць з sqlite_master + live COUNT + tracker meta
	meta := map[string]TableStat{}
	mrows, _ := db.Query(`SELECT table_name, COALESCE(row_count,0), COALESCE(last_action,''), COALESCE(datetime(last_update,'localtime'),'') FROM table_tracker`)
	if mrows != nil {
		for mrows.Next() {
			var ts TableStat
			mrows.Scan(&ts.TableName, &ts.RowCount, &ts.LastAction, &ts.LastUpdate)
			meta[ts.TableName] = ts
		}
		mrows.Close()
	}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	var names []string
	if err == nil && rows != nil {
		for rows.Next() {
			var name string
			rows.Scan(&name)
			names = append(names, name)
		}
		rows.Close()
	}
	var list []TableStat
	for _, name := range names {
		ts := meta[name]
		ts.TableName = name
		var cnt int
		db.QueryRow("SELECT COUNT(*) FROM `" + name + "`").Scan(&cnt)
		ts.RowCount = cnt
		list = append(list, ts)
	}
	if list == nil {
		list = []TableStat{}
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


// handleAdminAllowedIPs — CRUD довідника дозволених IP
func handleAdminAllowedIPs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`SELECT id, cidr, COALESCE(label,''), COALESCE(enabled,1), COALESCE(datetime(created_at,'localtime'),'') FROM allowed_ips ORDER BY id`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		type row struct {
			ID      int    `json:"id"`
			CIDR    string `json:"cidr"`
			Label   string `json:"label"`
			Enabled bool   `json:"enabled"`
			Created string `json:"created_at"`
		}
		var list []row
		for rows.Next() {
			var x row
			var en int
			rows.Scan(&x.ID, &x.CIDR, &x.Label, &en, &x.Created)
			x.Enabled = en == 1
			list = append(list, x)
		}
		if list == nil {
			list = []row{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var req struct {
			CIDR    string `json:"cidr"`
			Label   string `json:"label"`
			Enabled *bool  `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		req.CIDR = strings.TrimSpace(req.CIDR)
		if req.CIDR == "" {
			http.Error(w, "cidr required", 400)
			return
		}
		en := 1
		if req.Enabled != nil && !*req.Enabled {
			en = 0
		}
		res, err := db.Exec(`INSERT INTO allowed_ips (cidr, label, enabled) VALUES (?,?,?)`, req.CIDR, req.Label, en)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		id, _ := res.LastInsertId()
		logAudit("admin", "ALLOWED_IP_ADD", clientIP(r), req.CIDR)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": id, "cidr": req.CIDR})
	case http.MethodPut:
		var req struct {
			ID      int    `json:"id"`
			CIDR    string `json:"cidr"`
			Label   string `json:"label"`
			Enabled *bool  `json:"enabled"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.ID <= 0 {
			http.Error(w, "id required", 400)
			return
		}
		if req.CIDR != "" {
			db.Exec(`UPDATE allowed_ips SET cidr=? WHERE id=?`, strings.TrimSpace(req.CIDR), req.ID)
		}
		if req.Label != "" || req.CIDR != "" {
			db.Exec(`UPDATE allowed_ips SET label=? WHERE id=?`, req.Label, req.ID)
		}
		if req.Enabled != nil {
			en := 0
			if *req.Enabled {
				en = 1
			}
			db.Exec(`UPDATE allowed_ips SET enabled=? WHERE id=?`, en, req.ID)
		}
		logAudit("admin", "ALLOWED_IP_UPDATE", clientIP(r), fmt.Sprintf("id=%d", req.ID))
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", 400)
			return
		}
		db.Exec(`DELETE FROM allowed_ips WHERE id=?`, id)
		logAudit("admin", "ALLOWED_IP_DELETE", clientIP(r), "id="+id)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
