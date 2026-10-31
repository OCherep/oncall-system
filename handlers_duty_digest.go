package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

func init() {
	go func() {
		for db == nil {
			time.Sleep(2 * time.Second)
		}
		time.Sleep(5 * time.Second)
		morningDigestLoop()
	}()
}

func morningDigestLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		maybeSendMorningDigest()
	}
}

// Injectable for tests: DM sender and team-channel notifier.
var (
	dutyDMSender = slackDM
	teamNotifier = notifyTeam
)

const (
	digestWindow        = 30 * time.Minute // retry window after morning_duty_time
	digestRetryInterval = 2 * time.Minute
)

var digestState struct {
	sync.Mutex
	lastAttempt time.Time
}

// maybeSendMorningDigest (item 4): sends within [morning_duty_time, +30m) Kyiv,
// retries failures every digestRetryInterval, marks morning_duty_last only after
// every recipient got the DM, and is idempotent per Kyiv day.
func maybeSendMorningDigest() {
	if db == nil || !settingOn("morning_duty_notify", "1") {
		return
	}
	now := kyivNow()
	want := getSetting("morning_duty_time", "08:00")
	if len(want) >= 5 {
		want = want[:5]
	}
	hm, err := time.Parse("15:04", want)
	if err != nil {
		log.Printf("morning digest: bad morning_duty_time %q", want)
		return
	}
	day := now.Format("2006-01-02")
	start := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
	if now.Before(start) {
		return
	}
	if getSetting("morning_duty_last", "") == day {
		return
	}
	if !now.Before(start.Add(digestWindow)) {
		if getSetting("morning_duty_gaveup", "") != day {
			setSetting("morning_duty_gaveup", day)
			log.Printf("morning digest %s: NOT sent within %v after %s", day, digestWindow, want)
		}
		return
	}
	digestState.Lock()
	if !digestState.lastAttempt.IsZero() && now.Sub(digestState.lastAttempt) < digestRetryInterval {
		digestState.Unlock()
		return
	}
	digestState.lastAttempt = now
	digestState.Unlock()

	p, b := todayShiftPair()
	if p == "" {
		log.Printf("morning digest %s: ERROR no on-call primary for today, nothing sent", day)
		if getSetting("morning_duty_empty_warned", "") != day {
			setSetting("morning_duty_empty_warned", day)
			teamNotifier(fmt.Sprintf("⚠️ На %s немає призначеного чергового — ранковий дайджест не надіслано.", day))
		}
		return
	}
	recipients := []struct{ name, role, key string }{{p, "основний", "morning_duty_sent_primary"}}
	if b != "" && b != p {
		recipients = append(recipients, struct{ name, role, key string }{b, "дублюючий", "morning_duty_sent_backup"})
	}
	failed := false
	for _, r := range recipients {
		mark := day + "|" + r.name
		if getSetting(r.key, "") == mark {
			continue // already delivered today
		}
		if err := sendDutyDM(r.name, r.role, day); err != nil {
			failed = true
			log.Printf("morning digest %s: ERROR %s %s: %v (will retry)", day, r.role, r.name, err)
			continue
		}
		setSetting(r.key, mark)
	}
	if failed {
		return
	}
	setSetting("morning_duty_last", day)
	log.Printf("morning digest sent %s primary=%s backup=%s", day, p, b)
}

func sendDutyDM(name, role, day string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("empty recipient")
	}
	var openN, workN int
	_ = db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE user_name=? AND status NOT IN ('Вирішено','Архів','У задачу')`, name).Scan(&openN)
	_ = db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE user_name=? AND status IN ('В роботі','У роботі')`, name).Scan(&workN)
	text := fmt.Sprintf("Чергування %s: ви %s. Відкритих звернень: %d, у роботі: %d. https://s.ks.tv:85/", day, role, openN, workN)
	return dutyDMSender(name, text)
}

func slackDM(userName, text string) error {
	sid := userSlackID(userName)
	token := slackBotToken()
	if sid == "" || token == "" {
		return fmt.Errorf("no slack id or token for %s", userName)
	}
	openBody, _ := json.Marshal(map[string]string{"users": sid})
	req, err := http.NewRequest(http.MethodPost, "https://slack.com/api/conversations.open", bytes.NewReader(openBody))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var opened struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&opened)
	if !opened.OK {
		return fmt.Errorf("conversations.open: %s", opened.Error)
	}
	msg, _ := json.Marshal(map[string]string{"channel": opened.Channel.ID, "text": text})
	req2, err := http.NewRequest(http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req2.Header.Set("Authorization", "Bearer "+token)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	var posted struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&posted)
	if !posted.OK {
		return fmt.Errorf("chat.postMessage: %s", posted.Error)
	}
	return nil
}
