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


func parseTimeFlexible(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.000",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.ParseInLocation(l, s, time.Local); err == nil {
			return t
		}
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func minutesBetween(a, b time.Time) int {
	if a.IsZero() || b.IsZero() || !b.After(a) {
		return 0
	}
	m := int(b.Sub(a).Minutes())
	if m < 0 {
		return 0
	}
	return m
}

func openTaskStatusLog(taskID int, status, by string) {
	now := time.Now()
	var openID int
	var started string
	err := db.QueryRow(`SELECT id, started_at FROM task_status_log WHERE task_id=? AND ended_at IS NULL ORDER BY id DESC LIMIT 1`, taskID).
		Scan(&openID, &started)
	if err == nil && openID > 0 {
		st := parseTimeFlexible(started)
		if st.IsZero() {
			st = now
		}
		mins := minutesBetween(st, now)
		db.Exec(`UPDATE task_status_log SET ended_at=?, minutes=? WHERE id=?`, now.Format("2006-01-02 15:04:05"), mins, openID)
	}
	db.Exec(`INSERT INTO task_status_log (task_id, status, started_at, changed_by) VALUES (?,?,?,?)`,
		taskID, status, now.Format("2006-01-02 15:04:05"), by)
}

// rebuildTaskStatusFromHistory — відновлює сегменти з коментарів «Статус: X → Y» + created_at задачі.
// Перезаписує task_status_log для узгодженого відображення в усіх UI.
func rebuildTaskStatusFromHistory(taskID int) {
	var created, curStatus string
	err := db.QueryRow(`SELECT COALESCE(created_at,''), COALESCE(status,'Нова') FROM daily_tasks WHERE id=?`, taskID).Scan(&created, &curStatus)
	if err != nil {
		return
	}
	type ev struct {
		at     time.Time
		from   string
		to     string
		by     string
		raw    string
	}
	var events []ev
	rows, err := db.Query(`SELECT COALESCE(body,''), COALESCE(created_at,''), COALESCE(author_name,'') FROM comments
		WHERE entity_type='task' AND entity_id=? ORDER BY id ASC`, taskID)
	if err == nil && rows != nil {
		defer rows.Close()
		for rows.Next() {
			var body, ca, author string
			rows.Scan(&body, &ca, &author)
			// Статус: X → Y  (various arrows)
			body = strings.TrimSpace(body)
			if !strings.Contains(body, "Статус:") && !strings.Contains(strings.ToLower(body), "status:") {
				continue
			}
			// normalize arrows
			repl := body
			for _, a := range []string{"→", "->", "⇒", "⟶"} {
				repl = strings.ReplaceAll(repl, a, "→")
			}
			idx := strings.Index(repl, "Статус:")
			if idx < 0 {
				idx = strings.Index(strings.ToLower(repl), "status:")
			}
			if idx < 0 {
				continue
			}
			part := strings.TrimSpace(repl[idx:])
			// after "Статус:"
			colon := strings.Index(part, ":")
			if colon < 0 {
				continue
			}
			rest := strings.TrimSpace(part[colon+1:])
			parts := strings.SplitN(rest, "→", 2)
			if len(parts) != 2 {
				continue
			}
			from := strings.TrimSpace(parts[0])
			to := strings.TrimSpace(parts[1])
			// strip trailing noise
			if i := strings.IndexAny(to, "\n("); i >= 0 {
				to = strings.TrimSpace(to[:i])
			}
			at := parseTimeFlexible(ca)
			if at.IsZero() {
				continue
			}
			events = append(events, ev{at: at, from: from, to: to, by: author, raw: body})
		}
	}

	startT := parseTimeFlexible(created)
	if startT.IsZero() && len(events) > 0 {
		startT = events[0].at
	}
	if startT.IsZero() {
		startT = time.Now().Add(-time.Hour)
	}

	// initial status = first transition's "from", else current, else Нова
	initStatus := "Нова"
	if len(events) > 0 && events[0].from != "" {
		initStatus = events[0].from
	} else if curStatus != "" {
		initStatus = curStatus
	}

	type seg struct {
		status string
		start  time.Time
		end    time.Time
		by     string
	}
	var segs []seg
	curSt := initStatus
	curStart := startT
	curBy := ""
	for _, e := range events {
		if e.at.Before(curStart) {
			continue
		}
		segs = append(segs, seg{status: curSt, start: curStart, end: e.at, by: curBy})
		curSt = e.to
		if curSt == "" {
			curSt = e.from
		}
		curStart = e.at
		curBy = e.by
	}
	// open segment until now
	segs = append(segs, seg{status: curSt, start: curStart, end: time.Now(), by: curBy})

	db.Exec(`DELETE FROM task_status_log WHERE task_id=?`, taskID)
	for i, s := range segs {
		mins := minutesBetween(s.start, s.end)
		if i == len(segs)-1 {
			// open
			db.Exec(`INSERT INTO task_status_log (task_id, status, started_at, ended_at, minutes, changed_by) VALUES (?,?,?,NULL,?,?)`,
				taskID, s.status, s.start.Format("2006-01-02 15:04:05"), mins, s.by)
		} else {
			db.Exec(`INSERT INTO task_status_log (task_id, status, started_at, ended_at, minutes, changed_by) VALUES (?,?,?,?,?,?)`,
				taskID, s.status, s.start.Format("2006-01-02 15:04:05"), s.end.Format("2006-01-02 15:04:05"), mins, s.by)
		}
	}
}

