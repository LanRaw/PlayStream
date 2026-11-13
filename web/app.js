const $ = (selector) => document.querySelector(selector);
const page = $("#page");
const dialog = $("#dialog");
const dialogForm = $("#dialog-form");

const shapes = {
  overview: '<rect x="3" y="3" width="7" height="7" rx="1.3"/><rect x="14" y="3" width="7" height="7" rx="1.3"/><rect x="3" y="14" width="7" height="7" rx="1.3"/><rect x="14" y="14" width="7" height="7" rx="1.3"/>',
  streams: '<rect x="3" y="5" width="18" height="14" rx="2"/><path d="m10 9 5 3-5 3z"/>',
  diagnostics: '<path d="M3 12h4l3-7 4 14 3-7h4"/>',
  settings: '<path d="M4 7h16M4 17h16"/><circle cx="8" cy="7" r="2.5" fill="var(--surface)"/><circle cx="16" cy="17" r="2.5" fill="var(--surface)"/>',
  copy: '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 4H6a2 2 0 0 0-2 2v10"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
};
const icon = (name) => '<svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.55" stroke-linecap="round" stroke-linejoin="round">' + shapes[name] + "</svg>";
const routes = { overview: "总览", streams: "直播对象", diagnostics: "诊断", settings: "设置" };
const states = { ok: "通过", error: "异常", waiting: "等待中" };
const escapeHTML = (value) => String(value ?? "").replace(/[&<>"']/g, (char) => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
}[char]));

let state;
let previousRoute;
let toastTimer;
let latestEvent;
let settingsDirty = false;
let settingsRevision;
let discovering = false;
const lanHost = /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|169\.254\.)/.test(location.hostname);

function currentRoute() {
  const key = location.hash.slice(1);
  return Object.hasOwn(routes, key) ? key : "overview";
}

function pill(status, label = states[status]) {
  return `<span class="pill ${status}"><span class="dot ${status}"></span>${escapeHTML(label)}</span>`;
}

function action(name, text, value = "", css = "", graphic = "") {
  return `<button type="button" class="${css}" data-action="${name}" data-value="${escapeHTML(value)}">${graphic ? icon(graphic) : ""}${text}</button>`;
}

function copyButton(value, text = "复制") {
  return action("copy", text, value, "", "copy");
}

function header(title, description, end = "") {
  return `<header class="head"><div><h1>${title}</h1><p>${description}</p></div>${end}</header>`;
}

function outputURL(s) {
  return state.address ? `rtmp://${state.address}:${state.rtmpPort}/${s.id}` : "";
}

function streamCard(s, edit = false) {
  const active = state.config.active === s.id;
  const bound = state.bound === s.id;
  const url = outputURL(s);
  let status = "停用";
  if (s.enabled) status = active ? "就绪" : "待切换";
  if (bound) status = "接收中";
  const label = pill(bound ? "ok" : "waiting", status);
  return `<article class="stream-card">
    <div class="row"><div><h3>${escapeHTML(s.name)}</h3><p class="stream-meta">${escapeHTML(s.id)} · RTMP</p></div>
    <div class="actions">${label}${edit ? action("edit", "编辑", s.id) : ""}</div></div>
    <div class="row url-row"><code>${escapeHTML(url) || "设置接收器地址后显示拉流地址"}</code>${url ? copyButton(url) : ""}</div>
    ${edit && s.enabled && !active ? `<div class="row">${action("activate", "切换", s.id)}</div>` : ""}
    </article>`;
}

function checkRow(c) {
  return `<span><strong>${escapeHTML(c.title)}</strong><p class="check-detail">${escapeHTML(c.detail)}</p></span>${pill(c.state)}`;
}

