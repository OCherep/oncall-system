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
	var workStarted, created sql.NullString
	err := scanner.Scan(&t.ID, &t.UserName, &t.Date, &t.TaskDescription, &t.Status, &t.Priority, &workStarted, &t.TotalMinutes, &created)
	if workStarted.Valid {
		t.WorkStartedAt = workStarted.String
	}
	if created.Valid {
		t.CreatedAt = created.String
	}
	if t.Status == "" {
		t.Status = "Нова"
	}
	if t.Priority == "" {
		t.Priority = "Базова"
	}
	return t, err
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

	// all team members for roster; oncall subset for shifts
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

	// absences
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

	// shifts from DB or generate; regenerate if oncall roster changed
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
		COALESCE(datetime(created_at,'localtime'), datetime('now','localtime'))
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
			incRows.Scan(&inc.ID, &inc.UserName, &inc.Date, &inc.Type, &inc.DurationMinutes, &inc.Description, &inc.CreatedAt)
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
	for _, st := range statsMap {
		stats = append(stats, *st)
	}

	// daily tasks
	taskRows, _ := db.Query(`SELECT id, user_name, date, task_description,
		COALESCE(status,'Нова'), COALESCE(priority,'Базова'), work_started_at,
		COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),'')
		FROM daily_tasks WHERE date LIKE ? ORDER BY id`, monthPattern)
	dailyTasks := make(map[string][]DailyTask)
	if taskRows != nil {
		defer taskRows.Close()
		for taskRows.Next() {
			t, _ := scanDailyTask(taskRows)
			dailyTasks[t.Date] = append(dailyTasks[t.Date], t)
		}
	}

	// absence types
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

	role := inc.Role
	if role == "" {
		db.QueryRow("SELECT role FROM users WHERE name = ? LIMIT 1", inc.UserName).Scan(&role)
	}
	today := time.Now().Format("2006-01-02")
	isAdmin := role == "admin"
	isFuture := inc.Date > today
	isPast := inc.Date < today

	if !isAdmin {
		if isPast {
			http.Error(w, "Без ролі admin можна фіксувати звернення лише на поточну або майбутню дату", http.StatusForbidden)
			return
		}
	}

	_, err := db.Exec(`INSERT INTO incidents (user_name, date, type, duration_minutes, description, created_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		inc.UserName, inc.Date, inc.Type, inc.DurationMinutes, inc.Description)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := map[string]interface{}{"status": "ok", "as_task": false}

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

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(result)
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
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if t.UserName == "" || t.Date == "" || t.TaskDescription == "" {
			http.Error(w, "Потрібні user_name, date, task_description", http.StatusBadRequest)
			return
		}
		if t.Status == "" {
			t.Status = "Нова"
		}
		if t.Priority == "" {
			t.Priority = "Базова"
		}
		res, err := db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at)
			VALUES (?, ?, ?, ?, ?, 0, CURRENT_TIMESTAMP)`,
			t.UserName, t.Date, t.TaskDescription, t.Status, t.Priority)
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
		var t DailyTask
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.ID == 0 {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		var cur DailyTask
		var ws, ca sql.NullString
		err := db.QueryRow(`SELECT id, user_name, date, task_description, COALESCE(status,'Нова'), COALESCE(priority,'Базова'),
			work_started_at, COALESCE(total_minutes,0), COALESCE(datetime(created_at,'localtime'),'')
			FROM daily_tasks WHERE id=?`, t.ID).
			Scan(&cur.ID, &cur.UserName, &cur.Date, &cur.TaskDescription, &cur.Status, &cur.Priority, &ws, &cur.TotalMinutes, &ca)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
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
			if cur.Status != "Виконана" && cur.Status != "Архів" {
				http.Error(w, "Перевідкрити можна лише виконану/архівну задачу", http.StatusBadRequest)
				return
			}
			newStatus = "Нова"
		}
		if cur.Status == "Виконана" && newStatus != "Нова" && newStatus != "Перевідкрита" && newStatus != "Архів" {
			if newStatus != cur.Status {
				http.Error(w, "Виконану задачу можна лише перевідкрити або архівувати", http.StatusBadRequest)
				return
			}
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
			workStarted = time.Now().Format(time.RFC3339)
		}
		if newStatus == "Виконана" && cur.Status == "У роботі" {
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
		db.Exec(`UPDATE daily_tasks SET user_name=?, date=?, task_description=?, status=?, priority=?,
			work_started_at=?, total_minutes=? WHERE id=?`,
			userName, date, desc, newStatus, newPriority, workArg, total, t.ID)

		t.UserName, t.Date, t.TaskDescription = userName, date, desc
		t.Status, t.Priority, t.TotalMinutes, t.WorkStartedAt = newStatus, newPriority, total, workStarted
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
