package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Slack Events API: message that mentions @devops-team (usergroup) becomes an incident.
// Assignee: today's primary if they have no «В роботі» incident, else backup, else empty (admin queue).
//
// Env:
//   SLACK_TEAM_SUBTEAM=S03QEQF27AN
//   SLACK_TEAM_HANDLE=devops-team
// Bot must be in the channel. Event Subscriptions: message.channels, message.groups.
// Request URL: https://s.ks.tv:85/api/webhooks/slack

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

func mentionsTeam(text string) bool {
	t := text
	low := strings.ToLower(t)
	handle := strings.ToLower(teamHandle())
	if strings.Contains(low, "@"+handle) || strings.Contains(low, "<!subteam^"+strings.ToLower(teamSubteamID())) {
		return true
	}
	if strings.Contains(t, "<!subteam^"+teamSubteamID()) {
		return true
	}
	return false
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

// pickOncallAssignee — primary if free, else backup if free, else "" (admin distributes).
func pickOncallAssignee() (assignee, reason string) {
	p, b := todayShiftPair()
	pBusy := inProgressIncidentCount(p) > 0
	bBusy := inProgressIncidentCount(b) > 0
	switch {
	case p != "" && !pBusy:
		return p, "primary free"
	case b != "" && !bBusy:
		return b, "backup free"
	case p == "" && b == "":
		return "", "no shift"
	default:
		return "", "both in progress"
	}
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
	assignee, why := pickOncallAssignee()
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

// tryTeamMentionEvent handles Slack event_callback. Returns true if the response was written.
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
