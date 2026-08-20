package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// handleIncidentWebhook — заготовка для зовнішніх систем (моніторинг тощо).
// За замовчуванням ВИМКНЕНО. Увімкнення: ENABLE_INCIDENT_WEBHOOK=1
// Опційний секрет: WEBHOOK_SECRET у заголовку X-Webhook-Secret.
//
// Приклад тіла:
// {"description":"alert text","priority":"Критичний","user_name":"","source":"monitoring"}
func handleIncidentWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	if os.Getenv("ENABLE_INCIDENT_WEBHOOK") != "1" {
		logAudit("webhook", "WEBHOOK_DISABLED", r.RemoteAddr, "ENABLE_INCIDENT_WEBHOOK not set")
		w.WriteHeader(http.StatusNotImplemented)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "disabled",
			"message": "Webhook вимкнено. Встановіть ENABLE_INCIDENT_WEBHOOK=1 щоб активувати.",
		})
		return
	}
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret != "" && r.Header.Get("X-Webhook-Secret") != secret {
		http.Error(w, "invalid webhook secret", 403)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var payload struct {
		Description string `json:"description"`
		Priority    string `json:"priority"`
		UserName    string `json:"user_name"`
		Responsible string `json:"responsible"`
		Source      string `json:"source"`
		Type        string `json:"type"`
		Date        string `json:"date"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Description == "" {
		http.Error(w, "JSON with description required", 400)
		return
	}
	if payload.Source == "" {
		payload.Source = "monitoring"
	}
	if payload.Type == "" {
		payload.Type = "Звернення"
	}
	if payload.Priority == "" {
		payload.Priority = "Звичайний"
	}
	if payload.Date == "" {
		payload.Date = time.Now().Format("2006-01-02")
	}
	res, err := db.Exec(`INSERT INTO incidents (user_name, date, type, duration_minutes, description, created_at,
		status, priority, source, responsible, created_by, total_minutes, reported_for)
		VALUES (?,?,?,0,?,CURRENT_TIMESTAMP,'Нове',?,?,?,'webhook',0,?)`,
		payload.UserName, payload.Date, payload.Type, payload.Description,
		payload.Priority, payload.Source, payload.Responsible, payload.UserName)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	id, _ := res.LastInsertId()
	logAudit("webhook", "CREATE_INCIDENT_WEBHOOK", r.RemoteAddr, fmt.Sprintf("#%d src=%s", id, payload.Source))
	w.WriteHeader(201)
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "id": id})
}