function overview() {
  const service = state.checks[0].state;
  const target = state.config.streams?.find((s) => s.id === (state.bound || state.config.active));
  const address = state.address;
  const refresh = action("refresh-address", "刷新");
  const addressHint = address ? "在 PS5 的网络高级设置中，将首选 DNS 填为此地址。" : state.bridge ? "请用运行 Docker 的电脑的局域网 IP 打开本页，接收器会自动识别该地址。" : "请确认电脑已连接到和 PS5 相同的局域网，然后刷新。";
  return header("总览", "在 PS5 上开播，在 OBS 中接收画面和声音。",
    pill(service, { ok: "服务正常", error: "服务异常", waiting: "正在启动" }[service])) +
    `<section class="receiver" aria-labelledby="receiver-title">
      <h2 id="receiver-title">PS5 首选 DNS</h2>
      <div class="split"><div class="address">${escapeHTML(address) || "待设置"}</div><div class="actions">${address ? copyButton(address) : ""}${refresh}</div></div>
      <p class="muted">${addressHint}</p>
    </section>
    <section class="section"><div class="split"><h2>直播对象</h2>${action("new", "新建", "", "", "plus")}</div>
      ${target ? streamCard(target) : '<p class="empty">新建直播对象后，即可复制固定的 OBS 拉流地址。</p>'}
      ${state.bound && state.bound !== state.config.active ? '<p class="empty">当前直播继续接收，重新开播后使用新选择。</p>' : ""}
    </section>
    <section class="section"><div class="split"><h2>连接诊断</h2><a class="link" href="#diagnostics">查看详情</a></div>
      <div class="check-list">${state.checks.map((c) => `<div class="check-row"><span class="dot ${c.state}"></span>${checkRow(c)}</div>`).join("")}</div>
    </section>`;
}

function streams() {
  return header("直播对象", "为每个直播对象保留一个固定的 OBS 拉流路径。", action("new", "新建", "", "primary", "plus")) +
    `<section aria-label="直播对象列表">${state.config.streams?.length ? state.config.streams.map((s) => streamCard(s, true)).join("") : '<p class="empty">还没有直播对象。</p>'}</section>`;
}

function diagnostics() {
  const showTest = state.testing ? '<button type="button" disabled>检测中…</button>' : action("diagnose", "开始检测", "", "primary");
  return header("诊断", state.testing ? "请在 45 秒检测窗口内，从 PS5 开启 Twitch 直播。" : "展开检查项，查看发生了什么和下一步操作。", showTest) +
    `<section class="section"><h2>连接检查</h2>
      <div class="check-list">${state.checks.map((c, i) => `<details data-stage="${i + 1}"><summary class="check-row">${checkRow(c)}</summary><div class="detail"><p>${escapeHTML(c.hint)}</p>
        ${eventList((state.events || []).filter((e) => e.stage === i + 1).slice(-3))}</div></details>`).join("")}</div>
    </section>
    <section class="section"><h2>最近事件</h2>${eventList((state.events || []).slice(-20)) || '<p class="empty">暂无事件。</p>'}</section>`;
}

function eventList(events) {
  return [...events].reverse().map((event) => `<div class="event"><time datetime="${escapeHTML(event.time)}">${new Date(event.time).toLocaleTimeString("zh-CN", { hour12: false })}</time><span>${escapeHTML(event.text)}</span></div>`).join("");
}

function settings() {
  const dns = state.config.dns;
  const webPort = location.port || (location.protocol === "https:" ? "443" : "80");
  return header("设置", "查看接收器的端口与配置。") +
    `<section class="section"><h2>网络</h2>
      <p class="check-detail">${dns.upstream ? "上游 DNS 已手动覆盖" : "上游 DNS 自动沿用系统或 Docker 的解析器"}</p>
      <details class="advanced"><summary>高级网络设置</summary>
        <form id="settings-form">
          <div class="field"><label for="upstream">上游 DNS 覆盖</label>
            <input id="upstream" name="upstream" autocomplete="off" value="${escapeHTML(dns.upstream)}" placeholder="留空即自动">
            <span class="hint">仅在路由器或加速器要求指定 DNS 时填写。</span>
          </div>
          <button type="submit" class="primary">保存</button>
        </form>
      </details>
    </section>
    <section class="section"><h2>服务端口</h2>
      <div class="split port-row"><span>DNS · UDP / TCP</span><code>${escapeHTML(state.dnsPort)}</code></div>
      <div class="split port-row"><span>RTMP · TCP</span><code>${escapeHTML(state.rtmpPort)}</code></div>
      <div class="split port-row"><span>网页管理 · TCP</span><code>${escapeHTML(webPort)}</code></div>
      <p class="check-detail">管理页默认使用系统分配的空闲端口。PS5 使用 DNS 53 和 RTMP 1935。</p>
    </section>
    <section class="section"><div class="split"><h2>配置文件</h2><div class="actions">
      <a class="button" href="/api/config" download="config.yaml">导出</a>
      ${action("import", "导入")}
      <input type="file" id="import-file" accept=".yaml,.yml,application/yaml" hidden>
    </div></div></section>
    <section class="section"><h2>恢复 PS5 网络</h2><p class="check-detail">停用接收器时，在 PS5 的网络高级设置中，将 DNS 设置恢复为自动。</p></section>`;
}

