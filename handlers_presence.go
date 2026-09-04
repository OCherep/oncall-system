package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func ensurePresenceTables() {
	db.Exec(`CREATE TABLE IF NOT EXISTS user_brb (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_name TEXT NOT NULL,
		until_at TEXT NOT NULL,
		note TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		cleared_at DATETIME
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS shift_relief (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT NOT NULL,
		role TEXT NOT NULL,
		original_user TEXT NOT NULL,
		relief_user TEXT NOT NULL,
		started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		ended_at DATETIME,
		started_by TEXT DEFAULT '',
		ended_by TEXT DEFAULT ''
	)`)
	// users.needs_resume — flag after being replaced on shift
	db.Exec(`ALTER TABLE users ADD COLUMN needs_resume INTEGER DEFAULT 0`)
}

// activeBRBMap user_name → until_at (only future/active)
func activeBRBMap() map[string]string {
	out := map[string]string{}
	tzName := getSetting("on_grid_timezone", "Europe/Kyiv")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.FixedZone("EET", 2*3600)
	}
	now := time.Now().In(loc).Format("2006-01-02 15:04:05")
	rows, err := db.Query(`SELECT user_name, until_at FROM user_brb
		WHERE cleared_at IS NULL AND until_at > ?`, now)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var u, until string
		rows.Scan(&u, &until)
		out[u] = until
	}
	return out
}


// userWantsSlackStatusOnBRB — default true if column missing/1
func userWantsSlackStatusOnBRB(userName string) bool {
	var v int
	err := db.QueryRow(`SELECT COALESCE(brb_slack_status,1) FROM users WHERE name=? LIMIT 1`, userName).Scan(&v)
	if err != nil {
		return true
	}
	return v != 0
}

func userSlackID(userName string) string {
	var id string
	_ = db.QueryRow(`SELECT COALESCE(slack_id,'') FROM users WHERE name=? LIMIT 1`, userName).Scan(&id)
	return strings.TrimSpace(id)
}

// setSlackCustomStatus — users.profile.set (потрібен scope users.profile:write; для чужих профілів часто потрібен user token / admin).
func setSlackCustomStatus(slackUserID, text, emoji string, expirationUnix int64) error {
	token := slackBotToken()
	if token == "" || slackUserID == "" {
		return fmt.Errorf("no token or slack user id")
	}
	profile := map[string]interface{}{
		"status_text":  text,
		"status_emoji": emoji,
	}
	if expirationUnix > 0 {
		profile["status_expiration"] = expirationUnix
	}
	payload := map[string]interface{}{
		"user":    slackUserID,
		"profile": profile,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, "https://slack.com/api/users.profile.set", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.OK {
		return fmt.Errorf("slack profile.set: %s", out.Error)
	}
	return nil
}

func clearSlackCustomStatus(slackUserID string) error {
	return setSlackCustomStatus(slackUserID, "", "", 0)
}

func maybeSetSlackStatusOnBRB(userName, untilHHMM string) {
	if !userWantsSlackStatusOnBRB(userName) {
		return
	}
	sid := userSlackID(userName)
	if sid == "" {
		return
	}
	exp := int64(0)
	tzName := getSetting("on_grid_timezone", "Europe/Kyiv")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		loc = time.FixedZone("EET", 2*3600)
	}
	until := untilHHMM
	if len(untilHHMM) <= 5 {
		until = time.Now().In(loc).Format("2006-01-02") + " " + untilHHMM + ":00"
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", until, loc); err == nil {
		exp = t.Unix()
	}
	text := "BRB до " + untilHHMM
	if err := setSlackCustomStatus(sid, text, ":brb:", exp); err != nil {
		// fallback emoji
		if err2 := setSlackCustomStatus(sid, text, ":no_entry:", exp); err2 != nil {
			log.Printf("BRB slack status %s: %v / %v", userName, err, err2)
		}
	}
}

func maybeClearSlackStatusOnBRB(userName string) {
	if !userWantsSlackStatusOnBRB(userName) {
		return
	}
	sid := userSlackID(userName)
	if sid == "" {
		return
	}
	if err := clearSlackCustomStatus(sid); err != nil {
		log.Printf("BRB clear slack status %s: %v", userName, err)
	}
}

