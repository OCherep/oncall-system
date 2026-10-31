package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupTestDB creates a fresh SQLite DB in a temp dir via the real initDB schema.
func setupTestDB(t *testing.T) {
	t.Helper()
	if db != nil {
		db.Close()
		db = nil
	}
	os.Setenv("DB_PATH", filepath.Join(t.TempDir(), "test.db"))
	initDB()
	t.Cleanup(func() {
		db.Close()
		db = nil
		nowFunc = time.Now
	})
}

// setNow freezes the clock at the given RFC3339 instant.
func setNow(t *testing.T, rfc3339 string) {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		t.Fatal(err)
	}
	nowFunc = func() time.Time { return ts }
}

func addOncall(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := db.Exec(`INSERT INTO users (username, password, name, role, is_oncall) VALUES (?,?,?,?,1)`, n, "x", n, "user"); err != nil {
			t.Fatal(err)
		}
	}
}

func shiftOn(t *testing.T, date string) (string, string) {
	t.Helper()
	var p, b string
	db.QueryRow(`SELECT COALESCE(primary_user,''), COALESCE(backup_user,'') FROM shifts WHERE date=?`, date).Scan(&p, &b)
	return p, b
}

func putShift(t *testing.T, date, p, b string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR REPLACE INTO shifts (date, primary_user, backup_user) VALUES (?,?,?)`, date, p, b); err != nil {
		t.Fatal(err)
	}
}
