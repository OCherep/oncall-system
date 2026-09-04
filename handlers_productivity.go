package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func ensureProductivityTables() {
	db.Exec(`CREATE TABLE IF NOT EXISTS work_day_anchor (
		user_name TEXT NOT NULL,
		work_date TEXT NOT NULL,
		first_event_at TEXT NOT NULL,
		first_event_type TEXT DEFAULT 'login',
		PRIMARY KEY (user_name, work_date)
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS presence_intervals (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_name TEXT NOT NULL,
		kind TEXT NOT NULL,
		started_at TEXT NOT NULL,
		ended_at TEXT,
		until_planned TEXT DEFAULT '',
		source TEXT DEFAULT '',
		note TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_presence_user_day ON presence_intervals(user_name, started_at)`)
}

func kyivNow() time.Time {
	tz := getSetting("on_grid_timezone", "Europe/Kyiv")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.FixedZone("EET", 2*3600)
	}
	return time.Now().In(loc)
}

func kyivDateStr(t time.Time) string {
	return t.Format("2006-01-02")
}

func formatKyiv(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

// recordWorkAnchor — перша подія дня (логін тощо). Ідемпотентно.
func recordWorkAnchor(userName, eventType string) {
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return
	}
	ensureProductivityTables()
	now := kyivNow()
	day := kyivDateStr(now)
	ts := formatKyiv(now)
	res, err := db.Exec(`INSERT OR IGNORE INTO work_day_anchor (user_name, work_date, first_event_at, first_event_type)
		VALUES (?,?,?,?)`, userName, day, ts, eventType)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		logAudit(userName, "WORK_ANCHOR", "", day+" "+eventType)
	}
}

// openPresenceInterval — start BRB/away
func openPresenceInterval(userName, kind, untilPlanned, source, note string) int64 {
	ensureProductivityTables()
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return 0
	}
	now := formatKyiv(kyivNow())
	// close any open same kind
	db.Exec(`UPDATE presence_intervals SET ended_at=? WHERE user_name=? AND kind=? AND ended_at IS NULL`,
		now, userName, kind)
	res, err := db.Exec(`INSERT INTO presence_intervals (user_name, kind, started_at, until_planned, source, note)
		VALUES (?,?,?,?,?,?)`, userName, kind, now, untilPlanned, source, note)
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// closePresenceInterval — return from BRB
func closePresenceInterval(userName, kind string) {
	ensureProductivityTables()
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return
	}
	now := formatKyiv(kyivNow())
	db.Exec(`UPDATE presence_intervals SET ended_at=? WHERE user_name=? AND kind=? AND ended_at IS NULL`,
		now, userName, kind)
	// повернення може бути першою подією дня якщо не було логіну
	recordWorkAnchor(userName, "brb_end")
}

func parseKyivTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	loc, err := time.LoadLocation(getSetting("on_grid_timezone", "Europe/Kyiv"))
	if err != nil {
		loc = time.FixedZone("EET", 2*3600)
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if t, e := time.ParseInLocation(layout, s, loc); e == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad time %q", s)
}

// intervalMinutes on day (clipped to work_date)
func intervalMinutesOnDay(started, ended, untilPlanned, workDate string, now time.Time) int {
	st, err := parseKyivTime(started)
	if err != nil {
		return 0
	}
	dayStart, _ := parseKyivTime(workDate + " 00:00:00")
	dayEnd := dayStart.Add(24 * time.Hour)
	end := now
	if ended != "" {
		if t, e := parseKyivTime(ended); e == nil {
			end = t
		}
	} else if untilPlanned != "" {
		if t, e := parseKyivTime(untilPlanned); e == nil && t.Before(end) {
			end = t
		}
	}
	if st.Before(dayStart) {
		st = dayStart
	}
	if end.After(dayEnd) {
		end = dayEnd
	}
	if !end.After(st) {
		return 0
	}
	return int(end.Sub(st).Minutes())
}

type prodDayRow struct {
	UserName       string `json:"user_name"`
	WorkDate       string `json:"work_date"`
	FirstEventAt   string `json:"first_event_at"`
	FirstEventType string `json:"first_event_type"`
	BRBMinutes     int    `json:"brb_minutes"`
	AwayMinutes    int    `json:"away_minutes"`
	SpanMinutes    int    `json:"span_minutes"` // first_event → now/eod
	ProductiveMin  int    `json:"productive_minutes"`
	SpecialMin     int    `json:"special_minutes"` // вихідні/свята/exception — входить у productive
	DayKind        string `json:"day_kind"`        // weekday|weekend|holiday|exception
	SlackActions   int    `json:"slack_actions"` // placeholder future
}

