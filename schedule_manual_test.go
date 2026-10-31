package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func fakeTeam(t *testing.T) *[]string {
	t.Helper()
	var msgs []string
	old := teamNotifier
	teamNotifier = func(s string) { msgs = append(msgs, s) }
	emptyDayWarned.Lock()
	emptyDayWarned.m = map[string]bool{}
	emptyDayWarned.Unlock()
	t.Cleanup(func() { teamNotifier = old })
	return &msgs
}

// Finding 6: a manual edit (PUT /api/admin/shifts) survives regeneration.
func TestManualEditSurvivesRegeneration(t *testing.T) {
	setupTestDB(t)
	fakeTeam(t)
	addOncall(t, "Anna", "Bohdan", "Dmytro")
	setNow(t, "2026-11-10T10:00:00Z")
	if _, err := regenerateFutureShifts("init"); err != nil {
		t.Fatal(err)
	}
	body := `{"actor":"admin","days":[{"date":"2026-11-18","primary":"Dmytro","backup":"Anna"}]}`
	rec := httptest.NewRecorder()
	handleAdminShifts(rec, httptest.NewRequest(http.MethodPut, "/api/admin/shifts", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("PUT %d %s", rec.Code, rec.Body.String())
	}
	roster := listOncallNames()
	addOncall(t, "Yarema") // roster change -> regeneration
	regenIfRosterChanged(roster, "test")
	if p, b := shiftOn(t, "2026-11-18"); p != "Dmytro" || b != "Anna" {
		t.Fatalf("manual edit lost: %s/%s", p, b)
	}
	if _, err := recalculateShiftsForward("2026-11-10", "2026-11-30", "Anna", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := shiftOn(t, "2026-11-18"); p != "Dmytro" {
		t.Fatalf("manual edit lost after recalc: %s", p)
	}
	// manual:false returns the day to auto planning
	body = `{"days":[{"date":"2026-11-18","primary":"Dmytro","manual":false}]}`
	handleAdminShifts(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/api/admin/shifts", strings.NewReader(body)))
	if m := loadManualDates("2026-11-18", "2026-11-18"); m["2026-11-18"] {
		t.Fatal("manual flag not cleared")
	}
}

// Finding 6: an approved absence re-plans future days, past days untouched.
func TestApprovedAbsenceRecalculatesFuture(t *testing.T) {
	setupTestDB(t)
	fakeTeam(t)
	addOncall(t, "Anna", "Bohdan", "Dmytro")
	putShift(t, "2026-11-05", "Anna", "Bohdan") // past
	setNow(t, "2026-11-10T10:00:00Z")
	if _, err := regenerateFutureShifts("init"); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO absences (user_name, type, start_date, end_date, status) VALUES ('Anna','Відпустка','2026-11-01','2026-11-20','Pending')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	body := `{"id":` + itoa(int(id)) + `,"status":"Approved"}`
	rec := httptest.NewRecorder()
	handleAdminRequests(rec, httptest.NewRequest(http.MethodPut, "/api/admin/requests", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("approve %d %s", rec.Code, rec.Body.String())
	}
	for _, d := range []string{"2026-11-10", "2026-11-11", "2026-11-14", "2026-11-20"} {
		p, b := shiftOn(t, d)
		if p == "Anna" || b == "Anna" || p == "" {
			t.Errorf("%s still has Anna or empty: %s/%s", d, p, b)
		}
	}
	if p, _ := shiftOn(t, "2026-11-05"); p != "Anna" {
		t.Error("past day changed by absence")
	}
}

// Finding 6: a day without anyone on duty is logged and announced via notifyTeam.
func TestEmptyDayWarning(t *testing.T) {
	setupTestDB(t)
	msgs := fakeTeam(t)
	addOncall(t, "Solo")
	db.Exec(`INSERT INTO absences (user_name, type, start_date, end_date, status) VALUES ('Solo','Відпустка','2026-11-12','2026-11-13','Approved')`)
	setNow(t, "2026-11-10T10:00:00Z")
	if _, err := regenerateFutureShifts("test"); err != nil {
		t.Fatal(err)
	}
	if p, _ := shiftOn(t, "2026-11-12"); p != "" {
		t.Fatalf("absent solo scheduled: %s", p)
	}
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "2026-11-12, 2026-11-13") {
		t.Fatalf("team msgs=%v", *msgs)
	}
	ensureShiftHorizon() // hourly loop must not spam
	if len(*msgs) != 1 {
		t.Fatalf("duplicate warning: %v", *msgs)
	}
}

func TestEmptyRosterWarnsAndWritesNothing(t *testing.T) {
	setupTestDB(t)
	msgs := fakeTeam(t)
	setNow(t, "2026-11-10T10:00:00Z")
	n, err := regenerateFutureShifts("test")
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if len(*msgs) != 1 {
		t.Fatalf("want one warning, got %v", *msgs)
	}
}

// Migration is additive and idempotent on a pre-existing DB without is_manual.
func TestManualColumnMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	old.Exec(`CREATE TABLE shifts (date TEXT PRIMARY KEY, primary_user TEXT, backup_user TEXT)`)
	old.Exec(`INSERT INTO shifts VALUES ('2026-10-01','Anna','Bohdan')`)
	old.Close()
	if db != nil {
		db.Close()
	}
	os.Setenv("DB_PATH", path)
	initDB()
	initDB() // second start: ALTER fails harmlessly
	t.Cleanup(func() { db.Close() })
	var p string
	var m int
	if err := db.QueryRow(`SELECT primary_user, is_manual FROM shifts WHERE date='2026-10-01'`).Scan(&p, &m); err != nil {
		t.Fatal(err)
	}
	if p != "Anna" || m != 0 {
		t.Fatalf("got %s/%d", p, m)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
