package main

import (
	"os/exec"
	"testing"
)

// Finding 1: "today" must be the Kyiv calendar date, not the UTC/container one.
func TestKyivTodayAtNightUTC(t *testing.T) {
	setupTestDB(t)
	cases := []struct{ now, want string }{
		{"2026-10-31T22:30:00Z", "2026-11-01"}, // 00:30 EET (winter)
		{"2026-10-26T22:30:00Z", "2026-10-27"}, // review example
		{"2026-07-01T21:30:00Z", "2026-07-02"}, // 00:30 EEST (summer)
		{"2026-10-24T21:30:00Z", "2026-10-25"}, // 00:30 EEST on DST switch night
		{"2026-10-25T21:59:00Z", "2026-10-25"}, // 23:59 EET after the switch
		{"2026-10-25T22:00:00Z", "2026-10-26"}, // 00:00 EET
	}
	for _, c := range cases {
		setNow(t, c.now)
		if got := kyivToday(); got != c.want {
			t.Errorf("now=%s kyivToday=%s want %s", c.now, got, c.want)
		}
	}
}

func TestTodayShiftPairUsesKyivDate(t *testing.T) {
	setupTestDB(t)
	putShift(t, "2026-10-31", "Yesterday", "Y2")
	putShift(t, "2026-11-01", "Today", "T2")
	setNow(t, "2026-10-31T22:30:00Z") // 00:30 Kyiv, 01.11
	p, b := todayShiftPair()
	if p != "Today" || b != "T2" {
		t.Fatalf("got %s/%s, want Today/T2", p, b)
	}
}

func TestDSTSwitchDigestHour(t *testing.T) {
	setupTestDB(t)
	// 08:00 Kyiv before and after 25.10.2026 maps to 05:00Z and 06:00Z.
	setNow(t, "2026-10-24T05:00:00Z")
	if got := kyivNow().Format("2006-01-02 15:04"); got != "2026-10-24 08:00" {
		t.Errorf("summer: %s", got)
	}
	setNow(t, "2026-10-26T06:00:00Z")
	if got := kyivNow().Format("2006-01-02 15:04"); got != "2026-10-26 08:00" {
		t.Errorf("winter: %s", got)
	}
}

// JS kyivToday() in static/*.html, executed with node if available.
func TestJSKyivToday(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; see scripts/kyiv_today.test.js for manual check")
	}
	out, err := exec.Command(node, "scripts/kyiv_today.test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
