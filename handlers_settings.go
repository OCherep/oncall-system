package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func ensureAppSettingsTable() {
	db.Exec(`CREATE TABLE IF NOT EXISTS app_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT '',
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	defaults := map[string]string{
		"on_grid_start":    "09:00",
		"on_grid_end":      "18:00",
		"on_grid_timezone": "Europe/Kyiv",
		"on_grid_weekdays": "1,2,3,4,5",
	}
	for k, v := range defaults {
		db.Exec(`INSERT OR IGNORE INTO app_settings (key, value) VALUES (?, ?)`, k, v)
	}
}

func getSetting(key, def string) string {
	var v string
	err := db.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key).Scan(&v)
	if err != nil || strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func setSetting(key, value string) {
	db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES (?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`, key, value)
}

func handleAppSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`SELECT key, value FROM app_settings ORDER BY key`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var k, v string
			rows.Scan(&k, &v)
			out[k] = v
		}
		for _, k := range []string{"on_grid_start", "on_grid_end", "on_grid_timezone", "on_grid_weekdays"} {
			if _, ok := out[k]; !ok {
				out[k] = getSetting(k, "")
			}
		}
		if isOnGridNow() {
			out["_on_grid_now"] = "1"
		} else {
			out["_on_grid_now"] = "0"
		}
		json.NewEncoder(w).Encode(out)
	case http.MethodPut, http.MethodPost:
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		allowed := map[string]bool{
			"on_grid_start": true, "on_grid_end": true, "on_grid_timezone": true, "on_grid_weekdays": true,
		}
		for k, v := range body {
			if allowed[k] {
				setSetting(k, strings.TrimSpace(v))
			}
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// isOnGridNow — робочий (on-grid) час за довідником app_settings.
func isOnGridNow() bool {
	start := getSetting("on_grid_start", "09:00")
	end := getSetting("on_grid_end", "18:00")
	tzName := getSetting("on_grid_timezone", "Europe/Kyiv")
	weekdays := getSetting("on_grid_weekdays", "1,2,3,4,5")

	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.FixedZone("EET", 2*3600)
	}
	now := time.Now().In(loc)
	wd := int(now.Weekday()) // Sun=0
	if wd == 0 {
		wd = 7
	}
	okDay := false
	for _, p := range strings.Split(weekdays, ",") {
		if strings.TrimSpace(p) == strconv.Itoa(wd) {
			okDay = true
			break
		}
	}
	if !okDay {
		return false
	}
	hm := now.Format("15:04")
	if start <= end {
		return hm >= start && hm < end
	}
	return hm >= start || hm < end
}

// priorityRank — для порівняння з «підвищений».
func priorityRank(p string) int {
	s := strings.ToLower(strings.TrimSpace(p))
	switch {
	case strings.Contains(s, "надкрит"), strings.Contains(s, "critical") && strings.Contains(s, "над"):
		return 5
	case strings.Contains(s, "термін"), strings.Contains(s, "urgent"):
		return 4
	case strings.Contains(s, "критич"), strings.Contains(s, "critical"):
		return 3
	case strings.Contains(s, "висок"), strings.Contains(s, "high"):
		return 2
	case strings.Contains(s, "підвищ"), strings.Contains(s, "elevat"):
		return 1
	default:
		return 0 // звичайний
	}
}

// isHotPriority — критичний / терміновий (виняток on-grid).
func isHotPriority(p string) bool {
	return priorityRank(p) >= 3
}

// isNotifyOffGridPriority — вищий за «підвищений» (≥ високий).
func isNotifyOffGridPriority(p string) bool {
	return priorityRank(p) >= 2
}

// shiftPairForDate — primary, backup на дату.
func shiftPairForDate(date string) (primary, backup string) {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	_ = db.QueryRow(`SELECT COALESCE(primary_user,''), COALESCE(backup_user,'') FROM shifts WHERE date=?`, date).
		Scan(&primary, &backup)
	return
}

// applyIncidentRouting — авто-призначення й чи слати сповіщення.
// Повертає: assignee (user_name), shouldNotify.
func applyIncidentRouting(inc *IncidentReport) (shouldNotify bool) {
	// якщо вже призначено вручну — не перетираємо
	if strings.TrimSpace(inc.UserName) != "" {
		return true
	}
	date := inc.Date
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	primary, backup := shiftPairForDate(date)
	onGrid := isOnGridNow()
	prio := inc.Priority

	if onGrid {
		if isHotPriority(prio) {
			// критичний/терміновий → пара чергових
			if primary != "" {
				inc.UserName = primary
			}
			if backup != "" && primary != "" {
				// secondary note via comment after insert
				_ = backup
			}
			return true
		}
		// звичайні/підвищені → без виконавця, notify admin для розподілу
		return true
	}
	// off-grid
	if isNotifyOffGridPriority(prio) {
		if primary != "" {
			inc.UserName = primary
		}
		return true
	}
	// низький пріоритет off-grid — без виконавця, без шуму
	return false
}
