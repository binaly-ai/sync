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
// scheduled task in Claude, and answering the lab's questions. Until anything has been
// processed, say so from the start (not only once files have arrived: people connected their
// AI assistant late because this appeared late) and point to the AI assistant page.
function processingNote(w) {
  const p = w.progress;
  if (!w.key || w.state !== "ok" || (p && p.processed > 0)) return "";
  const waiting = p ? stages(p).waiting : 0;
  return `<div class="note-box">
    <b>Next: connect your AI assistant.</b> Binaly Sync only sends the lab's files to Binaly. Your
    AI assistant (Claude Desktop) reads each one into the lab's notes, as a scheduled task that runs
    every hour, and answers the lab's questions from them. It's set up once.${waiting ? ` Until it runs, ${fmt(waiting)} file${waiting === 1 ? " waits" : "s wait"} for it.` : ""}
    <div class="actions"><button class="primary" data-assistant="${esc(w.key)}">Connect your AI assistant</button></div>
  </div>`;
}

// ---- AI assistant ----
// The steps come from the workspace's server (GET /sync/v1/assistant), the same text as the
// lab's Binaly Sync page in the app. A server that predates them gets a link to that page.
let assistantKey = "";
let assistantTab = 0;
let assistantData = null;
function fillAssistantWs() {
  const accts = (last && last.accounts) || [];
  if (!assistantKey || !accts.some((a) => a.key === assistantKey)) assistantKey = accts[0] ? accts[0].key : "";
  $("as-ws").innerHTML = accts.map((a) => `<option value="${esc(a.key)}"${a.key === assistantKey ? " selected" : ""}>${esc(a.workspaceName)}</option>`).join("");
  $("as-ws-label").hidden = accts.length < 2;
}
async function showAssistant() {
  if (!last) { $("assistant").innerHTML = `<p class="muted">Loading...</p>`; return; } // renderStatus calls back
  fillAssistantWs();
  if (!assistantKey) {
    $("assistant").innerHTML = `<p class="card">Sign in to a workspace first. <button class="primary" data-signin>Sign in</button></p>`;
    return;
  }
  $("assistant").innerHTML = `<p class="muted">Loading...</p>`;
  try {
    assistantData = await call("assistant", { accountKey: assistantKey });
  } catch (e) {
    $("assistant").innerHTML = `<div class="warning">Couldn't load the steps from Binaly: ${esc(e.message)}</div>
      <div class="actions"><button data-assistant-retry>Try again</button><button class="quiet" data-setup="${esc(assistantKey)}">Open the Binaly Sync page in the app</button></div>`;
    return;
  }
  renderAssistant();
}
function renderAssistant() {
  const d = assistantData || {};
  if (d.missing || !d.connectorUrl) {
    $("assistant").innerHTML = `<div class="card"><p>The lab's Binaly Sync page in the app shows how to connect your AI assistant and set up processing.</p>
      <div class="actions"><button class="primary" data-setup="${esc(assistantKey)}">Open the Binaly Sync page</button></div></div>`;
    return;
  }
  $("assistant").innerHTML = connectCard(d, "1. Add Binaly to your AI assistant") +
    processingCard(d, "2. Set up processing") +
    (d.docsUrl ? `<div class="actions"><button class="quiet link" data-docs="${esc(assistantKey)}">The full guide, in your browser</button></div>` : "");
}

