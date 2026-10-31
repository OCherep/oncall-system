package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func snapshotShifts(t *testing.T) map[string]Shift {
	t.Helper()
	return loadShiftsRange("0000-00-00", "9999-99-99")
}

// Finding 2: adding a person on 20.11 must not change 01..19.11.
func TestPastDaysUnchangedAfterAddingPersonOn20Nov(t *testing.T) {
	setupTestDB(t)
	addOncall(t, "Anna", "Bohdan", "Dmytro")
	setNow(t, "2026-10-31T22:30:00Z") // 01.11 00:30 Kyiv
	if _, err := regenerateFutureShifts("test"); err != nil {
		t.Fatal(err)
	}
	before := snapshotShifts(t)
	if len(before) != 61 { // 01.11..31.12
		t.Fatalf("planned %d days, want 61", len(before))
	}
	setNow(t, "2026-11-20T08:00:00Z")
	roster := listOncallNames()
	addOncall(t, "Yarema")
	regenIfRosterChanged(roster, "test add")
	after := snapshotShifts(t)
	for d, s := range before {
		if d < "2026-11-20" && after[d] != s {
			t.Errorf("past day %s changed: %+v -> %+v", d, s, after[d])
		}
	}
	seen := false
	for d, s := range after {
		if d >= "2026-11-20" && (s.PrimaryUser == "Yarema" || s.BackupUser == "Yarema") {
			seen = true
		}
	}
	if !seen {
		t.Error("new person not scheduled in future days")
	}
}

// Finding 2: GET /api/data must not write shifts, even when the month is empty
// or the roster differs from the stored shifts.
func TestGetDataDoesNotRegenerate(t *testing.T) {
	setupTestDB(t)
	addOncall(t, "Anna", "Bohdan")
	setNow(t, "2026-11-10T10:00:00Z")
	putShift(t, "2026-11-05", "Ghost", "Ghost") // not in roster: old code regenerated the month
	req := httptest.NewRequest(http.MethodGet, "/api/data?year=2026&month=11", nil)
	rec := httptest.NewRecorder()
	handleGetData(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := snapshotShifts(t)
	if len(got) != 1 || got["2026-11-05"].PrimaryUser != "Ghost" {
		t.Fatalf("GET modified shifts: %+v", got)
	}
	// empty month: still nothing written
	req = httptest.NewRequest(http.MethodGet, "/api/data?year=2026&month=12", nil)
	handleGetData(httptest.NewRecorder(), req)
	if n := len(snapshotShifts(t)); n != 1 {
		t.Fatalf("GET on empty month wrote %d rows", n)
	}
}

func TestAdminRegenerateKeepsHistory(t *testing.T) {
	setupTestDB(t)
	addOncall(t, "Anna", "Bohdan")
	putShift(t, "2026-11-02", "Old", "Old")
	setNow(t, "2026-11-10T10:00:00Z")
	// regenerate-shifts is password-protected; call the planner it delegates to.
	if _, err := regenerateFutureShifts("admin regenerate"); err != nil {
		t.Fatal(err)
	}
	if p, _ := shiftOn(t, "2026-11-02"); p != "Old" {
		t.Fatalf("history rewritten: %s", p)
	}
	if p, _ := shiftOn(t, "2026-11-10"); p == "" {
		t.Fatal("today not planned")
	}
}

func TestRecalcForwardClampsToToday(t *testing.T) {
	setupTestDB(t)
	addOncall(t, "Anna", "Bohdan")
	putShift(t, "2026-11-02", "Old", "Old")
	setNow(t, "2026-11-10T10:00:00Z")
	if _, err := recalculateShiftsForward("2026-11-01", "2026-11-30", "Anna", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := shiftOn(t, "2026-11-02"); p != "Old" {
		t.Fatalf("past day rewritten by recalc: %s", p)
	}
}

func TestHorizonFillsOnlyMissing(t *testing.T) {
	setupTestDB(t)
	addOncall(t, "Anna", "Bohdan")
	setNow(t, "2026-11-10T10:00:00Z")
	putShift(t, "2026-11-12", "Bohdan", "Anna")
	ensureShiftHorizon()
	if p, _ := shiftOn(t, "2026-11-12"); p != "Bohdan" {
		t.Fatalf("existing row overwritten: %s", p)
	}
	if p, _ := shiftOn(t, "2026-11-09"); p != "" {
		t.Fatalf("past day filled: %s", p)
	}
	if p, _ := shiftOn(t, "2026-12-31"); p == "" {
		t.Fatal("horizon not filled to end of next month")
	}
}
