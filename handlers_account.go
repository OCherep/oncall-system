package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/me/password", withIPAllow(securityHeaders(handleChangePassword)))
	http.HandleFunc("/api/me/profile", withIPAllow(securityHeaders(handleMeProfile)))
	http.HandleFunc("/api/tasks/pool", withIPAllow(securityHeaders(handleTaskPool)))
	http.HandleFunc("/api/tasks/claim", withIPAllow(securityHeaders(handleClaimTask)))
}

func handleChangePassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, 405)
		return
	}
	tok := sessionTokenFromRequest(r)
	s, ok := lookupSession(tok)
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var req struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad json"}`, 400)
		return
	}
	req.Current = strings.TrimSpace(req.Current)
	req.Next = strings.TrimSpace(req.Next)
	if len(req.Next) < 8 {
		http.Error(w, `{"error":"новий пароль мінімум 8 символів"}`, 400)
		return
	}
	if req.Next == req.Current {
		http.Error(w, `{"error":"новий пароль збігається з поточним"}`, 400)
		return
	}
	res, err := db.Exec(`UPDATE users SET password=? WHERE id=? AND password=?`, req.Next, s.UserID, req.Current)
	if err != nil {
		http.Error(w, `{"error":"db"}`, 500)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, `{"error":"поточний пароль невірний"}`, 403)
		return
	}
	logAudit(s.Username, "PASSWORD_CHANGE", clientIP(r), "self")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleMeProfile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, 405)
		return
	}
	s, ok := lookupSession(sessionTokenFromRequest(r))
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var email, phone, slackID, teamRole string
	_ = db.QueryRow(`SELECT COALESCE(u.email,''), COALESCE(u.phone,''), COALESCE(u.slack_id,''), COALESCE(tr.name,'')
		FROM users u LEFT JOIN team_roles tr ON u.team_role_id = tr.id WHERE u.id=?`, s.UserID).
		Scan(&email, &phone, &slackID, &teamRole)
	out := map[string]interface{}{
		"id": s.UserID, "username": s.Username, "name": s.Name, "role": s.Role,
		"email": email, "phone": phone, "slack_id": slackID, "team_role": teamRole,
		"title": "", "image": "", "real_name": s.Name,
	}
	token := strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	if token != "" {
		q := slackID
		if q == "" {
			q = email
		}
		if q == "" {
			q = s.Name
		}
		if m, err := slackResolveUser(token, q); err == nil && m != nil {
			out["slack_id"] = m.ID
			out["real_name"] = m.RealName()
			out["name"] = m.DisplayName()
			if m.Email() != "" {
				out["email"] = m.Email()
			}
			if m.Phone() != "" {
				out["phone"] = m.Phone()
			}
			out["title"] = m.Title()
			out["image"] = m.Profile.Image48
			if slackID == "" && m.ID != "" {
				db.Exec(`UPDATE users SET slack_id=? WHERE id=? AND (slack_id IS NULL OR slack_id='')`, m.ID, s.UserID)
			}
		}
	}
	json.NewEncoder(w).Encode(out)
}

func handleTaskPool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"GET only"}`, 405)
		return
	}
	if _, ok := lookupSession(sessionTokenFromRequest(r)); !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	rows, err := db.Query(`SELECT id, COALESCE(task_description,''), COALESCE(status,''), COALESCE(priority,''),
		COALESCE(external_id,''), COALESCE(due_date,''), COALESCE(user_name,'')
		FROM daily_tasks
		WHERE COALESCE(status,'') NOT IN ('Архів','Виконана','Вирішено')
		  AND (COALESCE(user_name,'') = '' OR status = 'Нерозподілена')
		  AND (status = 'Нерозподілена' OR priority = 'Техборг')
		ORDER BY CASE priority WHEN 'Техборг' THEN 0 ELSE 1 END, id DESC
		LIMIT 100`)
	if err != nil {
		http.Error(w, `{"error":"db"}`, 500)
		return
	}
	defer rows.Close()
	type item struct {
		ID          int    `json:"id"`
		Description string `json:"task_description"`
		Status      string `json:"status"`
		Priority    string `json:"priority"`
		ExternalID  string `json:"external_id"`
		DueDate     string `json:"due_date"`
		UserName    string `json:"user_name"`
	}
	var out []item
	for rows.Next() {
		var it item
		rows.Scan(&it.ID, &it.Description, &it.Status, &it.Priority, &it.ExternalID, &it.DueDate, &it.UserName)
		out = append(out, it)
	}
	if out == nil {
		out = []item{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"items": out})
}

func handleClaimTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, 405)
		return
	}
	s, ok := lookupSession(sessionTokenFromRequest(r))
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var req struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, `{"error":"id required"}`, 400)
		return
	}
	var status, prio, user string
	err := db.QueryRow(`SELECT COALESCE(status,''), COALESCE(priority,''), COALESCE(user_name,'') FROM daily_tasks WHERE id=?`, req.ID).
		Scan(&status, &prio, &user)
	if err != nil {
		http.Error(w, `{"error":"не знайдено"}`, 404)
		return
	}
	if status != "Нерозподілена" && prio != "Техборг" {
		http.Error(w, `{"error":"брати можна лише Нерозподілена або Техборг"}`, 409)
		return
	}
	if strings.TrimSpace(user) != "" && status != "Нерозподілена" {
		http.Error(w, `{"error":"вже є виконавець"}`, 409)
		return
	}
	next := status
	if status == "Нерозподілена" || status == "" {
		next = "Нова"
	}
	today := time.Now().Format("2006-01-02")
	_, err = db.Exec(`UPDATE daily_tasks SET user_name=?, status=?, date=COALESCE(NULLIF(date,''), ?) WHERE id=?`, s.Name, next, today, req.ID)
	if err != nil {
		http.Error(w, `{"error":"db"}`, 500)
		return
	}
	db.Exec(`INSERT INTO task_assignees (task_id, user_name, total_minutes) VALUES (?,?,0)
		ON CONFLICT(task_id, user_name) DO NOTHING`, req.ID, s.Name)
	addSystemComment("task", req.ID, "Взяв сам: "+s.Name)
	logAudit(s.Username, "TASK_CLAIM", clientIP(r), s.Name)
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "id": req.ID, "user_name": s.Name, "task_status": next})
}
