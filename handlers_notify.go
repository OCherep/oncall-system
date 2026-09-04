package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Сповіщення: Slack (командний канал + DM) і Telegram-дзеркало.
//
// Env:
//   SLACK_WEBHOOK_URL     — Incoming Webhook командного каналу
//   SLACK_BOT_TOKEN       — xoxb-… для chat.postMessage (канал + DM)
//   SLACK_TEAM_CHANNEL    — channel ID (C…) або #name
//   TELEGRAM_BOT_TOKEN    — токен бота від @BotFather
//   TELEGRAM_CHAT_ID      — ID командного чату/групи (для дзеркала)
//   NOTIFY_ON_INCIDENT=1  — увімкнути (default: on якщо є будь-який токен)

func publicBaseURL() string {
	u := strings.TrimSpace(os.Getenv("APP_PUBLIC_URL"))
	if u == "" {
		u = strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL"))
	}
	return strings.TrimRight(u, "/")
}

func slackWebhookURL() string { return strings.TrimSpace(os.Getenv("SLACK_WEBHOOK_URL")) }
func slackBotToken() string   { return strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN")) }
func slackTeamChannel() string {
	ch := strings.TrimSpace(os.Getenv("SLACK_TEAM_CHANNEL"))
	if ch == "" {
		ch = strings.TrimSpace(os.Getenv("SLACK_CHANNEL"))
	}
	return ch
}
func telegramBotToken() string { return strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")) }
func telegramChatID() string   { return strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID")) }

func notifyEnabled() bool {
	v := strings.TrimSpace(os.Getenv("NOTIFY_ON_INCIDENT"))
	if v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	return slackWebhookURL() != "" || slackBotToken() != "" || telegramBotToken() != ""
}

// notifyTeam — командний канал Slack + Telegram-дзеркало.
func notifyTeam(text string) {
	if !notifyEnabled() || strings.TrimSpace(text) == "" {
		return
	}
	go func() {
		// Slack: bot API if channel+token, else incoming webhook
		token := slackBotToken()
		hook := slackWebhookURL()
		ch := slackTeamChannel()
		if token != "" && ch != "" {
			if err := postSlackAPI(token, ch, text); err != nil {
				log.Printf("slack team api: %v", err)
			}
		} else if hook != "" {
			if err := postSlackWebhook(hook, text); err != nil {
				log.Printf("slack team webhook: %v", err)
			}
		}
		// Telegram mirror
		if err := postTelegram(telegramChatID(), text); err != nil {
			log.Printf("telegram team: %v", err)
		}
	}()
}

// backward-compatible alias
func notifyTeamSlack(text string) { notifyTeam(text) }

// notifyUserSlack — особисте повідомлення в Slack за users.slack_id.
func notifyUserSlack(userName, text string) {
	if !notifyEnabled() || strings.TrimSpace(text) == "" || strings.TrimSpace(userName) == "" {
		return
	}
	go func() {
		var slackID string
		_ = db.QueryRow(`SELECT COALESCE(slack_id,'') FROM users WHERE name = ? LIMIT 1`, userName).Scan(&slackID)
		if slackID == "" {
			log.Printf("slack dm: no slack_id for %q", userName)
			return
		}
		if err := postSlackAPI(slackBotToken(), slackID, text); err != nil {
			log.Printf("slack dm %s: %v", userName, err)
		}
	}()
}


// --- B6: диспетчери + дедуп сповіщень ---

var notifyDedupe = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

func notifyOnce(key string, ttl time.Duration) bool {
	notifyDedupe.Lock()
	defer notifyDedupe.Unlock()
	now := time.Now()
	for k, t := range notifyDedupe.last {
		if now.Sub(t) > 10*time.Minute {
			delete(notifyDedupe.last, k)
		}
	}
	if t, ok := notifyDedupe.last[key]; ok && now.Sub(t) < ttl {
		return false
	}
	notifyDedupe.last[key] = now
	return true
}

// listDispatchers — імена з app_settings.dispatchers (comma) або всі role=admin.
func listDispatchers() []string {
	raw := strings.TrimSpace(getSetting("dispatchers", ""))
	var out []string
	seen := map[string]bool{}
	if raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
		if len(out) > 0 {
			return out
		}
	}
	rows, err := db.Query(`SELECT name FROM users WHERE role='admin' ORDER BY id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		rows.Scan(&n)
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

func notifyDispatchers(text string) {
	for _, name := range listDispatchers() {
		notifyUserSlack(name, text)
	}
}

// notifyOncallAboutIncident — team channel (Slack+TG) + DM черговим / диспетчерам.
func notifyOncallAboutIncident(inc IncidentReport) {
	if !notifyEnabled() {
		return
	}
	key := fmt.Sprintf("inc-new-%d", inc.ID)
	if inc.ID > 0 && !notifyOnce(key, 2*time.Minute) {
		log.Printf("notify dedupe skip %s", key)
		return
	}
	today := time.Now().Format("2006-01-02")
	date := inc.Date
	if date == "" {
		date = today
	}
	var primary, backup string
	_ = db.QueryRow(`SELECT COALESCE(primary_user,''), COALESCE(backup_user,'') FROM shifts WHERE date=?`, date).
		Scan(&primary, &backup)

	msg := formatIncidentNotifyMsg(inc, primary, backup)
	notifyTeam(msg)

	seen := map[string]bool{}
	personal := fmt.Sprintf("🔔 Звернення #%d (%s)\n%s\nПріоритет: %s · Source: %s",
		inc.ID, date, truncateRunes(inc.Description, 200), nz(inc.Priority, "Звичайний"), nz(inc.Source, "webhook"))
	if inc.ExternalID != "" {
		personal += "\nJira: " + inc.ExternalID
	}
	if base := publicBaseURL(); base != "" && inc.ID > 0 {
		personal += fmt.Sprintf("\n%s/admin.html#inc=%d", strings.TrimRight(base, "/"), inc.ID)
	}

	var targets []string
	if strings.TrimSpace(inc.UserName) != "" {
		targets = append(targets, inc.UserName)
		if priorityRank(inc.Priority) >= 3 {
			targets = append(targets, primary, backup)
		}
	} else {
		targets = append(targets, listDispatchers()...)
		targets = append(targets, primary, backup)
	}
	for _, name := range targets {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		notifyUserSlack(name, personal)
	}
}

func formatIncidentNotifyMsg(inc IncidentReport, primary, backup string) string {
	var b strings.Builder
	b.WriteString("*Нове звернення*")
	if inc.ID > 0 {
		b.WriteString(fmt.Sprintf(" `#%d`", inc.ID))
	}
	if inc.ExternalID != "" {
		b.WriteString(fmt.Sprintf(" · %s", inc.ExternalID))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("• *Опис:* %s\n", truncateRunes(inc.Description, 300)))
	b.WriteString(fmt.Sprintf("• *Дата:* %s · *Пріоритет:* %s · *Source:* %s\n",
		nz(inc.Date, time.Now().Format("2006-01-02")),
		nz(inc.Priority, "Звичайний"),
		nz(inc.Source, "webhook")))
	if inc.UserName != "" {
		b.WriteString(fmt.Sprintf("• *Виконавець:* %s\n", inc.UserName))
	}
	if primary != "" {
		b.WriteString(fmt.Sprintf("• *Черговий (осн.):* %s", primary))
		if backup != "" {
			b.WriteString(fmt.Sprintf(" · *Дубль:* %s", backup))
		}
		b.WriteString("\n")
	}
	if inc.CreatedBy != "" {
		b.WriteString(fmt.Sprintf("• *Від:* %s\n", inc.CreatedBy))
	}
	return b.String()
}

func postSlackWebhook(hookURL, text string) error {
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequest(http.MethodPost, hookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func postSlackAPI(token, channel, text string) error {
	if token == "" {
		return fmt.Errorf("SLACK_BOT_TOKEN empty")
	}
	if channel == "" {
		return fmt.Errorf("channel/user required for bot API")
	}
	payload := map[string]interface{}{
		"channel": channel,
		"text":    text,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.OK {
		return fmt.Errorf("slack api: %s", out.Error)
	}
	return nil
}

// postTelegram — дзеркало в командний чат Telegram (HTML).
func postTelegram(chatID, text string) error {
	token := telegramBotToken()
	if token == "" {
		return nil // not configured — skip silently
	}
	if chatID == "" {
		return fmt.Errorf("TELEGRAM_CHAT_ID empty")
	}
	html := slackMrkdwnToTelegramHTML(text)
	api := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", html)
	form.Set("parse_mode", "HTML")
	form.Set("disable_web_page_preview", "true")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.PostForm(api, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if !out.OK {
		return fmt.Errorf("telegram: %s", out.Description)
	}
	return nil
}

func slackMrkdwnToTelegramHTML(s string) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	var b strings.Builder
	parts := strings.Split(esc, "*")
	for i, p := range parts {
		if i%2 == 1 {
			b.WriteString("<b>")
			b.WriteString(p)
			b.WriteString("</b>")
		} else {
			b.WriteString(p)
		}
	}
	return b.String()
}

func nz(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}


func userEmailByName(name string) string {
	var email string
	_ = db.QueryRow(`SELECT COALESCE(email,'') FROM users WHERE name=? OR username=? LIMIT 1`, name, name).Scan(&email)
	return strings.TrimSpace(email)
}

func notifyEmail(to, subject, body string) {
	to = strings.TrimSpace(to)
	if to == "" {
		return
	}
	// SMTP optional — if not configured, log only
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	if host == "" {
		log.Printf("notify email (no SMTP): to=%s subject=%s", to, subject)
		return
	}
	// Minimal SMTP via net/smtp would need more deps; log for now if not fully wired
	log.Printf("notify email queued: to=%s subject=%s body=%s", to, subject, body)
}

func notifyIncidentUpdate(inc IncidentReport, text string) {
	if !notifyEnabled() && strings.TrimSpace(os.Getenv("SMTP_HOST")) == "" {
		return
	}
	msg := text
	if inc.Description != "" {
		msg += "\n«" + truncateRunes(inc.Description, 120) + "»"
	}
	notifyTeam(msg)
	if inc.UserName != "" {
		notifyUserSlack(inc.UserName, msg)
	}
	// reporter
	if inc.ReporterSlack != "" && slackBotToken() != "" {
		_ = postSlackAPI(slackBotToken(), inc.ReporterSlack, msg)
	} else if inc.ReporterEmail != "" {
		notifyEmail(inc.ReporterEmail, "OnCall: оновлення звернення", msg)
	} else if who := strings.TrimSpace(inc.CreatedBy); who != "" {
		if em := userEmailByName(who); em != "" {
			notifyEmail(em, "OnCall: оновлення звернення", msg)
		}
		notifyUserSlack(who, msg)
	}
}

func notifyIncidentComment(incidentID int, author, body string) {
	var inc IncidentReport
	_ = db.QueryRow(`SELECT id, COALESCE(user_name,''), COALESCE(description,''), COALESCE(created_by,''),
		COALESCE(reporter_name,''), COALESCE(reporter_email,''), COALESCE(reporter_slack,'') FROM incidents WHERE id=?`, incidentID).
		Scan(&inc.ID, &inc.UserName, &inc.Description, &inc.CreatedBy, &inc.ReporterName, &inc.ReporterEmail, &inc.ReporterSlack)
	if inc.ID == 0 {
		return
	}
	msg := fmt.Sprintf("Коментар до звернення #%d від %s:\n%s", incidentID, author, truncateRunes(body, 200))
	notifyIncidentUpdate(inc, msg)
}

func notifyTaskComment(taskID int, author, body string) {
	var userName, desc string
	_ = db.QueryRow(`SELECT COALESCE(user_name,''), COALESCE(task_description,'') FROM daily_tasks WHERE id=?`, taskID).Scan(&userName, &desc)
	msg := fmt.Sprintf("Коментар до задачі #%d від %s:\n%s\n«%s»", taskID, author, truncateRunes(body, 200), truncateRunes(desc, 80))
	notifyTeam(msg)
	if userName != "" {
		notifyUserSlack(userName, msg)
	}
}



// notifyNewIncident — нове звернення: team channel + усі admin (Slack DM / email).
// Пізніше: відповідальний за розподіл (dispatcher) замість усіх admin.
func notifyNewIncident(inc IncidentReport) {
	who := strings.TrimSpace(inc.ReporterName)
	if who == "" {
		who = strings.TrimSpace(inc.CreatedBy)
	}
	if who == "" {
		who = inc.Source
	}
	assignee := strings.TrimSpace(inc.UserName)
	prio := inc.Priority
	if prio == "" {
		prio = "Звичайний"
	}
	msg := fmt.Sprintf("🆕 Звернення #%d [%s]\nАвтор: %s (%s)", inc.ID, prio, who, inc.Source)
	if inc.ReporterEmail != "" {
		msg += "\nEmail: " + inc.ReporterEmail
	}
	if inc.ReporterSlack != "" {
		msg += "\nSlack: " + inc.ReporterSlack
	}
	if assignee == "" {
		msg += "\n⚠️ Без виконавця — потрібен розподіл"
	} else {
		msg += "\nВиконавець: " + assignee
	}
	msg += "\n«" + truncateRunes(inc.Description, 160) + "»"
	if base := publicBaseURL(); base != "" {
		// deep-link: admin → Усі звернення, підсвітка/редагування #id
		msg += fmt.Sprintf("\n🔗 Розподіл: %s/admin.html#inc=%d", base, inc.ID)
	}
	notifyTeam(msg)
	// DM / email усім admin
	rows, err := db.Query(`SELECT COALESCE(name,''), COALESCE(slack_id,''), COALESCE(email,'') FROM users WHERE role='admin'`)
	if err == nil && rows != nil {
		defer rows.Close()
		for rows.Next() {
			var name, slack, email string
			rows.Scan(&name, &slack, &email)
			if name != "" {
				notifyUserSlack(name, msg)
			}
			if email != "" {
				notifyEmail(email, fmt.Sprintf("OnCall: нове звернення #%d", inc.ID), msg)
			}
		}
	}
	if assignee != "" {
		notifyUserSlack(assignee, msg)
	}
	// чергова пара
	p, b := shiftPairForDate(inc.Date)
	for _, name := range []string{p, b} {
		if name != "" && name != assignee {
			notifyUserSlack(name, msg)
		}
	}
}


// notifyAbsenceDecision — DM співробітнику + командний канал при Approved/Rejected.
func notifyAbsenceDecision(a AbsenceRequest, newStatus, actor string) {
	if !notifyEnabled() {
		return
	}
	key := fmt.Sprintf("abs-%d-%s", a.ID, newStatus)
	if a.ID > 0 && !notifyOnce(key, 30*time.Second) {
		return
	}
	statusUA := newStatus
	switch newStatus {
	case "Approved":
		statusUA = "затверджено ✅"
	case "Rejected":
		statusUA = "відхилено ❌"
	case "Pending":
		statusUA = "на розгляді ⏳"
	}
	who := strings.TrimSpace(actor)
	if who == "" {
		who = "система"
	}
	msg := fmt.Sprintf("*Відсутність* `%s` · %s\n• *Період:* %s — %s\n• *Тип:* %s\n• *Рішення:* %s\n• *Хто:* %s",
		a.UserName, statusUA, a.StartDate, a.EndDate, a.Type, statusUA, who)
	notifyTeam(msg)
	notifyUserSlack(a.UserName, fmt.Sprintf("Ваша заявка на відсутність (%s, %s — %s): %s",
		a.Type, a.StartDate, a.EndDate, statusUA))
	// диспетчерам — лише Pending (нова заявка)
	if newStatus == "Pending" {
		notifyDispatchers(fmt.Sprintf("📋 Нова заявка на відсутність від *%s*\n%s — %s · %s\nПотрібне затвердження в адмінці → Для затвердження.",
			a.UserName, a.StartDate, a.EndDate, a.Type))
	}
}
