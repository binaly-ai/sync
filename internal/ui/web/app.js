"use strict";
// Binaly Sync's local screens. Everything here talks only to this computer's Binaly Sync.

const $ = (id) => document.getElementById(id);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const fmt = (n) => Number(n || 0).toLocaleString();

async function call(name, body) {
  const opts = body === undefined ? {} : {
    method: "POST",
    headers: { "content-type": "application/json", "x-binaly-sync": "1" },
    body: JSON.stringify(body),
  };
  const res = await fetch("api/" + name, opts);
  const json = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(json.error || "HTTP " + res.status);
  return json;
}

function human(b) {
  if (b >= 1 << 30) return (b / (1 << 30)).toFixed(1) + " GB";
  if (b >= 1 << 20) return (b / (1 << 20)).toFixed(1) + " MB";
  if (b >= 1 << 10) return Math.round(b / (1 << 10)) + " KB";
  return b + " bytes";
}

// The bar: processed inside synced inside total, problems as their own segment at the end.
function bar(p, small) {
  if (!p || p.total === 0) return `<div class="bar${small ? " small" : ""}"></div>`;
  const pct = (n) => (100 * n / p.total).toFixed(2) + "%";
  const syncedOnly = Math.max(0, p.synced - p.processed - p.attention);
  return `<div class="bar${small ? " small" : ""}" role="img" aria-label="${fmt(p.processed)} processed, ${fmt(p.synced)} synced, of ${fmt(p.total)} files">` +
    `<span class="processed" style="width:${pct(p.processed)}"></span>` +
    `<span class="synced" style="width:${pct(syncedOnly)}"></span>` +
    `<span style="flex:1"></span>` +
    `<span class="attention" style="width:${p.attention ? `max(4px, ${pct(p.attention)})` : "0"}"></span></div>`;
}

function legend(p) {
  if (!p) return "";
  if (p.counting && p.total === 0) return `<div class="legend">Counting files...</div>`;
  const parts = [
    `<span><b>${fmt(p.processed)}</b> processed</span>`,
    `<span><b>${fmt(p.synced)}</b> synced</span>`,
    `<span><b>${fmt(p.total)}</b> files${p.counting ? " so far (still counting)" : ""}</span>`,
  ];
  if (p.attention) parts.push(`<span class="warn"><b>${fmt(p.attention)}</b> need attention</span>`);
  if (p.notInScope) parts.push(`<span>${fmt(p.notInScope)} not in scope</span>`);
  if (p.uploadBytesLeft) parts.push(`<span>${human(p.uploadBytesLeft)} left to upload</span>`);
  return `<div class="legend">${parts.join("")}</div>`;
}

let last = null;

function renderStatus(s) {
  last = s;
  const st = s.status || {};
  $("activity").textContent = s.pausedUntil ? `Paused until ${new Date(s.pausedUntil).toLocaleString()}` : (st.running ? st.activity : (st.lastRunAt ? `Up to date. Last sync ${new Date(st.lastRunAt).toLocaleTimeString()}` : st.activity || ""));
  $("btn-pause").textContent = s.pausedUntil ? "Resume" : "Pause";
  $("btn-sync").disabled = !!st.running;
  const wss = st.workspaces && st.workspaces.length ? st.workspaces : (s.accounts || []).map((a) => ({ key: a.key, name: a.workspaceName, email: a.email, state: "ok" }));
  $("empty").hidden = wss.length > 0;
  $("workspaces").innerHTML = wss.map((w) => {
    const bad = w.state !== "ok";
    const badge = { ok: "", "needs-sign-in": "Needs sign-in", revoked: "Disconnected", error: "Problem" }[w.state] ?? w.state;
    const folders = (w.folders || []).map((f) => `
      <div class="folder">
        <div class="path">${esc(f.path)}</div>
        ${bar(f, true)}${legend(f)}
        ${f.warning ? `<div class="warning">${esc(f.warning.reason)}<div class="actions"><button data-confirm="${esc(w.key)}">These files really were removed</button></div></div>` : ""}
      </div>`).join("");
    const warnings = (w.warnings || []).filter((x) => !(w.folders || []).some((f) => f.warning && f.warning.reason === x));
    return `<div class="card">
      <div class="ws-head"><span class="ws-name">${esc(w.name)}</span>
        <span>${w.email ? `<span class="muted">${esc(w.email)}</span> ` : ""}${badge ? `<span class="badge${bad ? " bad" : ""}">${esc(badge)}</span>` : ""}</span></div>
      ${w.error ? `<p class="muted">${esc(w.error)}</p>` : ""}
      ${w.state === "needs-sign-in" ? `<div class="actions"><button class="primary" data-signin>Sign in again</button></div>` : ""}
      ${bar(w.progress)}${legend(w.progress)}
      ${warnings.map((x) => `<div class="warning">${esc(x)}</div>`).join("")}
      ${folders ? `<div class="folders">${folders}</div>` : ""}
    </div>`;
  }).join("");
  renderSettingsWorkspaces(s.accounts || []);
  renderUpdate(s.update || {});
  $("version").textContent = s.version || "";
}

