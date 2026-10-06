package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Slack Events API: @devops-team → звернення.
// app_settings.slack_auto_assign=1 — вільний черговий (без «В роботі»).
// =0 — лише адмін, крім: усі адміни у BRB або черга без виконавця ≥ slack_queue_limit.
// Ранковий DM — handlers_duty_digest.go (morning_duty_notify / morning_duty_time).

func teamSubteamID() string {
	if v := strings.TrimSpace(os.Getenv("SLACK_TEAM_SUBTEAM")); v != "" {
		return v
	}
	return "S03QEQF27AN"
}

func teamHandle() string {
	if v := strings.TrimSpace(os.Getenv("SLACK_TEAM_HANDLE")); v != "" {
		return strings.TrimPrefix(v, "@")
	}
	return "devops-team"
}

func settingOn(key, def string) bool {
	v := strings.ToLower(getSetting(key, def))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func mentionsTeam(text string) bool {
	low := strings.ToLower(text)
	handle := strings.ToLower(teamHandle())
	if strings.Contains(low, "@"+handle) || strings.Contains(low, "<!subteam^"+strings.ToLower(teamSubteamID())) {
		return true
	}
	return strings.Contains(text, "<!subteam^"+teamSubteamID())
}

func todayShiftPair() (primary, backup string) {
	day := time.Now().Format("2006-01-02")
	_ = db.QueryRow(`SELECT COALESCE(primary_user,''), COALESCE(backup_user,'') FROM shifts WHERE date=?`, day).Scan(&primary, &backup)
	return strings.TrimSpace(primary), strings.TrimSpace(backup)
}

func inProgressIncidentCount(userName string) int {
	userName = strings.TrimSpace(userName)
	if userName == "" {
		return 0
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE user_name=? AND status IN ('В роботі','У роботі')`, userName).Scan(&n)
	return n
}

func unassignedOpenCount() int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE TRIM(COALESCE(user_name,''))='' AND status NOT IN ('Вирішено','Архів','У задачу')`).Scan(&n)
	return n
}

func adminsAllOnBRB() bool {
	brb := activeBRBMap()
	if len(brb) == 0 {
		return false
	}
	names := listDispatchers()
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if _, ok := brb[name]; !ok {
			return false
		}
	}
	return true
}

func pickOncallAssignee() (assignee, reason string) {
	p, b := todayShiftPair()
	switch {
	case p != "" && inProgressIncidentCount(p) == 0:
		return p, "primary free"
	case b != "" && inProgressIncidentCount(b) == 0:
		return b, "backup free"
	case p == "" && b == "":
		return "", "no shift"
	default:
		return "", "both in progress"
	}
}

func resolveTeamAssignee() (assignee, reason string) {
	if settingOn("slack_auto_assign", "1") {
		a, why := pickOncallAssignee()
		return a, "auto: " + why
	}
	limit := 5
	fmt.Sscan(getSetting("slack_queue_limit", "5"), &limit)
	if limit < 1 {
		limit = 5
	}
	if adminsAllOnBRB() {
		a, why := pickOncallAssignee()
		return a, "admin BRB → " + why
	}
	if unassignedOpenCount() >= limit {
		a, why := pickOncallAssignee()
		return a, fmt.Sprintf("queue>=%d → %s", limit, why)
	}
	return "", "admin queue"
}

func slackPermalink(channel, ts string) string {
	ts = strings.ReplaceAll(ts, ".", "")
	return "https://vidmindtalk.slack.com/archives/" + channel + "/p" + ts
}

func createIncidentFromTeamMention(channel, ts, authorID, text string) (int64, string, string, error) {
	extID := "slack:" + channel + ":" + ts
	var existing int
	if err := db.QueryRow(`SELECT id FROM incidents WHERE external_id=? LIMIT 1`, extID).Scan(&existing); err == nil && existing > 0 {
		return int64(existing), "", "duplicate", nil
	}
	assignee, why := resolveTeamAssignee()
	desc := strings.TrimSpace(text)
	if len(desc) > 1500 {
		desc = desc[:1500]
	}
	link := slackPermalink(channel, ts)
	desc = desc + "\n" + link
	now := time.Now().Format("2006-01-02")
	res, err := db.Exec(`INSERT INTO incidents (
		user_name, date, type, duration_minutes, description, created_at,
		status, priority, source, total_minutes, created_by, reported_for, external_id, reporter_slack
	) VALUES (?, ?, 'Звернення', 15, ?, CURRENT_TIMESTAMP, 'Нове', 'Звичайний', 'slack', 15, ?, ?, ?, ?)`,
		assignee, now, desc, "slack:"+authorID, authorID, extID, authorID)
	if err != nil {
		return 0, "", "", err
	}
	id, _ := res.LastInsertId()
	note := fmt.Sprintf("Slack @%s %s → %s (%s)", teamHandle(), link, firstNonEmpty(assignee, "без виконавця"), why)
	addSystemComment("incident", int(id), note)
	logAudit("slack", "SLACK_TEAM_INCIDENT", channel, note)
	inc := IncidentReport{
		ID: int(id), UserName: assignee, Date: now, Type: "Звернення",
		Description: desc, Status: "Нове", Priority: "Звичайний", Source: "slack", ExternalID: extID,
	}
	go notifyOncallAboutIncident(inc)
	return id, assignee, why, nil
}

func tryTeamMentionEvent(raw map[string]interface{}, w http.ResponseWriter) bool {
	if raw["type"] != "event_callback" {
		return false
	}
	ev, _ := raw["event"].(map[string]interface{})
	if ev == nil {
		w.WriteHeader(http.StatusOK)
		return true
	}
	et, _ := ev["type"].(string)
	if et != "message" && et != "app_mention" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	if sub, _ := ev["subtype"].(string); sub != "" && sub != "file_share" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	text, _ := ev["text"].(string)
	if !mentionsTeam(text) {
		w.WriteHeader(http.StatusOK)
		return true
	}
	channel, _ := ev["channel"].(string)
	ts, _ := ev["ts"].(string)
	author, _ := ev["user"].(string)
	if channel == "" || ts == "" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	id, assignee, why, err := createIncidentFromTeamMention(channel, ts, author, text)
	if err != nil {
		log.Printf("slack team mention: %v", err)
		w.WriteHeader(http.StatusOK)
		return true
	}
	log.Printf("slack team mention incident=%d assignee=%q why=%s", id, assignee, why)
	w.WriteHeader(http.StatusOK)
	return true
}
