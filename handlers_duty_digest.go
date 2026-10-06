package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
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

func kyivNow() time.Time {
	tz := getSetting("on_grid_timezone", "Europe/Kyiv")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.FixedZone("EET", 2*3600)
	}
	return time.Now().In(loc)
}

func morningDigestLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		maybeSendMorningDigest()
	}
}

func maybeSendMorningDigest() {
	if db == nil || !settingOn("morning_duty_notify", "1") {
		return
	}
	now := kyivNow()
	want := getSetting("morning_duty_time", "08:00")
	if len(want) >= 5 {
		want = want[:5]
	}
	if now.Format("15:04") != want {
		return
	}
	day := now.Format("2006-01-02")
	if getSetting("morning_duty_last", "") == day {
		return
	}
	setSetting("morning_duty_last", day)
	p, b := todayShiftPair()
	sendDutyDM(p, "основний", day)
	if b != "" && b != p {
		sendDutyDM(b, "дублюючий", day)
	}
	log.Printf("morning digest sent %s primary=%s backup=%s", day, p, b)
}

func sendDutyDM(name, role, day string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	var openN, workN int
	_ = db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE user_name=? AND status NOT IN ('Вирішено','Архів','У задачу')`, name).Scan(&openN)
	_ = db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE user_name=? AND status IN ('В роботі','У роботі')`, name).Scan(&workN)
	text := fmt.Sprintf("Чергування %s: ви %s. Відкритих звернень: %d, у роботі: %d. https://s.ks.tv:85/", day, role, openN, workN)
	if err := slackDM(name, text); err != nil {
		log.Printf("duty digest %s: %v", name, err)
	}
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
