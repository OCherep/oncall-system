package main

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// fairWindowDays — history window for fairness counters (plus already planned days).
const fairWindowDays = 90

// dutyCounts are per-person fairness counters (rule 4).
type dutyCounts struct {
	prim, bak, wkd map[string]int // primary, backup, weekend/holiday (any role)
}

func newDutyCounts() dutyCounts {
	return dutyCounts{prim: map[string]int{}, bak: map[string]int{}, wkd: map[string]int{}}
}

func (c dutyCounts) add(s Shift, special bool) {
	if s.PrimaryUser != "" {
		c.prim[s.PrimaryUser]++
		if special {
			c.wkd[s.PrimaryUser]++
		}
	}
	if s.BackupUser != "" && s.BackupUser != s.PrimaryUser {
		c.bak[s.BackupUser]++
		if special {
			c.wkd[s.BackupUser]++
		}
	}
}

// planParams is the pure input of the shift planner (no DB access inside planRange).
type planParams struct {
	pool        []string
	abs         []AbsenceRequest
	cal         dutyCal
	prev        Shift            // shift of the day before `from`
	prevSpecial bool             // whether that day was a weekend/holiday
	counts      dutyCounts       // fairness counters from the history window
	keep        map[string]Shift // dates that must not be changed (still counted)
}

// planResult is the planner output.
type planResult struct {
	shifts   map[string]Shift
	empty    []string // dates nobody could cover
	warnings []string // constraints relaxed because they were infeasible
}

// normalizeNewcomers: a pool member with no duties in the window starts at the
// minimum of the others, so a newcomer is not scheduled every day to "catch up".
func normalizeNewcomers(pool []string, c dutyCounts) {
	minOf := func(m map[string]int, skip map[string]bool) int {
		best := -1
		for _, n := range pool {
			if skip[n] {
				continue
			}
			if best < 0 || m[n] < best {
				best = m[n]
			}
		}
		if best < 0 {
			return 0
		}
		return best
	}
	fresh := map[string]bool{}
	for _, n := range pool {
		if c.prim[n]+c.bak[n]+c.wkd[n] == 0 {
			fresh[n] = true
		}
	}
	if len(fresh) == 0 || len(fresh) == len(pool) {
		return
	}
	mp, mb, mw := minOf(c.prim, fresh), minOf(c.bak, fresh), minOf(c.wkd, fresh)
	for n := range fresh {
		c.prim[n], c.bak[n], c.wkd[n] = mp, mb, mw
	}
}

// pickFair is the greedy choice: the candidate with the smallest key wins,
// ties broken by name (deterministic).
func pickFair(cands []string, key func(string) [2]int) string {
	best := ""
	var bk [2]int
	for _, n := range cands {
		k := key(n)
		if best == "" || k[0] < bk[0] || (k[0] == bk[0] && (k[1] < bk[1] || (k[1] == bk[1] && n < best))) {
			best, bk = n, k
		}
	}
	return best
}

