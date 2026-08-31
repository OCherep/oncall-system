package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Webhook прийому звернень від Jira / ботів / зовнішніх систем.
//
// Env:
//   ENABLE_INCIDENT_WEBHOOK=1     — увімкнути endpoint (інакше 404)
//   WEBHOOK_SECRET=...            — обов'язковий секрет (header або query)
//
// Auth (будь-який варіант):
//   Header: X-Webhook-Secret: <secret>
//   Header: Authorization: Bearer <secret>
//   Query:  ?secret=<secret>
//
// POST /api/webhooks/incidents
// Content-Type: application/json
//
// Підтримувані формати тіла:
//
// 1) Нативний:
// {
//   "source": "jira",
//   "external_id": "OPS-123",
//   "description": "Текст проблеми",
//   "user_name": "devops1",
//   "priority": "Звичайний",
//   "duration_minutes": 15,
//   "date": "2026-08-21",
//   "create_daily_task": true,
//   "created_by": "jira-bot",
//   "reported_for": "devops1"
// }
//
// 2) Спрощений Jira webhook (issue event):
// {
//   "webhookEvent": "jira:issue_created",
//   "issue": {
//     "key": "OPS-123",
//     "fields": {
//       "summary": "Заголовок",
//       "description": "…",
//       "assignee": { "displayName": "devops1" },
//       "priority": { "name": "High" }
//     }
//   }
// }

func webhookEnabled() bool {
	v := strings.TrimSpace(os.Getenv("ENABLE_INCIDENT_WEBHOOK"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func webhookSecret() string {
	return strings.TrimSpace(os.Getenv("WEBHOOK_SECRET"))
}

func checkWebhookAuth(r *http.Request) bool {
	secret := webhookSecret()
	if secret == "" {
		// без секрету — небезпечно; дозволяємо лише якщо явно ENABLE і порожній секрет (dev)
		log.Printf("webhook: WEBHOOK_SECRET is empty — accepting requests (dev only)")
		return true
	}
	if h := strings.TrimSpace(r.Header.Get("X-Webhook-Secret")); h != "" && h == secret {
		return true
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		if strings.TrimSpace(auth[7:]) == secret {
			return true
		}
	}
	if q := r.URL.Query().Get("secret"); q != "" && q == secret {
		return true
	}
	return false
}

func handleWebhookHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"webhook": webhookEnabled(),
		"notify":  notifyEnabled(),
		"time":    time.Now().Format(time.RFC3339),
	})
}

type webhookIncidentReq struct {
	Source           string `json:"source"`
	ExternalID       string `json:"external_id"`
	Description      string `json:"description"`
	UserName         string `json:"user_name"`
	Priority         string `json:"priority"`
	DurationMinutes  int    `json:"duration_minutes"`
	Date             string `json:"date"`
	CreateDailyTask  *bool  `json:"create_daily_task"`
	CreatedBy        string `json:"created_by"`
	ReportedFor      string `json:"reported_for"`
	Type             string `json:"type"`
	JiraStatus       string `json:"jira_status"`
	// raw passthrough for jira-shaped payloads
	WebhookEvent string `json:"webhookEvent"`
	Issue        *struct {
		Key    string `json:"key"`
		Fields *struct {
			Summary     string `json:"summary"`
			Description interface{} `json:"description"` // string or ADF object
			Assignee    *struct {
				DisplayName string `json:"displayName"`
				Name        string `json:"name"`
			} `json:"assignee"`
			Priority *struct {
				Name string `json:"name"`
			} `json:"priority"`
			Status *struct {
				Name string `json:"name"`
			} `json:"status"`
		} `json:"fields"`
	} `json:"issue"`
}