func taskStatusBreakdown(taskID int) []map[string]interface{} {
	rebuildTaskStatusFromHistory(taskID)
	rows, err := db.Query(`SELECT status, started_at, ended_at, COALESCE(minutes,0) FROM task_status_log WHERE task_id=?`, taskID)
	if err != nil || rows == nil {
		return nil
	}
	defer rows.Close()
	sums := map[string]int{}
	now := time.Now()
	for rows.Next() {
		var st, started string
		var ended sql.NullString
		var mins int
		if err := rows.Scan(&st, &started, &ended, &mins); err != nil {
			continue
		}
		if ended.Valid && strings.TrimSpace(ended.String) != "" {
			sums[st] += mins
		} else {
			stt := parseTimeFlexible(started)
			sums[st] += minutesBetween(stt, now)
		}
	}
	var out []map[string]interface{}
	for st, m := range sums {
		if m < 0 {
			m = 0
		}
		out = append(out, map[string]interface{}{"status": st, "minutes": m})
	}
	return out
}

func taskLastActivity(taskID int) string {
	var ts string
	err := db.QueryRow(`SELECT COALESCE(datetime(MAX(started_at),'localtime'),'') FROM task_status_log WHERE task_id=?`, taskID).Scan(&ts)
	if err != nil || ts == "" {
		db.QueryRow(`SELECT COALESCE(datetime(MAX(created_at),'localtime'),'') FROM comments WHERE entity_type='task' AND entity_id=?`, taskID).Scan(&ts)
	}
	return ts
}



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
		ip := clientIP(r)
		recordLoginAttempt(ip)
		logAudit(req.Username, "LOGIN_FAILED", ip, "Невдала спроба входу")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "Невірне ім'я користувача або пароль"})
		return
	}
	u.IsOncall = isOncallInt == 1
	ip := clientIP(r)
	if loginRateLimited(ip) {
		logAudit(req.Username, "LOGIN_RATE_LIMIT", ip, "too many attempts")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "Забагато спроб входу. Спробуйте пізніше."})
		return
	}
	logAudit(u.Username, "LOGIN_SUCCESS", ip, "Успішна авторизація")
	tok, exp, err := createSession(u, ip, r.UserAgent())
	if err != nil {
		log.Printf("createSession: %v", err)
	} else {
		setSessionCookie(w, tok, exp)
	}
	// Legacy cookies для UI (не HttpOnly — читає JS); ім'я/роль для відображення
	maxAge := int(sessionTTL().Seconds())
	http.SetCookie(w, &http.Cookie{Name: "oncall_user", Value: u.Username, Path: "/", MaxAge: maxAge, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "oncall_name", Value: u.Name, Path: "/", MaxAge: maxAge, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "oncall_role", Value: u.Role, Path: "/", MaxAge: maxAge, SameSite: http.SameSiteLaxMode})
	w.Header().Set("Content-Type", "application/json")
	var needsResume int
	_ = db.QueryRow(`SELECT COALESCE(needs_resume,0) FROM users WHERE id=?`, u.ID).Scan(&needsResume)
	out := map[string]interface{}{
		"id": u.ID, "username": u.Username, "name": u.Name, "role": u.Role,
		"team_role_id": u.TeamRoleID, "team_role": u.TeamRole, "is_oncall": u.IsOncall,
		"session_expires": exp.Format(time.RFC3339),
		"needs_resume": needsResume == 1,
	}
	if tok != "" {
		out["session_token"] = tok // для Authorization header якщо cookie блокується
	}
	json.NewEncoder(w).Encode(out)
	_ = err
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


