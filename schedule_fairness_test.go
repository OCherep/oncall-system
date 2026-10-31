package main

import (
	"sort"
	"testing"
	"time"
)

func spread(m map[string]int, pool []string) (int, int) {
	lo, hi := -1, 0
	for _, n := range pool {
		v := m[n]
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
		c.prim[n], c.bak[n], c.wkd[n] = 20, 20, 6
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
	_ = time.Now
	_ = dates
}
