// Header: claim between notifications and absence; Account menu instead of logout.
(function () {
  if (typeof nextStatuses === "function") {
    const _orig = nextStatuses;
    window.nextStatuses = function (cur) {
      const list = _orig(cur) || [];
      if (cur && list.indexOf(cur) < 0) list.unshift(cur);
      return list;
    };
  }

  const HUB = "https://s.ks.tv";
  function absHub(href) {
    if (!href || href.startsWith("http") || href.startsWith("#") || href.startsWith("mailto:")) return href;
    if (href.startsWith("/")) return HUB + href;
    return href;
  }
  function fixToolLinks() {
    document.querySelectorAll("#hub-tools-menu a, a[href^='/radar'], a[href^='/certs'], a[href^='/mentions'], a[href^='/ether'], a[href^='/aws'], a[href^='/netmap']").forEach((a) => {
      const h = a.getAttribute("href") || "";
      if (h.startsWith("/") && location.host.indexOf(":85") >= 0) a.setAttribute("href", absHub(h));
    });
  }
  setInterval(fixToolLinks, 800);

  const _fetch = window.fetch;
  window.fetch = function (url, opts) {
    const u = String(url || "");
    if (u.indexOf("/api/admin/jira/import") >= 0 && opts && opts.body) {
      try {
        const b = JSON.parse(opts.body);
        if (!b.jql || String(b.jql).indexOf("project = VID") < 0) delete b.jql;
        opts = Object.assign({}, opts, { body: JSON.stringify(b) });
      } catch (e) {}
    }
    return _fetch.call(this, url, opts);
  };

  function loggedIn() {
    try { return !!JSON.parse(localStorage.getItem("u") || "null"); } catch (e) { return false; }
  }
  function authHeaders() {
    const h = { "Content-Type": "application/json", Accept: "application/json" };
    try {
      const t = localStorage.getItem("oncall_session") || localStorage.getItem("oncall_session_token") || "";
      if (t) h.Authorization = "Bearer " + t;
    } catch (e) {}
    return h;
  }
  const origToggle = window.toggleAuth;

  function ensureClaim() {
    const abs = document.getElementById("btn-abs");
    if (!abs || document.getElementById("btn-claim")) return;
    const b = document.createElement("button");
    b.id = "btn-claim";
    b.className = "btn";
    b.type = "button";
    b.textContent = "Взяти задачу";
    b.style.display = "none";
    b.onclick = openPool;
    abs.parentNode.insertBefore(b, abs);
  }

  function dressAccount() {
    ensureClaim();
    const auth = document.getElementById("btn-auth");
    const claim = document.getElementById("btn-claim");
    const old = document.getElementById("oc-self");
    if (old) old.remove();
    if (!auth) return;
    if (loggedIn()) {
      auth.textContent = "Акаунт";
      auth.onclick = (e) => { e.preventDefault(); e.stopPropagation(); toggleAccount(); };
      if (claim) claim.style.display = "";
    } else {
      auth.textContent = "Увійти";
      auth.onclick = () => { if (origToggle) origToggle(); };
      if (claim) claim.style.display = "none";
      const menu = document.getElementById("oc-account");
      if (menu) menu.remove();
    }
  }

  function toggleAccount() {
    let menu = document.getElementById("oc-account");
    if (menu) { menu.remove(); return; }
    const auth = document.getElementById("btn-auth");
    menu = document.createElement("div");
    menu.id = "oc-account";
    menu.style.cssText = "position:absolute;right:0;top:120%;min-width:260px;background:#0f172a;color:#e5e7eb;border:1px solid #334155;border-radius:12px;padding:12px;z-index:200;box-shadow:0 8px 24px rgba(0,0,0,.35)";
    menu.innerHTML = '<div id="oc-acc-body">Завантаження…</div><div style="display:flex;gap:6px;margin-top:10px"><button type="button" id="oc-acc-pw" class="btn btn-s">Змінити пароль</button><button type="button" id="oc-acc-out" class="btn btn-d">Вийти</button></div>';
    const host = auth.parentElement;
    if (host && getComputedStyle(host).position === "static") host.style.position = "relative";
    (host || document.body).appendChild(menu);
    document.getElementById("oc-acc-pw").onclick = () => { menu.remove(); openPassword(); };
    document.getElementById("oc-acc-out").onclick = () => { menu.remove(); if (origToggle) origToggle(); };
    loadProfile();
  }

  async function loadProfile() {
    const box = document.getElementById("oc-acc-body");
    if (!box) return;
    try {
      const r = await _fetch("/api/me/profile", { credentials: "include", headers: authHeaders() });
      const d = await r.json();
      if (!r.ok) { box.textContent = d.error || "немає профілю"; return; }
      const img = d.image ? '<img src="' + d.image + '" alt="" style="width:48px;height:48px;border-radius:50%;object-fit:cover">' : '<div style="width:48px;height:48px;border-radius:50%;background:#334155"></div>';
      box.innerHTML = '<div style="display:flex;gap:10px;align-items:center">' + img + '<div><b>' + esc(d.real_name || d.name) + '</b><div style="font-size:12px;opacity:.8">' + esc(d.title || d.team_role || d.role || "") + '</div></div></div><div style="font-size:12px;margin-top:8px;line-height:1.45">' + row("Email", d.email) + row("Slack", d.slack_id) + row("Телефон", d.phone) + row("Логін", d.username) + '</div>';
    } catch (e) { box.textContent = e.message; }
  }
  function row(k, v) { return v ? '<div>' + k + ': ' + esc(v) + '</div>' : ''; }
  function esc(s) { return String(s || "").replace(/[&<>"]/g, (c) => ({ "&": "&", "<": "<", ">": ">", '"': """ }[c])); }

  function openPassword() {
    let el = document.getElementById("oc-self-modal");
    if (!el) {
      el = document.createElement("div");
      el.id = "oc-self-modal";
      el.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:220;display:flex;align-items:center;justify-content:center";
      document.body.appendChild(el);
    }
    el.innerHTML = '<div style="background:#0f172a;color:#e5e7eb;padding:16px;border-radius:12px;width:92%;max-width:420px"><h3 style="margin:0 0 8px">Змінити пароль</h3><label>Поточний<br><input id="oc-p1" type="password" style="width:100%;margin:4px 0 8px"></label><label>Новий (від 8)<br><input id="oc-p2" type="password" style="width:100%;margin:4px 0 8px"></label><p id="oc-pst"></p><button id="oc-psave" type="button" class="btn">Зберегти</button> <button type="button" id="oc-pcancel" class="btn btn-s">Скасувати</button></div>';
    document.getElementById("oc-pcancel").onclick = () => el.remove();
    document.getElementById("oc-psave").onclick = async () => {
      const st = document.getElementById("oc-pst");
      try {
        const r = await _fetch("/api/me/password", { method: "POST", credentials: "include", headers: authHeaders(), body: JSON.stringify({ current: document.getElementById("oc-p1").value, next: document.getElementById("oc-p2").value }) });
        const d = await r.json().catch(() => ({}));
        if (!r.ok) { st.textContent = d.error || r.status; return; }
        st.textContent = "Збережено";
        setTimeout(() => el.remove(), 600);
      } catch (e) { st.textContent = e.message; }
    };
  }

  async function openPool() {
    let el = document.getElementById("oc-self-modal");
    if (!el) {
      el = document.createElement("div");
      el.id = "oc-self-modal";
      el.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:220;display:flex;align-items:center;justify-content:center";
      document.body.appendChild(el);
    }
    el.innerHTML = '<div style="background:#0f172a;color:#e5e7eb;padding:16px;border-radius:12px;width:92%;max-width:560px;max-height:80vh;overflow:auto"><h3 style="margin:0 0 8px">Нерозподілена / Техборг</h3><div id="oc-plist">Завантаження…</div><button type="button" id="oc-pclose" class="btn btn-s" style="margin-top:8px">Закрити</button></div>';
    document.getElementById("oc-pclose").onclick = () => el.remove();
    const box = document.getElementById("oc-plist");
    try {
      const r = await _fetch("/api/tasks/pool", { credentials: "include", headers: authHeaders() });
      const d = await r.json();
      if (!r.ok) { box.textContent = d.error || r.status; return; }
      const items = d.items || [];
      if (!items.length) { box.textContent = "Немає вільних задач"; return; }
      box.innerHTML = items.map((t) => '<div style="border-top:1px solid #334;padding:8px 0"><b>#' + t.id + '</b> ' + esc(t.priority) + ' · ' + esc(t.status) + '<div>' + esc(t.task_description) + '</div><button type="button" data-claim="' + t.id + '">Взяти</button></div>').join("");
      box.querySelectorAll("[data-claim]").forEach((b) => b.onclick = async () => {
        const id = Number(b.getAttribute("data-claim"));
        const rr = await _fetch("/api/tasks/claim", { method: "POST", credentials: "include", headers: authHeaders(), body: JSON.stringify({ id }) });
        const dd = await rr.json().catch(() => ({}));
        b.textContent = rr.ok ? "Взято" : (dd.error || rr.status);
        b.disabled = rr.ok;
      });
    } catch (e) { box.textContent = e.message; }
  }

  if (typeof window.sess === "function") {
    const orig = window.sess;
    window.sess = function () { const r = orig.apply(this, arguments); dressAccount(); return r; };
  }
  document.addEventListener("click", (e) => {
    const menu = document.getElementById("oc-account");
    if (!menu) return;
    if (e.target && (menu.contains(e.target) || e.target.id === "btn-auth")) return;
    menu.remove();
  });
  function boot() { dressAccount(); fixToolLinks(); }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
  else boot();
  setInterval(dressAccount, 1500);
})();
