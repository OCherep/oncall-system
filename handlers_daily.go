package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// handleDailyBoard — дашборд дейлі: задачі + звернення на дату, з джерелом (jira/oncall).
func handleDailyBoard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	day := strings.TrimSpace(r.URL.Query().Get("date"))
	if day == "" || len(day) != 10 {
		day = time.Now().Format("2006-01-02")
	}

	type boardTask struct {
		ID          int    `json:"id"`
		UserName    string `json:"user_name"`
		Date        string `json:"date"`
		Description string `json:"task_description"`
		Status      string `json:"status"`
		Priority    string `json:"priority"`
		DueDate     string `json:"due_date"`
		Responsible string `json:"responsible"`
		ExternalID  string `json:"external_id"`
		Source      string `json:"source"` // jira | oncall | incident
		CreatedBy   string `json:"created_by"`
		TotalMinutes int   `json:"total_minutes"`
	}

	var tasks []boardTask
	rows, err := db.Query(`SELECT id, COALESCE(user_name,''), COALESCE(date,''), COALESCE(task_description,''),
		COALESCE(status,'Нова'), COALESCE(priority,'Базова'), COALESCE(due_date,''), COALESCE(responsible,''),
		COALESCE(external_id,''), COALESCE(created_by,''), COALESCE(total_minutes,0)
		FROM daily_tasks
		WHERE COALESCE(status,'') NOT IN ('Архів')
		  AND (
		    date = ? OR due_date = ?
		    OR (date <= ? AND COALESCE(status,'Нова') NOT IN ('Виконана','Архів'))
		  )
		ORDER BY
		  CASE priority WHEN 'Надкритична' THEN 0 WHEN 'Термінова' THEN 1 WHEN 'Базова' THEN 2 ELSE 3 END,
		  id DESC
		LIMIT 300`, day, day, day)
	if err == nil && rows != nil {
		defer rows.Close()
		for rows.Next() {
			var t boardTask
			rows.Scan(&t.ID, &t.UserName, &t.Date, &t.Description, &t.Status, &t.Priority, &t.DueDate, &t.Responsible, &t.ExternalID, &t.CreatedBy, &t.TotalMinutes)
			if t.ExternalID != "" {
				t.Source = "jira"
			} else if strings.Contains(t.Description, "[зі звернення") || strings.HasPrefix(t.Description, "[webhook]") {
				t.Source = "incident"
			} else {
				t.Source = "oncall"
			}
			tasks = append(tasks, t)
		}
	}
	if tasks == nil {
		tasks = []boardTask{}
	}

	type boardInc struct {
		ID          int    `json:"id"`
		UserName    string `json:"user_name"`
		Description string `json:"description"`
		Status      string `json:"status"`
		Priority    string `json:"priority"`
		Source      string `json:"source"`
		ExternalID  string `json:"external_id"`
		Reporter    string `json:"reporter_name"`
	}
	var incs []boardInc
	irows, _ := db.Query(`SELECT id, COALESCE(user_name,''), COALESCE(description,''), COALESCE(status,'Нове'),
		COALESCE(priority,'Звичайний'), COALESCE(source,'self'), COALESCE(external_id,''), COALESCE(reporter_name,'')
		FROM incidents WHERE date=? AND COALESCE(converted_to_task_id,0)=0
		ORDER BY id DESC LIMIT 100`, day)
	if irows != nil {
		defer irows.Close()
		for irows.Next() {
			var i boardInc
			irows.Scan(&i.ID, &i.UserName, &i.Description, &i.Status, &i.Priority, &i.Source, &i.ExternalID, &i.Reporter)
			incs = append(incs, i)
		}
	}
	if incs == nil {
		incs = []boardInc{}
	}

	// shift for the day
	var primary, backup string
	db.QueryRow(`SELECT COALESCE(primary_user,''), COALESCE(backup_user,'') FROM shifts WHERE date=?`, day).Scan(&primary, &backup)

	byStatus := map[string]int{}
	unassigned := 0
	for _, t := range tasks {
		byStatus[t.Status]++
		if t.UserName == "" {
			unassigned++
		}
	}

	jiraN := 0
	for _, t := range tasks {
		if t.Source == "jira" {
			jiraN++
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"date":        day,
		"primary":     primary,
		"backup":      backup,
		"tasks":       tasks,
		"incidents":   incs,
		"by_status":   byStatus,
		"unassigned":  unassigned,
		"jira_linked": jiraN,
	})
}
