package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// handleSlackDirectory — GET list workspace members; POST import selected into users.
func handleSlackDirectory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	token := slackBotToken()
	if token == "" {
		http.Error(w, `{"error":"SLACK_BOT_TOKEN not set"}`, 400)
		return
	}
	switch r.Method {
	case http.MethodGet:
		members, err := slackFetchUsers(token)
		if err != nil {
			log.Printf("slack directory: %v", err)
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 502)
			return
		}
		// mark already linked
		existing := map[string]int{}
		rows, _ := db.Query(`SELECT id, COALESCE(slack_id,''), COALESCE(email,''), COALESCE(username,'') FROM users`)
		if rows != nil {
			for rows.Next() {
				var id int
				var sid, email, uname string
				rows.Scan(&id, &sid, &email, &uname)
				if sid != "" {
					existing["id:"+sid] = id
				}
				if email != "" {
					existing["email:"+strings.ToLower(email)] = id
				}
				if uname != "" {
					existing["user:"+strings.ToLower(uname)] = id
				}
			}
			rows.Close()
		}
		out := make([]map[string]interface{}, 0, len(members))
		for _, m := range members {
			item := map[string]interface{}{
				"slack_id":     m.ID,
				"name":         m.DisplayName(),
				"real_name":    m.RealName(),
				"username":     m.SuggestedUsername(),
				"email":        m.Email(),
				"phone":        m.Phone(),
				"title":        m.Title(),
				"is_bot":       m.IsBot,
				"is_admin":     m.IsAdmin,
				"deleted":      m.Deleted,
				"already_id":   0,
				"already_link": false,
			}
			if id, ok := existing["id:"+m.ID]; ok {
				item["already_id"] = id
				item["already_link"] = true
			} else if em := m.Email(); em != "" {
				if id, ok := existing["email:"+strings.ToLower(em)]; ok {
					item["already_id"] = id
					item["already_link"] = true
				}
			}
			out = append(out, item)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"count":   len(out),
			"members": out,
		})
	case http.MethodPost:
		var req struct {
			Members []struct {
				SlackID  string `json:"slack_id"`
				Name     string `json:"name"`
				Username string `json:"username"`
				Email    string `json:"email"`
				Phone    string `json:"phone"`
				Title    string `json:"title"`
				IsOncall bool   `json:"is_oncall"`
				Role     string `json:"role"`
				Password string `json:"password"`
				Update   bool   `json:"update"` // update existing by slack_id
			} `json:"members"`
			DefaultPassword string `json:"default_password"`
			DefaultOncall   *bool  `json:"default_oncall"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if len(req.Members) == 0 {
			http.Error(w, "members required", 400)
			return
		}
		defPass := strings.TrimSpace(req.DefaultPassword)
		if defPass == "" {
			defPass = strings.TrimSpace(os.Getenv("SLACK_IMPORT_DEFAULT_PASSWORD"))
		}
		if defPass == "" {
			defPass = "ChangeMe1!"
		}
		defOn := true
		if req.DefaultOncall != nil {
			defOn = *req.DefaultOncall
		}
		created, updated, skipped := 0, 0, 0
		for _, m := range req.Members {
			sid := strings.TrimSpace(m.SlackID)
			if sid == "" {
				skipped++
				continue
			}
			name := strings.TrimSpace(m.Name)
			if name == "" {
				name = strings.TrimSpace(m.Username)
			}
			uname := strings.TrimSpace(m.Username)
			if uname == "" {
				uname = slugUsername(name)
			}
			role := m.Role
			if role == "" {
				role = "user"
			}
			on := 0
			if m.IsOncall || defOn {
				on = 1
			}
			pass := m.Password
			if pass == "" {
				pass = defPass
			}
			var existingID int
			_ = db.QueryRow(`SELECT id FROM users WHERE slack_id=? LIMIT 1`, sid).Scan(&existingID)
			if existingID == 0 && m.Email != "" {
				_ = db.QueryRow(`SELECT id FROM users WHERE lower(email)=lower(?) LIMIT 1`, m.Email).Scan(&existingID)
			}
			if existingID > 0 {
				if m.Update || true { // always refresh contact fields
					db.Exec(`UPDATE users SET name=COALESCE(NULLIF(?,''), name),
						email=COALESCE(NULLIF(?,''), email),
						phone=COALESCE(NULLIF(?,''), phone),
						slack_id=?, is_oncall=CASE WHEN is_oncall=1 THEN 1 ELSE ? END
						WHERE id=?`,
						name, m.Email, m.Phone, sid, on, existingID)
					updated++
				} else {
					skipped++
				}
				continue
			}
			// unique username
			base := uname
			for i := 0; i < 20; i++ {
				var cnt int
				db.QueryRow(`SELECT COUNT(*) FROM users WHERE username=?`, uname).Scan(&cnt)
				if cnt == 0 {
					break
				}
				uname = fmt.Sprintf("%s%d", base, i+2)
			}
			_, err := db.Exec(`INSERT INTO users (username, password, name, role, team_role_id, is_oncall, slack_id, email, phone, show_in_roster)
				VALUES (?,?,?,?,NULL,?,?,?,?,?)`,
				uname, pass, name, role, on, sid, m.Email, m.Phone, on)
			if err != nil {
				log.Printf("slack import insert %s: %v", uname, err)
				skipped++
				continue
			}
			created++
			logAudit("admin", "SLACK_IMPORT_USER", clientIP(r), fmt.Sprintf("%s %s", uname, sid))
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"created": created,
			"updated": updated,
			"skipped": skipped,
		})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

type slackMember struct {
	ID      string `json:"id"`
	Name    string `json:"name"` // legacy username
	Deleted bool   `json:"deleted"`
	IsBot   bool   `json:"is_bot"`
	IsAdmin bool   `json:"is_admin"`
	Profile struct {
		RealName      string `json:"real_name"`
		DisplayName   string `json:"display_name"`
		Email         string `json:"email"`
		Phone         string `json:"phone"`
		Title         string `json:"title"`
		Image48       string `json:"image_48"`
	} `json:"profile"`
}

func (m slackMember) DisplayName() string {
	if s := strings.TrimSpace(m.Profile.DisplayName); s != "" {
		return s
	}
	if s := strings.TrimSpace(m.Profile.RealName); s != "" {
		return s
	}
	return m.Name
}
func (m slackMember) RealName() string {
	if s := strings.TrimSpace(m.Profile.RealName); s != "" {
		return s
	}
	return m.DisplayName()
}
func (m slackMember) Email() string  { return strings.TrimSpace(m.Profile.Email) }
func (m slackMember) Phone() string  { return strings.TrimSpace(m.Profile.Phone) }
func (m slackMember) Title() string  { return strings.TrimSpace(m.Profile.Title) }
func (m slackMember) SuggestedUsername() string {
	base := m.Name
	if base == "" {
		base = m.DisplayName()
	}
	return slugUsername(base)
}

func slugUsername(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else if r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) {
			b.WriteRune('.')
		}
	}
	out := b.String()
	out = regexp.MustCompile(`\.+`).ReplaceAllString(out, ".")
	out = strings.Trim(out, ".")
	if out == "" {
		out = "user"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

func slackFetchUsers(token string) ([]slackMember, error) {
	var all []slackMember
	cursor := ""
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		u := "https://slack.com/api/users.list?limit=200"
		if cursor != "" {
			u += "&cursor=" + url.QueryEscape(cursor)
		}
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		var out struct {
			OK       bool          `json:"ok"`
			Error    string        `json:"error"`
			Members  []slackMember `json:"members"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		if !out.OK {
			return nil, fmt.Errorf("slack users.list: %s", out.Error)
		}
		for _, m := range out.Members {
			if m.Deleted || m.IsBot || m.ID == "USLACKBOT" {
				continue
			}
			all = append(all, m)
		}
		cursor = strings.TrimSpace(out.ResponseMetadata.NextCursor)
		if cursor == "" {
			break
		}
	}
	return all, nil
}


