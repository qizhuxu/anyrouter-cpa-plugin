package main

// panelJS drives the ledger. Vanilla JS on purpose: the page is a single
// resource artifact inside the plugin (see panel.go) and the project already
// uses plain DOM code, so no framework, bundler or CDN is introduced.
//
// Motion: exactly three purposeful animations - the pressed control reports its
// result (spinner, then the balance figure rolls to its new value), state
// changes have a visible origin (the drawer slides in from the side it lives on,
// rows flash where the change happened), and the first paint rises in reading
// order. Everything else is a 150ms native transition. prefers-reduced-motion
// turns all of it off in CSS and skips the number roll here.
const panelJS = `
(function(){
"use strict";

var KEY_STORE = "anyrouter.mgmt.key";
var reduced = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;

var state = {
  key: "",
  accounts: [],
  status: null,
  config: {},
  events: [],
  busy: {},
  flash: {},
  dollars: {},
  editing: "",
  deleteArmed: false,
  banner: null,
  loading: false,
  loaded: false,
  firstPaint: true,
  loadedAt: ""
};

function el(id){ return document.getElementById(id); }
function node(tag, cls, text){
  var n = document.createElement(tag);
  if (cls) { n.className = cls; }
  if (text !== undefined && text !== null) { n.textContent = text; }
  return n;
}
function clear(n){ while (n.firstChild) { n.removeChild(n.firstChild); } }

/* ---------- formatting ---------- */

function money(value){
  if (typeof value !== "number" || !isFinite(value)) { return "—"; }
  var fixed = Math.abs(value).toFixed(2);
  var parts = fixed.split(".");
  parts[0] = parts[0].replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return (value < 0 ? "-$" : "$") + parts.join(".");
}
function hm(d){
  return ("0" + d.getHours()).slice(-2) + ":" + ("0" + d.getMinutes()).slice(-2);
}
function parseTime(iso){
  if (!iso) { return null; }
  var d = new Date(iso);
  return isNaN(d.getTime()) ? null : d;
}
function dayStart(d){ return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime(); }
function relTime(iso){
  var d = parseTime(iso);
  if (!d) { return ""; }
  var days = Math.round((dayStart(new Date()) - dayStart(d)) / 86400000);
  if (days <= 0) { return "今天 " + hm(d); }
  if (days === 1) { return "昨天 " + hm(d); }
  if (days < 7) { return days + " 天前 " + hm(d); }
  return (d.getMonth() + 1) + "月" + d.getDate() + "日 " + hm(d);
}
function agoText(iso){
  var d = parseTime(iso);
  if (!d) { return ""; }
  var sec = Math.max(0, Math.round((Date.now() - d.getTime()) / 1000));
  if (sec < 60) { return "刚刚"; }
  if (sec < 3600) { return Math.floor(sec / 60) + " 分钟前"; }
  if (sec < 86400) { return Math.floor(sec / 3600) + " 小时前"; }
  return Math.floor(sec / 86400) + " 天前";
}
function statusMeta(a){
  if (a.disabled) { return { dot: "off", label: "已停用", tone: "dim" }; }
  switch (a.last_checkin_status) {
    case "success": return { dot: "ok", label: "签到成功" };
    case "already": return { dot: "ring", label: "今日已签到" };
    case "failed": return { dot: "err", label: "签到失败" };
    case "unknown": return { dot: "warn", label: "结果未知" };
    default: return { dot: "", label: "未签到" };
  }
}
function quotaFraction(b){
  if (!b || !b.total) { return 0; }
  var f = b.quota / b.total;
  return f < 0 ? 0 : (f > 1 ? 1 : f);
}
function quotaTone(f){
  if (f < 0.2) { return "err"; }
  if (f < 0.5) { return "warn"; }
  return "";
}

/* ---------- api ---------- */

function api(path, method, body){
  var k = state.key;
  if (!k) { return Promise.reject(new Error("请先填写 CPA 管理密钥")); }
  var opts = { method: method || "GET", headers: { "Authorization": "Bearer " + k, "X-Management-Key": k } };
  if (body !== undefined && body !== null) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  return fetch("/v0/management/anyrouter" + path, opts).then(function(resp){
    return resp.text().then(function(text){
      var data = null;
      try { data = text ? JSON.parse(text) : null; } catch (e) { data = { raw: text }; }
      if (!resp.ok) {
        if (resp.status === 401) { throw new Error("管理密钥无效或已失效，请重新填写"); }
        throw new Error((data && (data.message || data.error)) || ("HTTP " + resp.status));
      }
      return data;
    });
  });
}

function hostDelete(fileName){
  var k = state.key;
  return fetch("/v0/management/auth-files?name=" + encodeURIComponent(fileName), {
    method: "DELETE",
    headers: { "Authorization": "Bearer " + k, "X-Management-Key": k }
  }).then(function(resp){
    if (!resp.ok) { throw new Error("删除失败：HTTP " + resp.status); }
    return true;
  });
}

/* ---------- loading ---------- */

function load(options){
  var opts = options || {};
  state.loading = true;
  renderLedger();
  var jobs = [api("/accounts"), api("/status")];
  return Promise.all(jobs).then(function(out){
    state.accounts = (out[0] && out[0].accounts) || [];
    state.config = (out[0] && out[0].config) || {};
    state.events = (out[0] && out[0].events) || [];
    state.status = out[1] || null;
    state.banner = null;
    state.loading = false;
    state.loaded = true;
    var now = new Date();
    state.loadedAt = hm(now);
    renderAll();
    if (state.firstPaint) {
      state.firstPaint = false;
      window.setTimeout(function(){ el("app").removeAttribute("data-enter"); }, 900);
    }
    if (opts.refreshBalances !== false) { maybeRefreshBalances(); }
  }).catch(function(err){
    state.loading = false;
    state.banner = { kind: "err", text: err.message, retry: true };
    renderAll();
  });
}

// The ledger keeps the last persisted reading, so a figure is visible before any
// request. When it is missing or older than the configured interval, it is
// refreshed once per page load, quietly.
function balanceIsStale(a){
  if (!a.balance || !a.balance.fetched_at) { return true; }
  var minutes = (state.config && state.config.balance_refresh_minutes) || 30;
  var at = parseTime(a.balance.fetched_at);
  return !at || (Date.now() - at.getTime()) > minutes * 60000;
}

function maybeRefreshBalances(){
  if (state.refreshingBalances || !state.loaded) { return; }
  var stale = state.accounts.filter(function(a){ return !a.disabled && balanceIsStale(a); });
  if (!stale.length) { return; }
  state.refreshingBalances = true;
  stale.forEach(function(a){ state.busy[a.name] = "balance"; });
  renderLedger();
  api("/balance", "POST", {}).then(function(){
    return load({ refreshBalances: false });
  }).catch(function(err){
    state.banner = { kind: "err", text: "自动刷新余额失败：" + err.message, retry: false, dismiss: true };
  }).then(function(){
    state.refreshingBalances = false;
    state.busy = {};
    renderAll();
  });
}

/* ---------- render ---------- */

function renderAll(){
  renderConnection();
  renderBanner();
  renderStrip();
  renderLedger();
  renderSchedule();
  renderEvents();
  renderRaw();
  applyStateParam();
}

function renderConnection(){
  var app = el("app");
  var failed = !!(state.banner && state.banner.kind === "err");
  var connected = state.loaded && !failed;
  el("connDot").className = "dot " + (connected ? "ok" : (state.key ? "warn" : ""));
  el("connText").textContent = connected ? "已连接" : (state.key ? "连接失败" : "未连接");
  el("keyToggle").textContent = state.key ? "更换密钥" : "填写管理密钥";
  app.setAttribute("data-need-key", state.key ? "0" : "1");
  el("site").textContent = state.config.base_url || "";
}

function renderBanner(){
  var box = el("banner");
  clear(box);
  if (!state.banner) { return; }
  if (!state.loaded && state.banner.kind === "err") { return; }
  var b = node("div", "banner" + (state.banner.kind === "info" ? " info" : ""));
  b.setAttribute("role", "status");
  b.appendChild(node("span", null, state.banner.text));
  b.appendChild(node("span", "spacer"));
  if (state.banner.retry) {
    var retry = node("button", "btn", "重试");
    retry.type = "button";
    retry.addEventListener("click", function(){ load(); });
    b.appendChild(retry);
  }
  if (state.banner.dismiss) {
    var close = node("button", "btn btn-quiet", "关闭");
    close.type = "button";
    close.addEventListener("click", function(){ state.banner = null; renderBanner(); });
    b.appendChild(close);
  }
  box.appendChild(b);
}

function fact(label, value, extra, tone){
  var box = node("div", "fact" + (tone ? " " + tone : ""));
  box.appendChild(node("span", "k", label));
  box.appendChild(node("span", "v num", value));
  if (extra) { box.appendChild(node("span", "sub", extra)); }
  return box;
}

function renderStrip(){
  var strip = el("strip");
  clear(strip);
  var list = state.accounts;
  // Nothing to summarise yet: the empty state already explains what to do.
  strip.style.display = list.length ? "" : "none";
  if (!list.length) { return; }
  var enabled = 0, signed = 0, failed = 0, total = 0, known = 0;
  list.forEach(function(a){
    if (!a.disabled) { enabled++; }
    if (a.last_checkin_status === "success" || a.last_checkin_status === "already") { signed++; }
    if (!a.disabled && (a.last_checkin_status === "failed" || a.last_error)) { failed++; }
    if (a.balance) { total += a.balance.dollars; known++; }
  });
  var cfg = state.config || {};
  var schedState = (state.status && state.status.schedule) ? state.status.schedule : {};
  var sched = { auto_checkin: schedState.auto_checkin, checkin_time: schedState.checkin_time, timezone: schedState.timezone };
  var next = state.status && state.status.schedule && state.status.schedule.next_checkin_at
    ? relTime(state.status.schedule.next_checkin_at)
    : "";

  strip.appendChild(fact("账号", String(list.length), enabled !== list.length ? ("启用 " + enabled) : "", "secondary"));
  strip.appendChild(fact("今日签到", signed + "/" + enabled, "", "primary"));
  if (failed > 0) {
    var bad = node("div", "fact");
    bad.appendChild(node("span", "k", "异常"));
    var badValue = node("span", "v num", String(failed));
    badValue.style.color = "var(--err)";
    bad.appendChild(badValue);
    strip.appendChild(bad);
  }
  strip.appendChild(fact("剩余合计", known ? money(total) : "未读取", "", "secondary"));
  strip.appendChild(fact("下次自动签到", sched.auto_checkin ? (sched.checkin_time + " · " + (sched.timezone || "Local")) : "已关闭", next ? ("约 " + next) : "", "secondary"));
  var challenge = state.status && state.status.challenge ? state.status.challenge : {};
  var hosts = Object.keys(challenge);
  strip.appendChild(fact("挑战 Cookie", hosts.length ? (hosts[0] + " · " + agoText(challenge[hosts[0]].solved_at)) : "按需获取", "", "secondary"));
  strip.appendChild(node("span", "spacer"));
  if (state.status && state.status.egress && !state.status.egress_default) {
    strip.appendChild(fact("出口", state.status.egress, "", "secondary"));
  }
  if (state.loadedAt) { strip.appendChild(fact("数据更新", state.loadedAt, "", "secondary")); }
}

function renderLedger(){
  var box = el("ledger");
  clear(box);
  el("count").textContent = state.accounts.length ? (state.accounts.length + " 个") : "";
  var hasAccounts = state.accounts.length > 0;
  el("checkinAll").disabled = !hasAccounts;
  el("balanceAll").disabled = !hasAccounts;
  rolls = [];

  if (state.loading && !state.loaded) { box.appendChild(skeleton()); return; }
  if (!state.accounts.length) {
    box.appendChild(state.loaded ? emptyState() : failedState());
    return;
  }

  var table = node("table");
  var thead = node("thead");
  var hr = node("tr");
  [["账号","c-name"],["状态","c-state"],["上次签到","c-when"],["剩余额度","c-quota"],["操作","c-ops"]].forEach(function(pair){
    var th = node("th", pair[1], pair[0]);
    if (pair[1] === "c-ops") { th.scope = "col"; }
    hr.appendChild(th);
  });
  thead.appendChild(hr);
  table.appendChild(thead);

  var tbody = node("tbody");
  state.accounts.forEach(function(a, index){
    tbody.appendChild(row(a, index));
  });
  table.appendChild(tbody);
  box.appendChild(table);
  flushRolls();
}

function skeleton(){
  var wrap = node("div", "skel");
  for (var i = 0; i < 3; i++) {
    var r = node("div", "skel-row");
    r.appendChild(node("div", "sk sh w1"));
    r.appendChild(node("div", "sk sh w2"));
    r.appendChild(node("div", "sk sh w3"));
    r.appendChild(node("div", "sk sh w4"));
    wrap.appendChild(r);
  }
  return wrap;
}

// failedState is shown when the first load never succeeded: an empty ledger
// would blame the user for a problem that is not theirs.
function failedState(){
  var wrap = node("div", "empty");
  wrap.appendChild(node("h3", null, "无法读取账号列表"));
  wrap.appendChild(node("p", null, state.banner ? state.banner.text : "请检查管理密钥与 CPA 服务是否可用，然后重试。"));
  var actions = node("div", "actions");
  var change = node("button", "btn btn-primary", "更换密钥");
  change.type = "button";
  change.addEventListener("click", function(){
    el("app").setAttribute("data-key-open", "1");
    el("key").focus();
  });
  var retry = node("button", "btn", "重试");
  retry.type = "button";
  retry.addEventListener("click", function(){ load(); });
  actions.appendChild(change);
  actions.appendChild(retry);
  wrap.appendChild(actions);
  return wrap;
}

function emptyState(){
  var wrap = node("div", "empty");
  wrap.appendChild(node("h3", null, "还没有账号"));
  wrap.appendChild(node("p", null, "账号以 CPA 凭证文件的形式保存，宿主的凭证列表里也能看到、能停用。"));
  var steps = node("ol", "steps");
  [
    "在浏览器登录 AnyRouter，打开开发者工具 → 网络 → 任意请求，复制请求标头里的 Cookie 整条。",
    "点上方「＋ 新增账号」，粘进「登录凭证」并保存。",
    "保存后即可签到与查看余额；每日自动签到按插件配置的时间执行。"
  ].forEach(function(text, index){
    var li = node("li");
    li.appendChild(node("span", "n", String(index + 1)));
    li.appendChild(node("span", null, text));
    steps.appendChild(li);
  });
  wrap.appendChild(steps);
  return wrap;
}

function row(a, index){
  var tr = node("tr");
  tr.setAttribute("data-busy", state.busy[a.name] ? "1" : "0");
  tr.setAttribute("data-flash", state.flash[a.name] ? "1" : "0");
  if (state.busy[a.name]) { /* keep the row readable while it works */ }

  /* 账号 */
  var name = node("td", "c-name");

  name.appendChild(node("div", "who", a.remark || a.name));
  var meta = node("div", "who-meta");
  if (a.user_id) { meta.appendChild(node("span", null, "用户 ID " + a.user_id + (a.user_id_source === "configured" ? "" : "（自动）"))); }
  var stem = a.name.replace(/\.json$/i, "");
  if (stem !== (a.remark || "")) {
    var chip = node("span", "chip", a.name);
    chip.title = a.name;
    meta.appendChild(chip);
  }
  name.appendChild(meta);
  tr.appendChild(name);

  /* 状态 */
  var st = statusMeta(a);
  var cell = node("td", "c-state");

  var line = node("div", "state");
  line.appendChild(node("span", "dot " + st.dot));
  var label = node("span", null, st.label);
  if (st.tone === "dim") { label.className = "dim"; }
  line.appendChild(label);
  cell.appendChild(line);
  if (a.proxy_url) { cell.appendChild(node("div", "when-sub", "出口 " + a.egress)); }
  if (a.last_error && !a.disabled) { cell.appendChild(node("div", "state-reason", a.last_error)); }
  tr.appendChild(cell);

  /* 上次签到 */
  var when = node("td", "c-when");
  when.setAttribute("data-label", "上次签到");
  when.appendChild(node("div", "when-main", a.last_checkin_at ? relTime(a.last_checkin_at) : "从未"));
  if (a.last_checkin_message && a.last_checkin_message !== st.label) {
    when.appendChild(node("div", "when-sub", a.last_checkin_message));
  }
  tr.appendChild(when);

  /* 剩余额度 */
  var quota = node("td", "c-quota");

  if (a.balance) {
    var head = node("div", "quota-head");
    var previous = state.dollars[a.name];
    var current = a.balance.dollars;
    var value = node("span", "quota-value", money(previous === undefined ? current : previous));
    value.setAttribute("data-quota-for", a.name);
    if (previous !== undefined && previous !== current) {
      rolls.push({ name: a.name, from: previous, to: current });
    }
    state.dollars[a.name] = current;
    head.appendChild(value);
    head.appendChild(node("span", "quota-unit", "剩余"));
    quota.appendChild(head);
    var f = quotaFraction(a.balance);
    var percent = Math.round(f * 100);
    var barRow = node("div", "bar-row");
    var track = node("div", "bar-track");
    track.setAttribute("role", "img");
    track.setAttribute("aria-label", "可用额度 " + percent + "%");
    track.title = "可用 " + percent + "%";
    var fill = node("div", "bar-fill " + quotaTone(f));
    fill.style.width = percent + "%";
    track.appendChild(fill);
    barRow.appendChild(track);
    barRow.appendChild(node("span", "bar-pct", percent + "%"));
    quota.appendChild(barRow);
    var meta2 = node("div", "quota-meta");
    meta2.appendChild(node("span", null, "已用 " + money(a.balance.used_dollars)));
    meta2.appendChild(node("span", null, a.balance.request_count + " 次请求"));
    if (a.balance.username) { meta2.appendChild(node("span", null, "@" + a.balance.username)); }
    if (a.balance.stale && a.balance.fetched_at) {
      meta2.appendChild(node("span", "dim", "上次读取 " + relTime(a.balance.fetched_at)));
    }
    quota.appendChild(meta2);
  } else {
    quota.appendChild(node("div", "quota-empty", "未读取"));
    var read = node("button", "btn btn-quiet", "读取余额");
    read.type = "button";
    read.addEventListener("click", function(){ runBalance(a.name); });
    quota.appendChild(read);
  }
  tr.appendChild(quota);

  /* 操作 */
  var ops = node("td", "c-ops");

  var box = node("div", "ops");
  box.appendChild(opButton(a, "checkin", "签到", function(){ runCheckin(a.name); }));
  box.appendChild(opButton(a, "balance", "查余额", function(){ runBalance(a.name); }));
  var edit = node("button", "btn btn-quiet", "编辑");
  edit.type = "button";
  edit.addEventListener("click", function(){ openDrawer("edit", a.name); });
  box.appendChild(edit);
  var toggle = node("button", "btn", a.disabled ? "启用" : "停用");
  toggle.type = "button";
  toggle.addEventListener("click", function(){ toggleAccount(a.name, !a.disabled); });
  box.appendChild(toggle);
  if (state.busy[a.name]) { edit.disabled = true; toggle.disabled = true; }
  ops.appendChild(box);
  tr.appendChild(ops);

  tr.style.setProperty("--i", String(index));
  return tr;
}

function opButton(a, kind, text, handler){
  var busy = state.busy[a.name] === kind;
  var b = node("button", "btn" + (kind === "checkin" ? "" : " btn-quiet"));
  b.type = "button";
  b.setAttribute("aria-busy", busy ? "true" : "false");
  if (busy) { b.appendChild(node("span", "spin")); }
  b.appendChild(node("span", null, busy ? (kind === "checkin" ? "签到中" : "读取中") : text));
  if (state.busy[a.name]) { b.disabled = true; }
  b.addEventListener("click", handler);
  return b;
}

function renderSchedule(){
  var schedule = (state.status && state.status.schedule) ? state.status.schedule : null;
  if (!schedule) { return; }
  el("schedCheckin").checked = !!schedule.auto_checkin;
  el("schedTime").value = schedule.checkin_time || "";
  el("schedBalance").checked = !!schedule.auto_refresh_balance;
  el("schedMinutes").value = schedule.balance_refresh_minutes || "";
  el("schedSource").textContent = schedule.source === "panel" ? "· 面板覆盖" : "· 跟随插件配置";
  el("schedNext").textContent = schedule.next_checkin_at
    ? ("下次自动签到 " + relTime(schedule.next_checkin_at))
    : "自动签到已关闭";
  var fileLine = "状态文件：" + (schedule.state_file || "—");
  if (schedule.updated_at) { fileLine += "　更新于 " + relTime(schedule.updated_at); }
  if (schedule.state_file_error) { fileLine += "　读取告警：" + schedule.state_file_error; }
  el("schedStateFile").textContent = fileLine;
}

function saveSchedule(reset){
  var payload = reset
    ? { reset: true }
    : {
        auto_checkin: el("schedCheckin").checked,
        checkin_time: el("schedTime").value.trim(),
        auto_refresh_balance: el("schedBalance").checked,
        balance_refresh_minutes: parseInt(el("schedMinutes").value, 10)
      };
  if (!reset) {
    if (!/^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(payload.checkin_time)) {
      setStatus("签到时间要写成 HH:MM，例如 08:00", "err");
      el("schedTime").focus();
      return;
    }
    if (isNaN(payload.balance_refresh_minutes)) { payload.balance_refresh_minutes = 30; }
  }
  var button = reset ? el("schedReset") : el("schedSave");
  button.disabled = true;
  setStatus(reset ? "正在恢复跟随配置…" : "正在保存自动任务…", "");
  api("/schedule", "POST", payload).then(function(data){
    if (state.status) { state.status.schedule = data.schedule; }
    renderSchedule();
    setStatus(reset ? "已恢复跟随插件配置" : "自动任务已保存", "ok");
    return load({ refreshBalances: false });
  }).catch(function(err){
    setStatus(err.message, "err");
  }).then(function(){
    button.disabled = false;
  });
}

function renderEvents(){
  var box = el("events");
  clear(box);
  var list = state.events || [];
  el("eventsCount").textContent = list.length ? ("　最近 " + list.length + " 条") : "";
  if (!list.length) {
    box.appendChild(node("div", "ev", "暂无事件。"));
    return;
  }
  list.forEach(function(e){
    var line = node("div", "ev");
    line.appendChild(node("span", "at", (e.at || "").replace("T", " ").slice(5, 16)));
    line.appendChild(node("span", "kind", e.kind || ""));
    line.appendChild(node("span", "who", e.account || ""));
    line.appendChild(node("span", "msg", e.message || ""));
    box.appendChild(line);
  });
}

function renderRaw(){
  var pre = el("raw");
  var payload = { status: state.status, config: state.config, accounts: (state.accounts || []).map(function(a){
    return { name: a.name, auth_index: a.auth_index, user_id: a.user_id, disabled: a.disabled,
      last_checkin_at: a.last_checkin_at, last_checkin_status: a.last_checkin_status,
      balance: a.balance || null, last_error: a.last_error || null };
  }) };
  pre.textContent = JSON.stringify(payload, null, 2);
}

/* ---------- number roll (motion #1) ---------- */

var rolls = [];

function rollNumber(name, from, to){
  var target = document.querySelector('[data-quota-for="' + name + '"]');
  if (!target) { return; }
  if (reduced || from === null || from === undefined || Math.abs(to - from) < 0.005) {
    target.textContent = money(to);
    return;
  }
  var start = performance.now();
  var dur = 280;
  function frame(now){
    var t = Math.min(1, (now - start) / dur);
    var eased = 1 - Math.pow(1 - t, 3);
    target.textContent = money(from + (to - from) * eased);
    if (t < 1) { window.requestAnimationFrame(frame); }
  }
  window.requestAnimationFrame(frame);
}

function flushRolls(){
  rolls.forEach(function(item){ rollNumber(item.name, item.from, item.to); });
  rolls = [];
}

/* ---------- actions ---------- */

function markBusy(name, kind){
  if (name) { state.busy[name] = kind; return; }
  state.accounts.forEach(function(a){ state.busy[a.name] = kind; });
}

function clearBusy(name){
  if (name) { delete state.busy[name]; return; }
  state.busy = {};
}

function runCheckin(name){
  var payload = name ? { name: name } : {};
  markBusy(name, "checkin");
  renderLedger();
  setBusyAll(true);
  api("/checkin", "POST", payload).then(function(data){
    (data.results || []).forEach(function(r){ state.flash[r.account] = true; });
    return load().then(function(){
      // The reload clears the banner, so the outcome is announced afterwards.
      state.banner = { kind: "info", text: data.summary || "签到完成", dismiss: true };
      renderBanner();
      window.setTimeout(function(){
        Object.keys(state.flash).forEach(function(k){ delete state.flash[k]; });
        renderLedger();
      }, 800);
    });
  }).catch(function(err){
    state.banner = { kind: "err", text: err.message, retry: false, dismiss: true };
  }).then(function(){
    clearBusy(name);
    setBusyAll(false);
    renderAll();
  });
}

function runBalance(name){
  var payload = name ? { name: name } : {};
  markBusy(name, "balance");
  renderLedger();
  setBusyAll(true);
  api("/balance", "POST", payload).then(function(data){
    return load().then(function(){
      state.banner = { kind: "info", text: data.summary || "余额已刷新", dismiss: true };
      renderBanner();
    });
  }).catch(function(err){
    state.banner = { kind: "err", text: err.message, retry: false, dismiss: true };
  }).then(function(){
    clearBusy(name);
    setBusyAll(false);
    renderAll();
  });
}

function refreshChallenge(){
  var button = el("challengeRefresh");
  button.disabled = true;
  button.setAttribute("aria-busy", "true");
  api("/challenge/refresh", "POST", {}).then(function(data){
    state.banner = { kind: data.ok ? "info" : "err", text: data.ok ? ("挑战 Cookie 已刷新（" + data.fields + " 个字段）") : (data.message || "刷新失败"), dismiss: true };
    return load();
  }).catch(function(err){
    state.banner = { kind: "err", text: err.message, dismiss: true };
  }).then(function(){
    button.disabled = false;
    button.removeAttribute("aria-busy");
    renderAll();
  });
}

function toggleAccount(name, disabled){
  state.busy[name] = "toggle";
  renderLedger();
  api("/account/disable", "POST", { name: name, disabled: disabled }).then(function(){
    state.banner = { kind: "info", text: disabled ? "账号已停用" : "账号已启用", dismiss: true };
    return load();
  }).catch(function(err){
    state.banner = { kind: "err", text: err.message, dismiss: true };
  }).then(function(){
    delete state.busy[name];
    renderAll();
  });
}

function setBusyAll(on){
  ["checkinAll","balanceAll","addAccount"].forEach(function(id){
    var b = el(id);
    if (b) { b.disabled = on; }
  });
}

function saveAccount(){
  var payload = {
    name: state.editing,
    remark: el("f-remark").value.trim(),
    cookie: el("f-cookie").value.trim(),
    user_id: el("f-userid").value.trim(),
    proxy_url: el("f-proxy").value.trim(),
    base_url: el("f-base").value.trim(),
    disabled: el("f-disabled").checked
  };
  if (!payload.cookie) {
    el("drawerHint").textContent = "登录凭证不能为空";
    el("f-cookie").focus();
    return;
  }
  var button = el("drawerSave");
  button.disabled = true;
  button.setAttribute("aria-busy", "true");
  el("drawerHint").textContent = "保存中…";
  api("/account", "POST", payload).then(function(data){
    closeDrawer();
    state.banner = { kind: "info", text: "账号已保存：" + (payload.remark || data.name), dismiss: true };
    return load();
  }).catch(function(err){
    el("drawerHint").textContent = err.message;
  }).then(function(){
    button.disabled = false;
    button.removeAttribute("aria-busy");
  });
}

function deleteAccount(){
  var button = el("drawerDelete");
  if (!state.deleteArmed) {
    state.deleteArmed = true;
    button.textContent = "确认删除？";
    window.setTimeout(function(){
      if (state.deleteArmed) {
        state.deleteArmed = false;
        button.textContent = "删除账号";
      }
    }, 4000);
    return;
  }
  button.disabled = true;
  el("drawerHint").textContent = "删除中…";
  hostDelete(state.editing).then(function(){
    closeDrawer();
    state.banner = { kind: "info", text: "账号已删除", dismiss: true };
    return load();
  }).catch(function(err){
    el("drawerHint").textContent = err.message;
  }).then(function(){
    button.disabled = false;
  });
}

/* ---------- drawer ---------- */

// renderCookieHelp turns the configured site into a clickable link, so the
// operator can jump straight to the login page the credential comes from.
function renderCookieHelp(){
  var help = el("cookieHelp");
  if (!help) { return; }
  var site = (state.config && state.config.base_url) || "";
  help.textContent = "";
  var prefix = document.createTextNode("浏览器登录 ");
  help.appendChild(prefix);
  if (site) {
    var link = document.createElement("a");
    link.href = site;
    link.target = "_blank";
    link.rel = "noopener";
    link.textContent = site.replace(/^https?:\\/\\//, "");
    help.appendChild(link);
  } else {
    help.appendChild(document.createTextNode("AnyRouter"));
  }
  help.appendChild(document.createTextNode(" 后，开发者工具 → 网络 → 任意请求 → 请求标头里的 Cookie 整条复制。"));
}

function openDrawer(mode, name){
  state.editing = mode === "edit" ? name : "";
  state.deleteArmed = false;
  el("drawerTitle").textContent = mode === "edit" ? "编辑账号" : "新增账号";
  renderCookieHelp();
  el("drawerDelete").hidden = mode !== "edit";
  el("drawerDelete").textContent = "删除账号";
  el("drawerHint").textContent = "";
  el("f-remark").value = "";
  el("f-cookie").value = "";
  el("f-userid").value = "";
  el("f-base").value = "";
  el("f-proxy").value = "";
  el("f-disabled").checked = false;
  if (mode === "edit") {
    el("drawerHint").textContent = "读取中…";
    api("/account?name=" + encodeURIComponent(name), "GET").then(function(data){
      var account = data.account || {};
      el("f-remark").value = account.remark || "";
      el("f-cookie").value = data.cookie || "";
      el("f-userid").value = data.user_id || "";
      el("f-base").value = data.base_url || "";
      el("f-proxy").value = data.proxy_url || "";
      el("f-disabled").checked = !!account.disabled;
      el("drawerHint").textContent = data.cookie ? "已填入当前凭证" : "";
    }).catch(function(err){
      el("drawerHint").textContent = err.message;
    });
  }
  el("drawer").setAttribute("data-open", "1");
  el("backdrop").setAttribute("data-open", "1");
  window.setTimeout(function(){ el("f-remark").focus(); }, 120);
  if (name) {
    var any = false;
    for (var i = 0; i < state.accounts.length; i++) { if (state.accounts[i].name === name) { any = true; } }
    if (any) { state.flash[name] = true; }
  }
}

function closeDrawer(){
  el("drawer").setAttribute("data-open", "0");
  el("backdrop").setAttribute("data-open", "0");
  state.editing = "";
  state.deleteArmed = false;
}

/* ---------- key gate ---------- */

function saveKey(){
  var value = el("key").value.trim();
  state.key = value;
  try { window.localStorage.setItem(KEY_STORE, value); } catch (e) {}
  el("app").setAttribute("data-key-open", "0");
  state.banner = null;
  renderConnection();
  if (!value) {
    state.banner = { kind: "err", text: "请先填写 CPA 管理密钥", dismiss: false };
    renderAll();
    return;
  }
  state.loaded = false;
  state.accounts = [];
  load();
}

/* ---------- evidence states ---------- */

function applyStateParam(){
  var params = new URLSearchParams(window.location.search);
  var wanted = params.get("state");
  if (!wanted) { return; }
  if (el("app").getAttribute("data-state-applied") === "1") { return; }
  el("app").setAttribute("data-state-applied", "1");
  if (wanted === "keys") {
    el("app").setAttribute("data-key-open", "1");
    return;
  }
  if (wanted === "busy" && state.accounts.length) {
    state.busy[state.accounts[0].name] = "balance";
    renderLedger();
    return;
  }
  if (wanted === "drawer") { openDrawer("create", ""); return; }
  if (!state.loaded) { return; }
  if ((wanted === "edit" || wanted === "confirm") && state.accounts.length) {
    openDrawer("edit", state.accounts[0].name);
    if (wanted === "confirm") {
      window.setTimeout(function(){
        state.deleteArmed = true;
        el("drawerDelete").textContent = "确认删除？";
      }, 400);
    }
  }
}

/* ---------- init ---------- */

function init(){
  try { state.key = window.localStorage.getItem(KEY_STORE) || ""; } catch (e) {}
  el("key").value = state.key;

  el("keySave").addEventListener("click", saveKey);
  el("key").addEventListener("keydown", function(e){ if (e.key === "Enter") { saveKey(); } });
  // Always reveals the key row: a toggle would make the control's meaning depend
  // on hidden state, and reopening it after saving stays possible.
  el("keyToggle").addEventListener("click", function(){
    el("app").setAttribute("data-key-open", "1");
    el("key").focus();
  });
  el("checkinAll").addEventListener("click", function(){ runCheckin(""); });
  el("balanceAll").addEventListener("click", function(){ runBalance(""); });
  el("challengeRefresh").addEventListener("click", refreshChallenge);
  el("schedSave").addEventListener("click", function(){ saveSchedule(false); });
  el("schedReset").addEventListener("click", function(){ saveSchedule(true); });
  el("schedTime").addEventListener("keydown", function(e){ if (e.key === "Enter") { saveSchedule(false); } });
  el("addAccount").addEventListener("click", function(){ openDrawer("create", ""); });
  el("drawerClose").addEventListener("click", closeDrawer);
  el("drawerCancel").addEventListener("click", closeDrawer);
  el("backdrop").addEventListener("click", closeDrawer);
  el("drawerSave").addEventListener("click", saveAccount);
  el("drawerDelete").addEventListener("click", deleteAccount);
  document.addEventListener("keydown", function(e){
    if (e.key === "Escape") {
      if (el("drawer").getAttribute("data-open") === "1") { closeDrawer(); return; }
      el("app").setAttribute("data-key-open", "0");
    }
  });

  if (state.key) { load(); } else { renderAll(); }
}
var booted = false;
function boot(){ if (booted) { return; } booted = true; init(); }
document.addEventListener("DOMContentLoaded", boot);
if (document.readyState !== "loading") { boot(); }
})();
`