function renderUpdate(u) {
  const el = $("update");
  if (u.installing) {
    el.innerHTML = `Installing Binaly Sync ${esc(u.available)}. It restarts by itself in a moment.`;
  } else if (u.available) {
    el.innerHTML = `Binaly Sync ${esc(u.available)} is available. <button class="primary" data-update>Install update</button>`;
  } else if (u.error) {
    el.innerHTML = `<span class="muted">${esc(u.error)}</span>`;
  }
  el.hidden = !(u.installing || u.available || u.error);
}

async function poll() {
  try { renderStatus(await call("status")); } catch { /* the app may be restarting */ }
}

// ---- preview ----
let previewFolder = "";
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
        <span class="n">${fmt(r.WillUpload)}</span><span>will upload (up to ${human(r.UploadLimit)} each)</span>
        <span class="n">${fmt(r.RegisterOnly)}</span><span>registered only (larger; Claude reads these from the drive)</span>
        <span class="n">${fmt(r.Excluded)}</span><span>skipped (lock files, temp files, system folders)</span>
        ${r.OnlineOnly ? `<span class="n">${fmt(r.OnlineOnly)}</span><span>online only (cloud files not on this disk; not synced)</span>` : ""}
        ${r.Unreadable ? `<span class="n">${fmt(r.Unreadable)}</span><span>unreadable (no permission)</span>` : ""}
      </div>`;
    const accts = (last && last.accounts) || [];
    $("ws-pick").innerHTML = accts.map((a) => `<option value="${esc(a.key)}">${esc(a.workspaceName)}</option>`).join("");
    $("ws-pick-label").hidden = accts.length < 2;
    $("btn-start").disabled = accts.length === 0;
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
  const [screen, query] = (location.hash.slice(1) || "status").split("?");
  for (const v of ["status", "preview", "settings"]) $("view-" + v).hidden = v !== screen;
  document.querySelectorAll("[data-nav]").forEach((a) => a.classList.toggle("on", a.dataset.nav === screen));
  if (screen === "preview") showPreview(new URLSearchParams(query).get("folder") || "");
  if (screen === "settings") loadSettings();
}
window.addEventListener("hashchange", route);

async function pickAndPreview() {
  try {
    const { folder } = await call("pick", {});
    if (folder) location.hash = "preview?folder=" + encodeURIComponent(folder);
  } catch (e) { alertBox(e.message); }
}
function alertBox(msg) { $("activity").textContent = msg; }

document.addEventListener("click", async (ev) => {
  const t = ev.target.closest("button");
  if (!t) return;
  try {
    if (t.id === "btn-sync") { await call("sync", {}); poll(); }
    else if (t.id === "btn-pause") { await call("pause", { minutes: last && last.pausedUntil ? 0 : 60 }); poll(); }
    else if (t.id === "btn-add" || t.id === "btn-change") await pickAndPreview();
    else if (t.id === "btn-start") {
      t.disabled = true;
      await call("start", { folder: previewFolder, accountKey: $("ws-pick").value });
      location.hash = "status";
    }
    else if (t.id === "btn-signin" || t.id === "btn-signin-empty" || t.hasAttribute("data-signin")) { await call("signin", {}); poll(); }
    else if (t.dataset.signout) { await call("signout", { accountKey: t.dataset.signout }); poll(); }
    else if (t.dataset.confirm) { await call("confirm-deletions", { accountKey: t.dataset.confirm }); poll(); }
    else if (t.id === "btn-log") await call("log", {});
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