// parseIncidentIDFromTaskDesc extracts N from "[зі звернення #N] ..."
func parseIncidentIDFromTaskDesc(desc string) int {
	const p = "[зі звернення #"
	i := strings.Index(desc, p)
	if i < 0 {
		return 0
	}
	rest := desc[i+len(p):]
	n := 0
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// dedupeTasksByIncident keeps lowest task id per source incident id
func dedupeTasksByIncident(list []DailyTask) []DailyTask {
	seen := map[int]bool{}
	var out []DailyTask
	// assume list ordered by id ASC
	for _, t := range list {
		iid := parseIncidentIDFromTaskDesc(t.TaskDescription)
		if iid > 0 {
			if seen[iid] {
				continue
			}
			seen[iid] = true
		}
		out = append(out, t)
	}
	return out
}

func allowedNextStatuses(cur, role string) []string {
	isAdmin := role == "admin"
	var next []string
	switch cur {
	case "Нерозподілена":
		// лише admin/task-master: виведення на денну дошку як «Нова»
		if isAdmin {
			next = []string{"Нерозподілена", "Нова", "Архів"}
		} else {
			next = []string{"Нерозподілена"}
		}
	case "Нова":
		if isAdmin {
			next = []string{"Нова", "У роботі", "Архів", "Нерозподілена"}
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
			next = []string{cur, "Нерозподілена", "Нова", "У роботі", "На паузі", "До перевірки", "Виконана", "Перевідкрита", "Архів"}
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
		COALESCE(total_minutes,0), COALESCE(created_by,''), COALESCE(reported_for,''),
		COALESCE(converted_to_task_id,0), COALESCE(reporter_name,''), COALESCE(reporter_email,''), COALESCE(reporter_slack,'')
		FROM incidents WHERE date LIKE ? AND COALESCE(converted_to_task_id,0)=0
		ORDER BY created_at ASC`, monthPattern)
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
				&inc.Status, &inc.Priority, &inc.Source, &inc.TotalMinutes, &inc.CreatedBy, &inc.ReportedFor, &inc.ConvertedToTaskID,
				&inc.ReporterName, &inc.ReporterEmail, &inc.ReporterSlack)
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
		for taskRows.Next() {
			t, _ := scanDailyTask(taskRows)
			dailyTasks[t.Date] = append(dailyTasks[t.Date], t)
		}
		taskRows.Close()
	}
	// no duplicate tasks from the same incident in UI payloads
	// «Нерозподілена» — лише адмін-інтерфейс, не публічний календар
	for day, list := range dailyTasks {
		list = dedupeTasksByIncident(list)
		filtered := list[:0]
		for _, t := range list {
			if t.Status == "Нерозподілена" {
				continue
			}
			filtered = append(filtered, t)
		}
		dailyTasks[day] = filtered
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
	snap := onGridSnapshot()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"year": year, "month": month,
		"team_members": team, "absence_types": absenceTypes,
		"shifts": shifts, "absences": absences, "incidents": incidents, "stats": stats,
		"daily_tasks": dailyTasks,
		"on_grid": snap,
		"brb": activeBRBMap(),
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
	logAudit(req.UserName, "REQUEST_ABSENCE", clientIP(r), req.Type+" "+req.StartDate+"-"+req.EndDate)
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

func copyComments(fromType string, fromID int, toType string, toID int) int {
	// Important: with SetMaxOpenConns(1) we must fully close the SELECT before any Exec,
	// otherwise SQLite deadlocks the whole process.
	type row struct {
		author string
		body   string
		isSys  int
	}
	var items []row
	rows, err := db.Query(`SELECT COALESCE(author_name,''), body, COALESCE(is_system,0) FROM comments
		WHERE entity_type=? AND entity_id=? ORDER BY id ASC`, fromType, fromID)
	if err != nil {
		return 0
	}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.author, &r.body, &r.isSys); err != nil {
			continue
		}
		if strings.TrimSpace(r.body) == "" {
			continue
		}
		items = append(items, r)
	}
	rows.Close()
	n := 0
	for _, r := range items {
		if _, err := db.Exec(`INSERT INTO comments (entity_type, entity_id, author_name, body, is_system) VALUES (?,?,?,?,?)`,
			toType, toID, r.author, r.body, r.isSys); err == nil {
			n++
		}
	}
	return n
}

// convertIncidentToTask створює задачу з звернення, копіює коментарі (залишаючи їх і на зверненні).
func convertIncidentToTask(inc IncidentReport, actor string) (int64, int, error) {
	// Запобіжник від дублювання
	var existing int
	_ = db.QueryRow(`SELECT COALESCE(converted_to_task_id,0) FROM incidents WHERE id=?`, inc.ID).Scan(&existing)
	if existing > 0 {
		return int64(existing), 0, fmt.Errorf("звернення #%d вже переведено в задачу #%d", inc.ID, existing)
	}
	// також шукаємо задачу з тим самим префіксом (legacy)
	var legacyID int
	_ = db.QueryRow(`SELECT id FROM daily_tasks WHERE task_description LIKE ? ORDER BY id ASC LIMIT 1`,
		fmt.Sprintf("[зі звернення #%d]%%", inc.ID)).Scan(&legacyID)
	if legacyID > 0 {
		db.Exec(`UPDATE incidents SET converted_to_task_id=?, status=CASE WHEN status IN ('Вирішено','Архів') THEN status ELSE 'Вирішено' END WHERE id=?`, legacyID, inc.ID)
		return int64(legacyID), 0, fmt.Errorf("звернення #%d вже має задачу #%d", inc.ID, legacyID)
	}

	desc := fmt.Sprintf("[зі звернення #%d] %s", inc.ID, inc.Description)
	prio := mapIncidentPrioToTask(inc.Priority)
	res, err := db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at, created_by, responsible)
		VALUES (?, ?, ?, 'Нерозподілена', ?, 0, CURRENT_TIMESTAMP, ?, ?)`,
		inc.UserName, inc.Date, desc, prio, actor, actor)
	if err != nil {
		return 0, 0, err
	}
	tid, _ := res.LastInsertId()
	// primary assignee row
	if strings.TrimSpace(inc.UserName) != "" {
		db.Exec(`INSERT OR IGNORE INTO task_assignees (task_id, user_name, total_minutes) VALUES (?,?,0)`, tid, inc.UserName)
	}
	copied := copyComments("incident", inc.ID, "task", int(tid))
	db.Exec(`UPDATE incidents SET converted_to_task_id=?, status='Вирішено' WHERE id=?`, tid, inc.ID)
	addSystemComment("incident", inc.ID, fmt.Sprintf("Переведено в задачу #%d", tid))
	openTaskStatusLog(int(tid), "Нова", actor)
	addSystemComment("task", int(tid), fmt.Sprintf("Створено зі звернення #%d (скопійовано коментарів: %d)", inc.ID, copied))
	return tid, copied, nil
}

func loadTaskAssignees(taskID int) []TaskAssignee {
	rows, err := db.Query(`SELECT user_name, COALESCE(total_minutes,0), COALESCE(work_started_at,'') FROM task_assignees WHERE task_id=? ORDER BY id`, taskID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var list []TaskAssignee
	for rows.Next() {
		var a TaskAssignee
		rows.Scan(&a.UserName, &a.TotalMinutes, &a.WorkStartedAt)
		list = append(list, a)
	}
	return list
}

func setTaskAssignees(taskID int, names []string) {
	db.Exec(`DELETE FROM task_assignees WHERE task_id=?`, taskID)
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		db.Exec(`INSERT INTO task_assignees (task_id, user_name, total_minutes) VALUES (?,?,0)`, taskID, n)
	}
}


// incidentFactMinutes — факт від призначення (або створення) до «зараз».
func incidentFactMinutes(assignedAt, createdAt, _resolvedHint string) int {
	start := strings.TrimSpace(assignedAt)
	if start == "" {
		start = strings.TrimSpace(createdAt)
	}
	if start == "" {
		return 0
	}
	st := parseTimeFlexible(start)
	if st.IsZero() {
		return 0
	}
	m := int(time.Since(st).Minutes())
	if m < 0 {
		return 0
	}
	return m
}

func handleIncidents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		// Список звернень (для admin-архіву): ?status=Архів|Вирішено|all &limit=
		status := r.URL.Query().Get("status")
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			fmt.Sscanf(v, "%d", &limit)
			if limit <= 0 || limit > 500 {
				limit = 100
			}
		}
		q := `SELECT id, user_name, date, type, duration_minutes, description,
			COALESCE(datetime(created_at,'localtime'),''),
			COALESCE(status,'Нове'), COALESCE(priority,'Звичайний'), COALESCE(source,'self'),
			COALESCE(total_minutes,0), COALESCE(created_by,''), COALESCE(reported_for,''), COALESCE(external_id,''),
			COALESCE(converted_to_task_id,0), COALESCE(reporter_name,''), COALESCE(reporter_email,''), COALESCE(reporter_slack,'')
			FROM incidents WHERE 1=1`
		args := []interface{}{}
		if status != "" && status != "all" {
			q += " AND COALESCE(status,'Нове') = ?"
			args = append(args, status)
		}
		if user := r.URL.Query().Get("user"); user != "" {
			q += " AND user_name = ?"
			args = append(args, user)
		}
		q += " ORDER BY id DESC LIMIT ?"
		args = append(args, limit)
		rows, err := db.Query(q, args...)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var list []IncidentReport
		for rows.Next() {
			var inc IncidentReport
			rows.Scan(&inc.ID, &inc.UserName, &inc.Date, &inc.Type, &inc.DurationMinutes, &inc.Description, &inc.CreatedAt,
				&inc.Status, &inc.Priority, &inc.Source, &inc.TotalMinutes, &inc.CreatedBy, &inc.ReportedFor, &inc.ExternalID, &inc.ConvertedToTaskID,
				&inc.ReporterName, &inc.ReporterEmail, &inc.ReporterSlack)
			if inc.TotalMinutes > 0 {
				inc.FactMinutes = inc.TotalMinutes
			} else if inc.Status == "Вирішено" || inc.Status == "Архів" {
				// fallback: від створення (точніше — після наступних резолвів з assigned_at)
				inc.FactMinutes = incidentFactMinutes("", inc.CreatedAt, "")
			}
			list = append(list, inc)
		}
		if list == nil {
			list = []IncidentReport{}
		}
		json.NewEncoder(w).Encode(list)

	case http.MethodPost:
		var inc IncidentReport
		if err := json.NewDecoder(r.Body).Decode(&inc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(inc.Description) == "" {
			http.Error(w, "Опис обов'язковий", http.StatusBadRequest)
			return
		}
		if inc.Date == "" {
			inc.Date = time.Now().Format("2006-01-02")
		}
		if inc.DurationMinutes <= 0 {
			inc.DurationMinutes = 15
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
		role := inc.Role
		guest := role == "" || role == "guest"
		if guest && inc.Source == "" {
			inc.Source = "guest"
		}
		if guest {
			if strings.TrimSpace(inc.ReporterEmail) == "" {
				http.Error(w, "Для гостьового звернення обов'язковий email", http.StatusBadRequest)
				return
			}
			if strings.TrimSpace(inc.ReporterName) == "" {
				http.Error(w, "Вкажіть ваше ім'я", http.StatusBadRequest)
				return
			}
		}
		if inc.Source == "" {
			inc.Source = "self"
		}
		if strings.TrimSpace(inc.CreatedBy) == "" {
			if guest {
				inc.CreatedBy = "гість"
			} else {
				inc.CreatedBy = inc.UserName
			}
		}
		if inc.ReportedFor == "" {
			inc.ReportedFor = inc.UserName
		}
		if inc.TotalMinutes == 0 {
			inc.TotalMinutes = inc.DurationMinutes
		}
		if role == "" && inc.UserName != "" {
			db.QueryRow("SELECT role FROM users WHERE name = ? LIMIT 1", inc.UserName).Scan(&role)
		}
		today := time.Now().Format("2006-01-02")
		isAdmin := role == "admin"
		isFuture := inc.Date > today
		isPast := inc.Date < today
		if !isAdmin && !guest && isPast {
			http.Error(w, "Без ролі admin можна фіксувати звернення лише на поточну або майбутню дату", http.StatusForbidden)
			return
		}
		// гість — лише сьогодні (або майбутнє, якщо явно передано)
		if guest && isPast {
			http.Error(w, "Гість може створювати звернення лише на сьогодні або майбутню дату", http.StatusForbidden)
			return
		}
		shouldNotify := applyIncidentRouting(&inc)
		res, err := db.Exec(`INSERT INTO incidents (user_name, date, type, duration_minutes, description, created_at,
			status, priority, source, total_minutes, created_by, reported_for, reporter_name, reporter_email, reporter_slack)
			VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			inc.UserName, inc.Date, inc.Type, inc.DurationMinutes, inc.Description,
			inc.Status, inc.Priority, inc.Source, inc.TotalMinutes, inc.CreatedBy, inc.ReportedFor,
			inc.ReporterName, inc.ReporterEmail, inc.ReporterSlack)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		id, _ := res.LastInsertId()
		inc.ID = int(id)
		who := strings.TrimSpace(inc.ReporterName)
		if who == "" {
			who = strings.TrimSpace(inc.CreatedBy)
		}
		if who == "" {
			who = inc.Source
		}
		addSystemComment("incident", int(id), fmt.Sprintf("Створено %s (%s)", who, inc.Source))
		if on := isOnGridNow(); true {
			mode := "off-grid"
			if on {
				mode = "on-grid"
			}
			extra := "Режим: " + mode
			if inc.UserName != "" {
				_, backup := shiftPairForDate(inc.Date)
				extra += "; авто-призначено: " + inc.UserName
				if backup != "" && backup != inc.UserName {
					extra += " (дубль: " + backup + ")"
				}
			} else {
				extra += "; без виконавця (очікує розподілу)"
			}
			addSystemComment("incident", int(id), extra)
		}
		if shouldNotify {
			go notifyNewIncident(inc)
		} else {
			log.Printf("incident #%d: off-grid low priority — skip notify", inc.ID)
		}
		result := map[string]interface{}{
			"status": "ok", "as_task": false, "id": id,
			"assignee": inc.UserName,
			"notified": shouldNotify,
			"on_grid":  isOnGridNow(),
		}
		if isFuture {
			tid, copied, cerr := convertIncidentToTask(inc, inc.CreatedBy)
			if cerr == nil {
				result["as_task"] = true
				result["task_id"] = tid
				result["comments_copied"] = copied
				result["message"] = fmt.Sprintf("Звернення зафіксовано на %s і додано як задачу #%d", inc.Date, tid)
			}
			logAudit(inc.CreatedBy, "CREATE_INCIDENT_AS_TASK", clientIP(r), inc.Description)
		} else {
			logAudit(inc.CreatedBy, "CREATE_INCIDENT", clientIP(r), fmt.Sprintf("#%d %s %dхв", id, inc.Date, inc.DurationMinutes))
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
		convert, _ := raw["convert_to_task"].(bool)

		var cur IncidentReport
		err := db.QueryRow(`SELECT id, user_name, date, description, COALESCE(status,'Нове'), COALESCE(priority,'Звичайний'),
			COALESCE(source,'self'), duration_minutes, COALESCE(total_minutes,0), COALESCE(external_id,''), COALESCE(created_by,'')
			FROM incidents WHERE id=?`, id).
			Scan(&cur.ID, &cur.UserName, &cur.Date, &cur.Description, &cur.Status, &cur.Priority, &cur.Source,
				&cur.DurationMinutes, &cur.TotalMinutes, &cur.ExternalID, &cur.CreatedBy)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		// Admin: призначення виконавця без перетворення в задачу
		if _, hasUser := raw["user_name"]; hasUser && roleHint == "admin" {
			newUser, _ := raw["user_name"].(string)
			if newUser != cur.UserName {
				db.Exec(`UPDATE incidents SET user_name=?, reported_for=?, assigned_at=CASE WHEN COALESCE(assigned_at,'')='' AND ?!='' THEN datetime('now','localtime') ELSE assigned_at END WHERE id=?`, newUser, newUser, newUser, id)
				addSystemComment("incident", id, fmt.Sprintf("Виконавець: «%s» → «%s»", cur.UserName, newUser))
				logAudit(actor, "ASSIGN_INCIDENT", clientIP(r), fmt.Sprintf("id=%d user=%s", id, newUser))
				cur.UserName = newUser
				go notifyIncidentUpdate(cur, fmt.Sprintf("Звернення #%d призначено на %s", id, newUser))
				if newUser != "" {
					go notifyUserSlack(newUser, fmt.Sprintf("Вам призначено звернення #%d: %s", id, cur.Description))
				}
			}
		}

		// Admin: конвертація в задачу + копія коментарів
		if convert {
			if roleHint != "admin" {
				http.Error(w, "Лише admin може переводити звернення в задачу", http.StatusForbidden)
				return
			}
			if actor == "" {
				actor = "admin"
			}
			// якщо передано user_name разом із convert — використати його
			if v, ok := raw["user_name"].(string); ok {
				cur.UserName = v
				db.Exec(`UPDATE incidents SET user_name=?, assigned_at=CASE WHEN COALESCE(assigned_at,'')='' AND ?!='' THEN datetime('now','localtime') ELSE assigned_at END WHERE id=?`, v, v, id)
			}
			tid, copied, cerr := convertIncidentToTask(cur, actor)
			if cerr != nil {
				// duplicate → 409
				if strings.Contains(cerr.Error(), "вже") {
					http.Error(w, cerr.Error(), http.StatusConflict)
					return
				}
				http.Error(w, cerr.Error(), 500)
				return
			}
			cur.Status = "Вирішено"
			cur.ConvertedToTaskID = int(tid)
			logAudit(actor, "CONVERT_INCIDENT_TO_TASK", clientIP(r), fmt.Sprintf("inc=%d task=%d copied=%d", id, tid, copied))
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok", "task_id": tid, "comments_copied": copied, "incident_status": cur.Status,
			})
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
		if newStatus != oldStatus {
			db.Exec(`UPDATE incidents SET status=? WHERE id=?`, newStatus, id)
			addSystemComment("incident", id, fmt.Sprintf("Статус: %s → %s", oldStatus, newStatus))
			if newStatus == "Вирішено" || newStatus == "Архів" {
				var assignedAt, createdAt string
				_ = db.QueryRow(`SELECT COALESCE(assigned_at,''), COALESCE(created_at,'') FROM incidents WHERE id=?`, id).Scan(&assignedAt, &createdAt)
				fact := incidentFactMinutes(assignedAt, createdAt, "")
				if fact > 0 {
					db.Exec(`UPDATE incidents SET total_minutes=? WHERE id=?`, fact, id)
					cur.TotalMinutes = fact
					cur.FactMinutes = fact
					addSystemComment("incident", id, fmt.Sprintf("Факт виконання: %d хв (план %d хв)", fact, cur.DurationMinutes))
				}
			}
			go notifyIncidentUpdate(cur, fmt.Sprintf("Звернення #%d: статус %s → %s", id, oldStatus, newStatus))
			if cur.ExternalID != "" {
				syncIncidentStatusToJira(cur.ExternalID, oldStatus, newStatus)
			}
		}
		// Admin: пріоритет / опис / тривалість / дата / тип
		if roleHint == "admin" {
			if v, ok := raw["priority"].(string); ok && v != "" && v != cur.Priority {
				db.Exec(`UPDATE incidents SET priority=? WHERE id=?`, v, id)
				addSystemComment("incident", id, fmt.Sprintf("Пріоритет: %s → %s", cur.Priority, v))
				cur.Priority = v
			}
			if v, ok := raw["description"].(string); ok && v != cur.Description {
				db.Exec(`UPDATE incidents SET description=? WHERE id=?`, v, id)
				cur.Description = v
			}
			if v, ok := raw["duration_minutes"].(float64); ok {
				m := int(v)
				if m > 0 && m != cur.DurationMinutes {
					db.Exec(`UPDATE incidents SET duration_minutes=? WHERE id=?`, m, id)
					cur.DurationMinutes = m
				}
			}
			if v, ok := raw["date"].(string); ok && v != "" && v != cur.Date {
				db.Exec(`UPDATE incidents SET date=? WHERE id=?`, v, id)
				cur.Date = v
			}
			if v, ok := raw["type"].(string); ok && v != "" {
				db.Exec(`UPDATE incidents SET type=? WHERE id=?`, v, id)
			}
			if v, ok := raw["source"].(string); ok && v != "" {
				db.Exec(`UPDATE incidents SET source=? WHERE id=?`, v, id)
				cur.Source = v
			}
		}
		logAudit(actor, "UPDATE_INCIDENT", clientIP(r), fmt.Sprintf("id=%d status=%s→%s", id, oldStatus, newStatus))
		cur.Status = newStatus
		json.NewEncoder(w).Encode(cur)

	case http.MethodDelete:
		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		// optional role guard via query/header is weak; allow delete (UI only for admin)
		res, err := db.Exec(`DELETE FROM incidents WHERE id=?`, idStr)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			http.Error(w, "not found", 404)
			return
		}
		db.Exec(`DELETE FROM comments WHERE entity_type='incident' AND entity_id=?`, idStr)
		logAudit("admin", "DELETE_INCIDENT", clientIP(r), "id="+idStr)
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})

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
		// user_name may be empty → «на розгляді»
		if t.Status == "" {
			t.Status = "Нова"
		}
		if t.Priority == "" {
			t.Priority = "Базова"
		}
		if t.VisibleFrom == "" {
			t.VisibleFrom = t.Date
		}
		if strings.TrimSpace(t.Status) == "" || t.Status == "Нова" {
			// нові задачі з адмінки/планування стартують як нерозподілені
			if roleHint := strings.ToLower(strings.TrimSpace(t.CreatedBy)); true {
				_ = roleHint
			}
			if t.Status == "" {
				t.Status = "Нерозподілена"
			}
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
		st := t.Status
		if st == "" {
			st = "Нова"
		}
		openTaskStatusLog(t.ID, st, t.CreatedBy)
		logAudit(t.UserName, "CREATE_DAILY_TASK", clientIP(r), t.TaskDescription)
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
		// unassigned executor («на розгляді») — only admin can change status / assign
		if cur.UserName == "" && roleHint != "admin" {
			http.Error(w, "Задача «на розгляді»: виконавця ще не призначено. Змінювати статус може лише admin", http.StatusForbidden)
			return
		}
		// non-admin may change status only on own assigned tasks
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
		// admin may assign executor (user_name) including clearing
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
			openTaskStatusLog(t.ID, newStatus, actor)
		}
		logAudit(userName, "UPDATE_DAILY_TASK", clientIP(r), fmt.Sprintf("id=%d status=%s priority=%s", t.ID, newStatus, newPriority))
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

// Comment model
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
		logAudit(author, "ADD_COMMENT", clientIP(r), fmt.Sprintf("%s#%d", c.EntityType, c.EntityID))
		if c.EntityType == "incident" {
			go notifyIncidentComment(c.EntityID, author, c.Body)
		} else if c.EntityType == "task" {
			go notifyTaskComment(c.EntityID, author, c.Body)
		}
		json.NewEncoder(w).Encode(c)
	default:
		http.Error(w, "Method not allowed", 405)
	}
}



// handleAdminBadges — лічильники для червоних крапок у меню адмінки.
func handleAdminBadges(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	today := time.Now().Format("2006-01-02")
	var openIncs, unassigned, newToday, approval, overdue, openTasks int
	db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE COALESCE(status,'Нове') NOT IN ('Вирішено','Архів') AND COALESCE(converted_to_task_id,0)=0`).Scan(&openIncs)
	db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE COALESCE(status,'Нове') NOT IN ('Вирішено','Архів') AND COALESCE(converted_to_task_id,0)=0 AND COALESCE(user_name,'')=''`).Scan(&unassigned)
	db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE date=? AND COALESCE(status,'Нове') NOT IN ('Вирішено','Архів') AND COALESCE(converted_to_task_id,0)=0`, today).Scan(&newToday)
	db.QueryRow(`SELECT COUNT(*) FROM daily_tasks WHERE COALESCE(status,'Нова') IN ('До перевірки')`).Scan(&approval)
	db.QueryRow(`SELECT COUNT(*) FROM daily_tasks WHERE due_date != '' AND due_date < ? AND COALESCE(status,'Нова') NOT IN ('Виконана','Архів')`, today).Scan(&overdue)
	db.QueryRow(`SELECT COUNT(*) FROM daily_tasks WHERE COALESCE(status,'Нова') NOT IN ('Виконана','Архів')`).Scan(&openTasks)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"open_incidents":   openIncs,
		"unassigned":       unassigned,
		"incidents_today":  newToday,
		"approval":         approval,
		"overdue":          overdue,
		"open_tasks":       openTasks,
		// dots: true if something needs attention
		"dot_queues":       unassigned > 0 || newToday > 0 || overdue > 0,
		"dot_incidents":    openIncs > 0,
		"dot_tasks":        overdue > 0,
		"dot_approval":     approval > 0,
	})
}

