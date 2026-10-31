package main

import (
	"log"
	"strings"
	"time"
)

// planParams is the pure input of the shift planner (no DB access inside planRange).
type planParams struct {
	pool        []string
	abs         []AbsenceRequest
	cal         dutyCal
	rot         int              // index in pool of the next weekday primary
	prev        Shift            // shift of the day before `from` (for weekend/weekday separation)
	prevSpecial bool             // whether that day was a weekend/holiday
	wPrim       map[string]int   // weekend/holiday primary counters before `from`
	wBak        map[string]int   // weekend/holiday backup counters before `from`
	keep        map[string]Shift // dates that must not be changed (still advance rotation)
}

// planRange plans shifts for [from, until] (inclusive, YYYY-MM-DD).
// It returns the planned shifts for non-kept dates and the dates nobody could cover.
func planRange(from, until string, p planParams) (map[string]Shift, []string) {
	out := map[string]Shift{}
	var empty []string
	start, err1 := time.Parse("2006-01-02", from)
	end, err2 := time.Parse("2006-01-02", until)
	if err1 != nil || err2 != nil || end.Before(start) {
		return out, nil
	}
	if p.wPrim == nil {
		p.wPrim = map[string]int{}
	}
	if p.wBak == nil {
		p.wBak = map[string]int{}
	}
	pool := p.pool
	rot := p.rot
	prevPrimary, prevSpecial := p.prev.PrimaryUser, p.prevSpecial
	if len(pool) > 0 {
		rot = ((rot % len(pool)) + len(pool)) % len(pool)
	}
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		special := p.cal.isSpecial(dateStr)
		if k, ok := p.keep[dateStr]; ok {
			if special {
				p.wPrim[k.PrimaryUser]++
				p.wBak[k.BackupUser]++
			} else if i := indexInPool(pool, k.PrimaryUser); i >= 0 {
				rot = (i + 1) % len(pool)
			}
			prevPrimary, prevSpecial = k.PrimaryUser, special
			continue
		}
		avail := availableOnDate(pool, dateStr, p.abs)
		if len(avail) == 0 {
			empty = append(empty, dateStr)
			prevPrimary, prevSpecial = "", special
			continue
		}
		// Item 3: the same person should not be primary on a weekend/holiday and the
		// adjacent weekday, when the roster has an alternative.
		var avoid map[string]bool
		if prevPrimary != "" && prevSpecial != special {
			avoid = map[string]bool{prevPrimary: true}
		}
		var primary, backup string
		if special {
			primary = pickLeastLoaded(avail, p.wPrim, avoid)
			if primary == "" {
				primary = pickLeastLoaded(avail, p.wPrim, nil)
			}
			backup = pickLeastLoaded(avail, p.wBak, map[string]bool{primary: true})
			if backup == "" {
				backup = primary
			}
			p.wPrim[primary]++
			p.wBak[backup]++
		} else {
			var pNext int
			primary, pNext = nextOncallFrom(pool, rot, dateStr, p.abs, avoid)
			if primary == "" {
				primary, pNext = nextOncallFrom(pool, rot, dateStr, p.abs, nil)
			}
			if primary == "" {
				empty = append(empty, dateStr)
				continue
			}
			backup, _ = nextOncallFrom(pool, pNext, dateStr, p.abs, map[string]bool{primary: true})
			if backup == "" {
				backup = primary
			}
			rot = pNext
		}
		prevPrimary, prevSpecial = primary, special
		out[dateStr] = Shift{Date: dateStr, PrimaryUser: primary, BackupUser: backup}
	}
	return out, empty
}

