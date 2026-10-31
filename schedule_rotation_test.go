package main

import (
	"testing"
	"time"
)

func emptyCal() dutyCal {
	return dutyCal{holidays: map[string]bool{}, off: map[string]bool{}, on: map[string]bool{}}
}

// Item 3 / rule 4: planning Oct and Nov separately (month boundary 31.10 -> 01.11)
// gives exactly the same result as planning both at once.
func TestRotationContinuesAcrossMonth(t *testing.T) {
	setupTestDB(t)
	fakeTeam(t)
	addOncall(t, "Anna", "Bohdan", "Dmytro", "Olena")
	setNow(t, "2026-09-30T21:30:00Z") // 01.10 00:30 Kyiv
	if _, err := planShifts("2026-10-01", "2026-11-30", false, "both"); err != nil {
		t.Fatal(err)
	}
	together := loadShiftsRange("2026-10-01", "2026-11-30")
	db.Exec(`DELETE FROM shifts`)
	if _, err := planShifts("2026-10-01", "2026-10-31", false, "oct"); err != nil {
		t.Fatal(err)
	}
	setNow(t, "2026-10-31T22:30:00Z") // 01.11 00:30 Kyiv
	if _, err := planShifts("2026-11-01", "2026-11-30", false, "nov"); err != nil {
		t.Fatal(err)
	}
	split := loadShiftsRange("2026-10-01", "2026-11-30")
	if len(split) != len(together) {
		t.Fatalf("days %d vs %d", len(split), len(together))
	}
	for d, s := range together {
		if split[d] != s {
			t.Errorf("%s: split %+v, together %+v", d, split[d], s)
		}
	}
}

// Weekend primary differs from adjacent weekday primary.
func TestWeekendNotAdjacentToSameWeekdayPrimary(t *testing.T) {
	pool := []string{"Anna", "Bohdan", "Dmytro"}
	res := planRange("2026-10-01", "2026-12-31", planParams{pool: pool, cal: emptyCal()})
	if len(res.empty) != 0 {
		t.Fatalf("unexpected empty days %v", res.empty)
	}
	got := res.shifts
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
	res := planRange("2026-11-01", "2026-11-30", planParams{pool: []string{"Solo"}, cal: emptyCal()})
	if len(res.shifts) != 30 || len(res.empty) != 0 {
		t.Fatalf("got %d days, empty %v", len(res.shifts), res.empty)
	}
	for d, s := range res.shifts {
		if s.PrimaryUser != "Solo" || s.BackupUser != "Solo" {
			t.Errorf("%s: %+v", d, s)
		}
	}
}

func TestPlanEmptyRoster(t *testing.T) {
	res := planRange("2026-11-01", "2026-11-30", planParams{cal: emptyCal()})
	if len(res.shifts) != 0 || len(res.empty) != 30 {
		t.Fatalf("got %d shifts, %d empty days", len(res.shifts), len(res.empty))
	}
}

// DST switch day 25.10.2026 (Sunday, 25h long) is planned exactly once.
func TestPlanAcrossDSTSwitch(t *testing.T) {
	res := planRange("2026-10-24", "2026-10-27", planParams{pool: []string{"Anna", "Bohdan"}, cal: emptyCal()})
	for _, d := range []string{"2026-10-24", "2026-10-25", "2026-10-26", "2026-10-27"} {
		if res.shifts[d].PrimaryUser == "" {
			t.Errorf("%s not planned", d)
		}
	}
	if len(res.shifts) != 4 {
		t.Errorf("planned %d days, want 4", len(res.shifts))
	}
}
