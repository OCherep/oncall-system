package main

// Щодня синхронізація Slack user group @oncall:
// у складі лише сьогоднішні основний і дублюючий.
// Перевірка кожні 2 хвилини; запис у Slack лише якщо склад змінився
// (новий день або заміна чергового).
//
// Env:
//   SLACK_BOT_TOKEN          — той самий bot token (потрібні usergroups:read + usergroups:write)
//   SLACK_ONCALL_GROUP_SYNC  — 0/false вимикає (за замовчуванням увімкнено)
//   SLACK_ONCALL_HANDLE      — handle групи без @ (за замовчуванням oncall)
//   SLACK_ONCALL_USERGROUP   — опційно ID групи (S…), щоб не шукати за handle
//
// Вимикач без редеплою: app_settings slack_oncall_group_sync=0.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

func init() {
	go func() {
		for db == nil {
			time.Sleep(2 * time.Second)
		}
		time.Sleep(10 * time.Second)
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		maybeSyncSlackOncallGroup()
		for range t.C {
			maybeSyncSlackOncallGroup()
		}
	}()
}

func slackOncallGroupEnabled() bool {
	v := strings.TrimSpace(os.Getenv("SLACK_ONCALL_GROUP_SYNC"))
	if v == "0" || strings.EqualFold(v, "false") || strings.EqualFold(v, "off") {
		return false
	}
	return settingOn("slack_oncall_group_sync", "1")
}

func oncallGroupHandle() string {
	h := strings.TrimSpace(os.Getenv("SLACK_ONCALL_HANDLE"))
	if h == "" {
		h = getSetting("slack_oncall_handle", "oncall")
	}
	h = strings.TrimPrefix(strings.TrimSpace(h), "@")
	if h == "" {
		h = "oncall"
	}
	return h
}

func maybeSyncSlackOncallGroup() {
	if db == nil || !slackOncallGroupEnabled() {
		return
	}
	token := slackBotToken()
	if token == "" {
		return
	}
	day := kyivToday()
	p, b := todayShiftPair()
	var names []string
	if p != "" {
		names = append(names, p)
	}
	if b != "" && !strings.EqualFold(b, p) {
		names = append(names, b)
	}
	var ids, missing []string
	seen := map[string]bool{}
	for _, n := range names {
		id := userSlackID(n)
		if id == "" {
			missing = append(missing, n)
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	want := strings.Join(ids, ",")
	handle := oncallGroupHandle()
	if want == "" {
		if getSetting("slack_oncall_group_empty", "") != day {
			setSetting("slack_oncall_group_empty", day)
			log.Printf("slack @%s: немає slack_id для чергових %s/%s, склад не змінено", handle, p, b)
		}
		return
	}
	mark := day + "|" + want
	if getSetting("slack_oncall_group_last", "") == mark {
		return
	}
	gid := strings.TrimSpace(os.Getenv("SLACK_ONCALL_USERGROUP"))
	if gid == "" {
		gid = getSetting("slack_oncall_usergroup_id", "")
	}
	if gid == "" {
		var err error
		gid, err = slackFindUsergroup(token, handle)
		if err != nil {
			log.Printf("slack @%s: не знайдено групу: %v", handle, err)
			return
		}
		setSetting("slack_oncall_usergroup_id", gid)
	}
	if err := slackSetUsergroupUsers(token, gid, ids); err != nil {
		log.Printf("slack @%s (%s): не оновлено: %v", handle, gid, err)
		if strings.Contains(err.Error(), "subteam_not_found") || strings.Contains(err.Error(), "no_such_subteam") {
			setSetting("slack_oncall_usergroup_id", "")
		}
		return
	}
	setSetting("slack_oncall_group_last", mark)
	log.Printf("slack @%s оновлено на %s: %s (%s)", handle, day, strings.Join(names, ", "), want)
	if len(missing) > 0 {
		log.Printf("slack @%s: немає slack_id у %s", handle, strings.Join(missing, ", "))
	}
}

func slackFindUsergroup(token, handle string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://slack.com/api/usergroups.list", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		Usergroups []struct {
			ID     string `json:"id"`
			Handle string `json:"handle"`
		} `json:"usergroups"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if !out.OK {
		return "", fmt.Errorf("%s", out.Error)
	}
	h := strings.ToLower(strings.TrimPrefix(handle, "@"))
	for _, g := range out.Usergroups {
		if strings.ToLower(g.Handle) == h {
			return g.ID, nil
		}
	}
	return "", fmt.Errorf("handle %q not in workspace", handle)
}

func slackSetUsergroupUsers(token, groupID string, users []string) error {
	body, _ := json.Marshal(map[string]string{
		"usergroup": groupID,
		"users":     strings.Join(users, ","),
	})
	req, err := http.NewRequest(http.MethodPost, "https://slack.com/api/usergroups.users.update", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("%s", out.Error)
	}
	return nil
}
