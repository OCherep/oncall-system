package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

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
        FROM users u LEFT JOIN team_roles tr ON u.team_role_id = tr.id
        WHERE u.username = ? AND u.password = ?`, req.Username, req.Password).
		Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.TeamRoleID, &u.TeamRole, &isOncallInt)
	if err != nil {
		logAudit(req.Username, "LOGIN_FAILED", r.RemoteAddr, "Невдала спроба входу")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Невірне ім'я користувача або пароль"})
		return
	}
	u.IsOncall = isOncallInt == 1
	logAudit(u.Username, "LOGIN_SUCCESS", r.RemoteAddr, "Успішна авторизація")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u)
}

func isAbsentOnDate(userName, dateStr string, absences []AbsenceRequest) bool {
	for _, a := range absences {
		if a.UserName == userName && dateStr >= a.StartDate && dateStr <= a.EndDate {
			return true
		}
	}
	return false
}

func absenceRank(typ string) int {
	n := strings.ToLower(typ)
	switch {
	case strings.Contains(n, "ікарн"), strings.Contains(n, "sick"):
		return 30
	case strings.Contains(n, "ідпуст"), strings.Contains(n, "vacation"):
		return 20
	case strings.Contains(n, "ихідн"), strings.Contains(n, "dayoff"):
		return 10
	default:
		return 5
	}
}

func generateShifts(year, month int, oncallUsers []string, absences []AbsenceRequest) map[string]Shift {
	shifts := make(map[string]Shift)
	daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if len(oncallUsers) == 0 {
		return shifts
	}
	rr := 0
	for d := 1; d <= daysInMonth; d++ {
		dateStr := fmt.Sprintf("%04d-%02d-%02d", year, month, d)
		var available []string
		for _, name := range oncallUsers {
			if !isAbsentOnDate(name, dateStr, absences) {
				available = append(available, name)
			}
		}
		if len(available) == 0 {
			continue
		}
		pIdx := rr % len(available)
		primary := available[pIdx]
		backup := ""
		if len(available) > 1 {
			backup = available[(pIdx+1)%len(available)]
		}
		rr++
		shifts[dateStr] = Shift{Date: dateStr, PrimaryUser: primary, BackupUser: backup}
	}
	return shifts
}

func scanDailyTask(scanner interface{ Scan(...interface{}) error }) (DailyTask, error) {
	var t DailyTask
	var workStarted, created, vis, due, cby, resp sql.NullString
	var est, iid sql.NullInt64
	err := scanner.Scan(&t.ID, &t.UserName, &t.Date, &t.TaskDescription, &t.Status, &t.Priority, &workStarted, &t.TotalMinutes, &created, &vis, &due, &cby, &resp, &est, &iid)
	if workStarted.Valid {
		t.WorkStartedAt = workStarted.String
	}
	if created.Valid {
		t.CreatedAt = created.String
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
	if est.Valid {
		t.EstimatedMinutes = int(est.Int64)
	}
	if iid.Valid {
		t.IncidentID = int(iid.Int64)
	}
	if t.Status == "" {
		t.Status = "Нова"
	}
	if t.Priority == "" {
		t.Priority = "Базова"
	}
	return t, err
}

func scanIncident(scanner interface{ Scan(...interface{}) error }) (IncidentReport, error) {
	var inc IncidentReport
	var st, pr, src, resp, cby, ws, due, rf sql.NullString
	var tot sql.NullInt64
	err := scanner.Scan(&inc.ID, &inc.UserName, &inc.Date, &inc.Type, &inc.DurationMinutes, &inc.Description, &inc.CreatedAt,
		&st, &pr, &src, &resp, &cby, &ws, &tot, &due, &rf)
	if st.Valid {
		inc.Status = st.String
	}
	if pr.Valid {
		inc.Priority = pr.String
	}
	if src.Valid {
		inc.Source = src.String
	}
	if resp.Valid {
		inc.Responsible = resp.String
	}
	if cby.Valid {
		inc.CreatedBy = cby.String
	}
	if ws.Valid {
		inc.WorkStartedAt = ws.String
	}
	if tot.Valid {
		inc.TotalMinutes = int(tot.Int64)
	}
	if due.Valid {
		inc.DueDate = due.String
	}
	if rf.Valid {
		inc.ReportedFor = rf.String
	}
	if inc.Status == "" {
		inc.Status = "Нове"
	}
	if inc.Priority == "" {
		inc.Priority = "Звичайний"
	}
	if inc.Source == "" {
		inc.Source = "self"
	}
	return inc, err
}

const incidentSelect = `SELECT id, user_name, date, type, COALESCE(duration_minutes,0), description,
	COALESCE(datetime(created_at,'localtime'),''),
	COALESCE(status,'Нове'), COALESCE(priority,'Звичайний'), COALESCE(source,'self'),
	COALESCE(responsible,''), COALESCE(created_by,''), work_started_at,
	COALESCE(total_minutes,0), COALESCE(due_date,''), COALESCE(reported_for,'')
	FROM incidents`

func allowedNextStatuses(cur, role string) []string {
	isAdmin := role == "admin"
	switch cur {
	case "Нова":
		if isAdmin {
			return []string{"Нова", "У роботі", "Архів"}
		}
		return []string{"У роботі"}
	case "У роботі":
		if isAdmin {
			return []string{"У роботі", "На паузі", "До перевірки", "Виконана", "Архів"}
		}
		return []string{"На паузі", "До перевірки"}
	case "На паузі":
		if isAdmin {
			return []string{"На паузі", "У роботі", "До перевірки", "Архів"}
		}
		return []string{"У роботі"}
	case "До перевірки":
		if isAdmin {
			return []string{"До перевірки", "У роботі", "Виконана", "Архів"}
		}
		return []string{"Виконана"}
	case "Виконана":
		if isAdmin {
			return []string{"Виконана", "Перевідкрита", "Архів"}
		}
		return []string{}
	case "Перевідкрита", "Архів":
		if isAdmin {
			return []string{"Перевідкрита", "Нова", "Архів", "У роботі"}
		}
		return []string{}
	default:
		if isAdmin {
			return []string{cur, "Нова", "У роботі", "На паузі", "До перевірки", "Виконана", "Перевідкрита", "Архів"}
		}
		return []string{"У роботі"}
	}
}

func isStatusAllowed(cur, next, role string) bool {
	if next == "" || next == cur {
		return true
	}
	for _, s := range allowedNextStatuses(cur, role) {
		if s == next {
			return true
		}
	}
	return false
}

func incidentNextStatuses(cur, role string) []string {
	isAdmin := role == "admin"
	switch cur {
	case "Нове":
		return []string{"Нове", "В роботі"}
	case "В роботі":
		return []string{"В роботі", "На паузі", "Вирішено"}
	case "На паузі":
		return []string{"На паузі", "В роботі"}
	case "Вирішено":
		if isAdmin {
			return []string{"Вирішено", "Архів", "Нове"}
		}
		return []string{}
	case "Архів":
		if isAdmin {
			return []string{"Архів", "Нове"}
		}
		return []string{}
	default:
		return []string{"Нове", "В роботі"}
	}
}

func isIncidentStatusAllowed(cur, next, role string) bool {
	if next == "" || next == cur {
		return true
	}
	for _, s := range incidentNextStatuses(cur, role) {
		if s == next {
			return true
		}
	}
	return false
}

func handleGetData(w http.ResponseWriter, r *http.Request) {
	year := time.Now().Year()
	month := int(time.Now().Month())
	if y := r.URL.Query().Get("year"); y != "" {
		fmt.Sscanf(y, "%d", &year)
	}
	if m := r.URL.Query().Get("month"); m != "" {
		fmt.Sscanf(m, "%d", &month)
	}
	viewer := r.URL.Query().Get("viewer") // name of logged-in user for personal alerts

	rows, err := db.Query(`SELECT u.id, u.username, u.name, u.role, u.team_role_id, COALESCE(tr.name, ''), COALESCE(u.is_oncall, 1)
		FROM users u LEFT JOIN team_roles tr ON u.team_role_id = tr.id WHERE u.role != 'admin' OR COALESCE(u.is_oncall, 0) = 1 ORDER BY u.name`)
	var team []User
	var oncallUsers []string
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var u User
			var isOn int
			rows.Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.TeamRoleID, &u.TeamRole, &isOn)
			u.IsOncall = isOn == 1
			team = append(team, u)
			if u.IsOncall {
				oncallUsers = append(oncallUsers, u.Name)
			}
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

	monthLike := fmt.Sprintf("%04d-%02d-%%", year, month)
	db.Exec(`DELETE FROM shifts WHERE date LIKE ?`, monthLike)
	shifts := generateShifts(year, month, oncallUsers, absences)
	for _, s := range shifts {
		db.Exec(`INSERT OR REPLACE INTO shifts (date, primary_user, backup_user) VALUES (?, ?, ?)`, s.Date, s.PrimaryUser, s.BackupUser)
	}

	incRows, err := db.Query(incidentSelect+` WHERE date LIKE ? ORDER BY created_at ASC`, monthLike)
	incidents := make(map[string][]IncidentReport)
	statsMap := make(map[string]*UserStat)
	for _, name := range oncallUsers {
		statsMap[name] = &UserStat{Name: name}
	}
	if err == nil {
		defer incRows.Close()
		for incRows.Next() {
			inc, _ := scanIncident(incRows)
			incidents[inc.Date] = append(incidents[inc.Date], inc)
			mins := inc.DurationMinutes
			if inc.TotalMinutes > mins {
				mins = inc.TotalMinutes
			}
			if _, exists := statsMap[inc.UserName]; !exists && inc.UserName != "" {
				statsMap[inc.UserName] = &UserStat{Name: inc.UserName}
			}
			if inc.UserName != "" {
				statsMap[inc.UserName].IncidentMinutes += mins
			}
		}
	}
	for _, s := range shifts {
		if st, ok := statsMap[s.PrimaryUser]; ok {
			st.PrimaryCount++
		} else if s.PrimaryUser != "" {
			statsMap[s.PrimaryUser] = &UserStat{Name: s.PrimaryUser, PrimaryCount: 1}
		}
		if s.BackupUser != "" {
			if st, ok := statsMap[s.BackupUser]; ok {
				st.BackupCount++
			} else {
				statsMap[s.BackupUser] = &UserStat{Name: s.BackupUser, BackupCount: 1}
			}
		}
	}
	var stats []UserStat
	for _, st := range statsMap {
		stats = append(stats, *st)
	}
	taskRows, _ := db.Query(`SELECT id, user_name, date, task_description,
		COALESCE(status,'Нова'), COALESCE(priority,'Базова'), work_started_at,
		COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),''),
		COALESCE(visible_from,''), COALESCE(due_date,''), COALESCE(created_by,''), COALESCE(responsible,''),
		COALESCE(estimated_minutes,0), COALESCE(incident_id,0)
		FROM daily_tasks WHERE date LIKE ? ORDER BY id`, monthLike)
	dailyTasks := make(map[string][]DailyTask)
	if taskRows != nil {
		defer taskRows.Close()
		for taskRows.Next() {
			t, _ := scanDailyTask(taskRows)
			dailyTasks[t.Date] = append(dailyTasks[t.Date], t)
		}
	}
	atRows, _ := db.Query(`SELECT id, name, code, COALESCE(color,'') FROM absence_types ORDER BY name`)
	var absenceTypes []AbsenceType
	if atRows != nil {
		defer atRows.Close()
		for atRows.Next() {
			var t AbsenceType
			atRows.Scan(&t.ID, &t.Name, &t.Code, &t.Color)
			absenceTypes = append(absenceTypes, t)
		}
	}
	ipRows, _ := db.Query(`SELECT id, name, code, COALESCE(color,''), COALESCE(sort_order,0), COALESCE(is_default,0) FROM incident_priorities ORDER BY sort_order, id`)
	var incPrios []IncidentPriority
	if ipRows != nil {
		defer ipRows.Close()
		for ipRows.Next() {
			var p IncidentPriority
			var def int
			ipRows.Scan(&p.ID, &p.Name, &p.Code, &p.Color, &p.SortOrder, &def)
			p.IsDefault = def == 1
			incPrios = append(incPrios, p)
		}
	}

	today := time.Now().Format("2006-01-02")
	var alerts []map[string]string
	for _, t := range dailyTasks[today] {
		if t.UserName == "" {
			continue
		}
		if isAbsentOnDate(t.UserName, today, absences) {
			alerts = append(alerts, map[string]string{
				"level":   "warning",
				"message": fmt.Sprintf("Задача #%d («%s») призначена відсутньому %s", t.ID, truncate(t.TaskDescription, 40), t.UserName),
			})
		}
	}
	if len(oncallUsers) > 0 {
		if _, ok := shifts[today]; !ok {
			alerts = append(alerts, map[string]string{
				"level":   "error",
				"message": "На сьогодні немає доступних чергових (усі on-call у відсутності)",
			})
		}
	}
	// personal / responsible alerts for non-self incidents today
	for _, inc := range incidents[today] {
		if inc.Status == "Вирішено" || inc.Status == "Архів" {
			continue
		}
		selfSource := inc.Source == "self"
		if selfSource && inc.CreatedBy != "" && inc.CreatedBy == inc.UserName {
			continue
		}
		if !selfSource || (inc.CreatedBy != "" && inc.CreatedBy != inc.UserName) {
			if viewer != "" && (inc.UserName == viewer || inc.Responsible == viewer) {
				alerts = append(alerts, map[string]string{
					"level":   "warning",
					"message": fmt.Sprintf("Звернення #%d (%s) → %s · джерело: %s", inc.ID, truncate(inc.Description, 36), inc.UserName, inc.Source),
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"year": year, "month": month,
		"team_members": team, "absence_types": absenceTypes,
		"shifts": shifts, "absences": absences, "incidents": incidents, "stats": stats,
		"daily_tasks":         dailyTasks,
		"incident_priorities":  incPrios,
		"alerts":              alerts,
		"today":               today,
	})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func handleRequestAbsence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req AbsenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.UserName == "" || req.Type == "" || req.StartDate == "" || req.EndDate == "" {
		http.Error(w, "required fields", http.StatusBadRequest)
		return
	}
	req.Status = "Pending"
	_, err := db.Exec(`INSERT INTO absences (user_name, type, start_date, end_date, status) VALUES (?, ?, ?, ?, ?)`,
		req.UserName, req.Type, req.StartDate, req.EndDate, req.Status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logAudit(req.UserName, "REQUEST_ABSENCE", r.RemoteAddr, req.Type+" "+req.StartDate+"-"+req.EndDate)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleIncidents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		date := r.URL.Query().Get("date")
		q := incidentSelect + " WHERE 1=1"
		args := []interface{}{}
		if date != "" {
			q += " AND date = ?"
			args = append(args, date)
		}
		q += " ORDER BY id DESC LIMIT 500"
		rows, err := db.Query(q, args...)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var list []IncidentReport
		for rows.Next() {
			inc, _ := scanIncident(rows)
			list = append(list, inc)
		}
		json.NewEncoder(w).Encode(list)

	case http.MethodPost:
		var inc IncidentReport
		if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if inc.Date == "" || inc.Description == "" {
			http.Error(w, "Потрібні date, description", http.StatusBadRequest)
			return
		}
		if inc.Type == "" {
			inc.Type = "Звернення"
		}
		if inc.Status == "" {
			inc.Status = "Нове"
		}
		if inc.Priority == "" {
			inc.Priority = "Звичайний"
		}
		if inc.Source == "" {
			if inc.CreatedBy != "" && inc.UserName != "" && inc.CreatedBy != inc.UserName {
				inc.Source = "team_lead"
			} else {
				inc.Source = "self"
			}
		}
		if inc.ReportedFor == "" {
			inc.ReportedFor = inc.UserName
		}
		role := inc.Role
		today := time.Now().Format("2006-01-02")
		isAdmin := role == "admin"
		if !isAdmin && inc.Date < today {
			http.Error(w, "Без admin можна фіксувати лише на поточну або майбутню дату", http.StatusForbidden)
			return
		}
		if !isAdmin && inc.UserName == "" {
			http.Error(w, "user_name required", http.StatusBadRequest)
			return
		}
		res, err := db.Exec(`INSERT INTO incidents (user_name, date, type, duration_minutes, description, created_at,
			status, priority, source, responsible, created_by, total_minutes, due_date, reported_for)
			VALUES (?,?,?,?,?,CURRENT_TIMESTAMP,?,?,?,?,?,?,?,?)`,
			inc.UserName, inc.Date, inc.Type, inc.DurationMinutes, inc.Description,
			inc.Status, inc.Priority, inc.Source, inc.Responsible, inc.CreatedBy, 0, inc.DueDate, inc.ReportedFor)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		inc.ID = int(id)
		result := map[string]interface{}{"status": "ok", "id": inc.ID, "as_task": false}
		if inc.Date > today {
			desc := "[для розгляду на дейлі] " + inc.Description
			db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at, created_by, incident_id)
				VALUES (?, ?, ?, 'Нова', 'У шухляду', 0, CURRENT_TIMESTAMP, ?, ?)`,
				inc.UserName, inc.Date, desc, inc.CreatedBy, inc.ID)
			result["as_task"] = true
			result["message"] = "Звернення зафіксовано і додано як задачу «для розгляду на дейлі»"
			logAudit(inc.CreatedBy, "CREATE_INCIDENT_AS_TASK", r.RemoteAddr, desc)
		} else {
			logAudit(inc.CreatedBy, "CREATE_INCIDENT", r.RemoteAddr, fmt.Sprintf("#%d %s src=%s", inc.ID, inc.Date, inc.Source))
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(result)

	case http.MethodPut:
		var raw map[string]interface{}
		json.NewDecoder(r.Body).Decode(&raw)
		b, _ := json.Marshal(raw)
		var inc IncidentReport
		json.Unmarshal(b, &inc)
		if inc.ID == 0 {
			http.Error(w, "id required", 400)
			return
		}
		roleHint := "user"
		if v, ok := raw["role"].(string); ok && v != "" {
			roleHint = v
		}
		rows, err := db.Query(incidentSelect+` WHERE id=?`, inc.ID)
		if err != nil {
			http.Error(w, "not found", 404)
			return
		}
		defer rows.Close()
		if !rows.Next() {
			http.Error(w, "not found", 404)
			return
		}
		cur, _ := scanIncident(rows)
		newStatus := inc.Status
		if newStatus == "" {
			newStatus = cur.Status
		}
		if !isIncidentStatusAllowed(cur.Status, newStatus, roleHint) {
			http.Error(w, "Недозволений перехід: "+cur.Status+" → "+newStatus, 400)
			return
		}
		userName := cur.UserName
		if _, ok := raw["user_name"]; ok && roleHint == "admin" {
			userName = inc.UserName
		}
		prio := cur.Priority
		if _, ok := raw["priority"]; ok {
			prio = inc.Priority
		}
		resp := cur.Responsible
		if _, ok := raw["responsible"]; ok && roleHint == "admin" {
			resp = inc.Responsible
		}
		desc := cur.Description
		if _, ok := raw["description"]; ok && inc.Description != "" {
			desc = inc.Description
		}
		total := cur.TotalMinutes
		ws := cur.WorkStartedAt
		if cur.Status == "В роботі" && newStatus != "В роботі" {
			if ws != "" {
				start, e := time.Parse(time.RFC3339, ws)
				if e != nil {
					start, e = time.Parse("2006-01-02 15:04:05", ws)
				}
				if e == nil {
					m := int(time.Since(start).Minutes())
					if m > 0 {
						total += m
					}
				}
			}
			ws = ""
		}
		if newStatus == "В роботі" && cur.Status != "В роботі" {
			ws = time.Now().Format(time.RFC3339)
		}
		var wsArg interface{}
		if ws == "" {
			wsArg = nil
		} else {
			wsArg = ws
		}
		db.Exec(`UPDATE incidents SET status=?, priority=?, user_name=?, responsible=?, description=?, total_minutes=?, work_started_at=? WHERE id=?`,
			newStatus, prio, userName, resp, desc, total, wsArg, inc.ID)
		logAudit(roleHint, "UPDATE_INCIDENT", r.RemoteAddr, fmt.Sprintf("id=%d %s→%s", inc.ID, cur.Status, newStatus))
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleComments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		et := r.URL.Query().Get("entity_type")
		eid := r.URL.Query().Get("entity_id")
		rows, err := db.Query(`SELECT id, entity_type, entity_id, author_name, body, COALESCE(is_system,0),
			COALESCE(datetime(created_at,'localtime'),'') FROM comments WHERE entity_type=? AND entity_id=? ORDER BY id`, et, eid)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var list []Comment
		for rows.Next() {
			var c Comment
			var sys int
			rows.Scan(&c.ID, &c.EntityType, &c.EntityID, &c.AuthorName, &c.Body, &sys, &c.CreatedAt)
			c.IsSystem = sys == 1
			list = append(list, c)
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var c Comment
		json.NewDecoder(r.Body).Decode(&c)
		if c.EntityType == "" || c.EntityID == 0 || c.Body == "" {
			http.Error(w, "entity_type, entity_id, body required", 400)
			return
		}
		sys := 0
		if c.IsSystem {
			sys = 1
		}
		res, err := db.Exec(`INSERT INTO comments (entity_type, entity_id, author_name, body, is_system, created_at)
			VALUES (?,?,?,?,?,CURRENT_TIMESTAMP)`, c.EntityType, c.EntityID, c.AuthorName, c.Body, sys)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		id, _ := res.LastInsertId()
		c.ID = int(id)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

func handleIncidentPrioritiesPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rows, _ := db.Query(`SELECT id, name, code, COALESCE(color,''), COALESCE(sort_order,0), COALESCE(is_default,0) FROM incident_priorities ORDER BY sort_order, id`)
	var list []IncidentPriority
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var p IncidentPriority
			var def int
			rows.Scan(&p.ID, &p.Name, &p.Code, &p.Color, &p.SortOrder, &def)
			p.IsDefault = def == 1
			list = append(list, p)
		}
	}
	json.NewEncoder(w).Encode(list)
}

