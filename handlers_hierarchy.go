package main

import (
	"fmt"
	"strings"
)

// Jira-aligned issue types (локальна модель для майбутнього sync).
var validIssueTypes = map[string]bool{
	"Epic": true, "Story": true, "Task": true, "Sub-task": true, "Bug": true,
}

func ensureTaskHierarchyColumns() {
	if db == nil {
		return
	}
	for _, c := range []string{
		"issue_type TEXT DEFAULT 'Task'",
		"parent_id INTEGER DEFAULT 0",
		"epic_id INTEGER DEFAULT 0",
		"external_id TEXT DEFAULT ''",
		"source TEXT DEFAULT ''",
	} {
		db.Exec("ALTER TABLE daily_tasks ADD COLUMN " + c)
	}
}

func normalizeIssueType(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Task"
	}
	// common aliases
	low := strings.ToLower(s)
	switch low {
	case "epic":
		return "Epic"
	case "story":
		return "Story"
	case "task", "задача":
		return "Task"
	case "sub-task", "subtask", "sub_task", "підзадача":
		return "Sub-task"
	case "bug", "баг":
		return "Bug"
	}
	if validIssueTypes[s] {
		return s
	}
	return "Task"
}

func defaultIssueTypeFromIncident(inc IncidentReport) string {
	p := strings.ToLower(inc.Priority)
	if strings.Contains(p, "крит") || strings.Contains(p, "термін") || strings.Contains(p, "urgent") {
		return "Bug"
	}
	return "Task"
}

// validateHierarchy — soft rules (admin may force later).
func validateHierarchy(issueType string, parentID int) error {
	issueType = normalizeIssueType(issueType)
	if issueType == "Sub-task" && parentID <= 0 {
		return fmt.Errorf("Sub-task потребує parent_id")
	}
	if issueType == "Epic" && parentID > 0 {
		return fmt.Errorf("Epic не може мати parent_id")
	}
	if parentID > 0 {
		var ptype string
		err := db.QueryRow(`SELECT COALESCE(issue_type,'Task') FROM daily_tasks WHERE id=?`, parentID).Scan(&ptype)
		if err != nil {
			return fmt.Errorf("parent_id #%d не знайдено", parentID)
		}
		ptype = normalizeIssueType(ptype)
		if ptype == "Sub-task" {
			return fmt.Errorf("батько не може бути Sub-task")
		}
	}
	return nil
}