// The two halves of connecting, shared by the AI assistant page and the wizard. Only one is
// on screen at a time, so their element ids don't clash.
function connectCard(d, heading) {
  const list = d.assistants || [];
  if (assistantTab >= list.length) assistantTab = 0;
  const a = list[assistantTab];
  const steps = (xs) => xs && xs.length ? `<ol class="steps">${xs.map((x) => `<li>${esc(x)}</li>`).join("")}</ol>` : "";
  return `<div class="card">
      <h2 style="margin-top:0">${esc(heading)}</h2>
      <div class="copy-line"><code id="as-url">${esc(d.connectorUrl)}</code><button data-copy="as-url">Copy address</button><span class="copied" id="as-url-copied" hidden>Copied</span></div>
      ${list.length > 1 ? `<div class="assistant-tabs">${list.map((x, i) => `<button class="small${i === assistantTab ? " on" : ""}" data-as-tab="${i}">${esc(x.name)}</button>`).join("")}</div>` : ""}
      ${a ? `${list.length === 1 ? `<p><b>${esc(a.name)}</b></p>` : ""}${steps(a.steps)}` : ""}
      ${d.afterConnect ? `<p>${esc(d.afterConnect)}</p>` : ""}
    </div>`;
}
function processingCard(d, heading) {
  const proc = d.processing || {};
  if (!proc.setupPrompt) return "";
  return `<div class="card">
      <h2 style="margin-top:0">${esc(heading)}</h2>
      ${proc.lead ? `<p>${esc(proc.lead)}</p>` : ""}
      <pre class="prompt" id="as-prompt">${esc(proc.setupPrompt)}</pre>
      <div class="copy-line"><button class="primary" data-copy="as-prompt">Copy the setup prompt</button><span class="copied" id="as-prompt-copied" hidden>Copied</span></div>
      <p class="muted">The task runs only while Claude Desktop is open, so set it up on a computer that stays on, with Claude Desktop running. It doesn't have to be this computer.</p>
    </div>`;
}

