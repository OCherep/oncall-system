package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

type digestFakes struct {
	sent   []string
	fail   bool
	notify []string
	logs   *bytes.Buffer
}

func setupDigest(t *testing.T) *digestFakes {
	t.Helper()
	setupTestDB(t)
	f := &digestFakes{logs: &bytes.Buffer{}}
	oldDM, oldNotify := dutyDMSender, teamNotifier
	dutyDMSender = func(name, text string) error {
		if f.fail {
			return errors.New("slack down")
		}
		f.sent = append(f.sent, name)
		return nil
	}
	teamNotifier = func(text string) { f.notify = append(f.notify, text) }
	log.SetOutput(f.logs)
	digestState.lastAttempt = time.Time{}
	t.Cleanup(func() {
		dutyDMSender, teamNotifier = oldDM, oldNotify
		log.SetOutput(os.Stderr)
		digestState.lastAttempt = time.Time{}
	})
	setSetting("morning_duty_notify", "1")
	setSetting("morning_duty_time", "08:00")
	return f
}

// Finding 4: failure must not set morning_duty_last nor log "sent"; retry succeeds.
func TestDigestFailureThenRetry(t *testing.T) {
	f := setupDigest(t)
	putShift(t, "2026-11-02", "Anna", "Bohdan")
	f.fail = true
	setNow(t, "2026-11-02T06:00:00Z") // 08:00 Kyiv
	maybeSendMorningDigest()
	if got := getSetting("morning_duty_last", ""); got != "" {
		t.Fatalf("last set after failure: %q", got)
	}
	if strings.Contains(f.logs.String(), "morning digest sent") || !strings.Contains(f.logs.String(), "slack down") {
		t.Fatalf("bad log: %s", f.logs.String())
	}
	// within retry interval: no new attempt
	setNow(t, "2026-11-02T06:01:00Z")
	f.fail = false
	maybeSendMorningDigest()
	if len(f.sent) != 0 {
		t.Fatal("retried before interval")
	}
	// 08:05 Kyiv: retry, not an exact-minute match
	setNow(t, "2026-11-02T06:05:00Z")
	maybeSendMorningDigest()
	if strings.Join(f.sent, ",") != "Anna,Bohdan" {
		t.Fatalf("sent=%v", f.sent)
	}
	if getSetting("morning_duty_last", "") != "2026-11-02" {
		t.Fatal("last not set after success")
	}
	// idempotent per day
	setNow(t, "2026-11-02T06:20:00Z")
	maybeSendMorningDigest()
	if len(f.sent) != 2 {
		t.Fatalf("duplicate send: %v", f.sent)
	}
}

func TestDigestPartialFailureDoesNotResendPrimary(t *testing.T) {
	f := setupDigest(t)
	putShift(t, "2026-11-02", "Anna", "Bohdan")
	dutyDMSender = func(name, text string) error {
		if name == "Bohdan" && len(f.sent) < 2 {
			f.sent = append(f.sent, "fail:"+name)
			return errors.New("boom")
		}
		f.sent = append(f.sent, name)
		return nil
	}
	setNow(t, "2026-11-02T06:00:00Z")
	maybeSendMorningDigest()
	setNow(t, "2026-11-02T06:03:00Z")
	maybeSendMorningDigest()
	if strings.Join(f.sent, ",") != "Anna,fail:Bohdan,Bohdan" {
		t.Fatalf("sent=%v", f.sent)
	}
	if getSetting("morning_duty_last", "") != "2026-11-02" {
		t.Fatal("last not set")
	}
}

func TestDigestWindow(t *testing.T) {
	f := setupDigest(t)
	putShift(t, "2026-11-02", "Anna", "Anna")
	setNow(t, "2026-11-02T05:59:00Z") // 07:59
	maybeSendMorningDigest()
	setNow(t, "2026-11-02T06:30:00Z") // 08:30 — window closed
	maybeSendMorningDigest()
	if len(f.sent) != 0 {
		t.Fatalf("sent outside window: %v", f.sent)
	}
	if !strings.Contains(f.logs.String(), "NOT sent") {
		t.Fatalf("no give-up log: %s", f.logs.String())
	}
	setNow(t, "2026-11-03T06:29:00Z") // next day 08:29 — inside window, one person
	maybeSendMorningDigest()
	putShift(t, "2026-11-03", "Anna", "Anna")
	digestState.lastAttempt = time.Time{}
	maybeSendMorningDigest()
	if strings.Join(f.sent, ",") != "Anna" {
		t.Fatalf("one-person roster: sent=%v", f.sent)
	}
}

// Empty roster / no shift: never "sent", no last mark, team warned once.
func TestDigestEmptyRoster(t *testing.T) {
	f := setupDigest(t)
	setNow(t, "2026-11-02T06:00:00Z")
	maybeSendMorningDigest()
	setNow(t, "2026-11-02T06:10:00Z")
	maybeSendMorningDigest()
	if strings.Contains(f.logs.String(), "morning digest sent") {
		t.Fatal("logged sent for empty roster")
	}
	if getSetting("morning_duty_last", "") != "" {
		t.Fatal("last set for empty roster")
	}
	if len(f.notify) != 1 {
		t.Fatalf("team warnings=%d, want 1", len(f.notify))
	}
}

// DST: 08:00 EET after 25.10.2026 is 06:00Z.
func TestDigestAfterDSTSwitch(t *testing.T) {
	f := setupDigest(t)
	putShift(t, "2026-10-26", "Anna", "Anna")
	setNow(t, "2026-10-26T05:30:00Z") // 07:30 EET
	maybeSendMorningDigest()
	if len(f.sent) != 0 {
		t.Fatal("sent before 08:00 EET")
	}
	setNow(t, "2026-10-26T06:00:00Z")
	maybeSendMorningDigest()
	if len(f.sent) != 1 {
		t.Fatal("not sent at 08:00 EET")
	}
}
