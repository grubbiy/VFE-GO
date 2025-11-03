package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// ----------------------- CONFIG + STRUCTS -----------------------

type Config struct {
	AppName   string `json:"appName"`
	Port      int    `json:"port"`
	DBPath    string `json:"dbPath"`
	JWTSecret string `json:"jwtSecret"`
}

type Server struct {
	cfg    Config
	db     *sql.DB
	jwtKey []byte
}

type userCtxKey struct{}

type userInfo struct {
	ID   int64
	Role string
}

// ----------------------- MAIN -----------------------

func main() {
	cfg, err := loadConfig("config.json")
	if err != nil {
		log.Fatal("Failed to load config:", err)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		log.Fatal("DB open error:", err)
	}
	defer db.Close()

	key, _ := base64.RawURLEncoding.DecodeString(cfg.JWTSecret)
	srv := &Server{cfg: cfg, db: db, jwtKey: key}

	// --- Auto scan on startup ---
	fmt.Println("🔍 Scanning for VODs...")
	if err := srv.ScanStorage(); err != nil {
		log.Println("Scan error:", err)
	}
	fmt.Println("✅ Scan complete.")

	// ----------------------- STATIC FILES -----------------------
	// Serve web directory as /web/
	fs := http.FileServer(http.Dir("web"))
	http.Handle("/web/", withCorrectMime(http.StripPrefix("/web/", fs)))

	// Redirect root URL to dashboard.html automatically
	http.Handle("/", withCorrectMime(http.FileServer(http.Dir("web"))))

	// Serve video storage
	http.Handle("/vods/", http.StripPrefix("/vods/", http.FileServer(http.Dir("storage"))))

	// ----------------------- API ROUTES -----------------------
	http.HandleFunc("/api/health", srv.health)
	http.HandleFunc("/api/login", srv.login)
	http.HandleFunc("/api/notes/add", srv.auth(srv.addNote))

	http.HandleFunc("/api/notes", srv.auth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			srv.listNotes(w, r)
		case http.MethodPost:
			srv.saveNotes(w, r)
		case http.MethodDelete:
			srv.deleteNotes(w, r)
		default:
			http.Error(w, "method not allowed", 405)
		}
	}))

	http.HandleFunc("/api/admin/add-team", srv.auth(srv.addTeam))
	http.HandleFunc("/api/admin/add-player", srv.auth(srv.addPlayer))
	http.HandleFunc("/api/list-vods", srv.auth(srv.listVods))
	http.HandleFunc("/api/admin/add-user", srv.auth(srv.addUser))
	http.HandleFunc("/api/teams", srv.auth(srv.listTeams))
	http.HandleFunc("/api/players", srv.auth(srv.listPlayers))

	// ----------------------- START SERVER -----------------------
	addr := fmt.Sprintf(":%d", cfg.Port)
	fmt.Printf("🚀 VFE server started on http://localhost%s\n", addr)
	fmt.Println("Press Ctrl+C to stop.")
	log.Fatal(http.ListenAndServe(addr, nil))
}

// ----------------------- ROUTES -----------------------

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"ok":   true,
		"time": time.Now().Format(time.RFC3339),
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}

	var id int64
	var hash, role string
	err := s.db.QueryRow(`SELECT id, password_hash, role FROM users WHERE username=?`, req.Username).Scan(&id, &hash, &role)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil {
		http.Error(w, "invalid credentials", 401)
		return
	}

	claims := jwt.MapClaims{
		"sub":  id,
		"role": role,
		"usr":  req.Username,
		"exp":  time.Now().Add(24 * time.Hour).Unix(),
	}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtKey)
	writeJSON(w, 200, map[string]any{"token": token})
}

// ----------------------- MIME FIX -----------------------

func withCorrectMime(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "application/javascript")
		} else if strings.HasSuffix(r.URL.Path, ".css") {
			w.Header().Set("Content-Type", "text/css")
		} else if strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		next.ServeHTTP(w, r)
	})
}

