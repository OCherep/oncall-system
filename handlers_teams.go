package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func ensureTeamsSchema() {
	if db == nil {
		return
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS teams (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL,
		code TEXT DEFAULT '',
		is_default INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`ALTER TABLE users ADD COLUMN team_id INTEGER DEFAULT 0`)
	db.Exec(`ALTER TABLE incidents ADD COLUMN team_id INTEGER DEFAULT 0`)
	db.Exec(`ALTER TABLE incidents ADD COLUMN directed_to TEXT DEFAULT ''`)
	db.Exec(`ALTER TABLE incidents ADD COLUMN directed_scope TEXT DEFAULT 'team'`)
	db.Exec(`ALTER TABLE daily_tasks ADD COLUMN team_id INTEGER DEFAULT 0`)

	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&n)
	if n == 0 {
		db.Exec(`INSERT INTO teams (name, code, is_default) VALUES ('DevOps', 'devops', 1)`)
	}
	// attach users without team to default
	var defID int
	_ = db.QueryRow(`SELECT id FROM teams WHERE is_default=1 ORDER BY id LIMIT 1`).Scan(&defID)
	if defID == 0 {
		_ = db.QueryRow(`SELECT id FROM teams ORDER BY id LIMIT 1`).Scan(&defID)
	}
	if defID > 0 {
		db.Exec(`UPDATE users SET team_id=? WHERE COALESCE(team_id,0)=0`, defID)
		db.Exec(`UPDATE incidents SET team_id=? WHERE COALESCE(team_id,0)=0`, defID)
		db.Exec(`UPDATE daily_tasks SET team_id=? WHERE COALESCE(team_id,0)=0`, defID)
	}
}

func defaultTeamID() int {
	var id int
	_ = db.QueryRow(`SELECT id FROM teams WHERE is_default=1 ORDER BY id LIMIT 1`).Scan(&id)
	if id == 0 {
		_ = db.QueryRow(`SELECT id FROM teams ORDER BY id LIMIT 1`).Scan(&id)
	}
	return id
}

type Team struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Code      string `json:"code"`
	IsDefault bool   `json:"is_default"`
}

func listTeams() []Team {
	rows, err := db.Query(`SELECT id, name, COALESCE(code,''), COALESCE(is_default,0) FROM teams ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var t Team
		var def int
		rows.Scan(&t.ID, &t.Name, &t.Code, &def)
		t.IsDefault = def == 1
		out = append(out, t)
	}
	return out
}

func handleAdminTeams(w http.ResponseWriter, r *http.Request) {
	ensureTeamsSchema()
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		list := listTeams()
		if list == nil {
			list = []Team{}
		}
		json.NewEncoder(w).Encode(list)
	case http.MethodPost:
		var body Team
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" {
			http.Error(w, "name required", 400)
			return
		}
		def := 0
		if body.IsDefault {
			def = 1
			db.Exec(`UPDATE teams SET is_default=0`)
		}
		res, err := db.Exec(`INSERT INTO teams (name, code, is_default) VALUES (?,?,?)`, body.Name, body.Code, def)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		id, _ := res.LastInsertId()
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "id": id})
	case http.MethodPut:
		var body Team
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.ID <= 0 {
			http.Error(w, "id required", 400)
			return
		}
		if body.IsDefault {
			db.Exec(`UPDATE teams SET is_default=0`)
		}
		def := 0
		if body.IsDefault {
			def = 1
		}
		db.Exec(`UPDATE teams SET name=?, code=?, is_default=? WHERE id=?`,
			strings.TrimSpace(body.Name), body.Code, def, body.ID)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		if id <= 0 {
			http.Error(w, "id required", 400)
			return
		}
		var def int
		_ = db.QueryRow(`SELECT COALESCE(is_default,0) FROM teams WHERE id=?`, id).Scan(&def)
		if def == 1 {
			http.Error(w, "не можна видалити команду за замовчуванням", 400)
			return
		}
		var cnt int
		_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE team_id=?`, id).Scan(&cnt)
		if cnt > 0 {
			http.Error(w, "є користувачі в цій команді — спочатку перепризначте", 400)
			return
		}
		db.Exec(`DELETE FROM teams WHERE id=?`, id)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// GET /api/teams — public list (id, name) for forms
func handleTeamsPublic(w http.ResponseWriter, r *http.Request) {
	ensureTeamsSchema()
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	json.NewEncoder(w).Encode(listTeams())
}