// handleSlackLookup — GET ?q=U123|@name → profile (name, email, phone, slack_id)
func handleSlackLookup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	token := slackBotToken()
	if token == "" {
		http.Error(w, `{"error":"SLACK_BOT_TOKEN not set"}`, 400)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		q = strings.TrimSpace(r.URL.Query().Get("slack"))
	}
	if q == "" {
		http.Error(w, `{"error":"q required"}`, 400)
		return
	}
	m, err := slackResolveUser(token, q)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q,"found":false}`, err.Error()), 404)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"found":      true,
		"slack_id":   m.ID,
		"name":       m.DisplayName(),
		"real_name":  m.RealName(),
		"username":   m.Name,
		"email":      m.Email(),
		"phone":      m.Phone(),
		"title":      m.Title(),
	})
}

func slackResolveUser(token, q string) (*slackMember, error) {
	q = strings.TrimSpace(q)
	q = strings.TrimPrefix(q, "@")
	// User ID
	if len(q) > 0 && (q[0] == 'U' || q[0] == 'W') && !strings.Contains(q, " ") {
		m, err := slackUserInfo(token, q)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
	// email
	if strings.Contains(q, "@") && strings.Contains(q, ".") {
		m, err := slackLookupByEmail(token, q)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
	// match username / display name via users.list
	members, err := slackFetchUsers(token)
	if err != nil {
		return nil, err
	}
	ql := strings.ToLower(q)
	for i := range members {
		m := &members[i]
		if strings.ToLower(m.Name) == ql {
			return m, nil
		}
		if strings.ToLower(m.Profile.DisplayName) == ql {
			return m, nil
		}
		if strings.ToLower(m.Profile.RealName) == ql {
			return m, nil
		}
	}
	// partial display name
	for i := range members {
		m := &members[i]
		if strings.Contains(strings.ToLower(m.Profile.DisplayName), ql) ||
			strings.Contains(strings.ToLower(m.Profile.RealName), ql) {
			return m, nil
		}
	}
	return nil, fmt.Errorf("користувача Slack «%s» не знайдено", q)
}

func slackUserInfo(token, userID string) (*slackMember, error) {
	u := "https://slack.com/api/users.info?user=" + url.QueryEscape(userID)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		OK    bool        `json:"ok"`
		Error string      `json:"error"`
		User  slackMember `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("slack: %s", out.Error)
	}
	if out.User.Deleted || out.User.IsBot {
		return nil, fmt.Errorf("користувач недоступний")
	}
	return &out.User, nil
}

func slackLookupByEmail(token, email string) (*slackMember, error) {
	u := "https://slack.com/api/users.lookupByEmail?email=" + url.QueryEscape(email)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		OK    bool        `json:"ok"`
		Error string      `json:"error"`
		User  slackMember `json:"user"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("slack: %s", out.Error)
	}
	return &out.User, nil
}
