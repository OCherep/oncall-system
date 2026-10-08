// status-fix.js — own password, claim pool, absolute hub links, Jira import without stale filter
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
    document.querySelectorAll("#hub-tools-menu a, a[href^='/radar'], a[href^='/certs'], a[href^='/mentions'], a[href^='/ether'], a[href^='/aws'], a[href^='/netmap'], a[href^='/oncall']").forEach((a) => {
      const h = a.getAttribute("href") || "";
      if (h.startsWith("/") && location.host.indexOf(":85") >= 0) a.setAttribute("href", absHub(h));
    });
  }
  setInterval(fixToolLinks, 800);
  document.addEventListener("click", fixToolLinks, true);

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

  function token() {
    try { return localStorage.getItem("oncall_session") || ""; } catch (e) { return ""; }
  }
  function authHeaders() {
    const h = { "Content-Type": "application/json", Accept: "application/json" };
    const t = token();
    if (t) h.Authorization = "Bearer " + t;
    return h;
  }

  function ensurePanel() {
    if (document.getElementById("oc-self")) return;
    const box = document.createElement("div");
    box.id = "oc-self";
    box.style.cssText = "position:fixed;right:16px;bottom:16px;z-index:80;display:flex;gap:8px";
    box.innerHTML = '<button type="button" id="oc-pw" style="padding:8px 12px;border-radius:8px;border:1px solid #334;background:#111827;color:#e5e7eb;cursor:pointer">Пароль</button><button type="button" id="oc-pool" style="padding:8px 12px;border-radius:8px;border:1px solid #334;background:#111827;color:#e5e7eb;cursor:pointer">Взяти задачу</button>';
    document.body.appendChild(box);
    document.getElementById("oc-pw").onclick = openPassword;
    document.getElementById("oc-pool").onclick = openPool;
  }

  function modal(html) {
    let el = document.getElementById("oc-self-modal");
    if (!el) {
      el = document.createElement("div");
      el.id = "oc-self-modal";
      el.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:90;display:flex;align-items:center;justify-content:center";
      document.body.appendChild(el);
    }
    el.innerHTML = '<div style="background:#0f172a;color:#e5e7eb;padding:16px;border-radius:12px;max-width:560px;width:92%;max-height:80vh;overflow:auto">' + html + "</div>";
    el.onclick = (e) => { if (e.target === el) el.remove(); };
    return el;
  }

  function openPassword() {
    modal('<h3 style="margin:0 0 8px">Змінити пароль</h3><label>Поточний<br><input id="oc-p1" type="password" style="width:100%;margin:4px 0 8px"></label><label>Новий (від 8)<br><input id="oc-p2" type="password" style="width:100%;margin:4px 0 8px"></label><p id="oc-pst"></p><button id="oc-psave" type="button">Зберегти</button>');
    document.getElementById("oc-psave").onclick = async () => {
      const st = document.getElementById("oc-pst");
      try {
        const r = await _fetch("/api/me/password", { method: "POST", credentials: "include", headers: authHeaders(), body: JSON.stringify({ current: document.getElementById("oc-p1").value, next: document.getElementById("oc-p2").value }) });
        const d = await r.json().catch(() => ({}));
        if (!r.ok) { st.textContent = d.error || r.status; return; }
        st.textContent = "Збережено";
      } catch (e) { st.textContent = e.message; }
    };
  }

  async function openPool() {
    modal('<h3 style="margin:0 0 8px">Нерозподілена / Техборг</h3><div id="oc-plist">Завантаження…</div>');
    const box = document.getElementById("oc-plist");
    try {
      const r = await _fetch("/api/tasks/pool", { credentials: "include", headers: authHeaders() });
      const d = await r.json();
      if (!r.ok) { box.textContent = d.error || r.status; return; }
      const items = d.items || [];
      if (!items.length) { box.textContent = "Немає вільних задач"; return; }
      box.innerHTML = items.map((t) => '<div style="border-top:1px solid #334;padding:8px 0"><b>#' + t.id + "</b> " + (t.priority || "") + " · " + (t.status || "") + "<div>" + String(t.task_description || "").replace(/</g, "<") + '</div><button type="button" data-claim="' + t.id + '">Взяти</button></div>').join("");
      box.querySelectorAll("[data-claim]").forEach((b) => b.onclick = async () => {
        const id = Number(b.getAttribute("data-claim"));
        const rr = await _fetch("/api/tasks/claim", { method: "POST", credentials: "include", headers: authHeaders(), body: JSON.stringify({ id }) });
        const dd = await rr.json().catch(() => ({}));
        b.textContent = rr.ok ? "Взято" : (dd.error || rr.status);
        b.disabled = rr.ok;
      });
    } catch (e) { box.textContent = e.message; }
  }

  function scrubJiraBox() {
    document.querySelectorAll("textarea").forEach((t) => {
      if ((t.value || "").indexOf("component = DevOps") >= 0) {
        t.value = "";
        t.placeholder = "Порожнє = борд DevOps (VID/10). JQL лише якщо явно project = VID";
      }
    });
  }

  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", () => { ensurePanel(); scrubJiraBox(); fixToolLinks(); });
  else { ensurePanel(); scrubJiraBox(); fixToolLinks(); }
})();