function render(force = false) {
  const route = currentRoute();
  if (route !== previousRoute) settingsDirty = false;
  if (!state || (route === "settings" && settingsDirty && !force)) return;
  const focus = document.activeElement;
  const focusAction = page.contains(focus) && focus?.dataset.action ? { ...focus.dataset } : null;
  const advancedOpen = page.querySelector(".advanced")?.open;
  const openStages = [...page.querySelectorAll("details[open]")].map((item) => item.dataset.stage);
  if (route !== previousRoute || !$("#navigation").children.length) {
    $("#navigation").innerHTML = Object.entries(routes).map(([key, title]) =>
      `<a href="#${key}"${route === key ? ' aria-current="page"' : ""}>${icon(key)}${title}</a>`).join("");
  }
  page.innerHTML = `<div class="wrap">${{ overview, streams, diagnostics, settings }[route]()}</div>`;
  page.setAttribute("aria-busy", "false");
  if (advancedOpen) page.querySelector(".advanced")?.setAttribute("open", "");
  for (const stage of openStages) page.querySelector(`details[data-stage="${stage}"]`)?.setAttribute("open", "");
  if (route === previousRoute && focusAction) {
    const next = [...page.querySelectorAll("[data-action]")].find((el) => el.dataset.action === focusAction.action && el.dataset.value === focusAction.value);
    next?.focus({ preventScroll: true });
  }
  if (route === "settings") settingsRevision = state.revision;
  previousRoute = route;
}

function connection(online) {
  $("#connection-dot").className = "dot " + (online ? "ok" : "waiting");
  $("#connection-label").textContent = online ? "已连接接收器" : "正在重新连接";
}

function receive(next, force = false) {
  if (force && state && state.revision > next.revision) {
    render(true);
    return;
  }
  const changed = !state || JSON.stringify(next) !== JSON.stringify(state);
  state = next;
  if (changed || force) render(force);
  if (next.bridge && lanHost && next.address !== location.hostname && !discovering) {
    discovering = true;
    refreshAddress().catch((error) => toast(error.message));
  }
}

function toast(message) {
  const banner = $("#toast");
  banner.textContent = message;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { banner.textContent = ""; }, 3800);
}

async function request(path, method, body, headers = {}) {
  const response = await fetch(path, { method, body, headers });
  if (!response.ok) {
    const detail = await response.json().catch(() => ({}));
    throw new Error(detail.error || `操作失败（${response.status}）`);
  }
  return response.status === 204 ? null : response.json();
}

function cloneConfig() {
  const cfg = structuredClone(state.config);
  cfg.streams ??= [];
  return cfg;
}

async function save(cfg, revision = state.revision) {
  const next = await request("/api/config", "PUT", JSON.stringify({ config: cfg, revision }), { "Content-Type": "application/json" });
  settingsDirty = false;
  receive(next, true);
}

function openDialog(title, fields, submit, danger = false) {
  dialogForm.innerHTML = `<h2 id="dialog-title">${title}</h2>${fields}
    <div class="actions"><button type="button" data-action="cancel">取消</button><button type="submit" class="${danger ? "danger" : "primary"}">${danger ? "删除" : "保存"}</button></div>`;
  dialogForm.onsubmit = async (event) => {
    event.preventDefault();
    event.submitter.disabled = true;
    try {
      await submit(new FormData(dialogForm));
      dialog.close();
    } catch (error) {
      toast(error.message);
    } finally {
      event.submitter.disabled = false;
    }
  };
  if (!dialog.open) dialog.showModal();
}