// ---- first-launch wizard ----
// Three steps: sign in and add a folder, connect the AI assistant, set up processing. Each can
// be skipped. Sync starts counting and uploading as soon as the folder is added, so a progress
// line runs under the later steps. Steps 2 and 3 also open for a workspace signed in later
// ("welcome?step=2&account=...").
let wizStep = 1;
let wizKey = "";
let wizData = null;
let wizDataKey = "";
let previewWizard = false;
function wizAccount() {
  const accts = (last && last.accounts) || [];
  if (!wizKey || !accts.some((a) => a.key === wizKey)) wizKey = accts[0] ? accts[0].key : "";
  return wizKey;
}
async function showWizard() {
  document.querySelectorAll("#wiz-steps li").forEach((li) => {
    const n = Number(li.dataset.step);
    li.classList.toggle("on", n === wizStep);
    li.classList.toggle("past", n < wizStep);
  });
  const later = `<button class="quiet" data-wiz-later>I'll do this later</button>`;
  if (!last) { $("wizard").innerHTML = `<p class="muted">Loading...</p>`; return; } // renderStatus calls back
  const accts = last.accounts || [];
  if (wizStep === 1) {
    if (!accts.length) {
      $("wizard").innerHTML = `<h1>Welcome to Binaly Sync</h1>
        <p>Binaly Sync keeps the lab's folders connected to Binaly, so your AI assistant can answer from the lab's own files. Start by signing in with your Binaly account; your browser opens for it.</p>
        <div class="actions"><button class="primary" data-wiz-signin>Sign in</button>${later}</div>`;
      return;
    }
    const synced = ((last.status || {}).workspaces || []).flatMap((w) => w.folders || []);
    $("wizard").innerHTML = `<h1>Choose a folder to sync</h1>
      <p>Pick a folder with the lab's files: papers, notebooks, data, presentations. You'll see what Binaly Sync found before anything is sent.</p>
      ${synced.length ? `<p class="muted">Already syncing: ${synced.map((f) => `<span class="path-inline">${esc(f.path)}</span>`).join(", ")}</p>` : ""}
      <div class="note-box sensitive"><b>Before you choose a folder.</b> Everything in a synced folder becomes notes that every member's AI assistant can read back. Leave out evaluations of people, medical or HR records, and peer reviews you're writing for a journal. Binaly holds back what it recognizes as sensitive and lists it on the Binaly Sync page for you to decide, but it can't recognize everything.</div>
      <div class="actions"><button class="primary" data-wiz-pick>Choose a folder...</button>${synced.length ? `<button data-wiz-next>Next</button>` : ""}${later}</div>`;
    return;
  }
  const key = wizAccount();
  if (!key) { finishWizard(); return; } // steps 2 and 3 need a workspace
  if (wizDataKey !== key) {
    $("wizard").innerHTML = `<p class="muted">Loading...</p>`;
    try {
      wizData = await call("assistant", { accountKey: key });
      wizDataKey = key;
    } catch (e) {
      $("wizard").innerHTML = `<div class="warning">Couldn't load the steps from Binaly: ${esc(e.message)}</div>
        <div class="actions"><button data-wiz-retry>Try again</button>${later}</div>`;
      return;
    }
  }
  const d = wizData || {};
  const name = (accts.find((a) => a.key === key) || {}).workspaceName || "";
  if (d.missing || !d.connectorUrl) {
    $("wizard").innerHTML = `<h1>Connect your AI assistant</h1>
      <p>The lab's Binaly Sync page in the app shows how to connect your AI assistant and set up processing.</p>
      <div class="actions"><button class="primary" data-setup="${esc(key)}">Open the Binaly Sync page</button><button data-wiz-finish>Done</button></div>`;
    return;
  }
  if (wizStep === 2) {
    $("wizard").innerHTML = `<h1>Connect Binaly to your AI assistant</h1>
      <p>Your AI assistant is how the lab uses its files: ask it questions, and it answers from ${name ? esc(name) + "'s" : "the lab's"} notes and files. Choose the one you use.</p>
      ${connectCard(d, "Add the Binaly connector")}
      <div class="actions"><button class="primary" data-wiz-next>Next</button>${later}</div>`;
    return;
  }
  $("wizard").innerHTML = `<h1>Set up processing</h1>
    <p>Processing reads each file Binaly Sync sends and writes it into the lab's notes. It runs as an hourly task in Claude Desktop, set up once for the whole lab. If someone in the lab already set it up, skip this step.</p>
    ${processingCard(d, "Paste this into Claude Desktop")}
    <div class="actions"><button class="primary" data-wiz-finish>Done</button>${later.replace("data-wiz-later", "data-wiz-finish")}</div>`;
}
function wizNext() {
  if (wizStep >= 3) { finishWizard(); return; }
  wizStep++;
  showWizard();
}
async function finishWizard() {
  try { await call("wizard-done", { accountKey: wizKey }); } catch { /* shown again next launch */ }
  previewWizard = false;
  go("status");
  poll();
}
// The live line under the wizard: Sync works while the person reads.
function wizProgress(s) {
  const el = $("wiz-progress");
  if (!el || $("view-welcome").hidden) return;
  if (!(s.accounts || []).length) { el.hidden = true; return; }
  const st = s.status || {};
  const ws = (st.workspaces || []).find((w) => w.key === wizKey) || (st.workspaces || [])[0];
  const p = ws && ws.progress;
  let text = "";
  if (p && p.total) {
    const x = stages(p);
    text = `${fmt(p.total)} files found${p.counting ? " so far" : ""}, ${fmt(x.processed + x.waiting + x.attention)} in Binaly${x.toUpload ? `, ${fmt(x.toUpload)} to upload` : ""}.`;
  } else if (p && p.counting) text = "Counting files...";
  if (st.running && st.activity) text = text ? `${text} ${st.activity}.` : `${st.activity}.`;
  el.innerHTML = text ? `${st.running ? spinner : ""}<span>${esc(text)}</span>` : "";
  el.hidden = !text;
}

// The window's page has no secure origin, so the clipboard API may be refused; a selected
// text area and the copy command work everywhere.
async function copyText(id) {
  const text = $(id).textContent;
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    ta.remove();
  }
  const done = $(id + "-copied");
  if (done) { done.hidden = false; setTimeout(() => (done.hidden = true), 2000); }
}

