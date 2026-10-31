package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type annPost struct{ ch, text string }

func setupAnnounce(t *testing.T) (*[]annPost, map[string]bool) {
	t.Helper()
	setupTestDB(t)
	ensureDutyAnnounceTable()
	t.Setenv("DUTY_ANNOUNCE_CHANNELS", "")
	t.Setenv("DUTY_ANNOUNCE_TIME", "")
	t.Setenv("DUTY_ANNOUNCE_ENABLED", "")
	posts := &[]annPost{}
	fail := map[string]bool{}
	old := dutyChannelPoster
	dutyChannelPoster = func(ch, text string) error {
		if fail[ch] {
			return errors.New("channel_not_found")
		}
		*posts = append(*posts, annPost{ch, text})
		return nil
	}
	dutyAnnounceState.lastAttempt = time.Time{}
	t.Cleanup(func() { dutyChannelPoster = old; dutyAnnounceState.lastAttempt = time.Time{} })
	return posts, fail
}

func utc(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts.UTC() // instance clock in UTC
}

func resetThrottle() { dutyAnnounceState.lastAttempt = time.Time{} }

func TestAnnounceKyivDateAroundMidnightUTC(t *testing.T) {
	posts, _ := setupAnnounce(t)
	t.Setenv("DUTY_ANNOUNCE_TIME", "00:15")
	t.Setenv("DUTY_ANNOUNCE_CHANNELS", "C1")
	putShift(t, "2026-11-01", "Old", "")
	putShift(t, "2026-11-02", "Anna", "Bohdan")
	// winter (UTC+2): 22:30Z Nov 1 = 00:30 Kyiv Nov 2
	maybeSendDutyAnnounce(utc(t, "2026-11-01T22:30:00Z"))
	if len(*posts) != 1 || !strings.Contains((*posts)[0].text, "2026-11-02") || !strings.Contains((*posts)[0].text, "Anna") {
		t.Fatalf("want Kyiv 2026-11-02 Anna, got %+v", *posts)
	}
	// summer (UTC+3): 21:30Z Jul 14 = 00:30 Kyiv Jul 15; 23:30Z is still Jul 15 in Kyiv
	posts2, _ := setupAnnounce(t)
	t.Setenv("DUTY_ANNOUNCE_TIME", "00:15")
	t.Setenv("DUTY_ANNOUNCE_CHANNELS", "C1")
	putShift(t, "2026-07-15", "Summer", "")
	maybeSendDutyAnnounce(utc(t, "2026-07-14T21:30:00Z"))
	if len(*posts2) != 1 || !strings.Contains((*posts2)[0].text, "2026-07-15") {
		t.Fatalf("summer: %+v", *posts2)
	}
}

func TestAnnounceBeforeTimeInKyivNotSent(t *testing.T) {
	posts, _ := setupAnnounce(t)
	putShift(t, "2026-11-02", "Anna", "")
	// 06:59Z = 08:59 Kyiv — before 09:00 even though UTC hour is "early"
	maybeSendDutyAnnounce(utc(t, "2026-11-02T06:59:00Z"))
	if len(*posts) != 0 {
		t.Fatalf("sent too early: %+v", *posts)
	}
	maybeSendDutyAnnounce(utc(t, "2026-11-02T07:00:00Z"))
	if len(*posts) != 2 {
		t.Fatalf("want 2 default channels, got %+v", *posts)
	}
	if (*posts)[0].ch != "#devops-pm-direct" || (*posts)[1].ch != "#devops_team" {
		t.Fatalf("default channels: %+v", *posts)
	}
}