function editStream(id) {
  const found = state.config.streams?.find((s) => s.id === id);
  const revision = state.revision;
  openDialog(found ? "编辑直播对象" : "新建直播对象", `
    <div class="field"><label for="stream-name">显示名称</label><input id="stream-name" name="name" required maxlength="80" value="${escapeHTML(found?.name)}" placeholder="例如 GT7 PS5"></div>
    <div class="field"><label for="stream-id">固定别名</label><input id="stream-id" name="id" pattern="[A-Za-z0-9_\\-]{1,48}" required maxlength="48" value="${escapeHTML(id)}" placeholder="gt7_ps5" ${found ? "readonly" : ""}><span class="hint">使用英文字母、数字、下划线或短横线。</span></div>
    <div class="field"><label><input name="enabled" type="checkbox" ${!found || found.enabled ? "checked" : ""}> 启用</label></div>
    ${found ? action("delete", "删除对象", id, "danger") : ""}`, async (form) => {
      const cfg = cloneConfig();
      const s = { id: found ? found.id : String(form.get("id")), name: String(form.get("name")).trim(), enabled: form.has("enabled") };
      const index = cfg.streams.findIndex((item) => item.id === s.id);
      if (!found && index >= 0) throw new Error("该别名已存在");
      if (found && index < 0) throw new Error("该对象已删除");
      if (index >= 0) cfg.streams[index] = s;
      else cfg.streams.push(s);
      if (!s.enabled && cfg.active === s.id) cfg.active = "";
      if (!cfg.active && s.enabled) cfg.active = s.id;
      await save(cfg, revision);
      toast(found ? "直播对象已更新" : "直播对象已创建");
    });
}

async function copy(value) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {}
  }
  const input = document.createElement("input");
  input.value = value;
  input.readOnly = true;
  input.className = "clipboard-input";
  document.body.append(input);
  input.select();
  const success = document.execCommand("copy");
  input.remove();
  if (!success) throw new Error("自动复制失败，请选中地址手动复制");
}

async function refreshAddress(showToast = false) {
  const next = await request("/api/address/refresh", "POST", "{}", { "Content-Type": "application/json" });
  receive(next);
  if (showToast) toast(next.address ? "接收器地址已刷新" : "请通过电脑的局域网 IP 打开此页");
}

const handlers = {
  copy: async (value) => { await copy(value); toast("已复制"); },
  new: () => editStream(""),
  edit: (id) => editStream(id),
  cancel: () => dialog.close(),
  delete: (id) => openDialog("删除直播对象", `<p>删除后，OBS 中的 <code>${escapeHTML(id)}</code> 地址将失效。</p>`, async () => {
    const cfg = cloneConfig();
    cfg.streams = cfg.streams.filter((s) => s.id !== id);
    if (cfg.active === id) cfg.active = "";
    await save(cfg);
    toast("直播对象已删除");
  }, true),
  activate: async (id) => {
    const cfg = cloneConfig();
    cfg.active = id;
    await save(cfg);
    toast("已切换直播对象");
  },
  diagnose: async () => {
    await request("/api/diagnose", "POST", "{}", { "Content-Type": "application/json" });
    toast("检测已开始，请在 PS5 开启 Twitch 直播");
  },
  "refresh-address": () => refreshAddress(true),
  import: () => $("#import-file").click(),
};

document.addEventListener("click", async (event) => {
  const button = event.target.closest("button[data-action]");
  if (!button) return;
  try {
    await handlers[button.dataset.action](button.dataset.value);
  } catch (error) {
    toast(error.message);
  }
});

page.addEventListener("input", (event) => {
  if (event.target.closest("#settings-form")) settingsDirty = true;
});

page.addEventListener("submit", async (event) => {
  if (event.target.id !== "settings-form") return;
  event.preventDefault();
  event.submitter.disabled = true;
  try {
    const form = new FormData(event.target);
    const cfg = cloneConfig();
    cfg.dns.upstream = String(form.get("upstream")).trim();
    await save(cfg, settingsRevision);
    toast("网络设置已保存");
  } catch (error) {
    toast(error.message);
  } finally {
    event.submitter.disabled = false;
  }
});

page.addEventListener("change", async (event) => {
  if (event.target.id !== "import-file" || !event.target.files.length) return;
  const [file] = event.target.files;
  event.target.value = "";
  try {
    const data = await file.text();
    const revision = state.revision;
    openDialog("导入配置", `<p>使用 <strong>${escapeHTML(file.name)}</strong> 替换当前网络设置与直播对象。</p>`, async () => {
      const next = await request("/api/config/import", "POST", data, { "Content-Type": "application/yaml", "If-Match": String(revision) });
      settingsDirty = false;
      receive(next, true);
      toast("配置已导入");
    });
  } catch (error) {
    toast(error.message);
  }
});

window.addEventListener("hashchange", () => {
  render();
  window.scrollTo(0, 0);
});

const feed = new EventSource("/api/events");
feed.onopen = () => connection(true);
feed.onerror = () => connection(false);
feed.onmessage = (event) => {
  latestEvent = event.data;
  receive(JSON.parse(event.data));
};
request("/api/state", "GET").then((data) => { if (!latestEvent) receive(data); }).catch((error) => toast(error.message));