// ----------------------- FEATURES -----------------------

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	userID, _ := userFrom(r.Context())

	var body struct {
		VodID     int64   `json:"vod_id"`
		TsSeconds float64 `json:"ts_seconds"`
		Content   string  `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		http.Error(w, "empty note", 400)
		return
	}
	_, err := s.db.Exec(`INSERT INTO notes (vod_id, user_id, ts_seconds, content) VALUES (?,?,?,?)`,
		body.VodID, userID, body.TsSeconds, body.Content)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// ----------------------- NOTE SYSTEM -----------------------

func (s *Server) listNotes(w http.ResponseWriter, r *http.Request) {
	vodID := r.URL.Query().Get("vod_id")
	if vodID == "" {
		http.Error(w, "missing vod_id", 400)
		return
	}

	rows, err := s.db.Query(`SELECT ts_seconds, content FROM notes WHERE vod_id = ? ORDER BY ts_seconds`, vodID)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	defer rows.Close()

	type Note struct {
		Time float64 `json:"ts_seconds"`
		Text string  `json:"content"`
	}
	var notes []Note
	for rows.Next() {
		var n Note
		rows.Scan(&n.Time, &n.Text)
		notes = append(notes, n)
	}

	writeJSON(w, 200, notes)
}

func (s *Server) saveNotes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}

	userID, _ := userFrom(r.Context())

	var body struct {
		VodID int64 `json:"vod_id"`
		Notes []struct {
			TsSeconds float64 `json:"ts_seconds"`
			Content   string  `json:"content"`
		} `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", 400)
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	defer tx.Rollback()

	_, err = tx.Exec(`DELETE FROM notes WHERE vod_id = ? AND user_id = ?`, body.VodID, userID)
	if err != nil {
		http.Error(w, "delete error", 500)
		return
	}

	for _, n := range body.Notes {
		if strings.TrimSpace(n.Content) == "" {
			continue
		}
		_, err := tx.Exec(`INSERT INTO notes (vod_id, user_id, ts_seconds, content) VALUES (?, ?, ?, ?)`,
			body.VodID, userID, n.TsSeconds, n.Content)
		if err != nil {
			http.Error(w, "insert error", 500)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "commit error", 500)
		return
	}

	writeJSON(w, 200, map[string]string{"status": "saved"})
}

func (s *Server) deleteNotes(w http.ResponseWriter, r *http.Request) {
	vodID := r.URL.Query().Get("vod_id")
	if vodID == "" {
		http.Error(w, "missing vod_id", 400)
		return
	}
	userID, _ := userFrom(r.Context())

	_, err := s.db.Exec(`DELETE FROM notes WHERE vod_id = ? AND user_id = ?`, vodID, userID)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}

	writeJSON(w, 200, map[string]string{"deleted": "true"})
}

func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT id, name FROM teams ORDER BY name`)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	defer rows.Close()
	type Team struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	var teams []Team
	for rows.Next() {
		var t Team
		rows.Scan(&t.ID, &t.Name)
		teams = append(teams, t)
	}
	writeJSON(w, 200, teams)
}

func (s *Server) listPlayers(w http.ResponseWriter, r *http.Request) {
	teamID := r.URL.Query().Get("team_id")
	if teamID == "" {
		http.Error(w, "missing team_id", 400)
		return
	}
	rows, err := s.db.Query(`SELECT id, name FROM players WHERE team_id = ? ORDER BY name`, teamID)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	defer rows.Close()
	type Player struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	var players []Player
	for rows.Next() {
		var p Player
		rows.Scan(&p.ID, &p.Name)
		players = append(players, p)
	}
	writeJSON(w, 200, players)
}

func (s *Server) addUser(w http.ResponseWriter, r *http.Request) {
	role := getRole(r.Context())
	if role != "admin" {
		http.Error(w, "forbidden", 403)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	body.Role = strings.TrimSpace(body.Role)
	if body.Username == "" || body.Password == "" {
		http.Error(w, "missing username/password", 400)
		return
	}
	if body.Role != "admin" && body.Role != "coach" && body.Role != "player" {
		http.Error(w, "invalid role", 400)
		return
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	_, err := s.db.Exec(`INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)`, body.Username, hash, body.Role)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true", "user": body.Username, "role": body.Role})
}

func (s *Server) listVods(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT id, file_path, title FROM vods ORDER BY id DESC`)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	defer rows.Close()
	type Vod struct {
		ID       int64  `json:"id"`
		FilePath string `json:"file_path"`
		Title    string `json:"title"`
	}
	var vods []Vod
	for rows.Next() {
		var v Vod
		rows.Scan(&v.ID, &v.FilePath, &v.Title)
		vods = append(vods, v)
	}
	writeJSON(w, 200, vods)
}

// ----------------------- ADMIN ENDPOINTS -----------------------

