package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// listOncallNames — ordered roster for rotation (stable by name).
func listOncallNames() []string {
	rows, err := db.Query(`SELECT name FROM users WHERE COALESCE(is_oncall,0)=1 AND role != 'admin' ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		if strings.TrimSpace(n) != "" {
			out = append(out, n)
		}
	}
	return out
}

func loadApprovedAbsences() []AbsenceRequest {
	rows, err := db.Query(`SELECT id, user_name, type, start_date, end_date, status FROM absences WHERE status = 'Approved'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var abs []AbsenceRequest
	for rows.Next() {
		var a AbsenceRequest
		rows.Scan(&a.ID, &a.UserName, &a.Type, &a.StartDate, &a.EndDate, &a.Status)
		abs = append(abs, a)
	}
	return abs
}

func availableOnDate(pool []string, dateStr string, abs []AbsenceRequest) []string {
	var out []string
	for _, n := range pool {
		if !isAbsentOnDate(n, dateStr, abs) {
			out = append(out, n)
		}
	}
	return out
}

func isWeekendDate(dateStr string) bool {
	t, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
	if err != nil {
		return false
	}
	w := t.Weekday()
	return w == time.Saturday || w == time.Sunday
}

// dutyCal — завантажені довідники. Не можна робити Query всередині відкритого rows:
// db.SetMaxOpenConns(1), вкладений запит блокує єдине з'єднання назавжди.
type dutyCal struct {
	holidays map[string]bool
	off      map[string]bool
	on       map[string]bool
}

func loadDutyCal() dutyCal {
	c := dutyCal{holidays: map[string]bool{}, off: map[string]bool{}, on: map[string]bool{}}
	raw := strings.TrimSpace(getSetting("holidays", ""))
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			c.holidays[p] = true
		}
	}
	rows, err := db.Query(`SELECT date, mode FROM on_grid_exceptions`)
	if err != nil {
		return c
	}
	defer rows.Close()
	for rows.Next() {
		var d, mode string
		rows.Scan(&d, &mode)
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "off":
			c.off[d] = true
		case "on":
			c.on[d] = true
		}
	}
	return c
}

func (c dutyCal) isHoliday(dateStr string) bool {
	if c.holidays[dateStr] {
		return true
	}
	return c.off[dateStr] && !isWeekendDate(dateStr)
}

func (c dutyCal) isExceptionWork(dateStr string) bool {
	return c.on[dateStr]
}

func (c dutyCal) kind(dateStr string) string {
	if c.isExceptionWork(dateStr) {
		return "exception"
	}
	if c.isHoliday(dateStr) {
		return "holiday"
	}
	if isWeekendDate(dateStr) {
		return "weekend"
	}
	return "weekday"
}

func (c dutyCal) isSpecial(dateStr string) bool {
	k := c.kind(dateStr)
	return k == "weekend" || k == "holiday"
}

func isHolidayDate(dateStr string) bool { return loadDutyCal().isHoliday(dateStr) }
func isExceptionWorkDate(dateStr string) bool {
	return loadDutyCal().isExceptionWork(dateStr)
}
func dayKindLabel(dateStr string) string { return loadDutyCal().kind(dateStr) }
func isSpecialDutyDay(dateStr string) bool {
	return loadDutyCal().isSpecial(dateStr)
}

func pickLeastLoaded(avail []string, load map[string]int, skip map[string]bool) string {
	best := ""
	bestN := int(^uint(0) >> 1)
	for _, n := range avail {
		if skip != nil && skip[n] {
			continue
		}
		if load[n] < bestN {
			bestN = load[n]
			best = n
		}
	}
	return best
}

func loadWeekendCountsBefore(before string, cal dutyCal) (prim map[string]int, bak map[string]int) {
	prim, bak = map[string]int{}, map[string]int{}
	rows, err := db.Query(`SELECT date, primary_user, backup_user FROM shifts WHERE date < ?`, before)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var d, p, b string
		rows.Scan(&d, &p, &b)
		if !cal.isSpecial(d) {
			continue
		}
		if p != "" {
			prim[p]++
		}
		if b != "" {
			bak[b]++
		}
	}
	return
}

