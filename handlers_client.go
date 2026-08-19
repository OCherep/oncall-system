package main

import (
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
	type TeamMember struct {
		Name     string `json:"name"`
		IsOncall bool   `json:"is_oncall"`
		TeamRole string `json:"team_role"`
	}
	var teamMembers []TeamMember
	var oncallUsers []string
	tmRows, err := db.Query(`SELECT u.name, COALESCE(u.is_oncall, 1), COALESCE(tr.name, '') FROM users u LEFT JOIN team_roles tr ON u.team_role_id = tr.id WHERE u.role != 'admin' ORDER BY u.name`)
	if err == nil {
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
	dbRows, err := db.Query("SELECT date, primary_user, backup_user FROM shifts WHERE date LIKE ?", prefix+"%")
	shifts := make(map[string]Shift)
	dbHasShifts := false
	if err == nil {
		defer dbRows.Close()
		for dbRows.Next() {
			var s Shift
			dbRows.Scan(&s.Date, &s.PrimaryUser, &s.BackupUser)
			shifts[s.Date] = s
			dbHasShifts = true
		}
	}
	if !dbHasShifts {
		shifts = generateShifts(year, month, oncallUsers, absences)
	} else {
		daysInMonth := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
		for d := 1; d <= daysInMonth; d++ {
			dateStr := fmt.Sprintf("%04d-%02d-%02d", year, month, d)
			s, ok := shifts[dateStr]
			needFix := !ok || isAbsentOnDate(s.PrimaryUser, dateStr, absences) || isAbsentOnDate(s.BackupUser, dateStr, absences)
			if !needFix {
				continue
			}
			var available []string
			for _, name := range oncallUsers {
				if !isAbsentOnDate(name, dateStr, absences) {
					available = append(available, name)
				}
			}
			if len(available) == 0 {
				delete(shifts, dateStr)
				continue
			}
			primary := available[0]
			if ok && !isAbsentOnDate(s.PrimaryUser, dateStr, absences) {
				primary = s.PrimaryUser
			}
			backup := primary
			for _, name := range available {
				if name != primary {
					backup = name
					break
				}
			}
			shifts[dateStr] = Shift{Date: dateStr, PrimaryUser: primary, BackupUser: backup}
		}
	}
	monthPattern := fmt.Sprintf("%04d-%02d-%%", year, month)
	incRows, err := db.Query("SELECT id, user_name, date, type, duration_minutes, description FROM incidents WHERE date LIKE ?", monthPattern)
	incidents := make(map[string][]IncidentReport)
	statsMap := make(map[string]*UserStat)
	for _, name := range oncallUsers {
		statsMap[name] = &UserStat{Name: name}
	}
	if err == nil {
		defer incRows.Close()
		for incRows.Next() {
			var inc IncidentReport
			incRows.Scan(&inc.ID, &inc.UserName, &inc.Date, &inc.Type, &inc.DurationMinutes, &inc.Description)
			incidents[inc.Date] = append(incidents[inc.Date], inc)
			if _, exists := statsMap[inc.UserName]; !exists {
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
	taskRows, err := db.Query("SELECT id, user_name, date, task_description FROM daily_tasks WHERE date LIKE ?", monthPattern)
	dailyTasks := make(map[string][]DailyTask)
	if err == nil {
		defer taskRows.Close()
		for taskRows.Next() {
			var t DailyTask
			taskRows.Scan(&t.ID, &t.UserName, &t.Date, &t.TaskDescription)
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
	json.NewEncoder(w).Encode(map[string]interface{}{
		"shifts": shifts, "absences": absences, "incidents": incidents, "stats": stats,
		"absence_types": absenceTypes, "team_members": teamMembers, "year": year, "month": month,
		"daily_tasks": dailyTasks,
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
	_, err := db.Exec("INSERT INTO absences (user_name, type, start_date, end_date, status) VALUES (?, ?, ?, ?, 'Pending')", req.UserName, req.Type, req.StartDate, req.EndDate)
	if err != nil {
		http.Error(w, "Помилка створення заявки", http.StatusInternalServerError)
		return
	}
	logAudit(req.UserName, "CREATE_ABSENCE_REQUEST", r.RemoteAddr, fmt.Sprintf("Тип: %s, Дати: %s - %s", req.Type, req.StartDate, req.EndDate))
	logAppEvent("OnCall Core", "INFO", fmt.Sprintf("Користувач %s створив заявку на %s", req.UserName, req.Type))
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
		http.Error(w, "Необхідні поля: user_name, date, duration_minutes, description", http.StatusBadRequest)
		return
	}
	if inc.Type == "" {
		inc.Type = "Звернення"
	}
	_, err := db.Exec("INSERT INTO incidents (user_name, date, type, duration_minutes, description) VALUES (?, ?, ?, ?, ?)", inc.UserName, inc.Date, inc.Type, inc.DurationMinutes, inc.Description)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logAudit(inc.UserName, "CREATE_INCIDENT", r.RemoteAddr, fmt.Sprintf("Дата: %s, Тип: %s, Тривалість: %d хв", inc.Date, inc.Type, inc.DurationMinutes))
	logAppEvent("OnCall Core", "INFO", fmt.Sprintf("Користувач %s створив звіт про звернення (%d хв)", inc.UserName, inc.DurationMinutes))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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
		if t.UserName == "" || t.Date == "" || t.TaskDescription == "" {
			http.Error(w, "Потрібні user_name, date, task_description", http.StatusBadRequest)
			return
		}
		res, err := db.Exec("INSERT INTO daily_tasks (user_name, date, task_description) VALUES (?, ?, ?)", t.UserName, t.Date, t.TaskDescription)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		t.ID = int(id)
		logAudit(t.UserName, "CREATE_DAILY_TASK", r.RemoteAddr, fmt.Sprintf("Дата: %s, Задача: %s", t.Date, t.TaskDescription))
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(t)
	case http.MethodDelete:
		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		db.Exec("DELETE FROM daily_tasks WHERE id = ?", idStr)
		logAudit("user", "DELETE_DAILY_TASK", r.RemoteAddr, "ID: "+idStr)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
