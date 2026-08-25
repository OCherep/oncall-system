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
		backup := primary
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
	err := scanner.Scan(&t.ID, &t.UserName, &t.Date, &t.TaskDescription, &t.Status, &t.Priority, &workStarted, &t.TotalMinutes, &created, &vis, &due, &cby, &resp)
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
	if t.Status == "" {
		t.Status = "Нова"
	}
	if t.Priority == "" {
		t.Priority = "Базова"
	}
	return t, err
}

func allowedNextStatuses(cur, role string) []string {
	isAdmin := role == "admin"
	var next []string
	switch cur {
	case "Нова":
		if isAdmin {
			next = []string{"Нова", "У роботі", "Архів"}
		} else {
			next = []string{"Нова", "У роботі"}
		}
	case "У роботі":
		if isAdmin {
			next = []string{"У роботі", "На паузі", "До перевірки", "Виконана", "Архів"}
		} else {
			next = []string{"У роботі", "На паузі", "До перевірки"}
		}
	case "На паузі":
		if isAdmin {
			next = []string{"На паузі", "У роботі", "До перевірки", "Архів"}
		} else {
			next = []string{"На паузі", "У роботі"}
		}
	case "До перевірки":
		if isAdmin {
			next = []string{"До перевірки", "У роботі", "Виконана", "Архів"}
		} else {
			next = []string{"До перевірки", "Виконана"}
		}
	case "Виконана":
		if isAdmin {
			next = []string{"Виконана", "Перевідкрита", "Архів"}
		} else {
			next = []string{"Виконана"}
		}
	case "Перевідкрита", "Архів":
		if isAdmin {
			next = []string{"Перевідкрита", "Нова", "Архів", "У роботі"}
		} else {
			next = []string{cur}
		}
	default:
		if isAdmin {
			next = []string{cur, "Нова", "У роботі", "На паузі", "До перевірки", "Виконана", "Перевідкрита", "Архів"}
		} else {
			next = []string{cur, "У роботі"}
		}
	}
	return next
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

func handleGetData(w http.ResponseWriter, r *http.Request) {
	year := time.Now().Year()
	month := int(time.Now().Month())
	if y := r.URL.Query().Get("year"); y != "" {
		fmt.Sscanf(y, "%d", &year)
	}
	if m := r.URL.Query().Get("month"); m != "" {
		fmt.Sscanf(m, "%d", &month)
	}
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
	shifts := make(map[string]Shift)
	shRows, _ := db.Query(`SELECT date, primary_user, backup_user FROM shifts WHERE date LIKE ?`, fmt.Sprintf("%04d-%02d-%%", year, month))
	if shRows != nil {
		defer shRows.Close()
		for shRows.Next() {
			var s Shift
			shRows.Scan(&s.Date, &s.PrimaryUser, &s.BackupUser)
			shifts[s.Date] = s
		}
	}
	needRegen := len(shifts) == 0
	if !needRegen && len(oncallUsers) > 0 {
		oncallSet := map[string]bool{}
		for _, n := range oncallUsers {
			oncallSet[n] = true
		}
		for _, s := range shifts {
			if !oncallSet[s.PrimaryUser] || !oncallSet[s.BackupUser] {
				needRegen = true
				break
			}
		}
		if !needRegen {
			seen := map[string]bool{}
			for _, s := range shifts {
				seen[s.PrimaryUser] = true
				seen[s.BackupUser] = true
			}
			if len(seen) < len(oncallUsers) && len(oncallUsers) > 1 {
				needRegen = true
			}
		}
	}
	if needRegen {
		db.Exec(`DELETE FROM shifts WHERE date LIKE ?`, fmt.Sprintf("%04d-%02d-%%", year, month))
		shifts = generateShifts(year, month, oncallUsers, absences)
		for _, s := range shifts {
			db.Exec(`INSERT OR REPLACE INTO shifts (date, primary_user, backup_user) VALUES (?, ?, ?)`, s.Date, s.PrimaryUser, s.BackupUser)
		}
	}
	monthPattern := fmt.Sprintf("%04d-%02d-%%", year, month)
	incRows, err := db.Query(`SELECT id, user_name, date, type, duration_minutes, description,
		COALESCE(datetime(created_at,'localtime'), datetime('now','localtime')),
		COALESCE(status,'Нове'), COALESCE(priority,'Звичайний'), COALESCE(source,'self'),
		COALESCE(total_minutes,0), COALESCE(created_by,''), COALESCE(reported_for,'')
		FROM incidents WHERE date LIKE ? ORDER BY created_at ASC`, monthPattern)
	incidents := make(map[string][]IncidentReport)
	statsMap := make(map[string]*UserStat)
	for _, name := range oncallUsers {
		statsMap[name] = &UserStat{Name: name}
	}
	if err == nil {
		defer incRows.Close()
		for incRows.Next() {
			var inc IncidentReport
			incRows.Scan(&inc.ID, &inc.UserName, &inc.Date, &inc.Type, &inc.DurationMinutes, &inc.Description, &inc.CreatedAt,
				&inc.Status, &inc.Priority, &inc.Source, &inc.TotalMinutes, &inc.CreatedBy, &inc.ReportedFor)
			if inc.TotalMinutes == 0 {
				inc.TotalMinutes = inc.DurationMinutes
			}
			incidents[inc.Date] = append(incidents[inc.Date], inc)
			if _, exists := statsMap[inc.UserName]; !exists {
				statsMap[inc.UserName] = &UserStat{Name: inc.UserName}
			}
			mins := inc.TotalMinutes
			if mins == 0 {
				mins = inc.DurationMinutes
			}
			statsMap[inc.UserName].IncidentMinutes += mins
		}
	}
	for _, s := range shifts {
		if st, ok := statsMap[s.PrimaryUser]; ok {
			st.PrimaryCount++
		} else {
			statsMap[s.PrimaryUser] = &UserStat{Name: s.PrimaryUser, PrimaryCount: 1}
		}
		if st, ok := statsMap[s.BackupUser]; ok {
			st.BackupCount++
		} else {
			statsMap[s.BackupUser] = &UserStat{Name: s.BackupUser, BackupCount: 1}
		}
	}
	var stats []UserStat
	for _, st := range statsMap {
		stats = append(stats, *st)
	}
	taskRows, _ := db.Query(`SELECT id, user_name, date, task_description,
		COALESCE(status,'Нова'), COALESCE(priority,'Базова'), work_started_at,
		COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),''),
		COALESCE(visible_from,''), COALESCE(due_date,''), COALESCE(created_by,''), COALESCE(responsible,'')
		FROM daily_tasks WHERE date LIKE ? ORDER BY id`, monthPattern)
	dailyTasks := make(map[string][]DailyTask)
	if taskRows != nil {
		defer taskRows.Close()
		for taskRows.Next() {
			t, _ := scanDailyTask(taskRows)
			dailyTasks[t.Date] = append(dailyTasks[t.Date], t)
		}
	}
	atRows, _ := db.Query(`SELECT id, name, code FROM absence_types ORDER BY name`)
	var absenceTypes []AbsenceType
	if atRows != nil {
		defer atRows.Close()
		for atRows.Next() {
			var t AbsenceType
			atRows.Scan(&t.ID, &t.Name, &t.Code)
			absenceTypes = append(absenceTypes, t)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"year": year, "month": month,
		"team_members": team, "absence_types": absenceTypes,
		"shifts": shifts, "absences": absences, "incidents": incidents, "stats": stats,
		"daily_tasks": dailyTasks,
	})
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

func allowedIncNextStatuses(cur, role string) []string {
	isAdmin := role == "admin"
	switch cur {
	case "Нове", "":
		if isAdmin {
			return []string{"Нове", "В роботі", "Архів"}
		}
		return []string{"Нове", "В роботі"}
	case "В роботі":
		if isAdmin {
			return []string{"В роботі", "На паузі", "Вирішено", "Архів"}
		}
		return []string{"В роботі", "На паузі", "Вирішено"}
	case "На паузі":
		if isAdmin {
			return []string{"На паузі", "В роботі", "Архів"}
		}
		return []string{"На паузі", "В роботі"}
	case "Вирішено":
		if isAdmin {
			return []string{"Вирішено", "Архів", "Нове"}
		}
		return []string{"Вирішено"}
	case "Архів":
		if isAdmin {
			return []string{"Архів", "Нове"}
		}
		return []string{"Архів"}
	default:
		if isAdmin {
			return []string{cur, "Нове", "В роботі", "На паузі", "Вирішено", "Архів"}
		}
		return []string{cur, "В роботі"}
	}
}

func isIncStatusAllowed(cur, next, role string) bool {
	if next == "" || next == cur {
		return true
	}
	for _, s := range allowedIncNextStatuses(cur, role) {
		if s == next {
			return true
		}
	}
	return false
}

func handleIncidents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodPost:
		var inc IncidentReport
		if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if inc.UserName == "" || inc.Date == "" || inc.DurationMinutes <= 0 || inc.Description == "" {
			http.Error(w, "Необхідні поля: user_name, date, duration_minutes, description", http.StatusBadRequest)
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
			inc.Source = "self"
		}
		if inc.TotalMinutes == 0 {
			inc.TotalMinutes = inc.DurationMinutes
		}
		role := inc.Role
		if role == "" {
			db.QueryRow("SELECT role FROM users WHERE name = ? LIMIT 1", inc.UserName).Scan(&role)
		}
		today := time.Now().Format("2006-01-02")
		isAdmin := role == "admin"
		isFuture := inc.Date > today
		isPast := inc.Date < today
		if !isAdmin && isPast {
			http.Error(w, "Без ролі admin можна фіксувати звернення лише на поточну або майбутню дату", http.StatusForbidden)
			return
		}
		res, err := db.Exec(`INSERT INTO incidents (user_name, date, type, duration_minutes, description, created_at,
			status, priority, source, total_minutes, created_by, reported_for)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?, ?, ?, ?)`,
			inc.UserName, inc.Date, inc.Type, inc.DurationMinutes, inc.Description,
			inc.Status, inc.Priority, inc.Source, inc.TotalMinutes, inc.CreatedBy, inc.ReportedFor)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		result := map[string]interface{}{"status": "ok", "as_task": false, "id": id}
		if isFuture {
			desc := "[для розгляду на дейлі] " + inc.Description
			if inc.Type != "" {
				desc = "[для розгляду на дейлі] [" + inc.Type + "] " + inc.Description
			}
			db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at)
				VALUES (?, ?, ?, 'Нова', 'У шухляду', 0, CURRENT_TIMESTAMP)`,
				inc.UserName, inc.Date, desc)
			result["as_task"] = true
			result["message"] = "Звернення зафіксовано на " + inc.Date + " і додано як задачу «для розгляду на дейлі»"
			logAudit(inc.UserName, "CREATE_INCIDENT_AS_TASK", r.RemoteAddr, desc)
		} else {
			logAudit(inc.UserName, "CREATE_INCIDENT", r.RemoteAddr, fmt.Sprintf("%s %dхв", inc.Date, inc.DurationMinutes))
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(result)

	case http.MethodPut:
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		idF, _ := raw["id"].(float64)
		id := int(idF)
		if id == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		roleHint := "user"
		if v, ok := raw["role"].(string); ok && v != "" {
			roleHint = v
		}
		actor := ""
		if v, ok := raw["actor"].(string); ok {
			actor = v
		}
		newStatus, _ := raw["status"].(string)
		var cur IncidentReport
		err := db.QueryRow(`SELECT id, user_name, COALESCE(status,'Нове'), COALESCE(priority,'Звичайний'),
			COALESCE(source,'self'), duration_minutes, COALESCE(total_minutes,0), COALESCE(external_id,'')
			FROM incidents WHERE id=?`, id).
			Scan(&cur.ID, &cur.UserName, &cur.Status, &cur.Priority, &cur.Source, &cur.DurationMinutes, &cur.TotalMinutes, &cur.ExternalID)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if newStatus == "" {
			newStatus = cur.Status
		}
		if roleHint != "admin" && newStatus != cur.Status {
			if actor == "" {
				actor = cur.UserName
			}
			if actor != cur.UserName {
				http.Error(w, "Користувач може змінювати статус лише власних звернень", http.StatusForbidden)
				return
			}
		}
		if !isIncStatusAllowed(cur.Status, newStatus, roleHint) {
			http.Error(w, "Недозволений перехід статусу звернення: "+cur.Status+" → "+newStatus, http.StatusBadRequest)
			return
		}
		oldStatus := cur.Status
		db.Exec(`UPDATE incidents SET status=? WHERE id=?`, newStatus, id)
		if newStatus != oldStatus {
			addSystemComment("incident", id, fmt.Sprintf("Статус: %s → %s", oldStatus, newStatus))
			if cur.ExternalID != "" {
				syncIncidentStatusToJira(cur.ExternalID, oldStatus, newStatus)
			}
		}
		logAudit(cur.UserName, "UPDATE_INCIDENT", r.RemoteAddr, fmt.Sprintf("id=%d status=%s→%s", id, oldStatus, newStatus))
		cur.Status = newStatus
		json.NewEncoder(w).Encode(cur)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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
		res, err := db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at, visible_from, due_date, created_by, responsible)
			VALUES (?, ?, ?, ?, ?, 0, CURRENT_TIMESTAMP, ?, ?, ?, ?)`,
			t.UserName, t.Date, t.TaskDescription, t.Status, t.Priority, t.VisibleFrom, t.DueDate, t.CreatedBy, t.Responsible)
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
		actor := ""
		if v, ok := raw["actor"].(string); ok {
			actor = v
		}
		var cur DailyTask
		var ws, ca, visN, dueN, respN sql.NullString
		err := db.QueryRow(`SELECT id, user_name, date, task_description, COALESCE(status,'Нова'), COALESCE(priority,'Базова'),
			work_started_at, COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),''),
			COALESCE(visible_from,''), COALESCE(due_date,''), COALESCE(responsible,'')
			FROM daily_tasks WHERE id=?`, t.ID).
			Scan(&cur.ID, &cur.UserName, &cur.Date, &cur.TaskDescription, &cur.Status, &cur.Priority, &ws, &cur.TotalMinutes, &ca, &visN, &dueN, &respN)
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
		userName := t.UserName
		if userName == "" {
			userName = cur.UserName
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
		if cur.UserName == "" && roleHint != "admin" {
			http.Error(w, "Задача «на розгляді»: виконавця ще не призначено. Змінювати статус може лише admin", http.StatusForbidden)
			return
		}
		if roleHint != "admin" && newStatus != cur.Status {
			if actor == "" || actor != cur.UserName {
				http.Error(w, "Користувач може змінювати статус лише власних задач", http.StatusForbidden)
				return
			}
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
		due := t.DueDate
		if due == "" {
			due = cur.DueDate
		}
		resp := t.Responsible
		if resp == "" {
			resp = cur.Responsible
		}
		if roleHint != "admin" && t.UserName == "" {
			userName = cur.UserName
		}
		db.Exec(`UPDATE daily_tasks SET user_name=?, date=?, task_description=?, status=?, priority=?,
			work_started_at=?, total_minutes=?, visible_from=?, due_date=?, responsible=? WHERE id=?`,
			userName, date, desc, newStatus, newPriority, workArg, total, vis, due, resp, t.ID)
		t.UserName, t.Date, t.TaskDescription = userName, date, desc
		t.Status, t.Priority, t.TotalMinutes, t.WorkStartedAt = newStatus, newPriority, total, workStarted
		if newStatus != cur.Status {
			addSystemComment("task", t.ID, fmt.Sprintf("Статус: %s → %s", cur.Status, newStatus))
		}
		logAudit(userName, "UPDATE_DAILY_TASK", r.RemoteAddr, fmt.Sprintf("id=%d status=%s priority=%s", t.ID, newStatus, newPriority))
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

type Comment struct {
	ID         int    `json:"id"`
	EntityType string `json:"entity_type"`
	EntityID   int    `json:"entity_id"`
	AuthorName string `json:"author_name"`
	Body       string `json:"body"`
	IsSystem   bool   `json:"is_system"`
	CreatedAt  string `json:"created_at"`
}

func addSystemComment(entityType string, entityID int, body string) {
	db.Exec(`INSERT INTO comments (entity_type, entity_id, author_name, body, is_system) VALUES (?,?,?,?,1)`,
		entityType, entityID, "система", body)
}

func handleComments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		etype := r.URL.Query().Get("entity_type")
		eid := r.URL.Query().Get("entity_id")
		if etype == "" || eid == "" {
			http.Error(w, "entity_type and entity_id required", 400)
			return
		}
		rows, err := db.Query(`SELECT id, entity_type, entity_id, COALESCE(author_name,''), body, COALESCE(is_system,0),
			COALESCE(datetime(created_at,'localtime'),'') FROM comments
			WHERE entity_type=? AND entity_id=? ORDER BY id ASC`, etype, eid)
		if err != nil {
			json.NewEncoder(w).Encode([]Comment{})
			return
		}
		defer rows.Close()
		var list []Comment
		for rows.Next() {
			var c Comment
			var isSys int
			rows.Scan(&c.ID, &c.EntityType, &c.EntityID, &c.AuthorName, &c.Body, &isSys, &c.CreatedAt)
			c.IsSystem = isSys == 1
			list = append(list, c)
		}
		if list == nil {
			list = []Comment{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var c Comment
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if c.EntityType == "" || c.EntityID == 0 || strings.TrimSpace(c.Body) == "" {
			http.Error(w, "entity_type, entity_id, body required", 400)
			return
		}
		if c.EntityType != "task" && c.EntityType != "incident" {
			http.Error(w, "entity_type must be task or incident", 400)
			return
		}
		author := strings.TrimSpace(c.AuthorName)
		if author == "" {
			author = "гість"
		}
		res, err := db.Exec(`INSERT INTO comments (entity_type, entity_id, author_name, body, is_system) VALUES (?,?,?,?,0)`,
			c.EntityType, c.EntityID, author, strings.TrimSpace(c.Body))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		id, _ := res.LastInsertId()
		c.ID = int(id)
		c.AuthorName = author
		c.IsSystem = false
		logAudit(author, "ADD_COMMENT", r.RemoteAddr, fmt.Sprintf("%s#%d", c.EntityType, c.EntityID))
		json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "Method not allowed", 405)
	}
}

func handleAdminQueues(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	today := time.Now().Format("2006-01-02")
	byStatus := map[string]int{}
	rows, _ := db.Query(`SELECT COALESCE(status,'Нова'), COUNT(*) FROM daily_tasks
		WHERE COALESCE(status,'') NOT IN ('Архів') GROUP BY COALESCE(status,'Нова')`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var s string
			var c int
			rows.Scan(&s, &c)
			byStatus[s] = c
		}
	}
	type assigneeCount struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
		Open  int    `json:"open"`
	}
	var byAssignee []assigneeCount
	rows2, _ := db.Query(`SELECT COALESCE(NULLIF(user_name,''),'(на розгляді)'), COUNT(*),
		SUM(CASE WHEN COALESCE(status,'Нова') NOT IN ('Виконана','Архів') THEN 1 ELSE 0 END)
		FROM daily_tasks GROUP BY COALESCE(NULLIF(user_name,''),'(на розгляді)') ORDER BY 3 DESC, 2 DESC`)
	if rows2 != nil {
		defer rows2.Close()
		for rows2.Next() {
			var a assigneeCount
			rows2.Scan(&a.Name, &a.Count, &a.Open)
			byAssignee = append(byAssignee, a)
		}
	}
	overdue := 0
	db.QueryRow(`SELECT COUNT(*) FROM daily_tasks
		WHERE due_date != '' AND due_date < ? AND COALESCE(status,'Нова') NOT IN ('Виконана','Архів')`, today).Scan(&overdue)
	dueToday := 0
	db.QueryRow(`SELECT COUNT(*) FROM daily_tasks
		WHERE due_date = ? AND COALESCE(status,'Нова') NOT IN ('Виконана','Архів')`, today).Scan(&dueToday)
	incToday := 0
	db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE date=?`, today).Scan(&incToday)
	incByStatus := map[string]int{"усі": incToday}
	incRows, _ := db.Query(`SELECT COALESCE(status,'Нове'), COUNT(*) FROM incidents WHERE date=? GROUP BY COALESCE(status,'Нове')`, today)
	if incRows != nil {
		defer incRows.Close()
		for incRows.Next() {
			var s string
			var c int
			incRows.Scan(&s, &c)
			incByStatus[s] = c
		}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"today":                     today,
		"tasks_by_status":           byStatus,
		"tasks_by_assignee":         byAssignee,
		"tasks_overdue":             overdue,
		"tasks_due_today":           dueToday,
		"incidents_today_by_status": incByStatus,
	})
}
