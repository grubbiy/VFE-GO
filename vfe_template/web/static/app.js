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

    console.log(`✅ Loaded ${vods.length} VOD(s).`);
}

// =======================================================
//  TEAM / PLAYER NAVIGATION
// =======================================================
function openTeam(teamName, playersObj) {
    const container = document.getElementById("vodlist");
    container.innerHTML = `<h2>${teamName}</h2>`;

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
    container.innerHTML = `<h3>${teamName} → ${playerName}</h3>`;

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
    notePanel.innerHTML = `
        <h3>Notation (coming soon)</h3>
        <p>This area will display the analysis tools later.</p>
    `;

    overlay.appendChild(closeBtn);
    overlay.appendChild(videoContainer);
    overlay.appendChild(notePanel);
    document.body.appendChild(overlay);
}