let last = null;
let wizRenderedFor = -1; // accounts known when the wizard last drew its step
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
  $("notice").hidden = !st.notice;
  $("notice").textContent = st.notice || "";
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
  // The window can open on the AI assistant page before the first status arrives: fill it in
  // once the workspaces are known, or when one is signed in from that page.
  if (!$("view-assistant").hidden && (s.accounts || []).length && !assistantKey) showAssistant();
  if (!$("view-tree").hidden && (s.accounts || []).length && !treeData) showTree();
  if (!$("view-welcome").hidden) {
    // First status, or a sign-in that just finished: the step's content depends on them.
    const n = (s.accounts || []).length;
    if (wizRenderedFor !== n) { wizRenderedFor = n; showWizard(); }
    wizProgress(s);
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
  $("set-check").value = s.checkMinutes || 15;
}

// ---- Tree ----
// This computer's folders as a tree that opens in place, like File Explorer: the synced folders
// first, with how far each one's files have got (POST /sync/v1/tree on the server), then the
// computer's own places, to find a folder that isn't synced yet. Pointing at a folder shows its
// buttons: "Process first" puts its files ahead of everything else, "Leave out" keeps them from
// being processed or sent.
let treeKey = "";
let treeData = null; // the top: synced folders and places
const treeNodes = new Map(); // path -> that folder's level, as the program returned it
const treeOpen = new Map(); // folders shown open: where in the tree (section and path) -> path
const treeLoading = new Set();
let treeConfirm = "";
let treeMsg = "";
let treeBusy = false;
const TREE_ORDER = ["processed", "partial", "waiting", "local", "uploading", "sensitive", "failed", "held", "unreachable", "skipped", "left-out", "not-in-scope", "not-read", "gone"];
const TREE_BAD = new Set(["sensitive", "failed", "held", "unreachable"]);
const TREE_RELEASE = new Set(["sensitive", "failed", "skipped", "unreachable"]);
// The mini bar's parts, in the status page's colors: done, in Binaly, on its way, needs a person.
const TREE_BAR = [
  ["processed", ["processed", "partial"]],
  ["waiting", ["waiting", "local"]],
  ["toUpload", ["uploading"]],
  ["attention", ["sensitive", "failed", "held", "unreachable"]],
];
const TREE_MAX_ROWS = 300;
const baseName = (p) => String(p).split(/[\\/]/).filter(Boolean).pop() || p;
const keyOf = (p) => String(p).toLowerCase();
const ICON_DIR = `<svg class="tn-icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M1.5 3.5h4l1.5 1.5h7.5v8h-13z" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>`;
const ICON_FILE = `<svg class="tn-icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 1.5h6l3 3v10h-9z M9.5 1.5v3h3" fill="none" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/></svg>`;

function fillTreeWs() {
  const accts = (last && last.accounts) || [];
  if (!treeKey || !accts.some((a) => a.key === treeKey)) treeKey = accts[0] ? accts[0].key : "";
  $("tree-ws").innerHTML = accts.map((a) => `<option value="${esc(a.key)}"${a.key === treeKey ? " selected" : ""}>${esc(a.workspaceName)}</option>`).join("");
  $("tree-ws-label").hidden = accts.length < 2;
}

async function loadNode(path) {
  treeLoading.add(keyOf(path));
  try {
    treeNodes.set(keyOf(path), await call("tree", { accountKey: treeKey, path }));
  } catch (e) {
    treeNodes.set(keyOf(path), { path, entries: [], localError: e.message });
  }
  treeLoading.delete(keyOf(path));
}

