package main

import (
	"sort"
	"testing"
	"time"
)

func spread(m map[string]int, pool []string) (int, int) {
	lo, hi := -1, 0
	for _, n := range pool {
		v := m[n] / dutyUnit
		if lo < 0 || v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

func countShifts(shifts map[string]Shift, cal dutyCal) dutyCounts {
	c := newDutyCounts()
	for d, s := range shifts {
		c.add(s, cal.isSpecial(d))
	}
	return c
}

func dates(shifts map[string]Shift) []string {
	var ds []string
	for d := range shifts {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	return ds
}

// Rule 4: over 3 months on 4 and 5 people, max-min <= 2 for primary, backup, weekend.
func TestFairnessThreeMonths(t *testing.T) {
	for _, pool := range [][]string{
		{"Anna", "Bohdan", "Dmytro", "Olena"},
		{"Anna", "Bohdan", "Dmytro", "Olena", "Yarema"},
	} {
		cal := emptyCal()
		cal.holidays["2026-12-25"] = true
		res := planRange("2026-10-01", "2026-12-31", planParams{pool: pool, cal: cal})
		c := countShifts(res.shifts, cal)
		for name, m := range map[string]map[string]int{"primary": c.prim, "backup": c.bak, "weekend": c.wkd} {
			lo, hi := spread(m, pool)
			if hi-lo > 2 {
				t.Errorf("pool=%d %s spread %d..%d: %v", len(pool), name, lo, hi, m)
			}
		}
		if len(res.warnings) != 0 {
			t.Errorf("pool=%d unexpected warnings %v", len(pool), res.warnings)
		}
	}
}

// Greedy planner is deterministic.
func TestPlannerDeterministic(t *testing.T) {
	pool := []string{"Olena", "Anna", "Dmytro", "Bohdan"}
	a := planRange("2026-11-01", "2026-12-31", planParams{pool: pool, cal: emptyCal()})
	b := planRange("2026-11-01", "2026-12-31", planParams{pool: pool, cal: emptyCal()})
	for d := range a.shifts {
		if a.shifts[d] != b.shifts[d] {
			t.Fatalf("%s differs", d)
		}
	}
}

// A newcomer does not get scheduled every day to catch up.
func TestNewcomerNormalized(t *testing.T) {
	pool := []string{"Anna", "Bohdan", "Dmytro", "Yarema"}
	c := newDutyCounts()
	for _, n := range pool[:3] {
		c.prim[n], c.bak[n], c.wkd[n] = 20*dutyUnit, 20*dutyUnit, 6*dutyUnit
	}
	res := planRange("2026-11-01", "2026-11-14", planParams{pool: pool, cal: emptyCal(), counts: c})
	n := 0
	for _, s := range res.shifts {
		if s.PrimaryUser == "Yarema" {
			n++
		}
	}
	if n > 5 {
		t.Fatalf("newcomer primary %d of 14 days", n)
	}
}

func assertNoConsecutivePrimary(t *testing.T, shifts map[string]Shift) {
	t.Helper()
	ds := dates(shifts)
	for i := 1; i < len(ds); i++ {
		a, _ := time.Parse("2006-01-02", ds[i-1])
		if a.AddDate(0, 0, 1).Format("2006-01-02") != ds[i] {
			continue
		}
		if shifts[ds[i-1]].PrimaryUser == shifts[ds[i]].PrimaryUser {
			t.Errorf("%s and %s: primary %s twice in a row", ds[i-1], ds[i], shifts[ds[i]].PrimaryUser)
		}
	}
}

// Rule 1: nobody is primary two days in a row (2..5 people, with absences).
func TestNoPrimaryTwoDaysInARow(t *testing.T) {
	abs := []AbsenceRequest{{UserName: "Anna", StartDate: "2026-11-09", EndDate: "2026-11-20", Status: "Approved"}}
	for _, pool := range [][]string{
		{"Anna", "Bohdan"},
		{"Anna", "Bohdan", "Dmytro"},
		{"Anna", "Bohdan", "Dmytro", "Olena", "Yarema"},
	} {
		res := planRange("2026-10-01", "2026-12-31", planParams{pool: pool, abs: abs, cal: emptyCal()})
		if len(pool) == 2 {
			// Anna away -> Bohdan alone 9..20.11: best possible + warnings
			for d := range res.shifts {
				if d >= "2026-11-10" && d <= "2026-11-20" {
					delete(res.shifts, d)
				}
			}
			if len(res.warnings) == 0 {
				t.Error("2 people with one away: expected warnings")
			}
		}
		assertNoConsecutivePrimary(t, res.shifts)
	}
}

// One person: rule 1 is infeasible -> still planned, with a warning.
func TestOnePersonWarns(t *testing.T) {
	res := planRange("2026-11-01", "2026-11-07", planParams{pool: []string{"Solo"}, cal: emptyCal()})
	if len(res.shifts) != 7 {
		t.Fatalf("planned %d", len(res.shifts))
	}
	if len(res.warnings) == 0 {
		t.Fatal("no warning for 1-person roster")
	}
}

// Rule 1 also respects a kept (manual) next day.
func TestNoConsecutivePrimaryAroundManualDay(t *testing.T) {
	keep := map[string]Shift{"2026-11-04": {Date: "2026-11-04", PrimaryUser: "Anna", BackupUser: "Bohdan"}}
	res := planRange("2026-11-02", "2026-11-06", planParams{pool: []string{"Anna", "Bohdan", "Dmytro"}, cal: emptyCal(), keep: keep})
	if res.shifts["2026-11-03"].PrimaryUser == "Anna" || res.shifts["2026-11-05"].PrimaryUser == "Anna" {
		t.Fatalf("Anna adjacent to manual day: %+v", res.shifts)
	}
}

// Rule 2: adjacent weekend/holiday days never share a person in any role.
func TestWeekendDaysDifferentPeople(t *testing.T) {
	cal := emptyCal()
	cal.holidays["2026-11-02"] = true // Monday holiday next to Sunday 01.11
	cal.holidays["2026-12-25"] = true
	for _, pool := range [][]string{
		{"Anna", "Bohdan", "Dmytro", "Olena"},
		{"Anna", "Bohdan", "Dmytro", "Olena", "Yarema"},
	} {
		res := planRange("2026-10-01", "2026-12-31", planParams{pool: pool, cal: cal})
		ds := dates(res.shifts)
		for i := 1; i < len(ds); i++ {
			a, b := ds[i-1], ds[i]
			if !cal.isSpecial(a) || !cal.isSpecial(b) {
				continue
			}
			x, y := res.shifts[a], res.shifts[b]
			for _, n := range []string{x.PrimaryUser, x.BackupUser} {
				if n == y.PrimaryUser || n == y.BackupUser {
					t.Errorf("pool=%d %s/%s share %s: %+v %+v", len(pool), a, b, n, x, y)
				}
			}
		}
		if len(res.warnings) != 0 {
			t.Errorf("pool=%d warnings %v", len(pool), res.warnings)
		}
	}
}

// 2 people: Sat uses both, so Sunday is infeasible -> best possible + warning;
// rule 1 still holds.
func TestTwoPeopleInfeasibleWeekend(t *testing.T) {
	res := planRange("2026-11-01", "2026-11-30", planParams{pool: []string{"Anna", "Bohdan"}, cal: emptyCal()})
	if len(res.shifts) != 30 {
		t.Fatalf("planned %d", len(res.shifts))
	}
	if len(res.warnings) == 0 {
		t.Fatal("expected warnings for 2-person weekends")
	}
	assertNoConsecutivePrimary(t, res.shifts)
}
