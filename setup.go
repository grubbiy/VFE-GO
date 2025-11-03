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
  <link rel="stylesheet" href="./static/style.css">
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
    window.location.href = './dashboard.html';
  });
  </script>
</body>
</html>
`

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>VodForEsports Dashboard</title>
  <link rel="stylesheet" href="./static/style.css" />
</head>
<body>
  <h2>VodForEsports Dashboard</h2>
  <button id="logoutBtn">Logout</button>
  
  <div id="vodlist" class="vod-container"></div>

  <script src="./static/app.js"></script>
</body>
</html>
`

const appJS = `// =======================================================
//  AUTH HANDLING
// =======================================================
document.addEventListener("DOMContentLoaded", () => {
    const token = localStorage.getItem("token");
    const DEBUG_STOP_REDIRECT = false;

    if (!token && !window.location.pathname.endsWith("index.html")) {
        console.warn("No token found — redirecting to login");
        if (!DEBUG_STOP_REDIRECT) {
            window.location.href = "/index.html";
            return;
        } else {
            document.body.style.background = "black";
            document.body.style.color = "white";
            document.body.innerHTML =
                "<h2>DEBUG MODE: No token detected</h2><p>Open console for details.</p>";
            throw new Error("Debug stop redirect");
        }
    }

    // --- Logout button ---
    const logoutBtn = document.getElementById("logoutBtn");
    if (logoutBtn) {
        logoutBtn.addEventListener("click", () => {
            localStorage.removeItem("token");
            window.location.href = "/index.html";
        });
    }

    // --- Load dashboard if we’re on that page ---
    if (window.location.pathname.endsWith("dashboard.html")) {
        loadDashboard();
    }
});

// =======================================================
//  HELPER FUNCTIONS
// =======================================================
async function apiFetch(url, options = {}) {
    options.headers = {
        ...(options.headers || {}),
        "Authorization": 'Bearer ' + localStorage.getItem("token"),
        "Content-Type": "application/json",
    };

    const res = await fetch(url, options);
    if (res.status === 401) {
        console.warn("Unauthorized — redirecting to login...");
        localStorage.removeItem("token");
        if (!window.location.pathname.endsWith("index.html")) {
            window.location.href = "/index.html";
        }
        throw new Error("unauthorized");
    }

    if (!res.ok) throw new Error('Request failed: ' + res.status);
    return res.json();
}

// =======================================================
//  DASHBOARD
// =======================================================
async function loadDashboard() {
    console.log("Loading dashboard...");

    const container = document.getElementById("vodlist");
    if (!container) {
        console.error("Container not found!");
        return;
    }

    container.innerHTML = "<p>Loading VODs...</p>";

    const token = localStorage.getItem("token");
    if (!token) {
        console.warn("No token found, redirecting to login...");
        window.location.href = "/index.html";
        return;
    }

    // --- Get VOD list ---
    let vods;
    try {
        const res = await fetch("/api/list-vods", {
            headers: { "Authorization": "Bearer " + token },
        });
        vods = await res.json();
    } catch (err) {
        console.error("Failed to load VODs:", err);
        container.innerHTML = "<p>Error loading VODs</p>";
        return;
    }

    container.innerHTML = "";

    if (!vods || vods.length === 0) {
        container.innerHTML = "<p>No VODs available.</p>";
        return;
    }

    // --- Group by team and player ---
    const grouped = {};
    vods.forEach(vod => {
        let team = vod.team_name || "Unknown Team";
        let player = vod.player_name || "Unknown Player";

        // Try to infer from file path
        if (vod.file_path) {
            const match = vod.file_path.match(/teams\/([^/]+)\/players\/([^/]+)/i);
            if (match) {
                team = match[1];
                player = match[2];
            }
        }

        if (!grouped[team]) grouped[team] = {};
        if (!grouped[team][player]) grouped[team][player] = [];
        grouped[team][player].push(vod);
    });

    // --- Create team grid ---
    const teamGrid = document.createElement("div");
    teamGrid.className = "grid";
    container.appendChild(teamGrid);

    Object.keys(grouped).forEach(team => {
        const teamCard = document.createElement("div");
        teamCard.className = "team-card";
        teamCard.textContent = team;
        teamCard.addEventListener("click", () => openTeam(team, grouped[team]));
        teamGrid.appendChild(teamCard);
    });

    console.log('✅ Loaded ' + vods.length + ' VOD(s).');
}

// =======================================================
//  TEAM / PLAYER NAVIGATION
// =======================================================
function openTeam(teamName, playersObj) {
    const container = document.getElementById("vodlist");
    container.innerHTML = '<h2>' + teamName + '</h2>';

    const playerGrid = document.createElement("div");
    playerGrid.className = "grid";
    container.appendChild(playerGrid);

    Object.keys(playersObj).forEach(playerName => {
        const card = document.createElement("div");
        card.className = "player-card";
        card.textContent = playerName;
        card.addEventListener("click", () => openPlayer(teamName, playerName, playersObj[playerName]));
        playerGrid.appendChild(card);
    });
}

function openPlayer(teamName, playerName, vods) {
    const container = document.getElementById("vodlist");
    container.innerHTML = '<h3>' + teamName + ' → ' + playerName + '</h3>';

    if (!vods || vods.length === 0) {
        container.innerHTML += "<p>No VODs found.</p>";
        return;
    }

    const grid = document.createElement("div");
    grid.className = "grid";

    vods.forEach(vod => {
        const card = document.createElement("div");
        card.className = "vod-card";

        const video = document.createElement("video");
        video.src = "/vods/" + vod.file_path.replace(/^storage[\\/]/, "");
        video.controls = false;
        video.muted = true;
        video.loop = true;
        video.width = 220;
        video.height = 120;
        video.addEventListener("mouseenter", () => video.play());
        video.addEventListener("mouseleave", () => {
            video.pause();
            video.currentTime = 0;
        });

        const title = document.createElement("p");
        title.textContent = vod.title || vod.file_name || "Untitled";

        card.appendChild(video);
        card.appendChild(title);

        card.addEventListener("click", () => openTheaterWithNotes(vod));
        grid.appendChild(card);
    });

    container.appendChild(grid);
}

// =======================================================
//  THEATER MODE (Fullscreen Video + Notes)
// =======================================================
function openTheaterWithNotes(vod) {
    const existing = document.querySelector(".theater-overlay");
    if (existing) existing.remove();

    const overlay = document.createElement("div");
    overlay.className = "theater-overlay";

    // Close button
    const closeBtn = document.createElement("button");
    closeBtn.className = "theater-close";
    closeBtn.textContent = "×";
    closeBtn.addEventListener("click", () => overlay.remove());

    // Video container
    const videoContainer = document.createElement("div");
    videoContainer.className = "theater-video-container";

    const title = document.createElement("h2");
    title.textContent = vod.title || "VOD Player";

    const video = document.createElement("video");
    video.src = "/vods/" + vod.file_path.replace(/^storage[\\/]/, "");
    video.controls = true;
    video.autoplay = true;

    videoContainer.appendChild(title);
    videoContainer.appendChild(video);

    // Note panel
    const notePanel = document.createElement("div");
    notePanel.className = "note-panel";
    notePanel.innerHTML = "
        <h3>Notation (coming soon)</h3>
        <p>This area will display the analysis tools later.</p>
    ";

    overlay.appendChild(closeBtn);
    overlay.appendChild(videoContainer);
    overlay.appendChild(notePanel);
    document.body.appendChild(overlay);
}
`
const styleCSS = `/* ===========================
   BASE STYLES
=========================== */
body {
  margin: 0;
  font-family: "Segoe UI", sans-serif;
  background-color: #0e0e0e;
  color: #eee;
  overflow-x: hidden;
}

/* ===========================
   LOGIN PAGE
=========================== */
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
  transition: background 0.2s;
}

button:hover {
  background: #0a84ff;
}

/* ===========================
   HEADER
=========================== */
header {
  background: #111;
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 20px;
}

h2,
h3 {
  margin: 10px 0;
}

/* ===========================
   DASHBOARD LAYOUT
=========================== */
#vodlist {
  padding: 20px;
}

.grid {
  display: flex;
  flex-wrap: wrap;
  gap: 20px;
  padding-top: 10px;
}

/* ===========================
   CARDS
=========================== */
.card {
  background: #1c1c1c;
  border: 2px solid #333;
  border-radius: 10px;
  width: 220px;
  padding: 10px;
  text-align: center;
  transition: transform 0.2s, border-color 0.2s;
}

.card:hover {
  transform: scale(1.05);
  border-color: #007bff;
}

/* --- Video Cards --- */
.vod-card video {
  width: 100%;
  border-radius: 10px;
  margin-bottom: 5px;
  box-shadow: 0 0 10px rgba(0, 0, 0, 0.6);
}

.vod-card p {
  font-size: 14px;
  color: #ccc;
  word-wrap: break-word;
}

/* --- Player Cards --- */
.player-card {
  background: #181818;
  font-weight: bold;
  padding: 20px;
  cursor: pointer;
  border-radius: 10px;
  transition: background 0.2s;
}

.player-card:hover {
  background: #252525;
}

/* --- Team Cards --- */
.team-card {
  background: linear-gradient(135deg, #1b1b1b, #252525);
  border: 2px solid #333;
  border-radius: 12px;
  width: 200px;
  height: 100px;
  display: flex;
  justify-content: center;
  align-items: center;
  cursor: pointer;
  transition: transform 0.2s, border-color 0.2s, background 0.3s;
}

.team-card:hover {
  transform: scale(1.05);
  border-color: #007bff;
  background: linear-gradient(135deg, #222, #333);
}

/* ===========================
   THEATER OVERLAY (Fullscreen Video + Notes)
=========================== */
.theater-overlay {
  position: fixed;
  top: 0;
  left: 0;
  width: 100vw;
  height: 100vh;
  background: rgba(0, 0, 0, 0.96);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 99999;
  overflow: hidden;
  gap: 20px;
  padding: 40px;
  box-sizing: border-box;
}

/* --- Close Button --- */
.theater-close {
  position: absolute;
  top: 20px;
  left: 25px;
  font-size: 36px;
  color: #fff;
  background: none;
  border: none;
  cursor: pointer;
  transition: transform 0.2s, color 0.2s;
}

.theater-close:hover {
  transform: scale(1.2);
  color: #007bff;
}

/* --- Video Section --- */
.theater-video-container {
  flex: 4;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  height: 85vh;
  background: #000;
  border-radius: 10px;
  padding: 10px;
  box-shadow: 0 0 25px rgba(0, 0, 0, 0.8);
}

.theater-video-container h2 {
  color: white;
  font-size: 18px;
  margin-bottom: 10px;
}

.theater-video-container video {
  width: 100%;
  height: 100%;
  object-fit: contain;
  border-radius: 10px;
}

/* --- Notation Panel --- */
.note-panel {
  flex: 1;
  height: 85vh;
  background: #181818;
  border-left: 2px solid #333;
  border-radius: 10px;
  padding: 20px;
  display: flex;
  flex-direction: column;
  justify-content: flex-start;
  box-shadow: -2px 0 15px rgba(0, 0, 0, 0.6);
}

.note-panel h3 {
  margin-bottom: 10px;
  color: #fff;
}

.note-panel p {
  font-size: 14px;
  color: #ccc;
  line-height: 1.5;
}

/* --- Animation --- */
@keyframes fadeIn {
  from {
    opacity: 0;
    transform: scale(1.02);
  }
  to {
    opacity: 1;
    transform: scale(1);
  }
}

.theater-overlay {
  animation: fadeIn 0.25s ease-out forwards;
}

/* --- TEAM BUTTONS --- */
.team-card {
  background: linear-gradient(145deg, #1a1a1a, #202020);
  border: 1px solid #333;
  border-radius: 12px;
  width: 220px;
  height: 100px;
  display: flex;
  justify-content: center;
  align-items: center;
  color: #eee;
  font-weight: 600;
  font-size: 18px;
  cursor: pointer;
  transition: all 0.25s ease;
  box-shadow: 0 4px 10px rgba(0, 0, 0, 0.3);
}

.team-card:hover {
  background: linear-gradient(145deg, #242424, #2f2f2f);
  transform: translateY(-3px) scale(1.03);
  border-color: #007bff;
  box-shadow: 0 6px 20px rgba(0, 123, 255, 0.3);
}

/* --- PLAYER BUTTONS --- */
.player-card {
  background: linear-gradient(145deg, #181818, #202020);
  border: 1px solid #333;
  border-radius: 10px;
  width: 180px;
  height: 70px;
  margin: 10px;
  display: flex;
  justify-content: center;
  align-items: center;
  color: #ccc;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s ease;
  box-shadow: 0 3px 8px rgba(0, 0, 0, 0.25);
}

.player-card:hover {
  background: linear-gradient(145deg, #232323, #2c2c2c);
  transform: translateY(-2px) scale(1.04);
  border-color: #0a84ff;
  color: #fff;
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
