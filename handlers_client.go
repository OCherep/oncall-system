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

func handleGetData(w http.ResponseWriter, r *http.Request) {
	yearStr := r.URL.Query().Get("year")
	monthStr := r.URL.Query().Get("month")

	now := time.Now()
	year, month := now.Year(), int(now.Month())

	if yearStr != "" && monthStr != "" {
		fmt.Sscanf(yearStr, "%d", &year)
		fmt.Sscanf(monthStr, "%d", &month)
	}

	prefix := fmt.Sprintf("%04d-%02d", year, month)

	rows, err := db.Query("SELECT date, primary_user, backup_user FROM shifts WHERE date LIKE ?", prefix+"%")
	shifts := make(map[string]Shift)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var s Shift
			rows.Scan(&s.Date, &s.PrimaryUser, &s.BackupUser)
			shifts[s.Date] = s
		}
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

	monthPattern := fmt.Sprintf("%04d-%02d-%%", year, month)
	incRows, err := db.Query("SELECT id, user_name, date, type, duration_minutes, description FROM incidents WHERE date LIKE ?", monthPattern)
	incidents := make(map[string][]IncidentReport)
	statsMap := make(map[string]*UserStat)

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

	userRows, err := db.Query("SELECT name FROM users WHERE role != 'admin' AND COALESCE(is_oncall, 1) = 1")
	if err == nil {
		defer userRows.Close()
		for userRows.Next() {
			var name string
			userRows.Scan(&name)
			if _, exists := statsMap[name]; !exists {
				statsMap[name] = &UserStat{Name: name}
			}
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
		"shifts":        shifts,
		"absences":      absences,
		"incidents":     incidents,
		"stats":         stats,
		"absence_types": absenceTypes,
		"year":          year,
		"month":         month,
		"daily_tasks":   map[string][]interface{}{},
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

	_, err := db.Exec("INSERT INTO absences (user_name, type, start_date, end_date, status) VALUES (?, ?, ?, ?, 'Pending')",
		req.UserName, req.Type, req.StartDate, req.EndDate)
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

	_, err := db.Exec("INSERT INTO incidents (user_name, date, type, duration_minutes, description) VALUES (?, ?, ?, ?, ?)",
		inc.UserName, inc.Date, inc.Type, inc.DurationMinutes, inc.Description)
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
