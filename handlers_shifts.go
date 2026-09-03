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

// indexInPool — position of name in pool, or -1.
func indexInPool(pool []string, name string) int {
	name = strings.TrimSpace(name)
	for i, n := range pool {
		if n == name {
			return i
		}
	}
	return -1
}

// pickPair — primary = pool[startIdx % len], backup = next different if possible.
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

// recalculateShiftsForward — only dates >= fromDate (inclusive). Past rows untouched.
// Seed: current primary/backup for fromDate; rotation continues by full on-call pool order.
// previous_* optional: used only to prefer rotation order continuity (find index after previous primary).
func recalculateShiftsForward(fromDate, untilDate, currPrimary, currBackup, prevPrimary, prevBackup string) (int, error) {
	fromDate = strings.TrimSpace(fromDate)
	if fromDate == "" {
		fromDate = time.Now().Format("2006-01-02")
	}
	if untilDate == "" {
		// default: end of fromDate's month + next full month
		t, err := time.ParseInLocation("2006-01-02", fromDate, time.Local)
		if err != nil {
			return 0, fmt.Errorf("bad from_date")
		}
		untilDate = t.AddDate(0, 2, 0).Format("2006-01-02")
		// last day of month+1
		end := time.Date(t.Year(), t.Month()+2, 0, 0, 0, 0, 0, time.Local)
		untilDate = end.Format("2006-01-02")
	}
	pool := listOncallNames()
	if len(pool) == 0 {
		return 0, fmt.Errorf("немає on-call користувачів")
	}
	abs := loadApprovedAbsences()

	// Стартовий індекс ротації в повному пулі (алфавітний порядок on-call).
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
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		var primary, backup string
		if first {
			primary = strings.TrimSpace(currPrimary)
			backup = strings.TrimSpace(currBackup)
			if primary == "" || isAbsentOnDate(primary, dateStr, abs) {
				primary, pNext := nextOncallFrom(pool, rot, dateStr, abs, nil)
				if primary == "" {
					continue
				}
				backup, _ = nextOncallFrom(pool, pNext, dateStr, abs, map[string]bool{primary: true})
				if backup == "" {
					backup = primary
				}
				rot = pNext
			} else {
				// Зафіксувати поточну пару; backup — наступний у пулі після primary, якщо не заданий/absent
				pIdx := indexInPool(pool, primary)
				if pIdx < 0 {
					pIdx = rot
				}
				if backup == "" || backup == primary || isAbsentOnDate(backup, dateStr, abs) {
					backup, _ = nextOncallFrom(pool, (pIdx+1)%len(pool), dateStr, abs, map[string]bool{primary: true})
					if backup == "" {
						backup = primary
					}
				}
				rot = (pIdx + 1) % len(pool)
			}
			first = false
		} else {
			// primary = наступний у ротації; backup = наступний після нього (не «перший алфавітно»)
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
		db.Exec(`INSERT INTO shifts (date, primary_user, backup_user) VALUES (?,?,?)
			ON CONFLICT(date) DO UPDATE SET primary_user=excluded.primary_user, backup_user=excluded.backup_user`,
			dateStr, primary, backup)
		n++
	}
	_ = prevBackup
	return n, nil
}

// handleAdminShifts — GET month / PUT bulk days / POST recalculate
func handleAdminShifts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		month := r.URL.Query().Get("month") // YYYY-MM
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
		// bulk correct specific days (past or future — admin explicit edit)
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
		// recalculate forward from seed pairs
		var body struct {
			Actor            string `json:"actor"`
			FromDate         string `json:"from_date"`
			UntilDate        string `json:"until_date"`
			CurrentPrimary   string `json:"current_primary"`
			CurrentBackup    string `json:"current_backup"`
			PreviousPrimary  string `json:"previous_primary"`
			PreviousBackup   string `json:"previous_backup"`
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