func setBRB(userName, untilHHMM, note string) error {
	userName = strings.TrimSpace(userName)
	untilHHMM = strings.TrimSpace(untilHHMM)
	if userName == "" || untilHHMM == "" {
		return fmt.Errorf("user and until required")
	}
	// accept HH:MM or full datetime (Europe/Kyiv)
	until := untilHHMM
	if len(untilHHMM) <= 5 {
		tzName := getSetting("on_grid_timezone", "Europe/Kyiv")
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			loc = time.FixedZone("EET", 2*3600)
		}
		now := time.Now().In(loc)
		until = now.Format("2006-01-02") + " " + untilHHMM + ":00"
	}
	// clear previous active
	db.Exec(`UPDATE user_brb SET cleared_at=CURRENT_TIMESTAMP WHERE user_name=? AND cleared_at IS NULL`, userName)
	_, err := db.Exec(`INSERT INTO user_brb (user_name, until_at, note) VALUES (?,?,?)`, userName, until, note)
	if err == nil {
		openPresenceInterval(userName, "brb", until, note, note)
		go maybeSetSlackStatusOnBRB(userName, untilHHMM)
	}
	return err
}

func clearBRB(userName string) {
	db.Exec(`UPDATE user_brb SET cleared_at=CURRENT_TIMESTAMP WHERE user_name=? AND cleared_at IS NULL`, userName)
	closePresenceInterval(userName, "brb")
	go maybeClearSlackStatusOnBRB(userName)
}

// handleBRB — GET list / POST set / DELETE clear
func handleBRB(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(activeBRBMap())
	case http.MethodPost:
		var body struct {
			UserName string `json:"user_name"`
			Until    string `json:"until"`
			Note     string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := setBRB(body.UserName, body.Until, body.Note); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		logAudit(body.UserName, "BRB_SET", clientIP(r), body.Until)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "until": body.Until})
	case http.MethodDelete:
		u := r.URL.Query().Get("user")
		if u == "" {
			http.Error(w, "user required", 400)
			return
		}
		clearBRB(u)
		logAudit(u, "BRB_CLEAR", clientIP(r), "")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// handleShiftRelief — temporary swap of primary/backup
