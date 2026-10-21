package main

// panelCSS is the stylesheet of the plugin panel. It is inlined into the single
// page the plugin serves (see panel.go): the resource page shares the CPA
// management origin, so it stays one self-contained artifact with no external
// stylesheet, script or icon font.
//
// Visual direction: "operator ledger" - a dense, quiet console where the balance
// figure is the hero and everything else recedes. One accent colour, hairline
// rules, tabular figures, four-tone status dots.
const panelCSS = `
:root{
  --bg:#f5f6f8;
  --surface:#ffffff;
  --surface-2:#fafbfc;
  --line:#e5e7eb;
  --line-soft:#eef0f3;
  --ink:#191c1f;
  --ink-2:#4a5260;
  --ink-3:#666e7a;
  --accent:#3355d1;
  --accent-ink:#ffffff;
  --accent-soft:#eef1fe;
  --ok:#137a3a;
  --ok-soft:#eaf7ef;
  --warn:#a35a08;
  --warn-soft:#fdf6e7;
  --err:#b3261e;
  --err-soft:#fdeeed;
  --idle:#8b95a1;
  --radius:10px;
  --radius-sm:7px;
  --shadow-1:0 1px 2px rgba(16,24,40,.05);
  --shadow-2:0 10px 30px rgba(16,24,40,.14);
  --mono:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;
  --sans:-apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Hiragino Sans GB","Microsoft YaHei","Noto Sans SC",sans-serif;
  --ease-out:cubic-bezier(.2,.8,.25,1);
  --ease-drawer:cubic-bezier(.16,1,.3,1);
}
*{box-sizing:border-box}
/* The button rules below set display, which would otherwise beat the user agent
   rule for [hidden]; an element marked hidden must never stay visible. */
[hidden]{display:none !important}
html,body{margin:0}
body{
  background:var(--bg);
  color:var(--ink);
  font:14px/1.55 var(--sans);
  -webkit-font-smoothing:antialiased;
}
.num{font-variant-numeric:tabular-nums}
.mono{font-family:var(--mono);font-variant-numeric:tabular-nums}
.muted{color:var(--ink-2)}
.dim{color:var(--ink-3)}
.app{max-width:1180px;margin:0 auto;padding:16px 20px 72px}

/* ---------- app bar ---------- */
.bar{display:flex;align-items:center;gap:12px;min-height:40px}
.bar h1{font-size:15px;font-weight:650;margin:0;letter-spacing:.01em}
.bar .site{font-family:var(--mono);font-size:12.5px;color:var(--ink-2);max-width:44ch;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.bar .spacer{flex:1}
.conn{display:flex;align-items:center;gap:8px;font-size:13px;color:var(--ink-2)}
.dot{width:7px;height:7px;border-radius:50%;background:var(--idle);flex:none}
.dot.ok{background:var(--ok)}
.dot.warn{background:var(--warn)}
.dot.err{background:var(--err)}
.dot.ring{background:transparent;box-shadow:inset 0 0 0 2px var(--ok)}
.dot.off{background:transparent;box-shadow:inset 0 0 0 1.5px var(--line-strong,#c9ced6)}

/* ---------- key gate ---------- */
.keyrow{display:none;align-items:center;gap:8px;margin-top:10px;padding:10px 12px;background:var(--surface);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow-1)}
.app[data-need-key="1"] .keyrow,.app[data-key-open="1"] .keyrow{display:flex}
.keyrow input{flex:1;min-width:0}
.keyhint{font-size:12.5px;color:var(--ink-3)}

/* ---------- summary strip ---------- */
.strip{display:flex;flex-wrap:wrap;align-items:center;gap:6px 26px;margin-top:14px;padding:11px 14px;background:var(--surface);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow-1)}
.fact{display:flex;align-items:baseline;gap:7px;min-width:0}
.fact .k{font-size:12.5px;color:var(--ink-3);white-space:nowrap}
.fact .v{font-size:13.5px;font-weight:550}
.fact.secondary .v{font-weight:450;color:var(--ink-2)}
.fact.primary .v{font-weight:650}
.fact.tight{flex-direction:column;gap:0}
.fact .sub{font-size:12.5px;color:var(--ink-3)}
.strip .spacer{flex:1}

/* ---------- section head ---------- */
.head{display:flex;align-items:center;gap:10px;margin:16px 0 8px}
.head h2{font-size:13px;font-weight:650;margin:0;letter-spacing:.02em}
.head .count{font-size:12.5px;color:var(--ink-3);font-variant-numeric:tabular-nums}
.head .spacer{flex:1}
.actions{display:flex;gap:8px;flex-wrap:wrap}

/* ---------- buttons ---------- */
.btn{
  font:inherit;font-size:13px;line-height:1;height:30px;padding:0 11px;
  display:inline-flex;align-items:center;gap:6px;
  background:var(--surface);color:var(--ink);
  border:1px solid var(--line);border-radius:var(--radius-sm);
  cursor:pointer;transition:background-color .15s var(--ease-out),border-color .15s var(--ease-out),transform .08s var(--ease-out),opacity .15s linear;
}
.btn:hover{background:var(--surface-2);border-color:#d8dce2}
.btn:active{transform:translateY(1px)}
.btn:focus-visible,input:focus-visible,textarea:focus-visible,summary:focus-visible,tr:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.btn[disabled]{opacity:.55;cursor:not-allowed;transform:none}
.btn-primary{background:var(--accent);border-color:var(--accent);color:var(--accent-ink)}
.btn-primary:hover{background:#2b49bb;border-color:#2b49bb}
.btn-quiet{border-color:transparent;background:transparent;color:var(--ink-2);padding:0 8px}
.btn-quiet:hover{background:var(--surface-2);border-color:var(--line);color:var(--ink)}
.btn-danger{border-color:transparent;background:transparent;color:var(--err);padding:0 8px}
.btn-danger:hover{background:var(--err-soft);border-color:#f2cfcc}
.btn[aria-busy="true"]{color:var(--ink-3)}
.spin{width:12px;height:12px;border-radius:50%;border:1.6px solid currentColor;border-top-color:transparent;animation:spin .7s linear infinite;flex:none}
@keyframes spin{to{transform:rotate(360deg)}}

/* ---------- ledger ---------- */
.ledger{background:var(--surface);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow-1);overflow:hidden}
table{width:100%;border-collapse:collapse;table-layout:fixed}
thead th{
  text-align:left;font-size:12.5px;font-weight:600;color:var(--ink-3);
  padding:9px 14px;background:var(--surface-2);border-bottom:1px solid var(--line);white-space:nowrap;
}
tbody td{padding:11px 14px;border-bottom:1px solid var(--line-soft);vertical-align:top}
tbody tr:last-child td{border-bottom:0}
tbody tr{transition:background-color .15s var(--ease-out)}
tbody tr:hover{background:#fcfcfd}
tbody tr[data-busy="1"]{background:#fbfcfe}
tbody tr[data-flash="1"]{animation:flash .7s var(--ease-out)}
@keyframes flash{0%{background:var(--accent-soft)}100%{background:transparent}}
th.c-name,td.c-name{width:26%}
th.c-state,td.c-state{width:14%}
th.c-when,td.c-when{width:15%}
th.c-quota,td.c-quota{width:27%}
th.c-ops,td.c-ops{width:18%;text-align:right}

.who{font-weight:600;font-size:14px;overflow-wrap:anywhere}
.who-meta{display:flex;gap:8px;flex-wrap:wrap;margin-top:3px;font-size:12.5px;color:var(--ink-3)}
.chip{font-family:var(--mono);font-size:12px;color:var(--ink-3);background:var(--surface-2);border:1px solid var(--line-soft);border-radius:5px;padding:1px 5px;max-width:100%;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.state{display:flex;align-items:center;gap:7px;font-size:13px;font-weight:550}
.state-reason{margin-top:4px;font-size:12.5px;color:var(--err);overflow-wrap:anywhere}
.when-main{font-size:13px}
.when-sub{margin-top:3px;font-size:12.5px;color:var(--ink-3);overflow-wrap:anywhere}

.quota-head{display:flex;align-items:baseline;gap:7px}
.quota-value{font-size:18px;font-weight:650;letter-spacing:-.01em;font-variant-numeric:tabular-nums}
.quota-unit{font-size:12.5px;color:var(--ink-3)}
.quota-empty{font-size:13px;color:var(--ink-3)}
.bar-row{display:flex;align-items:center;gap:8px;margin:7px 0 6px}
.bar-track{height:4px;border-radius:2px;background:#edeff3;flex:1;overflow:hidden}
.bar-pct{flex:none;font-size:12.5px;color:var(--ink-3);font-variant-numeric:tabular-nums}
.bar-fill{height:100%;border-radius:2px;background:var(--ok);transition:width .5s var(--ease-out),background-color .3s linear}
.bar-fill.warn{background:var(--warn)}
.bar-fill.err{background:var(--err)}
.quota-meta{font-size:12.5px;color:var(--ink-3);display:flex;gap:10px;flex-wrap:wrap}
.ops{display:flex;gap:2px;justify-content:flex-end;flex-wrap:nowrap;white-space:nowrap}
.confirm{display:flex;gap:4px;justify-content:flex-end;align-items:center;font-size:12.5px;color:var(--err)}

/* ---------- empty / skeleton / banner ---------- */
.empty{padding:22px 14px 26px;text-align:left}
.empty h3{font-size:14px;margin:0 0 4px}
.empty p{font-size:13px;color:var(--ink-2);margin:0 0 14px;max-width:60ch}
.steps{list-style:none;margin:0 0 18px;padding:0;display:grid;gap:9px;max-width:64ch}
.steps li{display:flex;gap:10px;font-size:13px;color:var(--ink-2)}
.steps .n{flex:none;width:18px;height:18px;border-radius:50%;background:var(--accent-soft);color:var(--accent);font-size:12px;font-weight:650;display:grid;place-items:center;margin-top:1px}
.steps code{font-family:var(--mono);font-size:12px;background:var(--surface-2);border:1px solid var(--line-soft);border-radius:5px;padding:1px 5px}
.skel{padding:12px 14px}
.skel-row{display:flex;gap:14px;align-items:center;padding:11px 0;border-bottom:1px solid var(--line-soft)}
.skel-row:last-child{border-bottom:0}
.sk{height:11px;border-radius:6px;background:linear-gradient(90deg,#eef0f3 25%,#f6f7f9 37%,#eef0f3 63%);background-size:400% 100%;animation:shimmer 1.4s ease-in-out infinite}
@keyframes shimmer{0%{background-position:100% 0}100%{background-position:0 0}}
.sk.w1{width:22%}.sk.w2{width:12%}.sk.w3{width:16%}.sk.w4{width:26%}
.banner{display:flex;align-items:flex-start;gap:10px;margin:12px 0 0;padding:11px 13px;background:var(--err-soft);border:1px solid #f2cfcc;border-radius:var(--radius);font-size:13px;color:var(--err)}
.banner .spacer{flex:1}
.banner.info{background:var(--accent-soft);border-color:#d6ddfb;color:#2b49bb}

/* ---------- automatic tasks ---------- */
.tasks{margin-top:20px;border-top:1px solid var(--line);padding-top:13px}
.tasks-head{display:flex;align-items:center;gap:10px}
.tasks-head h2{font-size:13px;font-weight:650;margin:0;letter-spacing:.02em}
.tasks-body{display:flex;flex-wrap:wrap;align-items:center;gap:8px 18px;margin-top:11px}
.tasks-body .spacer{flex:1;min-width:8px}
.tasks .time-input{width:78px;padding:5px 8px}
.tasks .num-input{width:68px;padding:5px 8px}
.tasks .help{margin-top:9px;font-size:12.5px;color:var(--ink-3);overflow-wrap:anywhere}

/* ---------- events ---------- */
.events{margin:0;background:transparent;border:0;border-top:1px solid var(--line);border-radius:0;box-shadow:none}
.events:first-of-type{margin-top:18px}
.events summary{cursor:pointer;padding:11px 0;font-size:13px;font-weight:600;list-style:none;display:flex;align-items:center;gap:8px}
.events summary::-webkit-details-marker{display:none}
.events summary .caret{color:var(--ink-3);transition:transform .2s var(--ease-out)}
.events[open] summary .caret{transform:rotate(90deg)}
.ev-list{padding:0 0 12px;max-height:320px;overflow:auto}
.ev{display:flex;gap:10px;align-items:baseline;padding:7px 0;border-top:1px solid var(--line-soft);font-size:13px}
.ev .at{font-family:var(--mono);font-size:12px;color:var(--ink-3);flex:none;width:104px}
.ev .kind{flex:none;width:56px;font-size:12.5px;color:var(--ink-3)}
.ev .who{font-size:13px;font-weight:550;overflow-wrap:anywhere}
.ev .msg{color:var(--ink-2);overflow-wrap:anywhere}

/* ---------- drawer ---------- */
.backdrop{position:fixed;inset:0;background:rgba(16,24,40,.32);opacity:0;pointer-events:none;transition:opacity .32s var(--ease-drawer);z-index:20}
.backdrop[data-open="1"]{opacity:1;pointer-events:auto}
.drawer{
  position:fixed;top:0;right:0;bottom:0;width:min(440px,100%);
  background:var(--surface);border-left:1px solid var(--line);box-shadow:var(--shadow-2);
  transform:translateX(102%);transition:transform .32s var(--ease-drawer);
  display:flex;flex-direction:column;z-index:21;
}
.drawer[data-open="1"]{transform:none}
.drawer-head{display:flex;align-items:center;gap:10px;padding:14px 18px;border-bottom:1px solid var(--line)}
.drawer-head h2{font-size:14px;margin:0;font-weight:650}
.drawer-head .spacer{flex:1}
.drawer-body{padding:16px 18px;overflow:auto;display:grid;gap:14px}
.drawer-foot{padding:14px 18px;border-top:1px solid var(--line);display:flex;gap:8px;align-items:center}
.drawer-foot .spacer{flex:1}
.field{display:grid;gap:5px}
.field label{font-size:12.5px;color:var(--ink-2);font-weight:550}
.field .help{font-size:12.5px;color:var(--ink-3)}
input,textarea{
  font:inherit;font-size:13px;color:var(--ink);
  padding:7px 9px;background:var(--surface);
  border:1px solid var(--line);border-radius:var(--radius-sm);
  transition:border-color .15s var(--ease-out),box-shadow .15s var(--ease-out);
}
input:hover,textarea:hover{border-color:#d8dce2}
input:focus,textarea:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-soft);outline:none}
textarea{min-height:92px;resize:vertical;font-family:var(--mono);font-size:12.5px;line-height:1.6}
.grid2{display:grid;grid-template-columns:1fr 1fr;gap:14px}
.inline-check{display:flex;align-items:center;gap:8px;font-size:13px}
.inline-check input{width:auto;padding:0}

/* ---------- first paint ---------- */
.app[data-enter="1"] .strip,.app[data-enter="1"] .ledger,.app[data-enter="1"] .head{animation:rise .42s var(--ease-out) both}
.app[data-enter="1"] tbody tr{animation:rise .38s var(--ease-out) both;animation-delay:calc(var(--i,0) * 45ms + 60ms)}
@keyframes rise{from{opacity:0;transform:translateY(6px)}to{opacity:1;transform:none}}

/* ---------- narrow ---------- */
@media (max-width:900px){
  .app{padding:14px 14px 60px}
  .grid2{grid-template-columns:1fr}

  /* app bar: title and connection share line one, the site drops to its own */
  .bar{flex-wrap:wrap;row-gap:2px}
  .bar .site{order:9;flex-basis:100%;max-width:100%}
  .conn{font-size:12.5px}

  /* section head: title and count, then the actions on their own line */
  .head{flex-wrap:wrap;row-gap:8px}
  .head .actions{width:100%;justify-content:flex-start}

  /* stacked ledger: the fixed column widths must not survive the layout switch */
  thead{display:none}
  table,tbody,tr,td{display:block;width:auto}
  th.c-name,td.c-name,
  th.c-state,td.c-state,
  th.c-when,td.c-when,
  th.c-quota,td.c-quota,
  th.c-ops,td.c-ops{width:auto}
  tbody tr{padding:13px 14px;border-bottom:1px solid var(--line-soft)}
  tbody td{border:0;padding:0}
  tbody td + td{margin-top:8px}
  tbody td::before{
    content:attr(data-label);display:block;
    font-size:12.5px;color:var(--ink-3);margin-bottom:3px;
  }
  th.c-ops,td.c-ops{text-align:left}
  .ops,.confirm{justify-content:flex-start}
  .fact.tight{flex-direction:row;gap:7px;align-items:baseline}
  .quota-value{font-size:17px}
}
@media (prefers-reduced-motion:reduce){
  *{animation-duration:.001ms !important;animation-iteration-count:1 !important;transition-duration:.001ms !important}
}
`
