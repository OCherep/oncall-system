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


// handleJiraImport — POST {jql?, max?} → upsert daily_tasks by external_id (issue key).
func handleJiraImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	if !jiraEnabled() {
		http.Error(w, `{"error":"Jira disabled; set JIRA_ENABLED=1 and credentials"}`, 400)
		return
	}
	var req struct {
		JQL string `json:"jql"`
		Max int    `json:"max"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Max <= 0 || req.Max > 100 {
		req.Max = 50
	}
	jql := strings.TrimSpace(req.JQL)
	if jql == "" {
		jql = strings.TrimSpace(getSetting("jira_jql", ""))
	}
	if jql == "" {
		jql = strings.TrimSpace(os.Getenv("JIRA_JQL_FILTER"))
	}
	if jql == "" {
		jql = `project in (Vidmind) AND (component = DevOps OR labels = DevOps) AND issuetype in (Epic, Story, Task, Sub-task, Bug) AND updated >= -60d ORDER BY updated DESC`
	}

	issues, err := jiraSearchIssues(jql, req.Max)
	if err != nil {
		log.Printf("jira import search: %v", err)
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 502)
		return
	}

	created, updated, skipped := 0, 0, 0
	today := time.Now().Format("2006-01-02")
	for _, is := range issues {
		key := strings.TrimSpace(is.Key)
		if key == "" {
			skipped++
			continue
		}
		summary := is.Fields.Summary
		if summary == "" {
			summary = key
		}
		statusLocal := mapJiraStatusToLocal(is.Fields.Status.Name)
		if statusLocal == "" {
			statusLocal = "Нова"
		}
		// map incident-like status to task status; нові імпорти — «Нерозподілена» до дейлі
		taskStatus := mapIncStatusToTask(statusLocal)
		if taskStatus == "Нова" || taskStatus == "" {
			taskStatus = "Нерозподілена"
		}
		prioName := ""
		if is.Fields.Priority != nil {
			prioName = is.Fields.Priority.Name
		}
		prio := mapJiraPriority(prioName)
		assignee := ""
		if is.Fields.Assignee != nil {
			assignee = firstNonEmpty(is.Fields.Assignee.DisplayName, is.Fields.Assignee.Name)
		}
		desc := fmt.Sprintf("[Jira %s] %s", key, summary)

		var existingID int
		err := db.QueryRow(`SELECT id FROM daily_tasks WHERE external_id=? LIMIT 1`, key).Scan(&existingID)
		if err == nil && existingID > 0 {
			due := strings.TrimSpace(is.Fields.Duedate)
			if len(due) >= 10 {
				due = due[:10]
			}
			db.Exec(`UPDATE daily_tasks SET task_description=?, priority=?, user_name=COALESCE(NULLIF(?,''), user_name), due_date=COALESCE(NULLIF(?,''), due_date) WHERE id=?`,
				desc, prio, assignee, due, existingID)
			addSystemComment("task", existingID, "Оновлено з Jira import")
			updated++
			continue
		}
		due := strings.TrimSpace(is.Fields.Duedate)
		if len(due) >= 10 {
			due = due[:10]
		}
		res, err := db.Exec(`INSERT INTO daily_tasks (user_name, date, task_description, status, priority, total_minutes, created_at, created_by, external_id, due_date)
			VALUES (?,?,?,?,?,0,CURRENT_TIMESTAMP,'jira',?,?)`,
			assignee, today, desc, taskStatus, prio, key, due)
		if err != nil {
			log.Printf("jira import insert %s: %v", key, err)
			skipped++
			continue
		}
		tid, _ := res.LastInsertId()
		addSystemComment("task", int(tid), "Імпортовано з Jira: "+key)
		created++
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"jql":     jql,
		"found":   len(issues),
		"created": created,
		"updated": updated,
		"skipped": skipped,
	})
}

type jiraSearchIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary  string `json:"summary"`
		Status   struct {
			Name string `json:"name"`
		} `json:"status"`
		Priority *struct {
			Name string `json:"name"`
		} `json:"priority"`
		Assignee *struct {
			DisplayName string `json:"displayName"`
			Name        string `json:"name"`
		} `json:"assignee"`
		Duedate string `json:"duedate"`
		Updated string `json:"updated"`
	} `json:"fields"`
}

func jiraSearchIssues(jql string, max int) ([]jiraSearchIssue, error) {
	// POST /rest/api/2/search
	url := jiraBaseURL() + "/rest/api/2/search"
	payload, _ := json.Marshal(map[string]interface{}{
		"jql":        jql,
		"maxResults": max,
		"fields":     []string{"summary", "status", "priority", "assignee", "duedate", "updated"},
	})
	req, err := jiraNewRequest(http.MethodPost, url, payload)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("jira search %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		Issues []jiraSearchIssue `json:"issues"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Issues, nil
}