func indexInPool(pool []string, name string) int {
	name = strings.TrimSpace(name)
	for i, n := range pool {
		if n == name {
			return i
		}
	}
	return -1
}

func pickPair(pool []string, startIdx int) (primary, backup string) {
	if len(pool) == 0 {
		return "", ""
	}
	if startIdx < 0 {
		startIdx = 0
	}
	primary = pool[startIdx%len(pool)]
	backup = primary
	if len(pool) > 1 {
		backup = pool[(startIdx+1)%len(pool)]
	}
	return primary, backup
}

func recalculateShiftsForward(fromDate, untilDate, currPrimary, currBackup, prevPrimary, prevBackup string) (int, error) {
	fromDate = strings.TrimSpace(fromDate)
	if fromDate == "" {
		fromDate = time.Now().Format("2006-01-02")
	}
	if untilDate == "" {
		t, err := time.ParseInLocation("2006-01-02", fromDate, time.Local)
		if err != nil {
			return 0, fmt.Errorf("bad from_date")
		}
		end := time.Date(t.Year(), t.Month()+2, 0, 0, 0, 0, 0, time.Local)
		untilDate = end.Format("2006-01-02")
	}
	pool := listOncallNames()
	if len(pool) == 0 {
		return 0, fmt.Errorf("немає on-call користувачів")
	}
	abs := loadApprovedAbsences()
	cal := loadDutyCal()

	rot := indexInPool(pool, currPrimary)
	if rot < 0 {
		rot = indexInPool(pool, prevPrimary)
		if rot >= 0 {
			rot = (rot + 1) % len(pool)
		} else {
			rot = 0
		}
	}

	start, err := time.ParseInLocation("2006-01-02", fromDate, time.Local)
	if err != nil {
		return 0, err
	}
	end, err := time.ParseInLocation("2006-01-02", untilDate, time.Local)
	if err != nil {
		return 0, err
	}
	if end.Before(start) {
		return 0, fmt.Errorf("until_date before from_date")
	}

	n := 0
	first := true
	wPrim, wBak := loadWeekendCountsBefore(fromDate, cal)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		avail := availableOnDate(pool, dateStr, abs)
		if len(avail) == 0 {
			continue
		}
		var primary, backup string
		if first {
			primary = strings.TrimSpace(currPrimary)
			backup = strings.TrimSpace(currBackup)
			if primary == "" || isAbsentOnDate(primary, dateStr, abs) {
				if cal.isSpecial(dateStr) {
					primary = pickLeastLoaded(avail, wPrim, nil)
				} else {
					primary, rot = nextOncallFrom(pool, rot, dateStr, abs, nil)
				}
			}
			if primary == "" {
				continue
			}
			if backup == "" || backup == primary || isAbsentOnDate(backup, dateStr, abs) {
				if cal.isSpecial(dateStr) {
					backup = pickLeastLoaded(avail, wBak, map[string]bool{primary: true})
				} else {
					pIdx := indexInPool(pool, primary)
					if pIdx < 0 {
						pIdx = rot
					}
					backup, _ = nextOncallFrom(pool, (pIdx+1)%len(pool), dateStr, abs, map[string]bool{primary: true})
				}
				if backup == "" {
					backup = primary
				}
			}
			if pi := indexInPool(pool, primary); pi >= 0 {
				rot = (pi + 1) % len(pool)
			}
			first = false
		} else if cal.isSpecial(dateStr) {
			primary = pickLeastLoaded(avail, wPrim, nil)
			backup = pickLeastLoaded(avail, wBak, map[string]bool{primary: true})
			if primary == "" {
				continue
			}
			if backup == "" {
				backup = primary
			}
		} else {
			var pNext int
			primary, pNext = nextOncallFrom(pool, rot, dateStr, abs, nil)
			if primary == "" {
				continue
			}
			backup, _ = nextOncallFrom(pool, pNext, dateStr, abs, map[string]bool{primary: true})
			if backup == "" {
				backup = primary
			}
			rot = pNext
		}
		if cal.isSpecial(dateStr) {
			wPrim[primary]++
			wBak[backup]++
		}
		if _, err := db.Exec(`INSERT INTO shifts (date, primary_user, backup_user) VALUES (?,?,?)
			ON CONFLICT(date) DO UPDATE SET primary_user=excluded.primary_user, backup_user=excluded.backup_user`,
			dateStr, primary, backup); err != nil {
			return n, err
		}
		n++
	}
	_ = prevBackup
	return n, nil
}

