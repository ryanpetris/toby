// A page's content is fetched with the session secret, which only this
// origin's storage holds; actions call the JSON API, and the content is
// fetched again after them and whenever tobyd reports a change.
"use strict";

const KEY = "toby-session";

function secret() {
  try { return localStorage.getItem(KEY) || ""; } catch (_) { return ""; }
}

function authorized(headers) {
  return Object.assign({ authorization: "Bearer " + secret() }, headers);
}

function showError(text) {
  document.getElementById("error").textContent = text;
}

// `toby web` opens a page with #login=<token>, which becomes the secret.
async function login() {
  const m = location.hash.match(/^#login=([0-9a-f]+)$/);
  if (!m) return;
  history.replaceState(null, "", location.pathname + location.search);
  const res = await fetch("/login", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ token: m[1] }),
  });
  if (res.ok) {
    try { localStorage.setItem(KEY, (await res.json()).session); } catch (_) {}
  }
}

// Whether the user is filling in a form, which a refresh would wipe.
function editing() {
  const main = document.querySelector("main");
  const active = document.activeElement;
  if (active && main.contains(active) && active.matches("input, select, textarea")) return true;
  for (const el of main.querySelectorAll("input, textarea")) {
    if (el.type === "checkbox" ? el.checked !== el.defaultChecked : el.value !== el.defaultValue) return true;
  }
  for (const s of main.querySelectorAll("select")) {
    const initial = Math.max(0, [...s.options].findIndex((o) => o.defaultSelected));
    if (s.selectedIndex !== initial) return true;
  }
  return false;
}

let pending = false;
// Only the newest refresh's answer is shown.
let generation = 0;

async function refresh(force) {
  if (!force && editing()) {
    pending = true;
    return;
  }
  pending = false;
  const mine = ++generation;
  const res = await fetch(location.pathname + location.search, { headers: authorized({ "x-toby-part": "main" }) });
  if (mine !== generation) return res.status !== 401;
  if (res.status === 401 || res.ok) {
    const html = await res.text();
    if (mine !== generation) return res.status !== 401;
    document.querySelector("main").innerHTML = html;
    changed = Date.now();
  }
  return res.status !== 401;
}

// Clicks just after the page changed are not meant for what is now under
// the pointer.
let changed = 0;
const settled = () => Date.now() - changed > 500;

document.addEventListener("focusout", () => setTimeout(() => { if (pending && !editing()) refresh(); }, 0));

async function call(method, url, body) {
  showError("");
  const res = await fetch(url, {
    method,
    headers: authorized(body === undefined ? {} : { "content-type": "application/json" }),
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).message || msg; } catch (_) {}
    showError(msg);
    return false;
  }
  return true;
}

// <button data-method="POST" data-url="…" data-body='{…}'> and forms with
// data-url, whose named fields become the JSON body.
document.addEventListener("click", (e) => {
  const b = e.target.closest("button[data-url]");
  if (!b || b.form) return;
  if (!settled()) return;
  if (b.dataset.confirm && !confirm(b.dataset.confirm)) return;
  b.disabled = true;
  call(b.dataset.method || "POST", b.dataset.url, b.dataset.body ? JSON.parse(b.dataset.body) : undefined)
    .then(() => refresh(true))
    .finally(() => { b.disabled = false; });
});

document.addEventListener("submit", (e) => {
  const f = e.target.closest("form[data-url]");
  if (!f) return;
  e.preventDefault();
  const body = {};
  for (const el of f.elements) {
    if (!el.name) continue;
    if (el.type === "checkbox") body[el.name] = el.checked;
    else if (el.dataset.bool !== undefined) body[el.name] = el.value === "true";
    else if (el.dataset.number !== undefined) body[el.name] = Number(el.value);
    else if (el.value !== "") body[el.name] = el.value;
  }
  call(f.dataset.method || "POST", f.dataset.url, body).then((ok) => {
    if (ok) f.reset();
    refresh(ok);
  });
});

function socket(path) {
  const url = (location.protocol === "https:" ? "wss://" : "ws://") + location.host + path;
  return new WebSocket(url, ["toby", secret()]);
}

// Appends to a log, keeping about its last million characters.
const KEEP = 1000000;
function append(pre, text) {
  const bottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 4;
  pre.append(text);
  pre.kept = (pre.kept || 0) + text.length;
  while (pre.kept > KEEP && pre.firstChild) {
    pre.kept -= pre.firstChild.textContent.length;
    pre.firstChild.remove();
  }
  if (bottom) pre.scrollTop = pre.scrollHeight;
}

// Log pages: <pre class="log" data-ws="/v1/…/logs">.
function follow(pre) {
  const ws = socket(pre.dataset.ws);
  ws.onmessage = (m) => append(pre, m.data + "\n");
  ws.onclose = () => append(pre, "[closed]\n");
}

// Build output: <pre class="log" data-stream="/v1/builds/…/logs">, read as
// it arrives.
async function stream(pre) {
  const res = await fetch(pre.dataset.stream, { headers: authorized({}) });
  if (!res.ok) return;
  const reader = res.body.getReader();
  const text = new TextDecoder();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    append(pre, text.decode(value, { stream: true }));
  }
  // The build ended: show how.
  const state = document.getElementById("build-state");
  const status = await fetch(pre.dataset.stream.replace(/\/logs$/, ""), { headers: authorized({}) });
  if (state && status.ok) {
    const s = (await status.json()).state;
    state.textContent = s;
    state.className = "state-" + s;
  }
}

// Changes before the socket opened are not reported, so an open refreshes;
// a page that is no longer logged in says so and stops.
function events() {
  let timer = null;
  const ws = socket("/v1/events");
  const soon = () => {
    clearTimeout(timer);
    timer = setTimeout(refresh, 300);
  };
  ws.onopen = soon;
  ws.onmessage = soon;
  ws.onclose = async () => {
    let loggedIn = true;
    try { loggedIn = await refresh(true); } catch (_) {}
    if (loggedIn) setTimeout(events, 3000);
  };
}

(async () => {
  await login();
  await refresh(true);
  const logs = document.querySelectorAll("pre[data-ws], pre[data-stream]");
  for (const pre of logs) {
    if (pre.dataset.ws) follow(pre);
    else stream(pre);
  }
  if (logs.length === 0 && secret()) events();
})();
