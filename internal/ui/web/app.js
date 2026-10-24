"use strict";
// Binaly Sync's screens. This page runs inside Binaly Sync's own window and talks only to the
// program, through the window (binalyCall, provided by the window). It can't reach the network.

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const fmt = (n) => Number(n || 0).toLocaleString();

const pending = new Map();
let seq = 0;
window._binalyDone = (id, ok, value) => {
  const p = pending.get(id);
  if (!p) return;
  pending.delete(id);
  if (ok) p.resolve(value); else p.reject(new Error(value));
};
function call(name, body) {
  return new Promise((resolve, reject) => {
    const id = ++seq;
    pending.set(id, { resolve, reject });
    window.binalyCall(id, name, JSON.stringify(body ?? null)).catch((e) => {
      pending.delete(id);
      reject(new Error(String(e)));
    });
  });
}

function human(b) {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + " GB";
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + " MB";
  if (b >= 1 << 10) return Math.round(b / (1 << 10)) + " KB";
  return b + " bytes";
}

// The stages a file moves through. Each file is in exactly one, so the bar's four parts sit
// side by side and add up to the whole. Same words as the lab's Binaly Sync page in the app.
const STAGES = [
  { key: "processed", label: "Processed", explain: "The LLM has read these and written them into the lab's notes." },
  { key: "waiting", label: "Waiting for LLM", explain: "In Binaly and ready for the LLM's next scheduled run." },
  { key: "toUpload", label: "Waiting to upload", explain: "Found on this computer, still being sent to Binaly." },
  { key: "attention", label: "Needs attention", explain: "The LLM couldn't finish these. The lab's Binaly Sync page in the app lists them and says why." },
];

// A server that predates the stage counts sends only total/synced/processed/attention.
function stages(p) {
  const toUpload = p.toUpload ?? Math.max(0, p.total - p.synced);
  const waiting = p.waiting ?? Math.max(0, p.synced - p.processed - p.attention);
  return { processed: p.processed, waiting, toUpload, attention: p.attention, waitingOnDrive: p.waitingOnDrive ?? 0 };
}

function bar(p, small) {
  const cls = `bar${small ? " small" : ""}`;
  if (!p || p.total === 0) return `<div class="${cls}"></div>`;
  const s = stages(p);
  const pct = (n) => (100 * n / p.total).toFixed(2) + "%";
  const label = STAGES.map((x) => `${fmt(s[x.key])} ${x.label.toLowerCase()}`).join(", ");
  return `<div class="${cls}" role="img" aria-label="${label}, of ${fmt(p.total)} files">` +
    STAGES.map((x) => s[x.key] ? `<span class="${x.key}" style="width:${x.key === "attention" ? `max(4px, ${pct(s[x.key])})` : pct(s[x.key])}"></span>` : "").join("") +
    `</div>`;
}

// What isn't counted in the total, said in full.
function notProcessedLine(p) {
  const parts = [];
  if (p.notProcessed) parts.push(`${fmt(p.notProcessed)} ${p.notProcessed === 1 ? "is a file type" : "are file types"} Binaly doesn't read`);
  if (p.notInScope) parts.push(`${fmt(p.notInScope)} ${p.notInScope === 1 ? "is" : "are"} outside the folders the lab chose to process`);
  if (p.removed) parts.push(`${fmt(p.removed)} ${p.removed === 1 ? "was" : "were"} removed from the folder (notes made from them stay)`);
  return parts.length ? `<p class="notproc">Not processed: ${parts.join("; ")}.</p>` : "";
}

// The full explanation under a lab's bar: every stage named, counted and explained.
function legend(p) {
  if (!p) return "";
  if (p.counting && p.total === 0) return `<p class="counting">Counting files: Binaly Sync is still looking through the folders for the first time.</p>`;
  const s = stages(p);
  const rows = STAGES.map((x) => {
    const n = s[x.key];
    if (x.key === "attention" && !n) return "";
    let extra = "";
    if (x.key === "waiting" && s.waitingOnDrive) extra = ` ${fmt(s.waitingOnDrive)} of them ${s.waitingOnDrive === 1 ? "is" : "are"} too large to upload; the LLM reads ${s.waitingOnDrive === 1 ? "it" : "those"} directly from the drive.`;
    if (x.key === "toUpload" && p.uploadBytesLeft) extra = ` ${human(p.uploadBytesLeft)} to go.`;
    // While paused nothing is being sent: say what will happen instead.
    const explain = x.key === "toUpload" && last && last.pausedUntil ? "Found on this computer; sending is paused and continues when you resume." : x.explain;
    return `<div class="stage ${x.key}"><span class="sw"></span><span class="n">${fmt(n)}</span><span><b>${x.label}.</b> ${explain}${extra}</span></div>`;
  }).join("");
  const total = `<p class="total"><b>${fmt(p.total)}</b> files in all${p.counting ? " so far. Still counting: the first look through a folder isn't finished, so the total will grow." : "."}</p>`;
  return `<div class="stages">${rows}</div>${total}${notProcessedLine(p)}`;
}