// showTree("") starts again from the top; showTree() reloads what is on screen.
async function showTree(reset) {
  if (!last) { $("tree").innerHTML = `<p class="muted">Loading...</p>`; return; }
  fillTreeWs();
  if (!treeKey) {
    $("tree").innerHTML = `<p class="card">Sign in to a workspace first. <button class="primary" data-signin>Sign in</button></p>`;
    return;
  }
  if (reset === "" || !treeData) {
    treeNodes.clear();
    treeOpen.clear();
    treeConfirm = "";
    $("tree").innerHTML = `<p class="muted">Loading...</p>`;
  }
  try {
    treeData = await call("tree", { accountKey: treeKey, path: "" });
  } catch (e) {
    $("tree").innerHTML = `<div class="warning">Couldn't load this computer's folders: ${esc(e.message)}</div>`;
    return;
  }
  // The synced folders' own counts, and every open folder, fresh.
  const want = [...(treeData.synced || []), ...treeOpen.values()];
  await Promise.all([...new Map(want.map((p) => [keyOf(p), p])).values()].map(loadNode));
  renderTree();
}

async function toggleNode(where, path) {
  const k = keyOf(path);
  if (treeOpen.has(where)) { treeOpen.delete(where); renderTree(); return; }
  treeOpen.set(where, path);
  if (!treeNodes.has(k)) { treeLoading.add(k); renderTree(); await loadNode(path); }
  renderTree();
}

function treeCounts(counts, labels) {
  return TREE_ORDER.filter((k) => counts && counts[k])
    .map((k) => `<span${TREE_BAD.has(k) ? ' class="warn"' : ""}>${fmt(counts[k])} ${esc((labels && labels[k]) || k)}</span>`)
    .join(" · ");
}

function miniBar(counts, files) {
  if (!files) return `<span class="tn-bar empty"></span>`;
  const parts = TREE_BAR.map(([cls, keys]) => {
    const n = keys.reduce((a, k) => a + ((counts && counts[k]) || 0), 0);
    return n ? `<span class="${cls}" style="width:${(100 * n) / files}%"></span>` : "";
  }).join("");
  return `<span class="tn-bar">${parts}</span>`;
}

// Only a folder's own mark: the folders inside it sit under its badge.
function markBadge(m) {
  if (!m || !m.own) return "";
  const what = m.mark === "first" ? "First" : "Left out";
  const who = `${m.mark === "first" ? "Process first" : "Left out"}. Set on ${m.computer}${m.by ? " by " + m.by : ""}, ${String(m.at).slice(0, 10)}`;
  return `<span class="badge${m.mark === "first" ? " first" : ""}" title="${esc(who)}">${what}</span>`;
}

// A synced folder's buttons: what its own mark allows, and "Read it" when files wait on a person.
function folderActions(path, mark, counts, isRoot) {
  const own = mark && mark.own ? mark.mark : "";
  const b = [];
  if (own === "first") b.push(`<button class="small" data-tree-mark="normal" data-path="${esc(path)}">Normal</button>`);
  else if (own === "leave-out") b.push(`<button class="small" data-tree-mark="normal" data-path="${esc(path)}">Undo leave out</button>`);
  else {
    b.push(`<button class="small" data-tree-mark="first" data-path="${esc(path)}">Process first</button>`);
    if (!isRoot) b.push(`<button class="small quiet" data-tree-leave="${esc(path)}">Leave out</button>`);
  }
  if (counts && counts.sensitive) b.push(`<button class="small quiet" data-tree-release="${esc(path)}">Read it</button>`);
  return `<span class="tn-act${own ? " keep" : ""}">${b.join("")}</span>`;
}

function leaveConfirm(path, files) {
  return `<div class="note-box tn-confirm"><b>Leave out ${esc(baseName(path))}?</b> Binaly won't process ${files ? `its ${fmt(files)} files` : "its files"}, and no computer will send them any more. Files already in Binaly stay there, with any notes made from them. You can undo this.
    <div class="actions"><button class="primary" data-tree-mark="leave-out" data-path="${esc(path)}">Leave out</button><button class="quiet" data-tree-cancel>Cancel</button></div></div>`;
}