// planRange plans shifts for [from, until] (inclusive, YYYY-MM-DD) with a greedy
// fairness planner (rule 4): every day each role goes to the available person
// with the smallest counter for that role in the window.
func planRange(from, until string, p planParams) planResult {
	res := planResult{shifts: map[string]Shift{}}
	start, err1 := time.Parse("2006-01-02", from)
	end, err2 := time.Parse("2006-01-02", until)
	if err1 != nil || err2 != nil || end.Before(start) {
		return res
	}
	c := p.counts
	if c.prim == nil {
		c = newDutyCounts()
	}
	normalizeNewcomers(p.pool, c)
	prev, prevSpecial := p.prev, p.prevSpecial
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		special := p.cal.isSpecial(dateStr)
		if k, ok := p.keep[dateStr]; ok {
			c.add(k, special)
			prev, prevSpecial = k, special
			continue
		}
		avail := availableOnDate(p.pool, dateStr, p.abs)
		if len(avail) == 0 {
			res.empty = append(res.empty, dateStr)
			prev, prevSpecial = Shift{}, special
			continue
		}
		next, hasNext := p.keep[d.AddDate(0, 0, 1).Format("2006-01-02")]
		nextSpecial := p.cal.isSpecial(d.AddDate(0, 0, 1).Format("2006-01-02"))
		ctx := dayCtx{date: dateStr, special: special, prev: prev, prevSpecial: prevSpecial,
			next: next, hasNext: hasNext, nextSpecial: nextSpecial, poolSize: len(p.pool)}

		primKey := func(n string) [2]int {
			if special {
				return [2]int{c.wkd[n], c.prim[n]}
			}
			return [2]int{c.prim[n], c.prim[n] + c.bak[n]}
		}
		bakKey := func(n string) [2]int {
			if special {
				return [2]int{c.wkd[n], c.bak[n]}
			}
			return [2]int{c.bak[n], c.prim[n] + c.bak[n]}
		}
		primary := pickConstrained(avail, primKey, ctx, rolePrimary, &res.warnings)
		var bcands []string
		for _, n := range avail {
			if n != primary {
				bcands = append(bcands, n)
			}
		}
		backup := ""
		if len(bcands) > 0 {
			backup = pickConstrained(bcands, bakKey, ctx, roleBackup, &res.warnings)
		}
		if backup == "" {
			backup = primary
		}
		sh := Shift{Date: dateStr, PrimaryUser: primary, BackupUser: backup}
		c.add(sh, special)
		res.shifts[dateStr] = sh
		prev, prevSpecial = sh, special
	}
	return res
}

const (
	rolePrimary = "primary"
	roleBackup  = "backup"
)

// dayCtx carries the neighbours of a day for the hard constraints.
type dayCtx struct {
	date                 string
	special, prevSpecial bool
	prev, next           Shift
	hasNext, nextSpecial bool
	poolSize             int
}

// constraint returns "" if n may take role on the day, otherwise a reason.
type constraint func(n, role string, ctx dayCtx) string

// hardConstraints are applied in order; when no candidate satisfies all of them,
// the last ones are relaxed first and a warning is recorded.
var hardConstraints = []constraint{noConsecutivePrimary}

// noConsecutivePrimary (fairness rule 1): never primary two days in a row,
// weekdays and weekends alike. Relaxed only when nobody else is available.
func noConsecutivePrimary(n, role string, ctx dayCtx) string {
	if role != rolePrimary {
		return ""
	}
	if ctx.prev.PrimaryUser == n || (ctx.hasNext && ctx.next.PrimaryUser == n) {
		return n + " primary on an adjacent day"
	}
	return ""
}

func pickConstrained(cands []string, key func(string) [2]int, ctx dayCtx, role string, warns *[]string) string {
	for k := len(hardConstraints); k >= 0; k-- {
		var ok []string
		for _, n := range cands {
			fail := ""
			for _, f := range hardConstraints[:k] {
				if r := f(n, role, ctx); r != "" {
					fail = r
					break
				}
			}
			if fail == "" {
				ok = append(ok, n)
			}
		}
		if len(ok) > 0 {
			if k < len(hardConstraints) {
				*warns = append(*warns, fmt.Sprintf("%s %s: constraint relaxed (%s), roster size %d", ctx.date, role, relaxedReason(cands, role, ctx, k), ctx.poolSize))
			}
			return pickFair(ok, key)
		}
	}
	return ""
}

func relaxedReason(cands []string, role string, ctx dayCtx, k int) string {
	var rs []string
	for _, f := range hardConstraints[k:] {
		for _, n := range cands {
			if r := f(n, role, ctx); r != "" {
				rs = append(rs, r)
				break
			}
		}
	}
	return strings.Join(rs, "; ")
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

// loadManualDates returns dates in [from, until] edited by an admin (is_manual=1).
func loadManualDates(from, until string) map[string]bool {
	out := map[string]bool{}
	rows, err := db.Query(`SELECT date FROM shifts WHERE date>=? AND date<=? AND COALESCE(is_manual,0)=1`, from, until)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		rows.Scan(&d)
		out[d] = true
	}
	return out
}