// One line per folder: the same stages, briefly.
function compactLegend(p) {
  if (!p) return "";
  if (p.counting && p.total === 0) return `<div class="legend">Counting files...</div>`;
  const s = stages(p);
  const parts = [
    `<span><b>${fmt(s.processed)}</b> processed</span>`,
    `<span><b>${fmt(s.waiting)}</b> waiting for LLM</span>`,
    `<span><b>${fmt(s.toUpload)}</b> waiting to upload</span>`,
  ];
  if (s.attention) parts.push(`<span class="warn"><b>${fmt(s.attention)}</b> need attention</span>`);
  parts.push(`<span>of ${fmt(p.total)}${p.counting ? " so far" : ""}</span>`);
  return `<div class="legend">${parts.join("")}</div>`;
}

// Sync's half is sending files; the AI assistant's half is reading them into notes, as a
// scheduled task in Claude. Until that has processed anything, say so here and link to the
// steps, which live on the lab's Binaly Sync page in the app (one copy, kept current there).
function processingNote(w) {
  const p = w.progress;
  if (!p || !w.key || p.synced === 0 || p.processed > 0) return "";
  const waiting = stages(p).waiting;
  return `<div class="note-box">
    <b>Next: processing by an LLM.</b> Binaly Sync only sends the lab's files to Binaly. An LLM
    (Claude Desktop) reads each one and writes it into the lab's notes, as a scheduled task that
    runs every hour. It's set up once, in Claude Desktop. Until it runs, ${fmt(waiting)}
    file${waiting === 1 ? " waits" : "s wait"} for it.
    <div class="actions"><button class="primary" data-setup="${esc(w.key)}">How to set it up</button></div>
  </div>`;
}

let last = null;
// The folder whose "Stop syncing" is asking for confirmation, if any; the one being stopped
// (waiting for the server); and the last stop's result, shown on the card.
let removing = "";
let stopping = "";
let stopError = "";