// One folder: its row, and when open, its children inside a guide line.
function folderNode(e, labels, isRoot, section) {
  const k = keyOf(e.path);
  const where = section + "|" + k;
  const open = treeOpen.has(where);
  const node = treeNodes.get(k);
  const lvl = node && node.level;
  // A folder's own level (once loaded) is fresher and complete; the parent's row is the fallback.
  const f = lvl ? { files: lvl.files, counts: lvl.counts, mark: lvl.mark, answer: lvl.answer } : e.folder;
  const gone = e.local === false ? ` <span class="muted">(not on this computer)</span>` : "";
  let status, actions = "";
  if (e.synced) {
    status = f ? `${miniBar(f.counts, f.files)}<span class="tn-counts">${f.files ? treeCounts(f.counts, (lvl && lvl.labels) || labels) : "nothing in Binaly yet"}</span>`
      : `<span class="tn-counts muted">nothing in Binaly yet</span>`;
    if (f) actions = folderActions(e.path, f.mark, f.counts, isRoot);
  } else {
    status = `<span class="tn-counts muted">${e.holdsSynced ? "a synced folder is inside" : "not synced"}</span>`;
    if (!e.holdsSynced) actions = `<span class="tn-act"><button class="small quiet" data-tree-add="${esc(e.path)}">Add</button></span>`;
  }
  const row = `<div class="tn-row${e.synced ? "" : " unsynced"}">
      <button class="tn-toggle" data-tree-open="${esc(e.path)}" data-where="${esc(where)}" aria-expanded="${open}" title="${esc(e.path)}">
        <span class="tn-chev${open ? " open" : ""}" aria-hidden="true"></span>${ICON_DIR}<span class="tn-name">${esc(e.name)}</span>
      </button>${gone}${f ? markBadge(f.mark) : ""}
      <span class="tn-status">${status}</span>${actions}
    </div>`;
  const why = f && f.answer && open ? `<div class="tn-why muted">The AI assistant's view of this folder: ${esc(f.answer.because)}</div>` : "";
  const confirm = treeConfirm === e.path ? leaveConfirm(e.path, f && f.files) : "";
  let kids = "";
  if (open) {
    if (treeLoading.has(k) || !node) kids = `<div class="tn-kids"><div class="tn-note muted">Loading...</div></div>`;
    else kids = `<div class="tn-kids">${why}${children(node, section)}</div>`;
  }
  return `<div class="tn">${row}${confirm}${kids}</div>`;
}

function fileNode(e, labels) {
  const f = e.file;
  const gone = e.local === false ? ` <span class="muted">(not on this computer)</span>` : "";
  const state = f ? `<span class="tn-state${TREE_BAD.has(f.state) ? " warn" : ""}">${esc(labels[f.state] || f.state)}</span>`
    : e.synced ? `<span class="tn-state muted">not in Binaly yet</span>` : "";
  const notes = f && (f.notes || []).length ? f.notes.join(", ") : "";
  const detail = f && f.detail && f.detail !== notes ? f.detail : "";
  const tip = [detail, notes ? "Notes: " + notes : ""].filter(Boolean).join("\n");
  return `<div class="tn"><div class="tn-row file"${tip ? ` title="${esc(tip)}"` : ""}>
      <span class="tn-spacer"></span>${ICON_FILE}<span class="tn-name">${esc(e.name)}</span>${gone}
      <span class="tn-status">${state}${e.size ? ` <span class="muted tn-size">${human(e.size)}</span>` : ""}${notes ? ` <span class="tn-notes muted">${esc(notes)}</span>` : ""}</span>
      ${f && TREE_RELEASE.has(f.state) ? `<span class="tn-act"><button class="small quiet" data-tree-release="${esc(e.path)}">Read it</button></span>` : ""}
    </div></div>`;
}

function children(node, section) {
  const labels = (node.level && node.level.labels) || {};
  const notes = [node.serverNote, node.localError].filter(Boolean).map((x) => `<div class="tn-note warn">${esc(x)}</div>`).join("");
  const entries = node.entries || [];
  const rows = entries.slice(0, TREE_MAX_ROWS).map((e) => (e.dir ? folderNode(e, labels, false, section) : fileNode(e, labels))).join("");
  const more = entries.length > TREE_MAX_ROWS ? `<div class="tn-note muted">And ${fmt(entries.length - TREE_MAX_ROWS)} more.</div>` : "";
  return notes + (rows || (notes ? "" : `<div class="tn-note muted">Empty.</div>`)) + more;
}

