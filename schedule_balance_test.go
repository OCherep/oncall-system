package main

import "testing"

// absenceScenario: 4 people planned for Nov–Dec on 01.11, then Anna gets an
// approved absence 09..20.11 of the given type (approved on 05.11).
// Returns Anna's December duties and the ledger balance.
func absenceScenario(t *testing.T, typ string) (annaDec int, bal map[string]int, rows int) {
	setupTestDB(t)
	fakeTeam(t)
	addOncall(t, "Anna", "Bohdan", "Dmytro", "Olena")
	setNow(t, "2026-10-31T22:30:00Z")
	if _, err := regenerateFutureShifts("init"); err != nil {
		t.Fatal(err)
	}
	setNow(t, "2026-11-05T10:00:00Z")
	if _, err := db.Exec(`INSERT INTO absences (user_name, type, start_date, end_date, status) VALUES ('Anna',?,'2026-11-09','2026-11-20','Approved')`, typ); err != nil {
		t.Fatal(err)
	}
	if _, err := regenerateFutureShifts("absence"); err != nil {
		t.Fatal(err)
	}
	for d, s := range loadShiftsRange("2026-11-09", "2026-11-20") {
		if s.PrimaryUser == "Anna" || s.BackupUser == "Anna" {
			t.Fatalf("%s: Anna still on duty during absence", d)
		}
	}
	for _, s := range loadShiftsRange("2026-12-01", "2026-12-31") {
		if s.PrimaryUser == "Anna" {
			annaDec++
		}
		if s.BackupUser == "Anna" {
			annaDec++
		}
	}
	db.QueryRow(`SELECT COUNT(*) FROM duty_balance`).Scan(&rows)
	return annaDec, dutyBalance(), rows
}

// Rule 3: a non-sick absence creates debt (ledger) and Anna works more afterwards.
func TestNonSickAbsenceCreatesDebt(t *testing.T) {
	vac, bal, rows := absenceScenario(t, "Відпустка")
	if rows == 0 || bal["Anna"] >= 0 {
		t.Fatalf("no debt recorded: rows=%d balance=%v", rows, bal)
	}
	credit := bal["Bohdan"] + bal["Dmytro"] + bal["Olena"]
	if credit != -bal["Anna"] {
		t.Fatalf("credit %d != debt %d", credit, -bal["Anna"])
	}
	sick, sbal, srows := absenceScenario(t, "Лікарняний")
	if srows == 0 {
		t.Fatal("sick replacement not recorded in ledger")
	}
	if sbal["Anna"] != 0 {
		t.Fatalf("sick created debt: %v", sbal)
	}
	// 4 people, 31 days*2 roles = 62 slots, fair share 15.5
	if vac <= sick {
		t.Fatalf("after vacation Anna Dec=%d, after sick Dec=%d: debt not repaid", vac, sick)
	}
	if sick > 17 {
		t.Fatalf("sick leave made Anna catch up: Dec=%d", sick)
	}
	t.Logf("Anna December duties: after vacation=%d, after sick=%d", vac, sick)
}

func TestSickTypeDetection(t *testing.T) {
	setupTestDB(t)
	sickTypeCache.names = nil
	for typ, want := range map[string]bool{"Лікарняний": true, "sick": true, "Відпустка": false, "vacation": false, "Вихідний": false, "dayoff": false, "Командировка": true, "Відрядження": true, "trip": true} {
		if got := isSickType(typ); got != want {
			t.Errorf("%s: %v", typ, got)
		}
	}
}

// Withdrawn absence removes its ledger rows.
func TestWithdrawnAbsenceClearsLedger(t *testing.T) {
	absenceScenario(t, "Відпустка")
	db.Exec(`UPDATE absences SET status='Rejected'`)
	if _, err := regenerateFutureShifts("withdrawn"); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM duty_balance`).Scan(&n)
	if n != 0 {
		t.Fatalf("ledger rows left: %d", n)
	}
}

// Rule 3 (owner decision): business trip creates no debt, day off does.
func TestTripNoDebtDayoffDebt(t *testing.T) {
	sickTypeCache.names = nil
	_, tbal, trows := absenceScenario(t, "trip")
	if trows == 0 {
		t.Fatal("trip replacement not recorded in ledger")
	}
	if tbal["Anna"] != 0 {
		t.Fatalf("trip created debt: %v", tbal)
	}
	sickTypeCache.names = nil
	_, dbal, _ := absenceScenario(t, "dayoff")
	if dbal["Anna"] >= 0 {
		t.Fatalf("dayoff created no debt: %v", dbal)
	}
}
