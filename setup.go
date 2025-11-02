package main

import (
	crypto_rand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type Config struct {
	AppName   string `json:"appName"`
	Port      int    `json:"port"`
	DBPath    string `json:"dbPath"`
	JWTSecret string `json:"jwtSecret"`
}

const schemaSQL = `PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT UNIQUE NOT NULL,
  display_name TEXT,
  role TEXT NOT NULL CHECK (role IN ('player','coach','admin')),
  password_hash TEXT NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS teams (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT UNIQUE NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS players (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (team_id, name)
);

CREATE TABLE IF NOT EXISTS memberships (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (user_id, team_id)
);

CREATE TABLE IF NOT EXISTS vods (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  player_id INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
  file_path TEXT NOT NULL,
  title TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS notes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  vod_id INTEGER NOT NULL REFERENCES vods(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  ts_seconds REAL NOT NULL,
  content TEXT NOT NULL,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

// Minimal web placeholders
const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>VFE Login</title>
  <link rel="stylesheet" href="style.css">
</head>
<body>
  <div class="login-container">
    <h1>VodForEsports</h1>
    <form id="loginForm">
      <input type="text" id="username" placeholder="Username" required>
      <input type="password" id="password" placeholder="Password" required>
      <button type="submit">Login</button>
    </form>
    <p id="error" class="error"></p>
  </div>

  <script>
  document.getElementById('loginForm').addEventListener('submit', async e => {
    e.preventDefault();
    const username = document.getElementById('username').value;
    const password = document.getElementById('password').value;
    const res = await fetch('/api/login', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({username, password})
    });
    if (!res.ok) {
      document.getElementById('error').innerText = 'Invalid login';
      return;
    }
    const data = await res.json();
    localStorage.setItem('token', data.token);
    window.location.href = '/dashboard.html';
  });
  </script>
</body>
</html>
`

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>VFE Dashboard</title>
  <link rel="stylesheet" href="style.css">
</head>
<body>
  <header>
    <h2>VodForEsports</h2>
    <button id="logout">Logout</button>
  </header>

  <div class="dashboard">
    <aside id="vodList">
      <h3>VODs</h3>
      <ul id="vodItems"></ul>
    </aside>

    <main>
      <video id="player" controls width="720" height="400">
        <source id="videoSource" src="" type="video/mp4">
        Your browser does not support HTML5 video.
      </video>
    </main>
  </div>

  <script src="app.js"></script>
</body>
</html>
`

const appJS = `async function loadVods() {
  const res = await fetch('/api/health');
  if (!res.ok) {
    alert('Server not responding');
    return;
  }

  // For now, list VODs by scanning storage directly (basic version)
  const vods = await (await fetch('/api/list-vods', {
    headers: { 'Authorization': 'Bearer ' + localStorage.getItem('token') }
  })).json();

  const list = document.getElementById('vodItems');
  list.innerHTML = '';
  vods.forEach(v => {
    const li = document.createElement('li');
    li.textContent = v.title;
    li.addEventListener('click', () => {
      const player = document.getElementById('player');
      player.src = '/' + v.file_path.replace(/\\/g, '/');
      player.play();
    });
    list.appendChild(li);
  });
}

document.getElementById('logout').addEventListener('click', () => {
  localStorage.removeItem('token');
  window.location.href = '/';
});

window.onload = loadVods;
`
const styleCSS = `body {
  margin: 0;
  font-family: sans-serif;
  background-color: #0e0e0e;
  color: #eee;
}

.login-container {
  width: 300px;
  margin: 100px auto;
  text-align: center;
}

.login-container input {
  display: block;
  width: 100%;
  margin: 8px 0;
  padding: 10px;
  border-radius: 6px;
  border: none;
}

button {
  padding: 10px 15px;
  background: #007bff;
  border: none;
  border-radius: 6px;
  color: white;
  cursor: pointer;
}

header {
  background: #111;
  display: flex;
  justify-content: space-between;
  padding: 10px 20px;
}

.dashboard {
  display: flex;
  gap: 20px;
  padding: 20px;
}

aside {
  width: 250px;
  background: #1c1c1c;
  padding: 10px;
  border-radius: 8px;
}

li {
  cursor: pointer;
  margin-bottom: 6px;
  list-style: none;
  padding: 6px;
  border-radius: 4px;
}

li:hover {
  background: #333;
}
  .dashboard {
  display: grid;
  grid-template-columns: 250px 1fr 300px;
  height: calc(100vh - 60px);
  gap: 10px;
  padding: 10px;
}

aside {
  background: #1a1a1a;
  padding: 10px;
  border-radius: 8px;
  overflow-y: auto;
}

main {
  display: flex;
  align-items: center;
  justify-content: center;
}

#notesPanel {
  background: #1a1a1a;
  padding: 10px;
  border-radius: 8px;
  overflow-y: auto;
}