function renderTree() {
  const d = treeData || {};
  const msg = treeMsg ? `<div class="note-box">${esc(treeMsg)}</div>` : "";
  const synced = (d.synced || []).map((p) => folderNode({ name: p, path: p, dir: true, local: true, synced: true }, {}, true, "s")).join("");
  const places = (d.places || []).map((p) => folderNode({ name: p, path: p, dir: true, local: true, synced: false, holdsSynced: (d.synced || []).some((s) => keyOf(s).startsWith(keyOf(p).replace(/[\\/]$/, "") + "\\")) }, {}, false, "c")).join("");
  $("tree").innerHTML = msg + `<div class="card tree">
      <div class="tn-head">Synced from this computer</div>
      ${synced || `<div class="tn-note muted">No folders synced from this computer yet.</div>`}
      <div class="tn-head">This computer</div>
      ${places}
    </div>`;
}

// One Tree action at a time; its result is said above the tree, and what's open is reloaded.
async function treeAction(fn, done) {
  if (treeBusy) return;
  treeBusy = true;
  treeMsg = "";
  try {
    treeMsg = done((await fn()) || {});
  } catch (e) {
    treeMsg = e.message;
  }
  treeBusy = false;
  treeConfirm = "";
  await showTree();
}

async function treeClick(t) {
  if (t.dataset.treeOpen !== undefined) { await toggleNode(t.dataset.where, t.dataset.treeOpen); return true; }
  if (t.dataset.treeAdd) { go("preview?folder=" + encodeURIComponent(t.dataset.treeAdd)); return true; }
  if (t.dataset.treeLeave) { treeConfirm = t.dataset.treeLeave; renderTree(); return true; }
  if (t.hasAttribute("data-tree-cancel")) { treeConfirm = ""; renderTree(); return true; }
  if (t.dataset.treeMark) {
    const mark = t.dataset.treeMark, path = t.dataset.path, name = baseName(path);
    await treeAction(() => call("mark", { accountKey: treeKey, path, mark }), (r) =>
      mark === "first" ? `${name} comes first now: its files are processed before everything else.${r.released ? ` ${fmt(r.released)} held file${r.released === 1 ? " goes" : "s go"} back to be read.` : ""}`
      : mark === "leave-out" ? `${name} is left out. Binaly won't process its files, and no computer will send them any more.`
      : `${name} is back to normal.`);
    return true;
  }
  if (t.dataset.treeRelease) {
    const path = t.dataset.treeRelease;
    await treeAction(() => call("release", { accountKey: treeKey, path }), (r) =>
      r.queued ? `${fmt(r.queued)} file${r.queued === 1 ? "" : "s"} will be read at the next processing run.` : "Nothing there was held back.");
    return true;
  }
  return false;
}

// ---- routing ----
function route() {
  const [screen, query] = (current || "status").split("?");
  for (const v of ["welcome", "status", "tree", "preview", "assistant", "settings", "about"]) $("view-" + v).hidden = v !== screen;
  // The wizard has its own way forward; the menu comes back when it's done.
  $("nav").hidden = screen === "welcome" || (screen === "preview" && previewWizard);
  document.querySelectorAll("[data-nav]").forEach((a) => a.classList.toggle("on", a.dataset.nav === screen));
  const q = new URLSearchParams(query);
  if (screen === "preview") {
    previewWizard = q.get("wizard") === "1";
    $("btn-wiz-later").hidden = !previewWizard;
    showPreview(q.get("folder") || "");
  }
  if (screen === "welcome") {
    wizStep = Number(q.get("step")) || 1;
    if (q.get("account")) wizKey = q.get("account");
    showWizard();
  }
  if (screen === "settings") loadSettings();
  if (screen === "assistant") showAssistant();
  if (screen === "tree") showTree();
}
// Screens are "welcome", "status", "assistant", "settings" or "preview?folder=...". The program opens one with
// binalyShow, e.g. after the tray's "Add folder...".
let current = window.binalyStartScreen || "status";
function go(screen) { current = screen; route(); }
window.binalyShow = go;
// This page has no address to reload; keep F5 and Ctrl+R from blanking the window.
document.addEventListener("keydown", (ev) => {
  if (ev.key === "F5" || ((ev.ctrlKey || ev.metaKey) && (ev.key === "r" || ev.key === "R"))) ev.preventDefault();
});