func computeProductivity(from, to, userFilter string) []prodDayRow {
	ensureProductivityTables()
	now := kyivNow()
	q := `SELECT user_name, work_date, first_event_at, first_event_type FROM work_day_anchor WHERE work_date>=? AND work_date<=?`
	args := []interface{}{from, to}
	if userFilter != "" {
		q += ` AND user_name=?`
		args = append(args, userFilter)
	}
	q += ` ORDER BY work_date DESC, user_name`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil
	}
	var anchors []prodDayRow
	for rows.Next() {
		var r prodDayRow
		if err := rows.Scan(&r.UserName, &r.WorkDate, &r.FirstEventAt, &r.FirstEventType); err != nil {
			continue
		}
		anchors = append(anchors, r)
	}
	rows.Close()
	var out []prodDayRow
	for _, r := range anchors {
		st, e1 := parseKyivTime(r.FirstEventAt)
		dayEnd, _ := parseKyivTime(r.WorkDate + " 23:59:59")
		end := now
		if kyivDateStr(now) != r.WorkDate {
			end = dayEnd
		} else if end.After(dayEnd) {
			end = dayEnd
		}
		if e1 == nil && end.After(st) {
			r.SpanMinutes = int(end.Sub(st).Minutes())
		}
		irows, ierr := db.Query(`SELECT kind, started_at, COALESCE(ended_at,''), COALESCE(until_planned,'')
			FROM presence_intervals WHERE user_name=? AND started_at>=? AND started_at<?`,
			r.UserName, r.WorkDate+" 00:00:00", r.WorkDate+" 23:59:59")
		if ierr == nil && irows != nil {
			for irows.Next() {
				var kind, started, ended, until string
				irows.Scan(&kind, &started, &ended, &until)
				mins := intervalMinutesOnDay(started, ended, until, r.WorkDate, end)
				switch strings.ToLower(kind) {
				case "brb":
					r.BRBMinutes += mins
				default:
					r.AwayMinutes += mins
				}
			}
			irows.Close()
		}
		r.ProductiveMin = r.SpanMinutes - r.BRBMinutes - r.AwayMinutes
		if r.ProductiveMin < 0 {
			r.ProductiveMin = 0
		}
		r.DayKind = dayKindLabel(r.WorkDate)
		if r.DayKind != "weekday" {
			r.SpecialMin = r.ProductiveMin // увесь продуктивний час дня на спецдні
		}
		out = append(out, r)
	}
	return out
}

func handleAdminProductivity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ensureProductivityTables()
	switch r.Method {
	case http.MethodGet:
		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		user := r.URL.Query().Get("user")
		day := r.URL.Query().Get("date")
		if day != "" {
			from, to = day, day
		}
		if from == "" {
			from = kyivNow().Format("2006-01") + "-01"
		}
		if to == "" {
			to = kyivDateStr(kyivNow())
		}
		list := computeProductivity(from, to, user)
		if list == nil {
			list = []prodDayRow{}
		}
		// also raw BRB intervals for admin table
		irows, _ := db.Query(`SELECT id, user_name, kind, started_at, COALESCE(ended_at,''), COALESCE(until_planned,''), COALESCE(source,''), COALESCE(note,'')
			FROM presence_intervals WHERE started_at>=? AND started_at<? ORDER BY id DESC LIMIT 500`,
			from+" 00:00:00", to+" 23:59:59")
		var intervals []map[string]interface{}
		if irows != nil {
			defer irows.Close()
			for irows.Next() {
				var id int
				var u, kind, st, en, until, src, note string
				irows.Scan(&id, &u, &kind, &st, &en, &until, &src, &note)
				intervals = append(intervals, map[string]interface{}{
					"id": id, "user_name": u, "kind": kind,
					"started_at": st, "ended_at": en, "until_planned": until,
					"source": src, "note": note,
				})
			}
		}
		if intervals == nil {
			intervals = []map[string]interface{}{}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"from": from, "to": to, "days": list, "intervals": intervals,
			"hint": "productive = (остання межа дня − перша подія) − BRB/away",
		})
	case http.MethodPost:
		// manual close BRB or anchor
		var body struct {
			Action   string `json:"action"` // close_brb | anchor
			UserName string `json:"user_name"`
			Actor    string `json:"actor"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		switch body.Action {
		case "close_brb":
			clearBRB(body.UserName)
			closePresenceInterval(body.UserName, "brb")
			logAudit(body.Actor, "BRB_CLEAR_ADMIN", clientIP(r), body.UserName)
		case "anchor":
			recordWorkAnchor(body.UserName, "manual")
		default:
			http.Error(w, "unknown action", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}