func handleShiftRelief(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		day := r.URL.Query().Get("date")
		if day == "" {
			day = time.Now().Format("2006-01-02")
		}
		rows, err := db.Query(`SELECT id, date, role, original_user, relief_user, started_at, COALESCE(ended_at,''), started_by, ended_by
			FROM shift_relief WHERE date=? ORDER BY id DESC`, day)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var list []map[string]interface{}
		for rows.Next() {
			var id int
			var date, role, orig, relief, started, ended, sb, eb string
			rows.Scan(&id, &date, &role, &orig, &relief, &started, &ended, &sb, &eb)
			list = append(list, map[string]interface{}{
				"id": id, "date": date, "role": role,
				"original_user": orig, "relief_user": relief,
				"started_at": started, "ended_at": ended,
				"started_by": sb, "ended_by": eb, "active": ended == "",
			})
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var body struct {
			Date         string `json:"date"`
			Role         string `json:"role"` // primary | backup
			OriginalUser string `json:"original_user"`
			ReliefUser   string `json:"relief_user"`
			Actor        string `json:"actor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.Date == "" {
			body.Date = time.Now().Format("2006-01-02")
		}
		body.Role = strings.ToLower(strings.TrimSpace(body.Role))
		if body.Role != "primary" && body.Role != "backup" {
			http.Error(w, "role must be primary|backup", 400)
			return
		}
		if body.OriginalUser == "" || body.ReliefUser == "" {
			http.Error(w, "original_user and relief_user required", 400)
			return
		}
		// close previous active relief for same role/date
		db.Exec(`UPDATE shift_relief SET ended_at=CURRENT_TIMESTAMP, ended_by=? WHERE date=? AND role=? AND ended_at IS NULL`,
			body.Actor, body.Date, body.Role)
		res, err := db.Exec(`INSERT INTO shift_relief (date, role, original_user, relief_user, started_by) VALUES (?,?,?,?,?)`,
			body.Date, body.Role, body.OriginalUser, body.ReliefUser, body.Actor)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// update shifts row for display
		if body.Role == "primary" {
			db.Exec(`UPDATE shifts SET primary_user=? WHERE date=?`, body.ReliefUser, body.Date)
		} else {
			db.Exec(`UPDATE shifts SET backup_user=? WHERE date=?`, body.ReliefUser, body.Date)
		}
		// mark original needs resume + invalidate sessions
		db.Exec(`UPDATE users SET needs_resume=1 WHERE name=? OR username=?`, body.OriginalUser, body.OriginalUser)
		db.Exec(`DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE name=? OR username=?)`, body.OriginalUser, body.OriginalUser)
		id, _ := res.LastInsertId()
		logAudit(body.Actor, "SHIFT_RELIEF", clientIP(r), fmt.Sprintf("%s %s → %s (was %s)", body.Date, body.Role, body.ReliefUser, body.OriginalUser))
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "id": id})
	case http.MethodPut:
		// end relief / "Я знову на місці"
		var body struct {
			ID     int    `json:"id"`
			Actor  string `json:"actor"`
			User   string `json:"user_name"` // who is coming back
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var date, role, orig, relief string
		err := db.QueryRow(`SELECT date, role, original_user, relief_user FROM shift_relief WHERE id=? AND ended_at IS NULL`, body.ID).
			Scan(&date, &role, &orig, &relief)
		if err != nil {
			// by user if id=0
			if body.User != "" {
				err = db.QueryRow(`SELECT id, date, role, original_user, relief_user FROM shift_relief
					WHERE original_user=? AND ended_at IS NULL ORDER BY id DESC LIMIT 1`, body.User).
					Scan(&body.ID, &date, &role, &orig, &relief)
			}
			if err != nil {
				http.Error(w, "active relief not found", 404)
				return
			}
		}
		db.Exec(`UPDATE shift_relief SET ended_at=CURRENT_TIMESTAMP, ended_by=? WHERE id=?`, body.Actor, body.ID)
		if role == "primary" {
			db.Exec(`UPDATE shifts SET primary_user=? WHERE date=?`, orig, date)
		} else {
			db.Exec(`UPDATE shifts SET backup_user=? WHERE date=?`, orig, date)
		}
		db.Exec(`UPDATE users SET needs_resume=0 WHERE name=? OR username=?`, orig, orig)
		logAudit(body.Actor, "SHIFT_RESUME", clientIP(r), fmt.Sprintf("%s %s back=%s", date, role, orig))
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "restored": orig})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// parseSlackBRB — "/brb 16:00" from Slack slash or message
func parseSlackBRB(text string) (until string, ok bool) {
	t := strings.TrimSpace(strings.ToLower(text))
	t = strings.TrimPrefix(t, "/brb")
	t = strings.TrimSpace(t)
	// also "brb 16:00"
	if strings.HasPrefix(t, "brb ") {
		t = strings.TrimSpace(t[4:])
	}
	if t == "" {
		return "", false
	}
	// HH:MM
	parts := strings.Fields(t)
	if len(parts) == 0 {
		return "", false
	}
	until = parts[0]
	// normalize H:MM → HH:MM
	if len(until) == 4 && until[1] == ':' {
		until = "0" + until
	}
	if len(until) >= 5 && until[2] == ':' {
		return until[:5], true
	}
	return "", false
}

func handleSlackEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	ct := r.Header.Get("Content-Type")
	var text, userName, userID string

	// Slack slash commands: application/x-www-form-urlencoded
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err != nil {
			log.Printf("slack webhook parse form: %v", err)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"response_type": "ephemeral", "text": "Parse error"})
			return
		}
		text = r.FormValue("text")
		if text == "" {
			text = r.FormValue("command") + " " + r.FormValue("text")
		}
		userName = r.FormValue("user_name")
		userID = r.FormValue("user_id")
		// command may be /brb with text "16:00"
		if strings.HasPrefix(r.FormValue("command"), "/brb") && !strings.Contains(strings.ToLower(text), "brb") {
			text = "/brb " + strings.TrimSpace(r.FormValue("text"))
		}
	} else {
		var raw map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// URL verification
		if raw["type"] == "url_verification" {
			json.NewEncoder(w).Encode(map[string]interface{}{"challenge": raw["challenge"]})
			return
		}
		text, _ = raw["text"].(string)
		userName, _ = raw["user_name"].(string)
		if userName == "" {
			userName, _ = raw["user"].(string)
		}
		if uid, ok := raw["user_id"].(string); ok {
			userID = uid
		}
		_ = userID
	}
	if until, ok := parseSlackBRB(text); ok {
		var uname string
		_ = db.QueryRow(`SELECT name FROM users WHERE slack_id=? OR slack_id=? OR username=? OR name=? LIMIT 1`,
			userID, userName, userName, userName).Scan(&uname)
		if uname == "" {
			uname = userName
		}
		msg := fmt.Sprintf("BRB до %s для %s", until, uname)
		if err := setBRB(uname, until, "slack"); err != nil {
			msg = "Помилка BRB: " + err.Error()
			log.Printf("slack BRB err: %v", err)
		} else {
			log.Printf("slack BRB: %s until %s", uname, until)
		}
		// Slack slash expects 200 within 3s; plain text also OK
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"response_type": "ephemeral",
			"text":          msg,
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"response_type": "ephemeral",
		"text":          "Не розпізнано. Приклад: /brb 16:00",
	})
}