func accumulateWorkTime(t *DailyTask) {
	if (t.Status == "У роботі" || t.Status == "На паузі") && t.WorkStartedAt != "" {
		start, err := time.Parse(time.RFC3339, t.WorkStartedAt)
		if err != nil {
			start, err = time.Parse("2006-01-02 15:04:05", t.WorkStartedAt)
		}
		if err == nil {
			mins := int(time.Since(start).Minutes())
			if mins > 0 {
				t.TotalMinutes += mins
			}
		}
		t.WorkStartedAt = ""
	}
}

func handleDailyTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodPost:
		var t DailyTask
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if t.Date == "" || t.TaskDescription == "" {
			http.Error(w, "Потрібні date, task_description", http.StatusBadRequest)
			return
		}
		if t.Status == "" {
			t.Status = "Нова"
		}
		if t.Priority == "" {
			t.Priority = "Базова"
		}
		if t.VisibleFrom == "" {
			t.VisibleFrom = t.Date
		}
		res, err := db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at, visible_from, due_date, created_by, responsible, estimated_minutes, incident_id)
			VALUES (?, ?, ?, ?, ?, 0, CURRENT_TIMESTAMP, ?, ?, ?, ?, ?, ?)`,
			t.UserName, t.Date, t.TaskDescription, t.Status, t.Priority, t.VisibleFrom, t.DueDate, t.CreatedBy, t.Responsible, t.EstimatedMinutes, t.IncidentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		t.ID = int(id)
		logAudit(t.UserName, "CREATE_DAILY_TASK", r.RemoteAddr, t.TaskDescription)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(t)

	case http.MethodPut:
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(raw)
		var t DailyTask
		json.Unmarshal(b, &t)
		if t.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		roleHint := "user"
		if v, ok := raw["role"].(string); ok && v != "" {
			roleHint = v
		}
		var cur DailyTask
		var ws, ca, visN, dueN, respN sql.NullString
		var estN, iidN sql.NullInt64
		err := db.QueryRow(`SELECT id, user_name, date, task_description, COALESCE(status,'Нова'), COALESCE(priority,'Базова'),
			work_started_at, COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),''),
			COALESCE(visible_from,''), COALESCE(due_date,''), COALESCE(responsible,''), COALESCE(estimated_minutes,0), COALESCE(incident_id,0)
			FROM daily_tasks WHERE id=?`, t.ID).
			Scan(&cur.ID, &cur.UserName, &cur.Date, &cur.TaskDescription, &cur.Status, &cur.Priority, &ws, &cur.TotalMinutes, &ca, &visN, &dueN, &respN, &estN, &iidN)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if ws.Valid {
			cur.WorkStartedAt = ws.String
		}
		if visN.Valid {
			cur.VisibleFrom = visN.String
		}
		if dueN.Valid {
			cur.DueDate = dueN.String
		}
		if respN.Valid {
			cur.Responsible = respN.String
		}
		if estN.Valid {
			cur.EstimatedMinutes = int(estN.Int64)
		}
		newStatus := t.Status
		if newStatus == "" {
			newStatus = cur.Status
		}
		newPriority := t.Priority
		if newPriority == "" {
			newPriority = cur.Priority
		}
		desc := t.TaskDescription
		if desc == "" {
			desc = cur.TaskDescription
		}
		date := t.Date
		if date == "" {
			date = cur.Date
		}
		userName := cur.UserName
		if _, hasUser := raw["user_name"]; hasUser {
			if roleHint != "admin" {
				http.Error(w, "Призначати виконавця може лише admin", http.StatusForbidden)
				return
			}
			userName = t.UserName
		}
		if newStatus == "Перевідкрита" {
			if roleHint != "admin" {
				http.Error(w, "Лише admin може перевідкрити задачу", http.StatusForbidden)
				return
			}
			if cur.Status != "Виконана" && cur.Status != "Архів" {
				http.Error(w, "Перевідкрити можна лише виконану/архівну задачу", http.StatusBadRequest)
				return
			}
			newStatus = "Нова"
		}
		if cur.UserName == "" && userName == "" && roleHint != "admin" {
			http.Error(w, "Задача «на розгляді»: змінювати статус може лише admin", http.StatusForbidden)
			return
		}
		if !isStatusAllowed(cur.Status, newStatus, roleHint) {
			http.Error(w, "Недозволений перехід статусу: "+cur.Status+" → "+newStatus, http.StatusBadRequest)
			return
		}
		total := cur.TotalMinutes
		workStarted := cur.WorkStartedAt
		activeWork := cur.Status == "У роботі"
		newActive := newStatus == "У роботі"
		if activeWork && !newActive {
			tmp := cur
			accumulateWorkTime(&tmp)
			total = tmp.TotalMinutes
			workStarted = ""
		}
		if newActive && !activeWork {
			workStarted = time.Now().Format(time.RFC3339)
		}
		if cur.Status == "У роботі" && newStatus == "На паузі" {
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
		vis := t.VisibleFrom
		if vis == "" {
			vis = cur.VisibleFrom
		}
		due := cur.DueDate
		if _, hasDue := raw["due_date"]; hasDue && roleHint == "admin" {
			due = t.DueDate
		}
		resp := cur.Responsible
		if _, hasResp := raw["responsible"]; hasResp && roleHint == "admin" {
			resp = t.Responsible
		}
		est := cur.EstimatedMinutes
		if _, hasEst := raw["estimated_minutes"]; hasEst {
			est = t.EstimatedMinutes
		}
		db.Exec(`UPDATE daily_tasks SET user_name=?, date=?, task_description=?, status=?, priority=?,
			work_started_at=?, total_minutes=?, visible_from=?, due_date=?, responsible=?, estimated_minutes=? WHERE id=?`,
			userName, date, desc, newStatus, newPriority, workArg, total, vis, due, resp, est, t.ID)
		t.UserName, t.Date, t.TaskDescription = userName, date, desc
		t.Status, t.Priority, t.TotalMinutes, t.WorkStartedAt = newStatus, newPriority, total, workStarted
		t.EstimatedMinutes = est
		logAudit(userName, "UPDATE_DAILY_TASK", r.RemoteAddr, fmt.Sprintf("id=%d status=%s", t.ID, newStatus))
		json.NewEncoder(w).Encode(t)

	case http.MethodDelete:
		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		db.Exec("DELETE FROM daily_tasks WHERE id = ?", idStr)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
