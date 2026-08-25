package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Двосторонній Jira sync (outbound: статус звернення → transition/comment у тікеті).
//
// Env:
//   JIRA_BASE_URL=https://your-domain.atlassian.net
//   JIRA_EMAIL=bot@company.com          // для Cloud Basic auth
//   JIRA_API_TOKEN=...                  // Atlassian API token
//   JIRA_ENABLED=1                      // явний увімкнення
//
// Мапінг статусів On-Call → назва transition у Jira (можна перевизначити через env JSON):
//   JIRA_STATUS_MAP={"Нове":"Open","В роботі":"In Progress","На паузі":"On Hold","Вирішено":"Done","Архів":"Done"}

func jiraEnabled() bool {
	v := strings.TrimSpace(os.Getenv("JIRA_ENABLED"))
	if v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	if v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	// auto: credentials present
	return jiraBaseURL() != "" && jiraEmail() != "" && jiraAPIToken() != ""
}

func jiraBaseURL() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("JIRA_BASE_URL")), "/")
}
func jiraEmail() string    { return strings.TrimSpace(os.Getenv("JIRA_EMAIL")) }
func jiraAPIToken() string { return strings.TrimSpace(os.Getenv("JIRA_API_TOKEN")) }

func defaultJiraStatusMap() map[string]string {
	return map[string]string{
		"Нове":     "Open",
		"В роботі": "In Progress",
		"На паузі": "On Hold",
		"Вирішено": "Done",
		"Архів":    "Done",
	}
}

func jiraStatusMap() map[string]string {
	m := defaultJiraStatusMap()
	raw := strings.TrimSpace(os.Getenv("JIRA_STATUS_MAP"))
	if raw == "" {
		return m
	}
	var override map[string]string
	if err := json.Unmarshal([]byte(raw), &override); err != nil {
		log.Printf("jira: bad JIRA_STATUS_MAP: %v", err)
		return m
	}
	for k, v := range override {
		m[k] = v
	}
	return m
}

// syncIncidentStatusToJira — викликати після успішної зміни статусу incident з external_id.
func syncIncidentStatusToJira(externalID, oldStatus, newStatus string) {
	if !jiraEnabled() || strings.TrimSpace(externalID) == "" || oldStatus == newStatus {
		return
	}
	go func() {
		if err := jiraTransitionAndComment(externalID, oldStatus, newStatus); err != nil {
			log.Printf("jira sync %s: %v", externalID, err)
			return
		}
		log.Printf("jira sync %s: %s → %s ok", externalID, oldStatus, newStatus)
	}()
}

func jiraTransitionAndComment(issueKey, oldStatus, newStatus string) error {
	// 1) Comment always
	comment := fmt.Sprintf("On-Call: статус змінено «%s» → «%s»", oldStatus, newStatus)
	if err := jiraAddComment(issueKey, comment); err != nil {
		log.Printf("jira comment %s: %v", issueKey, err)
		// continue to transition
	}

	// 2) Try transition by mapped name
	targetName := jiraStatusMap()[newStatus]
	if targetName == "" {
		return nil // no mapping — comment only
	}
	return jiraDoTransition(issueKey, targetName)
}

func jiraAddComment(issueKey, body string) error {
	url := fmt.Sprintf("%s/rest/api/2/issue/%s/comment", jiraBaseURL(), issueKey)
	payload, _ := json.Marshal(map[string]string{"body": body})
	return jiraDo(http.MethodPost, url, payload)
}

func jiraDoTransition(issueKey, targetStatusName string) error {
	// GET available transitions
	url := fmt.Sprintf("%s/rest/api/2/issue/%s/transitions", jiraBaseURL(), issueKey)
	req, err := jiraNewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("transitions status %d: %s", resp.StatusCode, string(b))
	}
	var data struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}
	target := strings.ToLower(strings.TrimSpace(targetStatusName))
	var tid string
	for _, t := range data.Transitions {
		if strings.ToLower(t.Name) == target || strings.ToLower(t.To.Name) == target {
			tid = t.ID
			break
		}
		// partial match
		if strings.Contains(strings.ToLower(t.Name), target) || strings.Contains(strings.ToLower(t.To.Name), target) {
			tid = t.ID
			break
		}
	}
	if tid == "" {
		return fmt.Errorf("no transition matching %q among %d options", targetStatusName, len(data.Transitions))
	}
	body, _ := json.Marshal(map[string]interface{}{
		"transition": map[string]string{"id": tid},
	})
	return jiraDo(http.MethodPost, url, body)
}

func jiraNewRequest(method, url string, body []byte) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(jiraEmail(), jiraAPIToken())
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func jiraDo(method, url string, body []byte) error {
	req, err := jiraNewRequest(method, url, body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("jira %s %s → %d: %s", method, url, resp.StatusCode, string(b))
	}
	return nil
}