func (s *Server) addTeam(w http.ResponseWriter, r *http.Request) {
	role := getRole(r.Context())
	if role != "admin" {
		http.Error(w, "forbidden", 403)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	teamName := strings.TrimSpace(body.Name)
	if teamName == "" {
		http.Error(w, "team name required", 400)
		return
	}

	_, err := s.db.Exec(`INSERT OR IGNORE INTO teams(name) VALUES(?)`, teamName)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}

	path := filepath.Join("storage", "teams", teamName, "players")
	if err := os.MkdirAll(path, 0755); err != nil {
		http.Error(w, "mkdir error", 500)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true", "team": teamName})
}

func (s *Server) addPlayer(w http.ResponseWriter, r *http.Request) {
	role := getRole(r.Context())
	if role != "admin" {
		http.Error(w, "forbidden", 403)
		return
	}
	var body struct {
		Team   string `json:"team"`
		Player string `json:"player"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", 400)
		return
	}

	team := strings.TrimSpace(body.Team)
	player := strings.TrimSpace(body.Player)
	if team == "" || player == "" {
		http.Error(w, "team and player required", 400)
		return
	}

	var teamID int64
	err := s.db.QueryRow(`SELECT id FROM teams WHERE name=?`, team).Scan(&teamID)
	if err == sql.ErrNoRows {
		http.Error(w, "team not found", 404)
		return
	} else if err != nil {
		http.Error(w, "db error", 500)
		return
	}

	_, err = s.db.Exec(`INSERT OR IGNORE INTO players(name, team_id) VALUES(?, ?)`, player, teamID)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}

	path := filepath.Join("storage", "teams", team, "players", player, "vods")
	if err := os.MkdirAll(path, 0755); err != nil {
		http.Error(w, "mkdir error", 500)
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true", "player": player, "team": team})
}

// ----------------------- AUTO-SCAN FEATURE -----------------------

func (s *Server) ScanStorage() error {
	fmt.Println("🔍 Scanning storage folder for VODs...")

	filesOnDisk := make(map[string]bool)
	err := filepath.Walk("storage", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(strings.ToLower(info.Name()), ".mp4") {
			return nil
		}

		rel := filepath.ToSlash(path)
		filesOnDisk[rel] = true

		// Example: storage/teams/TeamTitan/players/Vegard/vods/Skjermopptak1.mp4
		parts := strings.Split(rel, "/")
		if len(parts) < 6 {
			fmt.Println("⚠️ Skipping invalid path:", rel)
			return nil
		}

		teamName := parts[2]
		playerName := parts[4]

		// Get or create team
		var teamID int64
		err = s.db.QueryRow(`SELECT id FROM teams WHERE name = ?`, teamName).Scan(&teamID)
		if err == sql.ErrNoRows {
			res, err := s.db.Exec(`INSERT INTO teams (name) VALUES (?)`, teamName)
			if err != nil {
				return err
			}
			teamID, _ = res.LastInsertId()
			fmt.Println("🧩 Added team:", teamName)
		} else if err != nil {
			return err
		}

		// Get or create player
		var playerID int64
		err = s.db.QueryRow(`SELECT id FROM players WHERE name = ? AND team_id = ?`, playerName, teamID).Scan(&playerID)
		if err == sql.ErrNoRows {
			res, err := s.db.Exec(`INSERT INTO players (name, team_id) VALUES (?, ?)`, playerName, teamID)
			if err != nil {
				return err
			}
			playerID, _ = res.LastInsertId()
			fmt.Println("👤 Added player:", playerName)
		} else if err != nil {
			return err
		}

		// Check if VOD exists
		var count int
		err = s.db.QueryRow(`SELECT COUNT(*) FROM vods WHERE file_path = ?`, rel).Scan(&count)
		if err != nil {
			return err
		}
		if count == 0 {
			_, err = s.db.Exec(`INSERT INTO vods (file_path, title, player_id) VALUES (?, ?, ?)`,
				rel, info.Name(), playerID)
			if err != nil {
				return err
			}
			fmt.Println("📹 Added:", rel)
		}

		return nil
	})
	if err != nil {
		return err
	}

	// Remove missing files
	rows, err := s.db.Query(`SELECT id, file_path FROM vods`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var filePath string
		rows.Scan(&id, &filePath)
		if _, exists := filesOnDisk[filePath]; !exists {
			fmt.Println("🗑 Removing missing VOD from DB:", filePath)
			s.db.Exec(`DELETE FROM vods WHERE id = ?`, id)
		}
	}

	fmt.Println("✅ Scan complete.")
	return nil
}

// ----------------------- AUTH + CONTEXT -----------------------

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			http.Error(w, "missing bearer", 401)
			return
		}
		tokenStr := strings.TrimPrefix(h, "Bearer ")
		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			return s.jwtKey, nil
		})
		if err != nil || !token.Valid {
			http.Error(w, "invalid token", 401)
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, "bad claims", 401)
			return
		}
		id := int64(claims["sub"].(float64))
		role := claims["role"].(string)

		ctx := withUser(r.Context(), id, role)
		next(w, r.WithContext(ctx))
	}
}

func withUser(ctx context.Context, id int64, role string) context.Context {
	return context.WithValue(ctx, userCtxKey{}, userInfo{ID: id, Role: role})
}

func userFrom(ctx context.Context) (int64, string) {
	if u, ok := ctx.Value(userCtxKey{}).(userInfo); ok {
		return u.ID, u.Role
	}
	return 0, ""
}

func getRole(ctx context.Context) string {
	if u, ok := ctx.Value(userCtxKey{}).(userInfo); ok {
		return u.Role
	}
	return ""
}

// ----------------------- HELPERS -----------------------

func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	err = json.Unmarshal(b, &cfg)
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.ToSlash(filepath.Join("db", "vfe.sqlite"))
	}
	return cfg, err
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
