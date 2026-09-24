// Actions call the JSON API; the page's main part is fetched again after
// them and whenever tobyd reports a change.
"use strict";

function showError(text) {
  document.getElementById("error").textContent = text;
}

async function refresh() {
  const res = await fetch(location.href, { headers: { "x-toby-part": "main" } });
  if (!res.ok) return;
  document.querySelector("main").innerHTML = await res.text();
}

async function call(method, url, body) {
  showError("");
  const res = await fetch(url, {
    method,
    headers: body === undefined ? {} : { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    let msg = res.statusText;
    try { msg = (await res.json()).message || msg; } catch (_) {}
    showError(msg);
    return false;
  }
  await refresh();
  return true;
}

// <button data-method="POST" data-url="…" data-body='{…}'> and forms with
// data-url, whose named fields become the JSON body.
document.addEventListener("click", (e) => {
  const b = e.target.closest("button[data-url]");
  if (!b || b.form) return;
  if (b.dataset.confirm && !confirm(b.dataset.confirm)) return;
  b.disabled = true;
  call(b.dataset.method || "POST", b.dataset.url, b.dataset.body ? JSON.parse(b.dataset.body) : undefined)
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
    else if (el.dataset.number !== undefined) body[el.name] = Number(el.value);
    else if (el.value !== "") body[el.name] = el.value;
  }
  call(f.dataset.method || "POST", f.dataset.url, body).then((ok) => { if (ok) f.reset(); });
});

function socket(path) {
  return new WebSocket((location.protocol === "https:" ? "wss://" : "ws://") + location.host + path);
}

// Log pages: <pre class="log" data-ws="/v1/…/logs">.
for (const pre of document.querySelectorAll("pre[data-ws]")) {
  const ws = socket(pre.dataset.ws);
  ws.onmessage = (m) => {
    const bottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 4;
    pre.textContent += m.data + "\n";
    if (bottom) pre.scrollTop = pre.scrollHeight;
  };
  ws.onclose = () => { pre.textContent += "[closed]\n"; };
}

// Build output: <pre class="log" data-stream="/v1/builds/…/logs">, read as
// it arrives.
for (const pre of document.querySelectorAll("pre[data-stream]")) {
  (async () => {
    const res = await fetch(pre.dataset.stream);
    const reader = res.body.getReader();
    const text = new TextDecoder();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      const bottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 4;
      pre.textContent += text.decode(value, { stream: true });
      if (bottom) pre.scrollTop = pre.scrollHeight;
    }
  })();
}

if (!document.querySelector("pre[data-ws], pre[data-stream]")) {
  let timer = null;
  const events = () => {
    const ws = socket("/v1/events");
    ws.onmessage = () => {
      clearTimeout(timer);
      timer = setTimeout(refresh, 300);
    };
    ws.onclose = () => setTimeout(events, 3000);
  };
  events();
}
