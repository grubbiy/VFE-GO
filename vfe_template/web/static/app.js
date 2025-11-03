// =======================================================
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

    const logoutBtn = document.getElementById("logoutBtn");
    if (logoutBtn) {
        logoutBtn.addEventListener("click", () => {
            localStorage.removeItem("token");
            window.location.href = "/index.html";
        });
    }

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
        "Authorization": `Bearer ${localStorage.getItem("token")}`,
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

    if (!res.ok) throw new Error(`Request failed: ${res.status}`);
    return res.json();
}

// =======================================================
//  DASHBOARD
// =======================================================
async function loadDashboard() {
    console.log("Loading dashboard...");

    const container = document.getElementById("vodlist");
    const navBar = document.getElementById("nav-bar");
    if (!container) return console.error("Container not found!");

    container.innerHTML = "<p>Loading VODs...</p>";
    navBar.innerHTML = ""; // clear back button area

    const token = localStorage.getItem("token");
    if (!token) {
        console.warn("No token found, redirecting...");
        window.location.href = "/index.html";
        return;
    }

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

    const grouped = {};
    vods.forEach(vod => {
        let team = vod.team_name || "Unknown Team";
        let player = vod.player_name || "Unknown Player";

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

    renderTeams(container, grouped, vods.length);
}

function renderTeams(container, grouped, count) {
    container.innerHTML = `<h2>Teams</h2>`;
    const navBar = document.getElementById("nav-bar");
    navBar.innerHTML = ""; // no back button on main view

    const teamGrid = document.createElement("div");
    teamGrid.className = "grid";
    container.appendChild(teamGrid);

    Object.keys(grouped).forEach(team => {
        const teamCard = document.createElement("div");
        teamCard.className = "team-card";
        teamCard.textContent = team;
        teamCard.addEventListener("click", () => openTeam(team, grouped[team], grouped));
        teamGrid.appendChild(teamCard);
    });

    console.log(`✅ Loaded ${count} VOD(s).`);
}

// =======================================================
//  TEAM / PLAYER NAVIGATION
// =======================================================
function openTeam(teamName, playersObj, grouped) {
    const container = document.getElementById("vodlist");
    const navBar = document.getElementById("nav-bar");

    // --- Back Button ---
    navBar.innerHTML = "";
    const backBtn = document.createElement("button");
    backBtn.textContent = "← Back to Teams";
    backBtn.className = "back-btn";
    backBtn.addEventListener("click", () => {
        navBar.innerHTML = "";
        renderTeams(container, grouped);
    });
    navBar.appendChild(backBtn);

    container.innerHTML = "";
    const title = document.createElement("h2");
    title.textContent = teamName;
    container.appendChild(title);

    const playerGrid = document.createElement("div");
    playerGrid.className = "grid";
    container.appendChild(playerGrid);

    Object.keys(playersObj).forEach(playerName => {
        const card = document.createElement("div");
        card.className = "player-card";
        card.textContent = playerName;
        card.addEventListener("click", () => openPlayer(teamName, playerName, playersObj[playerName], grouped));
        playerGrid.appendChild(card);
    });
}

function openPlayer(teamName, playerName, vods, grouped) {
    const container = document.getElementById("vodlist");
    const navBar = document.getElementById("nav-bar");

    // --- Back Button ---
    navBar.innerHTML = "";
    const backBtn = document.createElement("button");
    backBtn.textContent = "← Back to Players";
    backBtn.className = "back-btn";
    backBtn.addEventListener("click", () => openTeam(teamName, grouped[teamName], grouped));
    navBar.appendChild(backBtn);

    container.innerHTML = "";
    const title = document.createElement("h3");
    title.textContent = `${teamName} → ${playerName}`;
    container.appendChild(title);

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
// =======================================================
//  THEATER MODE (Fullscreen Video + Notes)
// =======================================================
async function openTheaterWithNotes(vod) {
    const existing = document.querySelector(".theater-overlay");
    if (existing) existing.remove();

    const overlay = document.createElement("div");
    overlay.className = "theater-overlay";

    // --- Close button ---
    const closeBtn = document.createElement("button");
    closeBtn.className = "theater-close";
    closeBtn.textContent = "×";
    closeBtn.addEventListener("click", () => overlay.remove());

    // --- Video Section ---
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

    // --- Note Panel ---
    const notePanel = document.createElement("div");
    notePanel.className = "note-panel";

    const noteHeader = document.createElement("div");
    noteHeader.className = "note-header";

    const addBtn = document.createElement("button");
    addBtn.textContent = "+ Add Note";
    addBtn.className = "add-note-btn";

    const delAllBtn = document.createElement("button");
    delAllBtn.textContent = "🗑️ Clear All";
    delAllBtn.className = "delete-all-btn";

    noteHeader.innerHTML = "<h3>Notation</h3>";
    noteHeader.appendChild(addBtn);
    noteHeader.appendChild(delAllBtn);

    notePanel.appendChild(noteHeader);

    const noteList = document.createElement("div");
    noteList.className = "note-list";
    notePanel.appendChild(noteList);

    overlay.appendChild(closeBtn);
    overlay.appendChild(videoContainer);
    overlay.appendChild(notePanel);
    document.body.appendChild(overlay);

    // === Load notes from backend ===
    let notes = [];
    try {
        const res = await apiFetch(`/api/notes?vod_id=${vod.id}`);
        notes = Array.isArray(res) ? res : [];
    } catch (err) {
        console.warn("Failed to load notes, falling back to localStorage:", err);
        notes = JSON.parse(localStorage.getItem(`notes_${vod.file_path}`) || "[]");
    }

    notes.forEach(n => renderNote(noteList, n, vod, video));

    // === Add Note Button ===
    addBtn.addEventListener("click", async () => {
        const timestamp = formatTimestamp(video.currentTime);
        const newNote = { time: timestamp, text: "" };
        renderNote(noteList, newNote, vod, video);
        await saveNotes(vod, noteList);
    });

    // === Delete All ===
    delAllBtn.addEventListener("click", async () => {
        if (!confirm("Delete all notes for this VOD?")) return;
        noteList.innerHTML = "";
        await deleteNotes(vod);
    });
}



// Helper: render note card
function renderNote(container, note, vod, video) {
    const noteCard = document.createElement("div");
    noteCard.className = "note-card";

    const header = document.createElement("div");
    header.className = "note-card-header";
    header.textContent = note.time || formatTimestamp(note.ts_seconds || 0);

    // Jump to timestamp
    header.style.cursor = "pointer";
    header.addEventListener("click", () => {
        const [m, s] = header.textContent.split(":").map(Number);
        video.currentTime = m * 60 + s;
    });

    const delBtn = document.createElement("button");
    delBtn.textContent = "✖";
    delBtn.className = "note-del-btn";
    delBtn.addEventListener("click", async () => {
        noteCard.remove();
        await saveNotes(vod, container);
    });
    header.appendChild(delBtn);

    const textarea = document.createElement("textarea");
    textarea.value = note.text || note.content || "";
    textarea.placeholder = "Write your notes here...";
    textarea.addEventListener("input", async () => {
        await saveNotes(vod, container);
    });

    noteCard.appendChild(header);
    noteCard.appendChild(textarea);
    container.appendChild(noteCard);
}



// Helper: format seconds into mm:ss
function formatTimestamp(seconds) {
    const m = Math.floor(seconds / 60).toString().padStart(2, "0");
    const s = Math.floor(seconds % 60).toString().padStart(2, "0");
    return `${m}:${s}`;
}

function parseTimestampToSeconds(t) {
    const [m, s] = t.split(":").map(Number);
    return m * 60 + s;
}

async function saveNotes(vod, container) {
    const notes = Array.from(container.querySelectorAll(".note-card")).map(card => ({
        ts_seconds: parseTimestampToSeconds(card.querySelector(".note-card-header").childNodes[0].textContent.trim()),
        content: card.querySelector("textarea").value,
    }));

    // local backup
    localStorage.setItem(`notes_${vod.file_path}`, JSON.stringify(notes));

    // send to backend
    try {
        await fetch("/api/notes", {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
                "Authorization": `Bearer ${localStorage.getItem("token")}`,
            },
            body: JSON.stringify({ vod_id: vod.id, notes }),
        });
    } catch (err) {
        console.warn("Could not sync to backend:", err);
    }
}

async function deleteNotes(vod) {
    try {
        await fetch(`/api/notes?vod_id=${vod.id}`, {
            method: "DELETE",
            headers: { "Authorization": `Bearer ${localStorage.getItem("token")}` },
        });
        localStorage.removeItem(`notes_${vod.file_path}`);
    } catch (err) {
        console.warn("Could not delete notes:", err);
    }
}


// Helper: save all notes
async function saveNotes(vod, container) {
    const notes = Array.from(container.querySelectorAll(".note-card")).map(card => ({
        ts_seconds: parseTimestampToSeconds(card.querySelector(".note-card-header").childNodes[0].textContent.trim()),
        content: card.querySelector("textarea").value,
    }));

    // Local backup so notes survive reloads
    localStorage.setItem(`notes_${vod.file_path}`, JSON.stringify(notes));

    // Send to backend for permanent storage
    try {
        const res = await fetch("/api/notes", {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
                "Authorization": `Bearer ${localStorage.getItem("token")}`,
            },
            body: JSON.stringify({
                vod_id: vod.id,  // from your Go /api/list-vods response
                notes: notes,
            }),
        });

        if (!res.ok) {
            console.warn("Failed to sync notes:", await res.text());
        } else {
            console.log("✅ Notes synced successfully");
        }
    } catch (err) {
        console.error("Network error while saving notes:", err);
    }
}