// defaultPlanUntil — end of next month relative to the Kyiv "today".
func defaultPlanUntil() string {
	n := kyivNow()
	return time.Date(n.Year(), n.Month()+2, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func loadShiftsRange(from, until string) map[string]Shift {
	out := map[string]Shift{}
	rows, err := db.Query(`SELECT date, COALESCE(primary_user,''), COALESCE(backup_user,'') FROM shifts WHERE date>=? AND date<=?`, from, until)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var s Shift
		rows.Scan(&s.Date, &s.PrimaryUser, &s.BackupUser)
		out[s.Date] = s
	}
	return out
}

// planShifts is the only writer of auto-generated shifts.
// Days before the Kyiv "today" are never touched. fillOnly=true only fills
// missing dates; otherwise future auto days are re-planned.
func planShifts(from, until string, fillOnly bool, reason string) (int, error) {
	today := kyivToday()
	from = strings.TrimSpace(from)
	if from == "" || from < today {
		from = today
	}
	if strings.TrimSpace(until) == "" {
		until = defaultPlanUntil()
	}
	if until < from {
		return 0, nil
	}
	pool := listOncallNames()
	abs := loadApprovedAbsences()
	cal := loadDutyCal()
	wPrim, wBak := loadWeekendCountsBefore(from)
	existing := loadShiftsRange(from, until)
	keep := map[string]Shift{}
	if fillOnly {
		keep = existing
	}
	rot, prev := rotationSeedBefore(from, pool, cal)
	prevDay, _ := time.Parse("2006-01-02", from)
	planned, empty := planRange(from, until, planParams{
		pool: pool, abs: abs, cal: cal, rot: rot,
		prev: prev, prevSpecial: cal.isSpecial(prevDay.AddDate(0, 0, -1).Format("2006-01-02")),
		wPrim: wPrim, wBak: wBak, keep: keep,
	})
	n := 0
	for date := range existing {
		if _, kept := keep[date]; kept {
			continue
		}
		if _, ok := planned[date]; !ok {
			db.Exec(`DELETE FROM shifts WHERE date=?`, date)
		}
	}
	for _, s := range planned {
		if _, err := db.Exec(`INSERT INTO shifts (date, primary_user, backup_user) VALUES (?,?,?)
			ON CONFLICT(date) DO UPDATE SET primary_user=excluded.primary_user, backup_user=excluded.backup_user`,
			s.Date, s.PrimaryUser, s.BackupUser); err != nil {
			return n, err
		}
		n++
	}
	_ = empty
	log.Printf("shifts plan (%s): %s..%s fill_only=%v written=%d", reason, from, until, fillOnly, n)
	return n, nil
}

// rotationSeedBefore continues the weekday rotation from the last weekday primary
// stored before `from` (also across month boundaries) instead of restarting at 0.
// It also returns the shift of the day right before `from`.
func rotationSeedBefore(from string, pool []string, cal dutyCal) (int, Shift) {
	var prev Shift
	if t, err := time.Parse("2006-01-02", from); err == nil {
		d := t.AddDate(0, 0, -1).Format("2006-01-02")
		db.QueryRow(`SELECT date, COALESCE(primary_user,''), COALESCE(backup_user,'') FROM shifts WHERE date=?`, d).
			Scan(&prev.Date, &prev.PrimaryUser, &prev.BackupUser)
	}
	if len(pool) == 0 {
		return 0, prev
	}
	rows, err := db.Query(`SELECT date, COALESCE(primary_user,'') FROM shifts WHERE date < ? ORDER BY date DESC LIMIT 400`, from)
	if err != nil {
		return 0, prev
	}
	type row struct{ d, p string }
	var list []row
	for rows.Next() {
		var r row
		rows.Scan(&r.d, &r.p)
		list = append(list, r)
	}
	rows.Close() // close before any other query (MaxOpenConns=1)
	for _, r := range list {
		if cal.isSpecial(r.d) {
			continue
		}
		if i := indexInPool(pool, r.p); i >= 0 {
			return (i + 1) % len(pool), prev
		}
	}
	return 0, prev
}

// regenerateFutureShifts re-plans auto shifts from the Kyiv today onward.
// Called on roster changes and explicit admin action — never from GET handlers.
func regenerateFutureShifts(reason string) (int, error) {
	return planShifts(kyivToday(), "", false, reason)
}

// ensureShiftHorizon fills missing future days (today..end of next month) without
// changing existing rows. Runs at startup and periodically, not on GET.
func ensureShiftHorizon() {
	if _, err := planShifts(kyivToday(), "", true, "horizon"); err != nil {
		log.Printf("shifts horizon: %v", err)
	}
}

func shiftHorizonLoop() {
	ensureShiftHorizon()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		ensureShiftHorizon()
	}
}

// regenIfRosterChanged re-plans future days when the on-call pool changed.
func regenIfRosterChanged(before []string, reason string) {
	after := listOncallNames()
	if strings.Join(before, "\x00") == strings.Join(after, "\x00") {
		return
	}
	if _, err := regenerateFutureShifts(reason); err != nil {
		log.Printf("shifts regen (%s): %v", reason, err)
	}
}