func handleAdminQueues(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", 405)
		return
	}
	today := time.Now().Format("2006-01-02")
	day := strings.TrimSpace(r.URL.Query().Get("date"))
	if day == "" {
		day = today
	}
	// basic validate YYYY-MM-DD
	if len(day) != 10 {
		day = today
	}

	// by status
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

	// by assignee
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

	// overdue: due_date < today and not done
	overdue := 0
	db.QueryRow(`SELECT COUNT(*) FROM daily_tasks
		WHERE due_date != '' AND due_date < ? AND COALESCE(status,'Нова') NOT IN ('Виконана','Архів')`, day).Scan(&overdue)

	// due on selected day
	dueToday := 0
	db.QueryRow(`SELECT COUNT(*) FROM daily_tasks
		WHERE due_date = ? AND COALESCE(status,'Нова') NOT IN ('Виконана','Архів')`, day).Scan(&dueToday)

	// incidents on selected day by status
	incToday := 0
	db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE date=?`, day).Scan(&incToday)
	incByStatus := map[string]int{"усі": incToday}
	incRows, _ := db.Query(`SELECT COALESCE(status,'Нове'), COUNT(*) FROM incidents WHERE date=? GROUP BY COALESCE(status,'Нове')`, day)
	if incRows != nil {
		defer incRows.Close()
		for incRows.Next() {
			var s string
			var cnt int
			incRows.Scan(&s, &cnt)
			incByStatus[s] = cnt
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"today":                     day,
		"server_today":              today,
		"tasks_by_status":           byStatus,
		"tasks_by_assignee":         byAssignee,
		"tasks_overdue":             overdue,
		"tasks_due_today":           dueToday,
		"incidents_today_by_status": incByStatus,
	})
}


func handleTaskStatusLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	// ?task_id= or ?ids=1,2,3 or ?date=YYYY-MM-DD (all tasks relevant that day)
	if ids := r.URL.Query().Get("ids"); ids != "" {
		out := map[string]interface{}{}
		for _, p := range strings.Split(ids, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			var id int
			fmt.Sscanf(p, "%d", &id)
			if id > 0 {
				out[p] = map[string]interface{}{
					"breakdown":    taskStatusBreakdown(id),
					"last_activity": taskLastActivity(id),
				}
			}
		}
		json.NewEncoder(w).Encode(out)
		return
	}
	idStr := r.URL.Query().Get("task_id")
	var id int
	fmt.Sscanf(idStr, "%d", &id)
	if id <= 0 {
		http.Error(w, "task_id or ids required", 400)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"breakdown":     taskStatusBreakdown(id),
		"last_activity": taskLastActivity(id),
	})
}
