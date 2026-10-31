package main

import (
	"testing"
	"time"
)

func emptyCal() dutyCal {
	return dutyCal{holidays: map[string]bool{}, off: map[string]bool{}, on: map[string]bool{}}
}

// Finding 3: rotation continues across the month boundary 31.10 -> 01.11.
func TestRotationContinuesAcrossMonth(t *testing.T) {
	setupTestDB(t)
	addOncall(t, "Anna", "Bohdan", "Dmytro", "Olena")
	setNow(t, "2026-09-30T21:30:00Z") // 01.10 00:30 Kyiv
	if _, err := planShifts("2026-10-01", "2026-10-31", false, "oct"); err != nil {
		t.Fatal(err)
	}
	lastOct, _ := shiftOn(t, "2026-10-30") // Friday
	setNow(t, "2026-10-31T22:30:00Z")      // 01.11 00:30 Kyiv
	if _, err := regenerateFutureShifts("nov"); err != nil {
		t.Fatal(err)
	}
	pool := []string{"Anna", "Bohdan", "Dmytro", "Olena"}
	want := pool[(indexInPool(pool, lastOct)+1)%4]
	if sun, _ := shiftOn(t, "2026-11-01"); sun == want {
		t.Fatalf("test setup: Sunday primary %s collides with expected Monday", sun)
	}
	firstNov, _ := shiftOn(t, "2026-11-02") // Monday
	if firstNov != want {
		t.Fatalf("30.10 primary=%s, 02.11 primary=%s, want %s (rotation restarted)", lastOct, firstNov, want)
	}
	if p, _ := shiftOn(t, "2026-10-30"); p != lastOct {
		t.Fatal("October changed")
	}
}

// Finding 3: weekend primary differs from adjacent weekday primary when possible.
func TestWeekendNotAdjacentToSameWeekdayPrimary(t *testing.T) {
	pool := []string{"Anna", "Bohdan", "Dmytro"}
	got, empty := planRange("2026-10-01", "2026-12-31", planParams{pool: pool, cal: emptyCal()})
	if len(empty) != 0 {
		t.Fatalf("unexpected empty days %v", empty)
	}
	start, _ := time.Parse("2006-01-02", "2026-10-01")
	for d := start; d.Before(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
		a, b := d.Format("2006-01-02"), d.AddDate(0, 0, 1).Format("2006-01-02")
		if isWeekendDate(a) == isWeekendDate(b) {
			continue
		}
		if got[a].PrimaryUser == got[b].PrimaryUser {
			t.Errorf("%s and %s both primary=%s", a, b, got[a].PrimaryUser)
		}
	}
}

func TestPlanOnePerson(t *testing.T) {
	got, empty := planRange("2026-11-01", "2026-11-30", planParams{pool: []string{"Solo"}, cal: emptyCal()})
	if len(got) != 30 || len(empty) != 0 {
		t.Fatalf("got %d days, empty %v", len(got), empty)
	}
	for d, s := range got {
		if s.PrimaryUser != "Solo" || s.BackupUser != "Solo" {
			t.Errorf("%s: %+v", d, s)
		}
	}
}

func TestPlanEmptyRoster(t *testing.T) {
	got, empty := planRange("2026-11-01", "2026-11-30", planParams{cal: emptyCal()})
	if len(got) != 0 || len(empty) != 30 {
		t.Fatalf("got %d shifts, %d empty days", len(got), len(empty))
	}
}

// DST switch day 25.10.2026 (Sunday, 25h long) is planned exactly once and
// the date sequence has no gaps/duplicates.
func TestPlanAcrossDSTSwitch(t *testing.T) {
	got, _ := planRange("2026-10-24", "2026-10-27", planParams{pool: []string{"Anna", "Bohdan"}, cal: emptyCal()})
	for _, d := range []string{"2026-10-24", "2026-10-25", "2026-10-26", "2026-10-27"} {
		if got[d].PrimaryUser == "" {
			t.Errorf("%s not planned", d)
		}
	}
	if len(got) != 4 {
		t.Errorf("planned %d days, want 4", len(got))
	}
}