func handleWebhookIncidents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !webhookEnabled() {
		http.Error(w, `{"error":"webhook disabled; set ENABLE_INCIDENT_WEBHOOK=1"}`, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"POST only"}`, http.StatusMethodNotAllowed)
		return
	}
	if !checkWebhookAuth(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB
	if err != nil {
		http.Error(w, `{"error":"read body"}`, http.StatusBadRequest)
		return
	}
	var req webhookIncidentReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, `{"error":"invalid json: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}

	// Normalize from Jira-shaped payload
	if req.Issue != nil && req.Issue.Fields != nil {
		if req.ExternalID == "" {
			req.ExternalID = req.Issue.Key
		}
		if req.Source == "" {
			req.Source = "jira"
		}
		f := req.Issue.Fields
		if req.Description == "" {
			req.Description = jiraDescriptionToString(f.Summary, f.Description)
		}
		if req.UserName == "" && f.Assignee != nil {
			req.UserName = firstNonEmpty(f.Assignee.DisplayName, f.Assignee.Name)
		}
		if req.Priority == "" && f.Priority != nil {
			req.Priority = mapJiraPriority(f.Priority.Name)
		}
		if req.CreatedBy == "" {
			req.CreatedBy = "jira"
		}
		if req.JiraStatus == "" && f.Status != nil {
			req.JiraStatus = f.Status.Name
		}
	}

	if strings.TrimSpace(req.Description) == "" {
		http.Error(w, `{"error":"description required"}`, http.StatusBadRequest)
		return
	}

	// Defaults
	if req.Source == "" {
		req.Source = "bot"
	}
	if req.Date == "" {
		req.Date = time.Now().Format("2006-01-02")
	}
	if req.DurationMinutes <= 0 {
		req.DurationMinutes = 15
	}
	if req.Priority == "" {
		req.Priority = "Звичайний"
	}
	if req.Type == "" {
		req.Type = "Звернення"
	}
	if req.CreatedBy == "" {
		req.CreatedBy = req.Source
	}
	// Append external id to description for traceability
	desc := strings.TrimSpace(req.Description)
	if req.ExternalID != "" && !strings.Contains(desc, req.ExternalID) {
		desc = "[" + req.ExternalID + "] " + desc
	}

	userName := strings.TrimSpace(req.UserName)
	// empty user_name → will show as «без виконавця» / can be assigned later
	reportedFor := strings.TrimSpace(req.ReportedFor)
	if reportedFor == "" {
		reportedFor = userName
	}

	// Upsert by external_id (двосторонній зв'язок з Jira)
	if req.ExternalID != "" {
		var existingID int
		errEx := db.QueryRow(`SELECT id FROM incidents WHERE external_id=? LIMIT 1`, req.ExternalID).Scan(&existingID)
		if errEx == nil && existingID > 0 {
			db.Exec(`UPDATE incidents SET description=?, priority=?, user_name=COALESCE(NULLIF(?,''), user_name) WHERE id=?`,
				desc, req.Priority, userName, existingID)
			// sync linked task
			var tid int
			_ = db.QueryRow(`SELECT id FROM daily_tasks WHERE external_id=? LIMIT 1`, req.ExternalID).Scan(&tid)
			if tid > 0 {
				db.Exec(`UPDATE daily_tasks SET task_description=?, priority=? WHERE id=?`,
					desc, mapIncidentPrioToTask(req.Priority), tid)
			} else {
				// maybe converted task without external_id
				_ = db.QueryRow(`SELECT converted_to_task_id FROM incidents WHERE id=?`, existingID).Scan(&tid)
			}
			if req.JiraStatus != "" {
				localSt := mapJiraStatusToLocal(req.JiraStatus)
				if localSt != "" {
					db.Exec(`UPDATE incidents SET status=? WHERE id=?`, localSt, existingID)
					if tid > 0 {
						db.Exec(`UPDATE daily_tasks SET status=? WHERE id=?`, mapIncStatusToTask(localSt), tid)
					}
					addSystemComment("incident", existingID, "Jira status → "+req.JiraStatus+" ("+localSt+")")
				}
			}
			addSystemComment("incident", existingID, "Оновлено з Jira webhook")
			logAudit(req.CreatedBy, "WEBHOOK_INCIDENT_UPDATE", r.RemoteAddr, fmt.Sprintf("id=%d ext=%s", existingID, req.ExternalID))
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "updated", "incident_id": existingID, "task_id": tid, "source": req.Source})
			return
		}
	}

	res, err := db.Exec(`INSERT INTO incidents (
		user_name, date, type, duration_minutes, description, created_at,
		status, priority, source, total_minutes, created_by, reported_for, external_id
	) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, 'Нове', ?, ?, ?, ?, ?, ?)`,
		userName, req.Date, req.Type, req.DurationMinutes, desc,
		req.Priority, req.Source, req.DurationMinutes, req.CreatedBy, reportedFor, req.ExternalID)
	if err != nil {
		log.Printf("webhook insert incident: %v", err)
		http.Error(w, `{"error":"db insert failed"}`, http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()

	createTask := true
	if req.CreateDailyTask != nil {
		createTask = *req.CreateDailyTask
	}
	// future date → always create daily task for planning
	if req.Date > time.Now().Format("2006-01-02") {
		createTask = true
	}

	taskID := int64(0)
	if createTask {
		taskDesc := desc
		if req.ExternalID != "" {
			taskDesc = "[webhook] " + desc
		}
		tres, terr := db.Exec(`INSERT INTO daily_tasks (
			user_name, date, task_description, status, priority, total_minutes, created_at, created_by, external_id
		) VALUES (?, ?, ?, 'Нова', ?, 0, CURRENT_TIMESTAMP, ?, ?)`,
			userName, req.Date, taskDesc, mapIncidentPrioToTask(req.Priority), req.CreatedBy, req.ExternalID)
		if terr != nil {
			log.Printf("webhook insert task: %v", terr)
		} else {
			taskID, _ = tres.LastInsertId()
		}
	}

	inc := IncidentReport{
		ID:              int(id),
		UserName:        userName,
		Date:            req.Date,
		Type:            req.Type,
		DurationMinutes: req.DurationMinutes,
		Description:     desc,
		Status:          "Нове",
		Priority:        req.Priority,
		Source:          req.Source,
		TotalMinutes:    req.DurationMinutes,
		CreatedBy:       req.CreatedBy,
		ReportedFor:     reportedFor,
		ExternalID:      req.ExternalID,
	}
	addSystemComment("incident", int(id), fmt.Sprintf("Створено через webhook (source=%s external_id=%s)", req.Source, req.ExternalID))
	logAudit(req.CreatedBy, "WEBHOOK_INCIDENT", r.RemoteAddr,
		fmt.Sprintf("id=%d source=%s ext=%s task=%d", id, req.Source, req.ExternalID, taskID))

	// Slack / notify (async inside)
	notifyOncallAboutIncident(inc)

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"incident_id": id,
		"task_id":     taskID,
		"source":      req.Source,
		"external_id": req.ExternalID,
		"date":        req.Date,
	})
}

