package main

// panelHTML returns the plugin panel: one self-contained document served under
// /v0/resource/plugins/anyrouter/panel. The resource route is NOT
// management-authenticated, so the page only renders and calls
// /v0/management/anyrouter/... itself, where the host enforces the key. No
// external stylesheet, script or icon font is referenced: the page shares the
// management origin, and one artifact means one thing to trust.
//
// Visual direction: "operator ledger" (运维台账). The status strip answers "今天
// 有没有事", the ledger answers "每个号还剩多少", and the balance figure is the
// only thing allowed to be loud. Structure lives in panel_css.go (tokens and
// layout) and panel_js.go (state and rendering); this file is the shell.
func panelHTML() string {
	return panelShellHead + panelShellBody + "\n<script>\n" + panelJS + "\n</script>\n</body>\n</html>\n"
}

const panelShellHead = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AnyRouter 账号台账</title>
<style>` + panelCSS + `</style>
</head>
<body>
`

const panelShellBody = `<div class="app" id="app" data-need-key="1" data-key-open="0" data-enter="1">
  <div class="bar">
    <h1>AnyRouter 账号台账</h1>
    <span class="site mono" id="site"></span>
    <span class="spacer"></span>
    <div class="conn">
      <span class="dot" id="connDot"></span>
      <span id="connText">未连接</span>
      <button class="btn btn-quiet" id="keyToggle" type="button">填写管理密钥</button>
    </div>
  </div>

  <div class="keyrow">
    <label class="keyhint" for="key">CPA 管理密钥</label>
    <input id="key" type="password" autocomplete="current-password" placeholder="CPA 管理中心的登录密钥">
    <button class="btn btn-primary" id="keySave" type="button">保存并连接</button>
    <span class="keyhint">只保存在本机浏览器，不会发给插件</span>
  </div>

  <div id="banner"></div>
  <div class="strip" id="strip" aria-live="polite"></div>

  <div class="head">
    <h2>账号台账</h2>
    <span class="count num" id="count"></span>
    <span class="spacer"></span>
    <div class="actions">
      <button class="btn" id="checkinAll" type="button">全部签到</button>
      <button class="btn" id="balanceAll" type="button">刷新余额</button>
      <button class="btn" id="challengeRefresh" type="button">刷新挑战 Cookie</button>
      <button class="btn btn-primary" id="addAccount" type="button">＋ 新增账号</button>
    </div>
  </div>

  <div class="ledger" id="ledger"></div>

  <section class="tasks" id="tasks">
    <div class="tasks-head">
      <h2>自动任务</h2>
      <span class="count" id="schedSource"></span>
      <span class="spacer"></span>
      <span class="dim" id="schedNext"></span>
    </div>
    <div class="tasks-body">
      <label class="inline-check"><input type="checkbox" id="schedCheckin"> 每日自动签到</label>
      <label class="inline-check">时间 <input id="schedTime" class="time-input" inputmode="numeric" placeholder="08:00"></label>
      <label class="inline-check"><input type="checkbox" id="schedBalance"> 定时刷新余额</label>
      <label class="inline-check">间隔 <input id="schedMinutes" class="num-input" inputmode="numeric" placeholder="30"> 分钟</label>
      <span class="spacer"></span>
      <button class="btn" id="schedReset" type="button">恢复跟随配置</button>
      <button class="btn btn-primary" id="schedSave" type="button">保存</button>
    </div>
    <div class="help" id="schedStateFile"></div>
  </section>

  <details class="events" id="eventsWrap">
    <summary><span class="caret">▸</span>事件<span class="dim" id="eventsCount"></span></summary>
    <div class="ev-list" id="events"></div>
  </details>

  <details class="events" id="rawWrap">
    <summary><span class="caret">▸</span>诊断数据</summary>
    <div class="ev-list"><pre class="mono" id="raw" style="white-space:pre-wrap;margin:0 0 10px"></pre></div>
  </details>
</div>

<div class="backdrop" id="backdrop"></div>
<aside class="drawer" id="drawer" role="dialog" aria-modal="true" aria-labelledby="drawerTitle" data-open="0">
  <div class="drawer-head">
    <h2 id="drawerTitle">新增账号</h2>
    <span class="spacer"></span>
    <button class="btn btn-quiet" id="drawerClose" type="button">关闭</button>
  </div>
  <div class="drawer-body">
    <div class="field">
      <label for="f-remark">备注</label>
      <input id="f-remark" placeholder="例如：主号（可留空）">
    </div>
    <div class="field">
      <label for="f-cookie">登录凭证（Cookie）</label>
      <textarea id="f-cookie" spellcheck="false" placeholder="session=…"></textarea>
      <div class="help" id="cookieHelp">浏览器登录 AnyRouter 后，开发者工具 → 网络 → 任意请求 → 请求标头里的 Cookie 整条复制。</div>
    </div>
    <div class="grid2">
      <div class="field">
        <label for="f-userid">用户 ID</label>
        <input id="f-userid" placeholder="留空自动推导">
        <div class="help">余额接口需要；留空时从凭证里推导。</div>
      </div>
      <div class="field">
        <label for="f-base">站点地址</label>
        <input id="f-base" placeholder="留空则继承插件配置">
      </div>
    </div>
    <div class="field">
      <label for="f-proxy">代理（可空）</label>
      <input id="f-proxy" placeholder="留空跟随 CPA 的全局代理">
      <div class="help">anyrouter.top 直连不通时填：http://127.0.0.1:10808、socks5://127.0.0.1:1080，或 direct 强制直连。</div>
    </div>
    <label class="inline-check"><input type="checkbox" id="f-disabled"> 停用该账号（保留凭证，不参与签到与余额）</label>
  </div>
  <div class="drawer-foot">
    <button class="btn btn-danger" id="drawerDelete" type="button" hidden>删除账号</button>
    <span class="keyhint" id="drawerHint"></span>
    <span class="spacer"></span>
    <button class="btn" id="drawerCancel" type="button">取消</button>
    <button class="btn btn-primary" id="drawerSave" type="button">保存账号</button>
  </div>
</aside>
`