func handleAdminShifts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		month := r.URL.Query().Get("month")
		if month != "" {
			from = month + "-01"
			t, _ := time.Parse("2006-01-02", from)
			to = time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		}
		if from == "" {
			from = time.Now().Format("2006-01") + "-01"
		}
		if to == "" {
			t, _ := time.Parse("2006-01-02", from)
			to = time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		}
		rows, err := db.Query(`SELECT date, primary_user, backup_user FROM shifts WHERE date>=? AND date<=? ORDER BY date`, from, to)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		var list []map[string]string
		for rows.Next() {
			var d, p, b string
			rows.Scan(&d, &p, &b)
			list = append(list, map[string]string{"date": d, "primary": p, "backup": b})
		}
		if list == nil {
			list = []map[string]string{}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"from": from, "to": to, "days": list, "oncall": listOncallNames(),
		})

	case http.MethodPut:
		var body struct {
			Actor string `json:"actor"`
			Days  []struct {
				Date    string `json:"date"`
				Primary string `json:"primary"`
				Backup  string `json:"backup"`
			} `json:"days"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if len(body.Days) == 0 {
			http.Error(w, "days required", 400)
			return
		}
		n := 0
		for _, d := range body.Days {
			d.Date = strings.TrimSpace(d.Date)
			d.Primary = strings.TrimSpace(d.Primary)
			d.Backup = strings.TrimSpace(d.Backup)
			if d.Date == "" || d.Primary == "" {
				continue
			}
			if d.Backup == "" {
				d.Backup = d.Primary
			}
			db.Exec(`INSERT INTO shifts (date, primary_user, backup_user) VALUES (?,?,?)
				ON CONFLICT(date) DO UPDATE SET primary_user=excluded.primary_user, backup_user=excluded.backup_user`,
				d.Date, d.Primary, d.Backup)
			n++
		}
		logAudit(body.Actor, "SHIFTS_BULK_EDIT", clientIP(r), fmt.Sprintf("%d days", n))
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "updated": n})

	case http.MethodPost:
		var body struct {
			Actor           string `json:"actor"`
			FromDate        string `json:"from_date"`
			UntilDate       string `json:"until_date"`
			CurrentPrimary  string `json:"current_primary"`
			CurrentBackup   string `json:"current_backup"`
			PreviousPrimary string `json:"previous_primary"`
			PreviousBackup  string `json:"previous_backup"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if strings.TrimSpace(body.CurrentPrimary) == "" {
			http.Error(w, "current_primary required", 400)
			return
		}
		n, err := recalculateShiftsForward(body.FromDate, body.UntilDate,
			body.CurrentPrimary, body.CurrentBackup, body.PreviousPrimary, body.PreviousBackup)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		logAudit(body.Actor, "SHIFTS_RECALC_FORWARD", clientIP(r),
			fmt.Sprintf("from=%s n=%d curr=%s/%s prev=%s/%s",
				body.FromDate, n, body.CurrentPrimary, body.CurrentBackup, body.PreviousPrimary, body.PreviousBackup))
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "updated": n, "from": body.FromDate})

	default:
		http.Error(w, "method not allowed", 405)
	}
}
