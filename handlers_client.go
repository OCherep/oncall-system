package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
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
	err := db.QueryRow(`SELECT u.id, u.username, u.name, u.role, u.team_role_id, COALESCE(tr.name, ''), COALESCE(u.is_oncall, 1) FROM users u LEFT JOIN team_roles tr ON u.team_role_id = tr.id WHERE u.username = ? AND u.password = ?`, req.Username, req.Password).Scan(&u.ID, &u.Username, &u.Name, &u.Role, &u.TeamRoleID, &u.TeamRole, &isOncallInt)
	if err != nil {
		logAudit(req.Username, "LOGIN_FAILED", r.RemoteAddr, "fail")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Невірне ім'я користувача або пароль"})
		return
	}
	u.IsOncall = isOncallInt == 1
	logAudit(u.Username, "LOGIN_SUCCESS", r.RemoteAddr, "ok")
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

func handleGetData(w http.ResponseWriter, r *http.Request) {
	yearStr := r.URL.Query().Get("year")
	monthStr := r.URL.Query().Get("month")
	now := time.Now()
	year, month := now.Year(), int(now.Month())
	if yearStr != "" && monthStr != "" {
		fmt.Sscanf(yearStr, "%d", &year)
		fmt.Sscanf(monthStr, "%d", &month)
	}
	absRows, _ := db.Query("SELECT id, user_name, type, start_date, end_date, status FROM absences WHERE status = 'Approved'")
	var absences []AbsenceRequest
	if absRows != nil {
		defer absRows.Close()
		for absRows.Next() {
			var a AbsenceRequest
			absRows.Scan(&a.ID, &a.UserName, &a.Type, &a.StartDate, &a.EndDate, &a.Status)
			absences = append(absences, a)
		}
	}
	type TeamMember struct {
		Name string `json:"name"`
		IsOncall bool `json:"is_oncall"`
		TeamRole string `json:"team_role"`
	}
	var teamMembers []TeamMember
	var oncallUsers []string
	tmRows, _ := db.Query(`SELECT u.name, COALESCE(u.is_oncall, 1), COALESCE(tr.name, '') FROM users u LEFT JOIN team_roles tr ON u.team_role_id = tr.id WHERE u.role != 'admin' ORDER BY u.name`)
	if tmRows != nil {
		defer tmRows.Close()
		for tmRows.Next() {
			var m TeamMember
			var isOn int
			tmRows.Scan(&m.Name, &isOn, &m.TeamRole)
			m.IsOncall = isOn == 1
			teamMembers = append(teamMembers, m)
			if m.IsOncall {
				oncallUsers = append(oncallUsers, m.Name)
			}
		}
	}
	prefix := fmt.Sprintf("%04d-%02d", year, month)
	dbRows, _ := db.Query("SELECT date, primary_user, backup_user FROM shifts WHERE date LIKE ?", prefix+"%")
	shifts := make(map[string]Shift)
	dbHas := false
	if dbRows != nil {
		defer dbRows.Close()
		for dbRows.Next() {
			var s Shift
			dbRows.Scan(&s.Date, &s.PrimaryUser, &s.BackupUser)
			shifts[s.Date] = s
			dbHas = true
		}
	}
	if !dbHas {
		shifts = generateShifts(year, month, oncallUsers, absences)
	}
	monthPattern := fmt.Sprintf("%04d-%02d-%%", year, month)
	incRows, _ := db.Query(`SELECT id, user_name, date, type, duration_minutes, description, COALESCE(datetime(created_at,'localtime'),'') FROM incidents WHERE date LIKE ? ORDER BY created_at`, monthPattern)
	incidents := make(map[string][]IncidentReport)
	statsMap := make(map[string]*UserStat)
	for _, n := range oncallUsers {
		statsMap[n] = &UserStat{Name: n}
	}
	if incRows != nil {
		defer incRows.Close()
		for incRows.Next() {
			var inc IncidentReport
			incRows.Scan(&inc.ID, &inc.UserName, &inc.Date, &inc.Type, &inc.DurationMinutes, &inc.Description, &inc.CreatedAt)
			incidents[inc.Date] = append(incidents[inc.Date], inc)
			if _, ok := statsMap[inc.UserName]; !ok {
				statsMap[inc.UserName] = &UserStat{Name: inc.UserName}
			}
			statsMap[inc.UserName].IncidentMinutes += inc.DurationMinutes
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
	for _, v := range statsMap {
		stats = append(stats, *v)
	}
	taskRows, _ := db.Query(`SELECT id, user_name, date, task_description, COALESCE(status,'Нова'), COALESCE(priority,'Базова'), work_started_at, COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),'') FROM daily_tasks WHERE date LIKE ? AND COALESCE(status,'') != 'Архів'`, monthPattern)
	dailyTasks := make(map[string][]DailyTask)
	if taskRows != nil {
		defer taskRows.Close()
		for taskRows.Next() {
			var t DailyTask
			var ws, ca sql.NullString
			taskRows.Scan(&t.ID, &t.UserName, &t.Date, &t.TaskDescription, &t.Status, &t.Priority, &ws, &t.TotalMinutes, &ca)
			if ws.Valid {
				t.WorkStartedAt = ws.String
			}
			if ca.Valid {
				t.CreatedAt = ca.String
			}
			dailyTasks[t.Date] = append(dailyTasks[t.Date], t)
		}
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
	json.NewEncoder(w).Encode(map[string]interface{}{"shifts": shifts, "absences": absences, "incidents": incidents, "stats": stats, "absence_types": absenceTypes, "team_members": teamMembers, "year": year, "month": month, "daily_tasks": dailyTasks})
}

func handleRequestAbsence(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		UserName, Type, StartDate, EndDate string
	}
	json.NewDecoder(r.Body).Decode(&req)
	db.Exec("INSERT INTO absences (user_name, type, start_date, end_date, status) VALUES (?, ?, ?, ?, 'Pending')", req.UserName, req.Type, req.StartDate, req.EndDate)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var inc IncidentReport
	if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if inc.UserName == "" || inc.Date == "" || inc.DurationMinutes <= 0 || inc.Description == "" {
		http.Error(w, "required fields missing", http.StatusBadRequest)
		return
	}
	if inc.Type == "" {
		inc.Type = "Звернення"
	}
	role := inc.Role
	if role == "" {
		db.QueryRow("SELECT role FROM users WHERE name = ? LIMIT 1", inc.UserName).Scan(&role)
	}
	today := time.Now().Format("2006-01-02")
	isAdmin := role == "admin"
	if !isAdmin && inc.Date < today {
		http.Error(w, "Без ролі admin можна фіксувати звернення лише на поточну або майбутню дату", http.StatusForbidden)
		return
	}
	_, err := db.Exec(`INSERT INTO incidents (user_name, date, type, duration_minutes, description, created_at) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`, inc.UserName, inc.Date, inc.Type, inc.DurationMinutes, inc.Description)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	res := map[string]interface{}{"status": "ok", "as_task": false}
	if inc.Date > today {
		desc := "[для розгляду на дейлі] [" + inc.Type + "] " + inc.Description
		db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at) VALUES (?, ?, ?, 'Нова', 'У шухляду', 0, CURRENT_TIMESTAMP)`, inc.UserName, inc.Date, desc)
		res["as_task"] = true
		res["message"] = "Звернення зафіксовано і додано як задачу «для розгляду на дейлі»"
	}
	logAudit(inc.UserName, "CREATE_INCIDENT", r.RemoteAddr, inc.Date)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(res)
}

func accumulateWorkTime(t *DailyTask) {
	if t.Status == "У роботі" && t.WorkStartedAt != "" {
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
		json.NewDecoder(r.Body).Decode(&t)
		if t.UserName == "" || t.Date == "" || t.TaskDescription == "" {
			http.Error(w, "required", http.StatusBadRequest)
			return
		}
		if t.Status == "" {
			t.Status = "Нова"
		}
		if t.Priority == "" {
			t.Priority = "Базова"
		}
		res, err := db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at) VALUES (?, ?, ?, ?, ?, 0, CURRENT_TIMESTAMP)`, t.UserName, t.Date, t.TaskDescription, t.Status, t.Priority)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		t.ID = int(id)
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(t)
	case http.MethodPut:
		var t DailyTask
		json.NewDecoder(r.Body).Decode(&t)
		if t.ID == 0 {
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
			if cur.Status != "Виконана" && cur.Status != "Архів" {
				http.Error(w, "can only reopen done/archive", http.StatusBadRequest)
				return
			}
			t.Status = "Нова"
		}
		if cur.Status == "Виконана" && t.Status != "Нова" && t.Status != "Архів" && t.Status != cur.Status {
			http.Error(w, "done task: only reopen or archive", http.StatusBadRequest)
			return
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
		db.Exec("DELETE FROM daily_tasks WHERE id = ?", r.URL.Query().Get("id"))
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