video {
  max-width: 100%;
  border-radius: 10px;
  box-shadow: 0 0 15px rgba(0,0,0,0.5);
}

`

// Utility functions
func must(err error) {
	if err != nil {
		panic(err)
	}
}

func randString(n int) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%*-_"
	var sb strings.Builder
	for i := 0; i < n; i++ {
		num, err := crypto_rand.Int(crypto_rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		sb.WriteByte(alphabet[num.Int64()])
	}
	return sb.String(), nil
}

func ensureDirs(base string, dirs []string) {
	for _, d := range dirs {
		must(os.MkdirAll(filepath.Join(base, d), fs.ModePerm))
	}
}

func writeFile(path, content string) {
	must(os.WriteFile(path, []byte(content), 0644))
}

func runSQL(db *sql.DB, sqlText string) {
	if strings.TrimSpace(sqlText) == "" {
		panic(errors.New("empty SQL"))
	}
	_, err := db.Exec(sqlText)
	must(err)
}

// Entry point
func main() {
	name := flag.String("name", "vfe_project", "Name of the project directory")
	port := flag.Int("port", 8000, "Default port (for future server use)")
	flag.Parse()

	base := filepath.Join(".", *name)
	fmt.Println("📁 Creating project at:", base)
	must(os.MkdirAll(base, 0755))

	// Create folder structure
	ensureDirs(base, []string{
		"db",
		"logs",
		"web/static",
		"storage/teams/sample-team/players/sample-player/vods",
	})

	// Generate config + secrets
	jwtRaw := make([]byte, 32)
	_, err := crypto_rand.Read(jwtRaw)
	must(err)
	cfg := Config{
		AppName:   "VodForEsports",
		Port:      *port,
		DBPath:    filepath.ToSlash(filepath.Join("db", "vfe.sqlite")),
		JWTSecret: base64.RawURLEncoding.EncodeToString(jwtRaw),
	}
	js, _ := json.MarshalIndent(cfg, "", "  ")
	writeFile(filepath.Join(base, "config.json"), string(js))
	writeFile(filepath.Join(base, ".env"),
		fmt.Sprintf("PORT=%d\nDB_PATH=%s\n", cfg.Port, cfg.DBPath))

	// Write basic web placeholders
	writeFile(filepath.Join(base, "web", "index.html"), indexHTML)
	writeFile(filepath.Join(base, "web", "dashboard.html"), dashboardHTML)
	writeFile(filepath.Join(base, "web", "static", "app.js"), appJS)
	writeFile(filepath.Join(base, "web", "static", "style.css"), styleCSS)

	// Write and apply schema
	writeFile(filepath.Join(base, "db", "schema.sql"), schemaSQL)
	db, err := sql.Open("sqlite", filepath.Join(base, cfg.DBPath))
	must(err)
	defer db.Close()
	runSQL(db, schemaSQL)

	// Seed admin
	tempPass, _ := randString(16)
	hash, _ := bcrypt.GenerateFromPassword([]byte(tempPass), 12)
	_, err = db.Exec(`INSERT INTO users (username, display_name, role, password_hash) VALUES ('admin','Administrator','admin',?)`, string(hash))
	must(err)

	// Seed sample data
	res, _ := db.Exec(`INSERT OR IGNORE INTO teams (name) VALUES ('sample-team')`)
	teamID, _ := res.LastInsertId()
	if teamID == 0 {
		db.QueryRow(`SELECT id FROM teams WHERE name='sample-team'`).Scan(&teamID)
	}
	db.Exec(`INSERT OR IGNORE INTO players (team_id,name) VALUES (?,?)`, teamID, "sample-player")

	fmt.Println("✅ Setup complete!")
	fmt.Println("🔑 Admin login: username=admin password=", tempPass)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1️⃣  Go into the folder:", base)
	fmt.Println("  2️⃣  Drop VODs into storage/teams/<team>/players/<player>/vods/")
	fmt.Println("  3️⃣  Later, run your server binary (not included yet).")
}
