package main

// Щоденне оголошення в Slack-каналах: хто сьогодні черговий (основний + дублюючий).
// «Сьогодні» і час відправки рахуються лише в Europe/Kyiv (kyivNow / on_grid_timezone),
// незалежно від TZ інстансу (контейнер зазвичай у UTC).
//
// Env:
//   DUTY_ANNOUNCE_ENABLED  — 0/false вимикає (за замовчуванням увімкнено)
//   DUTY_ANNOUNCE_CHANNELS — список через кому: channel ID (C…) або #name
//                            (за замовчуванням "#devops-pm-direct,#devops_team")
//   DUTY_ANNOUNCE_TIME     — HH:MM за Києвом (за замовчуванням 09:00)
//   SLACK_BOT_TOKEN        — той самий bot token (chat.postMessage)
//
// Ідемпотентність: таблиця duty_announce_sent (day, channel) — рядок пишеться лише
// після ok=true від Slack; помилка одного каналу не блокує інші й повторюється.

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	defaultDutyAnnounceChannels = "#devops-pm-direct,#devops_team"
	defaultDutyAnnounceTime     = "09:00"
	dutyAnnounceRetryInterval   = 2 * time.Minute
)

// Injectable for tests.
var dutyChannelPoster = func(channel, text string) error {
	return postSlackAPI(slackBotToken(), channel, text)
}

var dutyAnnounceState struct {
	sync.Mutex
	lastAttempt time.Time
}

func init() {
	go func() {
		for db == nil {
			time.Sleep(2 * time.Second)
		}
		time.Sleep(7 * time.Second)
		ensureDutyAnnounceTable()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			maybeSendDutyAnnounce(kyivNow())
		}
	}()
}

func ensureDutyAnnounceTable() {
	db.Exec(`CREATE TABLE IF NOT EXISTS duty_announce_sent (
		day TEXT NOT NULL,
		channel TEXT NOT NULL,
		sent_at TEXT NOT NULL,
		PRIMARY KEY (day, channel)
	)`)
}

func dutyAnnounceEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DUTY_ANNOUNCE_ENABLED")))
	return !(v == "0" || v == "false" || v == "no" || v == "off")
}

func dutyAnnounceChannels() []string {
	raw := strings.TrimSpace(os.Getenv("DUTY_ANNOUNCE_CHANNELS"))
	if raw == "" {
		raw = defaultDutyAnnounceChannels
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range strings.Split(raw, ",") {
		c = strings.TrimSpace(c)
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func dutyAnnounceTime() (h, m int) {
	v := strings.TrimSpace(os.Getenv("DUTY_ANNOUNCE_TIME"))
	if v == "" {
		v = defaultDutyAnnounceTime
	}
	t, err := time.Parse("15:04", v)
	if err != nil {
		log.Printf("duty announce: bad DUTY_ANNOUNCE_TIME %q, using %s", v, defaultDutyAnnounceTime)
		t, _ = time.Parse("15:04", defaultDutyAnnounceTime)
	}
	return t.Hour(), t.Minute()
}

func dutyAnnounceSent(day, channel string) bool {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM duty_announce_sent WHERE day=? AND channel=?`, day, channel).Scan(&n)
	return n > 0
}

func shiftPairOn(day string) (primary, backup string) {
	_ = db.QueryRow(`SELECT COALESCE(primary_user,''), COALESCE(backup_user,'') FROM shifts WHERE date=?`, day).Scan(&primary, &backup)
	return strings.TrimSpace(primary), strings.TrimSpace(backup)
}

func slackMention(name string) string {
	if sid := userSlackID(name); sid != "" {
		return fmt.Sprintf("<@%s> (%s)", sid, name)
	}
	return name
}

var ukWeekdays = [...]string{"неділя", "понеділок", "вівторок", "середа", "четвер", "пʼятниця", "субота"}

func dutyAnnounceText(kyivDay time.Time, primary, backup string) string {
	day := kyivDay.Format("2006-01-02")
	var b strings.Builder
	fmt.Fprintf(&b, "🛡️ Чергові на сьогодні, %s (%s, Київ):\n", day, ukWeekdays[kyivDay.Weekday()])
	if primary == "" {
		b.WriteString("• Основний: ⚠️ не призначено\n")
	} else {
		fmt.Fprintf(&b, "• Основний: %s\n", slackMention(primary))
	}
	if backup != "" && backup != primary {
		fmt.Fprintf(&b, "• Дублюючий: %s\n", slackMention(backup))
	} else {
		b.WriteString("• Дублюючий: —\n")
	}
	if wd := kyivDay.Weekday(); wd == time.Saturday || wd == time.Sunday {
		b.WriteString("📅 Сьогодні вихідний — чергові на звʼязку за викликом.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// maybeSendDutyAnnounce: now is converted to Kyiv; posts once per channel per Kyiv
// day after DUTY_ANNOUNCE_TIME (catch-up till the end of that Kyiv day after restarts).
func maybeSendDutyAnnounce(now time.Time) {
	if db == nil || !dutyAnnounceEnabled() {
		return
	}
	now = now.In(kyivLocation())
	h, m := dutyAnnounceTime()
	start := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if now.Before(start) {
		return
	}
	day := now.Format("2006-01-02")
	var pending []string
	for _, ch := range dutyAnnounceChannels() {
		if !dutyAnnounceSent(day, ch) {
			pending = append(pending, ch)
		}
	}
	if len(pending) == 0 {
		return
	}
	dutyAnnounceState.Lock()
	if !dutyAnnounceState.lastAttempt.IsZero() && now.Sub(dutyAnnounceState.lastAttempt) < dutyAnnounceRetryInterval && now.After(dutyAnnounceState.lastAttempt) {
		dutyAnnounceState.Unlock()
		return
	}
	dutyAnnounceState.lastAttempt = now
	dutyAnnounceState.Unlock()

	p, b := shiftPairOn(day)
	text := dutyAnnounceText(now, p, b)
	for _, ch := range pending {
		if err := dutyChannelPoster(ch, text); err != nil {
			log.Printf("duty announce %s: ERROR channel %s: %v (will retry)", day, ch, err)
			continue
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO duty_announce_sent (day, channel, sent_at) VALUES (?,?,?)`,
			day, ch, now.Format(time.RFC3339)); err != nil {
			log.Printf("duty announce %s: posted to %s but failed to persist: %v", day, ch, err)
			continue
		}
		log.Printf("duty announce sent %s channel=%s primary=%s backup=%s", day, ch, p, b)
	}
}