function renderStatus(s) {
  last = s;
  const st = s.status || {};
  const signedIn = (s.accounts || []).length > 0;
  const pausedText = s.pausedUntil === "until-resumed" ? "Paused until you resume"
    : s.pausedUntil ? `Paused until ${new Date(s.pausedUntil).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" })}` : "";
  $("activity").textContent = !signedIn ? "" : pausedText || (st.running ? st.activity : (st.lastRunAt ? `Up to date. Last sync ${new Date(st.lastRunAt).toLocaleTimeString()}` : st.activity || ""));
  // Paused: only Resume (which also syncs right away). Running: Pause, and Sync now to sync
  // without waiting for the next one. Never a button that can't do anything.
  $("btn-pause").textContent = s.pausedUntil ? "Resume" : "Pause";
  $("btn-pause").className = s.pausedUntil ? "primary" : "quiet";
  $("btn-sync").hidden = !!s.pausedUntil;
  $("btn-sync").disabled = !!st.running;
  $("btn-sync").title = st.running ? "A sync is running now" : "Sync now, without waiting for the next one";
  const wss = st.workspaces && st.workspaces.length ? st.workspaces : (s.accounts || []).map((a) => ({ key: a.key, name: a.workspaceName, email: a.email, state: "ok" }));
  $("empty").hidden = wss.length > 0;
  $("workspaces").innerHTML = wss.map((w) => {
    const bad = w.state !== "ok";
    const badge = { ok: "", "needs-sign-in": "Needs sign-in", revoked: "Disconnected", error: "Problem" }[w.state] ?? w.state;
    const folders = (w.folders || []).map((f) => `
      <div class="folder">
        <div class="path">${esc(f.path)}</div>
        ${bar(f, true)}${compactLegend(f)}
        ${f.warning ? `<div class="warning">${esc(f.warning.reason)}<div class="actions"><button data-confirm="${esc(w.key)}">These files really were removed</button></div></div>` : ""}
        ${stopping === f.id
          ? `<div class="note-box stopping">${spinner}<span>Stopping syncing this folder...</span></div>`
          : removing === f.id
          ? `<div class="note-box"><b>Stop syncing this folder from this computer?</b> Files already in Binaly, and the notes made from them, stay. ${parentOf(f, w.folders)
              ? `Its files stay synced as part of <span class="path-inline">${esc(parentOf(f, w.folders).path)}</span>, so nothing else changes.`
              : "New and changed files in this folder won't be sent from this computer. If another computer also syncs it, that computer keeps going."}
              <div class="actions"><button class="primary" data-remove-yes="${esc(f.id)}" data-key="${esc(w.key)}">Stop syncing</button><button class="quiet" data-remove-no>Cancel</button></div></div>`
          : `<div class="folder-actions"><button class="quiet small" data-remove="${esc(f.id)}">Stop syncing</button></div>`}
      </div>`).join("");
    const warnings = (w.warnings || []).filter((x) => !(w.folders || []).some((f) => f.warning && f.warning.reason === x));
    return `<div class="card">
      <div class="ws-head"><span class="ws-name">${esc(w.name)}</span>
        <span>${w.email ? `<span class="muted">${esc(w.email)}</span> ` : ""}${badge ? `<span class="badge${bad ? " bad" : ""}">${esc(badge)}</span>` : ""}${w.key ? ` <button class="quiet small" data-open="${esc(w.key)}">Open in Binaly</button>` : ""}</span></div>
      ${w.error ? `<p class="muted">${esc(w.error)}</p>` : ""}
      ${w.state === "needs-sign-in" ? `<div class="actions"><button class="primary" data-signin>Sign in again</button></div>` : ""}
      ${bar(w.progress)}${legend(w.progress)}
      ${processingNote(w)}
      ${warnings.map((x) => `<div class="warning">${esc(x)}</div>`).join("")}
      ${stopError ? `<div class="warning">${esc(stopError)}</div>` : ""}
      ${folders ? `<div class="folders">${folders}</div>` : ""}
    </div>`;
  }).join("");
  renderSettingsWorkspaces(s.accounts || []);
  // The preview can open before the first status arrives (first run): fill in the labs and
  // the "already synced" check once they're known.
  if (!$("view-preview").hidden && $("inside-note") && !$("ws-pick").options.length && (s.accounts || []).length) {
    fillWsPick();
    updateInsideNote();
  }
  renderUpdate(s.update || {});
  $("version").textContent = (s.version || "").replace(/^v/, "");
}

const ver = (v) => esc(String(v || "").replace(/^v/, ""));
const spinner = `<span class="spinner" aria-hidden="true"></span>`;

// The status page mentions an update only when there's something to see: one being installed,
// or one waiting for "Install update" (with "tell me, don't install"). Errors show in About.
function renderUpdate(u) {
  const el = $("update");
  if (u.installing) {
    el.innerHTML = `${spinner}<span>${u.phase === "downloading" ? `Downloading Binaly Sync ${ver(u.available)}...` : `Installing Binaly Sync ${ver(u.available)}. It restarts by itself in a moment, and syncing continues after.`}</span>`;
  } else if (u.available) {
    el.innerHTML = `<span>Binaly Sync ${ver(u.available)} is available.</span><button class="primary" data-update>Install now</button>`;
  }
  el.hidden = !(u.installing || u.available);
  renderAboutUpdate(u);
}