var emptyDayWarned = struct {
	sync.Mutex
	m map[string]bool
}{m: map[string]bool{}}

// warnEmptyDays (item 6) logs and notifies the team channel about future days
// without an on-call person. Each date is announced once per process.
func warnEmptyDays(days []string) {
	emptyDayWarned.Lock()
	var fresh []string
	for _, d := range days {
		if !emptyDayWarned.m[d] {
			emptyDayWarned.m[d] = true
			fresh = append(fresh, d)
		}
	}
	emptyDayWarned.Unlock()
	if len(fresh) == 0 {
		return
	}
	sort.Strings(fresh)
	log.Printf("shifts WARNING: no on-call person for %s", strings.Join(fresh, ", "))
	list := strings.Join(fresh, ", ")
	if len(fresh) > 10 {
		list = strings.Join(fresh[:10], ", ") + fmt.Sprintf(" … і ще %d", len(fresh)-10)
	}
	teamNotifier("⚠️ Графік чергувань: немає чергового на " + list + ". Перевірте ростер і відсутності.")
}

func clearEmptyDayWarnings(days map[string]Shift) {
	emptyDayWarned.Lock()
	for d := range days {
		delete(emptyDayWarned.m, d)
	}
	emptyDayWarned.Unlock()
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
	existing := loadShiftsRange(from, until)
	keep := map[string]Shift{}
	if fillOnly {
		keep = existing
	} else {
		// item 6: manual edits survive regeneration
		manual := loadManualDates(from, until)
		for d, sh := range existing {
			if manual[d] {
				keep[d] = sh
			}
		}
	}
	prev, counts := fairnessSeed(from, cal)
	prevDay, _ := time.Parse("2006-01-02", from)
	pr := planRange(from, until, planParams{
		pool: pool, abs: abs, cal: cal, counts: counts,
		prev: prev, prevSpecial: cal.isSpecial(prevDay.AddDate(0, 0, -1).Format("2006-01-02")),
		keep: keep,
	})
	planned, empty := pr.shifts, pr.empty
	for _, w := range pr.warnings {
		log.Printf("shifts WARNING: %s", w)
	}
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
		if _, err := db.Exec(`INSERT INTO shifts (date, primary_user, backup_user, is_manual) VALUES (?,?,?,0)
			ON CONFLICT(date) DO UPDATE SET primary_user=excluded.primary_user, backup_user=excluded.backup_user
			WHERE COALESCE(shifts.is_manual,0)=0`,
			s.Date, s.PrimaryUser, s.BackupUser); err != nil {
			return n, err
		}
		n++
	}
	clearEmptyDayWarnings(planned)
	clearEmptyDayWarnings(keep)
	if len(empty) > 0 {
		warnEmptyDays(empty)
	}
	log.Printf("shifts plan (%s): %s..%s fill_only=%v written=%d", reason, from, until, fillOnly, n)
	return n, nil
}

// fairnessSeed loads the shift of the day before `from` and the fairness counters
// over [from-fairWindowDays, from). Because counters come from stored history,
// fairness continues seamlessly across months (no restart at month start).
func fairnessSeed(from string, cal dutyCal) (Shift, dutyCounts) {
	var prev Shift
	c := newDutyCounts()
	t, err := time.Parse("2006-01-02", from)
	if err != nil {
		return prev, c
	}
	winStart := t.AddDate(0, 0, -fairWindowDays).Format("2006-01-02")
	yesterday := t.AddDate(0, 0, -1).Format("2006-01-02")
	hist := loadShiftsRange(winStart, yesterday) // cursor closed inside
	for d, s := range hist {
		c.add(s, cal.isSpecial(d))
	}
	prev = hist[yesterday]
	return prev, c
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