func jiraDescriptionToString(summary string, desc interface{}) string {
	parts := []string{}
	if strings.TrimSpace(summary) != "" {
		parts = append(parts, strings.TrimSpace(summary))
	}
	switch v := desc.(type) {
	case string:
		if strings.TrimSpace(v) != "" {
			parts = append(parts, strings.TrimSpace(v))
		}
	case map[string]interface{}:
		// Atlassian Document Format — extract plain text roughly
		if t := extractADFText(v); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " — ")
}

func extractADFText(m map[string]interface{}) string {
	var out []string
	var walk func(interface{})
	walk = func(node interface{}) {
		switch n := node.(type) {
		case map[string]interface{}:
			if t, ok := n["type"].(string); ok && t == "text" {
				if txt, ok := n["text"].(string); ok {
					out = append(out, txt)
				}
			}
			if c, ok := n["content"].([]interface{}); ok {
				for _, ch := range c {
					walk(ch)
				}
			}
		case []interface{}:
			for _, ch := range n {
				walk(ch)
			}
		}
	}
	walk(m)
	return strings.TrimSpace(strings.Join(out, " "))
}

func mapJiraPriority(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(n, "highest"), strings.Contains(n, "blocker"), strings.Contains(n, "critical"):
		return "Критичний"
	case strings.Contains(n, "high"):
		return "Високий"
	case strings.Contains(n, "low"), strings.Contains(n, "lowest"):
		return "Низький"
	default:
		return "Звичайний"
	}
}

func mapIncidentPrioToTask(p string) string {
	n := strings.ToLower(p)
	switch {
	case strings.Contains(n, "крит"), strings.Contains(n, "critical"), strings.Contains(n, "надкрит"):
		return "Надкритична"
	case strings.Contains(n, "висок"), strings.Contains(n, "high"), strings.Contains(n, "термін"):
		return "Термінова"
	default:
		return "Базова"
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}


func mapJiraStatusToLocal(js string) string {
	s := strings.ToLower(strings.TrimSpace(js))
	switch {
	case strings.Contains(s, "progress"), strings.Contains(s, "робот"):
		return "В роботі"
	case strings.Contains(s, "hold"), strings.Contains(s, "pause"), strings.Contains(s, "пауз"):
		return "На паузі"
	case strings.Contains(s, "done"), strings.Contains(s, "resolved"), strings.Contains(s, "closed"), strings.Contains(s, "виріш"):
		return "Вирішено"
	case strings.Contains(s, "open"), strings.Contains(s, "to do"), strings.Contains(s, "backlog"), strings.Contains(s, "нов"):
		return "Нове"
	default:
		return ""
	}
}

func mapIncStatusToTask(st string) string {
	switch st {
	case "В роботі":
		return "У роботі"
	case "На паузі":
		return "На паузі"
	case "Вирішено", "Архів":
		return "Виконана"
	default:
		return "Нова"
	}
}