func TestAnnounceDST(t *testing.T) {
	// Spring: 2027-03-28 03:00 Kyiv -> 04:00 (UTC+3). 09:00 Kyiv = 06:00Z.
	posts, _ := setupAnnounce(t)
	t.Setenv("DUTY_ANNOUNCE_CHANNELS", "C1")
	putShift(t, "2027-03-28", "Spring", "")
	maybeSendDutyAnnounce(utc(t, "2027-03-28T05:59:00Z"))
	if len(*posts) != 0 {
		t.Fatal("spring: sent before 09:00 Kyiv")
	}
	maybeSendDutyAnnounce(utc(t, "2027-03-28T06:00:00Z"))
	if len(*posts) != 1 || !strings.Contains((*posts)[0].text, "Spring") || !strings.Contains((*posts)[0].text, "вихідний") {
		t.Fatalf("spring: %+v", *posts)
	}
	// Autumn: 2026-10-25 back to UTC+2. 09:00 Kyiv = 07:00Z.
	posts2, _ := setupAnnounce(t)
	t.Setenv("DUTY_ANNOUNCE_CHANNELS", "C1")
	putShift(t, "2026-10-25", "Autumn", "")
	maybeSendDutyAnnounce(utc(t, "2026-10-25T06:30:00Z")) // 08:30 Kyiv
	if len(*posts2) != 0 {
		t.Fatal("autumn: sent before 09:00 Kyiv")
	}
	maybeSendDutyAnnounce(utc(t, "2026-10-25T07:00:00Z"))
	if len(*posts2) != 1 || !strings.Contains((*posts2)[0].text, "2026-10-25") {
		t.Fatalf("autumn: %+v", *posts2)
	}
}

func TestAnnounceMonthBoundary(t *testing.T) {
	posts, _ := setupAnnounce(t)
	t.Setenv("DUTY_ANNOUNCE_TIME", "00:05")
	t.Setenv("DUTY_ANNOUNCE_CHANNELS", "C1")
	putShift(t, "2026-11-30", "Nov", "")
	putShift(t, "2026-12-01", "Dec", "Back")
	maybeSendDutyAnnounce(utc(t, "2026-11-30T23:30:00Z")) // 01:30 Kyiv Dec 1
	if len(*posts) != 1 || !strings.Contains((*posts)[0].text, "2026-12-01") || !strings.Contains((*posts)[0].text, "Dec") {
		t.Fatalf("month boundary: %+v", *posts)
	}
}

func TestAnnounceNoDuplicate(t *testing.T) {
	posts, _ := setupAnnounce(t)
	putShift(t, "2026-11-02", "Anna", "Bohdan")
	maybeSendDutyAnnounce(utc(t, "2026-11-02T07:01:00Z"))
	resetThrottle()
	maybeSendDutyAnnounce(utc(t, "2026-11-02T07:10:00Z"))
	resetThrottle() // simulates restart: state lost, DB persists
	maybeSendDutyAnnounce(utc(t, "2026-11-02T15:00:00Z"))
	if len(*posts) != 2 {
		t.Fatalf("want exactly 2 posts, got %d", len(*posts))
	}
	if !strings.Contains((*posts)[0].text, "Дублюючий: Bohdan") || strings.Contains((*posts)[0].text, "вихідний") {
		t.Fatalf("text: %s", (*posts)[0].text)
	}
}

func TestAnnounceFailureOneChannel(t *testing.T) {
	posts, fail := setupAnnounce(t)
	putShift(t, "2026-11-02", "Anna", "")
	fail["#devops_team"] = true
	maybeSendDutyAnnounce(utc(t, "2026-11-02T07:01:00Z"))
	if !dutyAnnounceSent("2026-11-02", "#devops-pm-direct") || dutyAnnounceSent("2026-11-02", "#devops_team") {
		t.Fatal("only successful channel must be marked")
	}
	// throttled retry
	maybeSendDutyAnnounce(utc(t, "2026-11-02T07:02:00Z"))
	fail["#devops_team"] = false
	maybeSendDutyAnnounce(utc(t, "2026-11-02T07:04:00Z"))
	if len(*posts) != 2 || (*posts)[1].ch != "#devops_team" || !dutyAnnounceSent("2026-11-02", "#devops_team") {
		t.Fatalf("retry: %+v", *posts)
	}
}
