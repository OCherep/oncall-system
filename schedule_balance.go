package main

import (
	"log"
	"strings"
	"sync"
)

// Fairness rule 3: absences on a duty day, debt and credit.

var sickTypeCache struct {
	sync.Mutex
	names map[string]bool
}

// isSickType: absence type is sickness (absence_types.code='sick', e.g.
// «Лікарняний»). absences.type may hold either the name or the code.
func isSickType(t string) bool {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return false
	}
	if t == "sick" || strings.Contains(t, "лікарн") || strings.Contains(t, "хвор") {
		return true
	}
	sickTypeCache.Lock()
	defer sickTypeCache.Unlock()
	if sickTypeCache.names == nil && db != nil {
		m := map[string]bool{}
		rows, err := db.Query(`SELECT LOWER(COALESCE(name,'')), LOWER(COALESCE(code,'')) FROM absence_types`)
		if err == nil {
			for rows.Next() {
				var n, c string
				rows.Scan(&n, &c)
				if c == "sick" {
					m[strings.TrimSpace(n)] = true
				}
			}
			rows.Close()
		}
		sickTypeCache.names = m
	}
	return sickTypeCache.names[t]
}

func absenceOnDate(userName, dateStr string, absences []AbsenceRequest) (AbsenceRequest, bool) {
	for _, a := range absences {
		if a.UserName == userName && dateStr >= a.StartDate && dateStr <= a.EndDate {
			return a, true
		}
	}
	return AbsenceRequest{}, false
}

func isSickOnDate(userName, dateStr string, absences []AbsenceRequest) bool {
	a, ok := absenceOnDate(userName, dateStr, absences)
	return ok && isSickType(a.Type)
}

func ensureDutyBalanceTable() {
	db.Exec(`CREATE TABLE IF NOT EXISTS duty_balance (
		date TEXT NOT NULL,
		role TEXT NOT NULL,
		absent_user TEXT NOT NULL,
		replacement TEXT DEFAULT '',
		absence_type TEXT DEFAULT '',
		is_sick INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (date, role)
	)`)
}

// recordReplacements writes the duty_balance ledger for re-planned days: when the
// previously assigned person is absent, a row (debt for absent unless sick,
// credit for the replacement) is stored. Rows whose absence was withdrawn are removed.
func recordReplacements(old, planned map[string]Shift, keep map[string]Shift, abs []AbsenceRequest) {
	type row struct {
		date, role, absent, repl, typ string
		sick                          int
	}
	var ins []row
	for date, o := range old {
		if _, kept := keep[date]; kept {
			continue
		}
		n := planned[date]
		for _, r := range []struct{ role, was, now string }{
			{rolePrimary, o.PrimaryUser, n.PrimaryUser},
			{roleBackup, o.BackupUser, n.BackupUser},
		} {
			if r.was == "" || r.was == r.now {
				continue
			}
			a, ok := absenceOnDate(r.was, date, abs)
			if !ok {
				continue
			}
			sick := 0
			if isSickType(a.Type) {
				sick = 1
			}
			ins = append(ins, row{date, r.role, r.was, r.now, a.Type, sick})
		}
	}
	for _, r := range ins {
		if _, err := db.Exec(`INSERT INTO duty_balance (date, role, absent_user, replacement, absence_type, is_sick)
			VALUES (?,?,?,?,?,?) ON CONFLICT(date, role) DO UPDATE SET
			absent_user=excluded.absent_user, replacement=excluded.replacement,
			absence_type=excluded.absence_type, is_sick=excluded.is_sick`,
			r.date, r.role, r.absent, r.repl, r.typ, r.sick); err != nil {
			log.Printf("duty_balance: %v", err)
		}
	}
	// drop ledger rows whose absence no longer exists (withdrawn/rejected)
	type key struct{ date, role, user string }
	var stale []key
	rows, err := db.Query(`SELECT date, role, absent_user FROM duty_balance`)
	if err == nil {
		for rows.Next() {
			var k key
			rows.Scan(&k.date, &k.role, &k.user)
			if _, kept := keep[k.date]; kept {
				continue
			}
			if _, inRange := planned[k.date]; !inRange {
				if _, wasOld := old[k.date]; !wasOld {
					continue
				}
			}
			if _, ok := absenceOnDate(k.user, k.date, abs); !ok {
				stale = append(stale, k)
			}
		}
		rows.Close()
	}
	for _, k := range stale {
		db.Exec(`DELETE FROM duty_balance WHERE date=? AND role=?`, k.date, k.role)
	}
}

// dutyBalance returns credit minus debt per person from the ledger:
// replacement +1 (credit), absent -1 (debt) unless the absence was sickness.
func dutyBalance() map[string]int {
	out := map[string]int{}
	rows, err := db.Query(`SELECT absent_user, COALESCE(replacement,''), is_sick FROM duty_balance`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a, r string
		var sick int
		rows.Scan(&a, &r, &sick)
		if sick == 0 {
			out[a]--
		}
		if r != "" {
			out[r]++
		}
	}
	return out
}