async function pickAndPreview(wizard) {
  try {
    const { folder } = await call("pick", {});
    if (folder) go("preview?folder=" + encodeURIComponent(folder) + (wizard ? "&wizard=1" : ""));
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
    else if (t.id === "btn-add" || t.id === "btn-change") await pickAndPreview(t.id === "btn-change" && previewWizard);
    else if (t.id === "btn-start") {
      t.disabled = true;
      await call("start", { folder: previewFolder, accountKey: $("ws-pick").value });
      if (previewWizard) {
        wizKey = $("ws-pick").value || wizKey;
        previewWizard = false;
        go("welcome?step=2");
      } else go("status");
    }
    else if (t.id === "btn-wiz-later") { previewWizard = false; go("welcome?step=2"); }
    else if (t.hasAttribute("data-wiz-signin")) { t.disabled = true; await call("signin", {}); await poll(); showWizard(); }
    else if (t.hasAttribute("data-wiz-pick")) await pickAndPreview(true);
    else if (t.hasAttribute("data-wiz-next") || t.hasAttribute("data-wiz-later")) {
      // Skipping the sign-in skips the steps that need a workspace too.
      if (wizStep === 1 && !((last && last.accounts) || []).length) finishWizard();
      else wizNext();
    }
    else if (t.hasAttribute("data-wiz-finish")) finishWizard();
    else if (t.hasAttribute("data-wiz-retry")) { wizDataKey = ""; showWizard(); }
    else if (t.id === "btn-signin" || t.id === "btn-signin-empty" || t.hasAttribute("data-signin")) { await call("signin", {}); poll(); }
    else if (t.dataset.signout) { await call("signout", { accountKey: t.dataset.signout }); poll(); }
    else if (t.dataset.confirm) { await call("confirm-deletions", { accountKey: t.dataset.confirm }); poll(); }
    else if (t.dataset.setup) await call("open-setup", { accountKey: t.dataset.setup });
    else if (t.dataset.assistant) { assistantKey = t.dataset.assistant; go("assistant"); }
    else if (t.hasAttribute("data-assistant-retry")) showAssistant();
    else if (t.dataset.asTab) { assistantTab = Number(t.dataset.asTab); if (!$("view-welcome").hidden) showWizard(); else renderAssistant(); }
    else if (t.dataset.copy) await copyText(t.dataset.copy);
    else if (t.dataset.docs) await call("open-docs", { accountKey: t.dataset.docs });
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
    else if (await treeClick(t)) { /* the Files page */ }
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
      await call("settings", { settings: { startAtLogon: $("set-logon").checked, updates: $("set-updates").value, bandwidthKBps: Number($("set-bw").value) || 0, checkMinutes: Number($("set-check").value) || 15 } });
      loadSettings(); // shows the interval as saved, within its limits
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
document.addEventListener("change", (ev) => {
  if (ev.target && ev.target.id === "ws-pick") updateInsideNote();
  if (ev.target && ev.target.id === "as-ws") { assistantKey = ev.target.value; assistantTab = 0; showAssistant(); }
  if (ev.target && ev.target.id === "tree-ws") { treeKey = ev.target.value; treeData = null; showTree(""); }
});
