package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func ensureOnGridExceptions() {
	db.Exec(`CREATE TABLE IF NOT EXISTS on_grid_exceptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT UNIQUE,
		year_month TEXT UNIQUE,
		mode TEXT NOT NULL,
		start TEXT DEFAULT '',
		end_time TEXT DEFAULT '',
		weekdays TEXT DEFAULT '',
		note TEXT DEFAULT ''
	)`)
}

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

// onGridSnapshot — публічний зріз режиму (без admin-префіксу в UI).
func onGridSnapshot() map[string]interface{} {
	start := getSetting("on_grid_start", "09:00")
	end := getSetting("on_grid_end", "18:00")
	tz := getSetting("on_grid_timezone", "Europe/Kyiv")
	days := getSetting("on_grid_weekdays", "1,2,3,4,5")
	on := isOnGridNow()
	label := "неробочий час"
	mode := "off-grid"
	onNow := "0"
	if on {
		label = "робочий час"
		mode = "on-grid"
		onNow = "1"
	}
	return map[string]interface{}{
		"on_grid":          on,
		"mode":             mode,
		"label":            label,
		"on_grid_start":    start,
		"on_grid_end":      end,
		"on_grid_timezone": tz,
		"on_grid_weekdays": days,
		"_on_grid_now":     onNow,
	}
}

func handleOnGridPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	json.NewEncoder(w).Encode(onGridSnapshot())
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
		snap := onGridSnapshot()
		for k, v := range snap {
			if ks, ok := v.(string); ok {
				out[k] = ks
			} else if kb, ok := v.(bool); ok {
				if kb {
					out[k] = "1"
				} else if k == "on_grid" {
					out[k] = "0"
				}
			}
		}
		out["_on_grid_now"] = snap["_on_grid_now"].(string)
		json.NewEncoder(w).Encode(out)
	case http.MethodPut, http.MethodPost:
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		allowed := map[string]bool{
			"on_grid_start": true, "on_grid_end": true, "on_grid_timezone": true, "on_grid_weekdays": true,
			"session_ttl_hours": true, "login_max_attempts": true, "login_lockout_minutes": true,
			"session_secure": true, "dispatchers": true, "holidays": true, "jira_jql": true, "jira_import_max": true,
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

// isOnGridNow — робочий (on-grid) час: винятки дати → override місяця → дефолт.
func isOnGridNow() bool {
	tzName := getSetting("on_grid_timezone", "Europe/Kyiv")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.FixedZone("EET", 2*3600)
	}
	now := time.Now().In(loc)
	day := now.Format("2006-01-02")
	ym := now.Format("2006-01")
	hm := now.Format("15:04")
	wd := int(now.Weekday())
	if wd == 0 {
		wd = 7
	}

	// 1) точна дата
	var mode, start, end, weekdays string
	err = db.QueryRow(`SELECT mode, COALESCE(start,''), COALESCE(end_time,''), COALESCE(weekdays,'')
		FROM on_grid_exceptions WHERE date=? LIMIT 1`, day).Scan(&mode, &start, &end, &weekdays)
	if err == nil && mode != "" {
		if mode == "off" {
			return false
		}
		if mode == "on" {
			if start == "" {
				start = getSetting("on_grid_start", "09:00")
			}
			if end == "" {
				end = getSetting("on_grid_end", "18:00")
			}
			if start <= end {
				return hm >= start && hm < end
			}
			return hm >= start || hm < end
		}
	}

	// 2) override місяця
	start, end, weekdays = "", "", ""
	err = db.QueryRow(`SELECT COALESCE(start,''), COALESCE(end_time,''), COALESCE(weekdays,'')
		FROM on_grid_exceptions WHERE year_month=? AND mode='month' LIMIT 1`, ym).
		Scan(&start, &end, &weekdays)
	if err != nil {
		start = getSetting("on_grid_start", "09:00")
		end = getSetting("on_grid_end", "18:00")
		weekdays = getSetting("on_grid_weekdays", "1,2,3,4,5")
	} else {
		if start == "" {
			start = getSetting("on_grid_start", "09:00")
		}
		if end == "" {
			end = getSetting("on_grid_end", "18:00")
		}
		if weekdays == "" {
			weekdays = getSetting("on_grid_weekdays", "1,2,3,4,5")
		}
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
// Повертає shouldNotify.
// Явне призначення лише для source=manual (admin обрав виконавця у формі).
// guest/self/webhook/jira — завжди через правила on-grid / off-grid.
func applyIncidentRouting(inc *IncidentReport) (shouldNotify bool) {
	src := strings.ToLower(strings.TrimSpace(inc.Source))
	explicit := src == "manual" || src == "admin"
	if explicit && strings.TrimSpace(inc.UserName) != "" {
		log.Printf("routing: explicit assignee=%q source=%s prio=%s", inc.UserName, src, inc.Priority)
		return true
	}

	// для guest/self скинути «себе як виконавця» — розподіл окремо
	if !explicit {
		inc.UserName = ""
	}

	date := inc.Date
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	primary, backup := shiftPairForDate(date)
	onGrid := isOnGridNow()
	prio := inc.Priority
	_ = backup

	if onGrid {
		if isHotPriority(prio) {
			if primary != "" {
				inc.UserName = primary
			}
			log.Printf("routing: on-grid HOT prio=%s → assignee=%q (shift primary=%q backup=%q)", prio, inc.UserName, primary, backup)
			return true
		}
		log.Printf("routing: on-grid normal prio=%s → unassigned, notify admin (shift %q/%q)", prio, primary, backup)
		return true
	}
	// off-grid
	if isNotifyOffGridPriority(prio) {
		if primary != "" {
			inc.UserName = primary
		}
		log.Printf("routing: off-grid high+ prio=%s → assignee=%q", prio, inc.UserName)
		return true
	}
	log.Printf("routing: off-grid low prio=%s → unassigned, no notify", prio)
	return false
}


func handleOnGridExceptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		rows, err := db.Query(`SELECT COALESCE(date,''), COALESCE(year_month,''), mode,
			COALESCE(start,''), COALESCE(end_time,''), COALESCE(weekdays,''), COALESCE(note,'')
			FROM on_grid_exceptions ORDER BY COALESCE(date, year_month)`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var list []map[string]string
		for rows.Next() {
			var date, ym, mode, start, end, wd, note string
			rows.Scan(&date, &ym, &mode, &start, &end, &wd, &note)
			list = append(list, map[string]string{
				"date": date, "year_month": ym, "mode": mode,
				"start": start, "end": end, "weekdays": wd, "note": note,
			})
		}
		if list == nil {
			list = []map[string]string{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var body struct {
			Date      string `json:"date"`
			YearMonth string `json:"year_month"`
			Mode      string `json:"mode"`
			Start     string `json:"start"`
			End       string `json:"end"`
			Weekdays  string `json:"weekdays"`
			Note      string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body.Mode = strings.TrimSpace(body.Mode)
		if body.Mode == "" {
			body.Mode = "off"
		}
		if body.Date != "" {
			db.Exec(`INSERT INTO on_grid_exceptions (date, mode, start, end_time, note) VALUES (?,?,?,?,?)
				ON CONFLICT(date) DO UPDATE SET mode=excluded.mode, start=excluded.start, end_time=excluded.end_time, note=excluded.note`,
				body.Date, body.Mode, body.Start, body.End, body.Note)
		} else if body.YearMonth != "" {
			db.Exec(`INSERT INTO on_grid_exceptions (year_month, mode, start, end_time, weekdays, note) VALUES (?,?,?,?,?,?)
				ON CONFLICT(year_month) DO UPDATE SET mode=excluded.mode, start=excluded.start, end_time=excluded.end_time, weekdays=excluded.weekdays, note=excluded.note`,
				body.YearMonth, "month", body.Start, body.End, body.Weekdays, body.Note)
		} else {
			http.Error(w, "date or year_month required", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		key := r.URL.Query().Get("key")
		if key == "" {
			http.Error(w, "key required", 400)
			return
		}
		if len(key) == 7 { // YYYY-MM
			db.Exec(`DELETE FROM on_grid_exceptions WHERE year_month=?`, key)
		} else {
			db.Exec(`DELETE FROM on_grid_exceptions WHERE date=?`, key)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
