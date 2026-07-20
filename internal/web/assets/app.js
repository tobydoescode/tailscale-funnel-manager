(() => {
  const TOKEN_KEY = "funnel-manager.token";
  const POLL_MS = 10_000;

  const tbody = document.querySelector("#services tbody");
  const emptyEl = document.getElementById("empty");
  const statusEl = document.getElementById("status");
  const refreshBtn = document.getElementById("refresh");
  const signoutBtn = document.getElementById("signout");

  let toggling = false;

  function getToken() {
    let t = sessionStorage.getItem(TOKEN_KEY);
    if (!t) {
      t = window.prompt("Enter funnel-manager bearer token:");
      if (t) sessionStorage.setItem(TOKEN_KEY, t);
    }
    return t;
  }

  function clearToken() {
    sessionStorage.removeItem(TOKEN_KEY);
  }

  async function apiFetch(path, init = {}) {
    const token = getToken();
    if (!token) throw new Error("no token");
    const headers = new Headers(init.headers || {});
    headers.set("Authorization", "Bearer " + token);
    if (init.body) headers.set("Content-Type", "application/json");
    const res = await fetch(path, { ...init, headers });
    if (res.status === 401) {
      clearToken();
      throw new Error("unauthorized (token cleared, refresh to re-enter)");
    }
    if (!res.ok) {
      let detail = "";
      try {
        const b = await res.json();
        detail = b.error || JSON.stringify(b);
      } catch {
        detail = await res.text();
      }
      throw new Error(`${res.status} ${detail}`);
    }
    return res.json();
  }

  function setStatus(msg, isErr = false) {
    statusEl.textContent = msg;
    statusEl.classList.toggle("err", !!isErr);
  }

  function renderRow(svc) {
    const tr = document.createElement("tr");
    tr.dataset.ns = svc.namespace;
    tr.dataset.name = svc.name;

    const fqName = document.createElement("td");
    const fqCode = document.createElement("code");
    fqCode.textContent = `${svc.namespace}/${svc.name}`;
    fqName.appendChild(fqCode);
    tr.appendChild(fqName);

    const host = document.createElement("td");
    host.textContent = svc.hostname || "—";
    tr.appendChild(host);

    const pp = document.createElement("td");
    pp.textContent = svc.pathPrefix || (svc.paths && svc.paths.length ? svc.paths.join(", ") : "/");
    tr.appendChild(pp);

    const tags = document.createElement("td");
    const tagsCode = document.createElement("code");
    tagsCode.textContent = svc.tags || "";
    tags.appendChild(tagsCode);
    tr.appendChild(tags);

    const tg = document.createElement("td");
    const label = document.createElement("label");
    label.className = "toggle";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = !!svc.funnelEnabled;
    input.disabled = !!svc.error;
    input.setAttribute("aria-label", `funnel for ${svc.namespace}/${svc.name}`);
    const slider = document.createElement("span");
    slider.className = "slider";
    label.append(input, slider);
    tg.appendChild(label);
    tr.appendChild(tg);

    const url = document.createElement("td");
    url.className = "url";
    if (svc.funnelEnabled && svc.funnelURL) {
      const link = document.createElement("a");
      link.href = svc.funnelURL;
      link.target = "_blank";
      link.rel = "noopener";
      link.textContent = svc.funnelURL;
      url.appendChild(link);
    } else if (svc.funnelEnabled) {
      url.textContent = "on (no tailnet configured)";
    } else {
      url.textContent = "—";
    }
    tr.appendChild(url);

    input.addEventListener("change", async () => {
      const desired = input.checked;
      input.disabled = true;
      toggling = true;
      try {
        setStatus(`${desired ? "enabling" : "disabling"} ${svc.namespace}/${svc.name}…`);
        await apiFetch(`/api/services/${encodeURIComponent(svc.namespace)}/${encodeURIComponent(svc.name)}/funnel`, {
          method: "POST",
          body: JSON.stringify({ enabled: desired }),
        });
        toggling = false;
        await refresh();
        setStatus(`${svc.namespace}/${svc.name} funnel ${desired ? "on" : "off"}`);
      } catch (e) {
        input.checked = !desired;
        setStatus(e.message, true);
      } finally {
        toggling = false;
        input.disabled = !!svc.error;
      }
    });

    if (svc.error) {
      const err = document.createElement("tr");
      err.className = "row-error";
      const td = document.createElement("td");
      td.colSpan = 6;
      td.textContent = `⚠ ${svc.error}`;
      err.appendChild(td);
      return [tr, err];
    }
    return [tr];
  }

  async function refresh() {
    try {
      const data = await apiFetch("/api/services");
      tbody.replaceChildren();
      if (!data.length) {
        emptyEl.hidden = false;
      } else {
        emptyEl.hidden = true;
        for (const svc of data) {
          for (const node of renderRow(svc)) tbody.appendChild(node);
        }
      }
      setStatus(`updated ${new Date().toLocaleTimeString()}`);
    } catch (e) {
      setStatus(e.message, true);
    }
  }

  refreshBtn.addEventListener("click", refresh);
  signoutBtn.addEventListener("click", () => {
    clearToken();
    location.reload();
  });

  refresh();
  // Skip the poll while a toggle is in flight — a full re-render would
  // replace the row mid-request and clobber its pending state.
  setInterval(() => {
    if (!toggling) refresh();
  }, POLL_MS);
})();