// About: every step of a check, as it happens, instead of a dialog box.
let checkClickedAt = 0;
function renderAboutUpdate(u) {
  const el = $("about-update");
  if (!el) return;
  // Just clicked: show "Checking" at once, before the program reports back.
  const checking = u.phase === "checking" || (!u.phase && Date.now() - checkClickedAt < 1500);
  let html = "";
  if (checking) html = `${spinner}<span>Checking for updates...</span>`;
  else if (u.phase === "downloading") html = `${spinner}<span>Found Binaly Sync ${ver(u.available)}. Downloading it...</span>`;
  else if (u.phase === "installing") html = `${spinner}<span>Installing Binaly Sync ${ver(u.available)}. It restarts by itself in a moment, and syncing continues after.</span>`;
  else if (u.error) html = `<span class="err-text">${esc(u.error)}</span><button data-check>Try again</button>`;
  else if (u.available) html = `<span>Binaly Sync ${ver(u.available)} is available.</span><button class="primary" data-update>Install now</button>`;
  else if (u.latest) html = `<span>You have the latest version.${u.checkedAt ? ` Checked ${new Date(u.checkedAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}.` : ""}</span>`;
  el.innerHTML = html;
  el.hidden = !html;
  $("btn-check").disabled = checking || !!u.phase;
}

async function poll() {
  try { renderStatus(await call("status")); } catch { /* the app may be restarting */ }
}

// ---- preview ----
let previewFolder = "";
// Synced folders that already contain the previewed one, per lab. Adding it to such a lab
// changes nothing (its files are already covered), so Start is off and the page says why.
// A synced folder (of the same lab) that contains this one, if any.
function parentOf(f, folders) {
  const key = (p) => String(p).replace(/\\/g, "/").replace(/\/+$/, "").toLowerCase();
  const k = key(f.path);
  return (folders || []).find((o) => o.id !== f.id && k.startsWith(key(o.path) + "/"));
}

let previewInsideOf = [];
let previewFolderKey = "";
function fillWsPick() {
  const accts = (last && last.accounts) || [];
  $("ws-pick").innerHTML = accts.map((a) => `<option value="${esc(a.key)}">${esc(a.workspaceName)}</option>`).join("");
  $("ws-pick-label").hidden = accts.length < 2;
}
function updateInsideNote() {
  const accts = (last && last.accounts) || [];
  const key = $("ws-pick").value || (accts[0] && accts[0].key) || "";
  const hit = previewInsideOf.find((x) => x.AccountKey === key);
  const same = hit && hit.Folder.replace(/[\\/]+$/, "").toLowerCase() === previewFolderKey;
  $("inside-note").innerHTML = !hit ? ""
    : same
      ? `<div class="note-box"><b>Already synced.</b> This computer already syncs this folder to ${esc(hit.Workspace)}.</div>`
      : `<div class="note-box"><b>Already synced.</b> This folder is part of <span class="path-inline">${esc(hit.Folder)}</span>, which this computer already syncs to ${esc(hit.Workspace)}. Its files are already covered, so adding it separately changes nothing.</div>`;
  $("btn-start").disabled = accts.length === 0 || !!hit;
}

async function showPreview(folder) {
  previewFolder = folder;
  $("preview-actions").hidden = true;
  $("preview").innerHTML = `<p class="muted">Looking through ${esc(folder)}...</p>`;
  try {
    const r = await call("preview", { folder });
    $("preview").innerHTML = `
      <div class="path">${esc(r.Folder)}</div>
      <p><b>${fmt(r.Files)}</b> files, ${human(r.Bytes)}</p>
      <div class="preview-grid">
        <span class="n">${fmt(r.WillUpload)}</span><span>will be uploaded to Binaly (up to ${human(r.UploadLimit)} each)</span>
        <span class="n">${fmt(r.RegisterOnly)}</span><span>too large to upload: Binaly records them, and the LLM reads them directly from this drive</span>
        <span class="n">${fmt(r.Excluded)}</span><span>skipped: lock files, temporary files and system folders</span>
        ${r.OnlineOnly ? `<span class="n">${fmt(r.OnlineOnly)}</span><span>online only: cloud files not on this disk, so not synced</span>` : ""}
        ${r.Unreadable ? `<span class="n">${fmt(r.Unreadable)}</span><span>unreadable: no permission to open them</span>` : ""}
      </div>
      <div id="inside-note"></div>`;
    previewInsideOf = r.InsideOf || [];
    previewFolderKey = String(r.Folder || "").replace(/[\\/]+$/, "").toLowerCase();
    fillWsPick();
    updateInsideNote();
    $("preview-actions").hidden = false;
  } catch (e) {
    $("preview").innerHTML = `<p>${esc(e.message)}</p>`;
    $("preview-actions").hidden = false;
    $("btn-start").disabled = true;
  }
}

// ---- settings ----
function renderSettingsWorkspaces(accts) {
  $("set-workspaces").innerHTML = accts.length ? accts.map((a) => `
    <div class="ws-line"><span><b>${esc(a.workspaceName)}</b> <span class="muted">${esc(a.email || "machine token")}</span></span>
    <button class="quiet" data-signout="${esc(a.key)}">Sign out</button></div>`).join("") : `<p class="muted">Not signed in.</p>`;
}

async function loadSettings() {
  const s = await call("settings");
  $("set-logon").checked = !!s.startAtLogon;
  $("set-updates").value = s.updates || "auto";
  $("set-bw").value = s.bandwidthKBps || 0;
}

// ---- routing ----
function route() {
  const [screen, query] = (current || "status").split("?");
  for (const v of ["status", "preview", "settings", "about"]) $("view-" + v).hidden = v !== screen;
  document.querySelectorAll("[data-nav]").forEach((a) => a.classList.toggle("on", a.dataset.nav === screen));
  if (screen === "preview") showPreview(new URLSearchParams(query).get("folder") || "");
  if (screen === "settings") loadSettings();
}
// Screens are "status", "settings" or "preview?folder=...". The program opens one with
// binalyShow, e.g. after the tray's "Add folder...".
let current = window.binalyStartScreen || "status";
function go(screen) { current = screen; route(); }
window.binalyShow = go;
// This page has no address to reload; keep F5 and Ctrl+R from blanking the window.
document.addEventListener("keydown", (ev) => {
  if (ev.key === "F5" || ((ev.ctrlKey || ev.metaKey) && (ev.key === "r" || ev.key === "R"))) ev.preventDefault();
});

async function pickAndPreview() {
  try {
    const { folder } = await call("pick", {});
    if (folder) go("preview?folder=" + encodeURIComponent(folder));
  } catch (e) { alertBox(e.message); }
}
function alertBox(msg) { $("activity").textContent = msg; }

document.addEventListener("click", async (ev) => {
  const nav = ev.target.closest("[data-nav]");
  if (nav) { go(nav.dataset.nav); return; }
  if (!ev.target.closest(".menu-wrap")) $("pause-menu").hidden = true; // a click elsewhere closes it
  const t = ev.target.closest("button");
  if (!t) return;
  try {
    if (t.id === "btn-sync") { await call("sync", {}); poll(); }
    else if (t.id === "btn-pause") {
      if (last && last.pausedUntil) { await call("pause", { minutes: 0 }); poll(); } // Resume
      else $("pause-menu").hidden = !$("pause-menu").hidden;
    }
    else if (t.dataset.pause) { $("pause-menu").hidden = true; await call("pause", { minutes: Number(t.dataset.pause) }); poll(); }
    else if (t.dataset.open) await call("open-binaly", { accountKey: t.dataset.open });
    else if (t.id === "btn-add" || t.id === "btn-change") await pickAndPreview();
    else if (t.id === "btn-start") {
      t.disabled = true;
      await call("start", { folder: previewFolder, accountKey: $("ws-pick").value });
      go("status");
    }
    else if (t.id === "btn-signin" || t.id === "btn-signin-empty" || t.hasAttribute("data-signin")) { await call("signin", {}); poll(); }
    else if (t.dataset.signout) { await call("signout", { accountKey: t.dataset.signout }); poll(); }
    else if (t.dataset.confirm) { await call("confirm-deletions", { accountKey: t.dataset.confirm }); poll(); }
    else if (t.dataset.setup) await call("open-setup", { accountKey: t.dataset.setup });
    else if (t.dataset.remove) { removing = t.dataset.remove; renderStatus(last); }
    else if (t.hasAttribute("data-remove-no")) { removing = ""; renderStatus(last); }
    else if (t.dataset.removeYes) {
      stopping = t.dataset.removeYes;
      removing = "";
      stopError = "";
      renderStatus(last);
      try {
        await call("remove-folder", { accountKey: t.dataset.key, folderId: stopping });
      } catch (e) {
        stopError = `Couldn't stop syncing that folder: ${e.message}`;
      }
      stopping = "";
      poll();
    }
    else if (t.id === "btn-log") await call("log", {});
    else if (t.dataset.link) await call("open-link", { link: t.dataset.link });
    else if (t.id === "btn-check" || t.hasAttribute("data-check")) {
      checkClickedAt = Date.now();
      renderAboutUpdate({ phase: "checking" });
      await call("check-update", {});
      poll();
    }
    else if (t.hasAttribute("data-update")) { t.disabled = true; await call("update", {}); poll(); }
    else if (t.id === "btn-save") {
      await call("settings", { settings: { startAtLogon: $("set-logon").checked, updates: $("set-updates").value, bandwidthKBps: Number($("set-bw").value) || 0 } });
      $("saved").hidden = false; setTimeout(() => ($("saved").hidden = true), 2000);
    }
  } catch (e) {
    alertBox(e.message);
    t.disabled = false;
  }
});

route();
poll();
setInterval(poll, 2000);

// Choosing another lab on the preview screen re-checks whether that lab already covers it.
document.addEventListener("change", (ev) => { if (ev.target && ev.target.id === "ws-pick") updateInsideNote(); });
