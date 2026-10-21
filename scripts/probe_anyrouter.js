// Dev probe: reproduce the plugin's AnyRouter balance request outside CPA.
//
//   $env:ANYROUTER_SESSION = "<session cookie value copied from the auth file>"
//   node scripts/probe_anyrouter.js
//
// It solves the JSL interstitial exactly like the plugin does, then walks a list
// of candidate endpoints with the same header set. Output is redacted: the
// cookie value never reaches stdout.
const BASE = process.env.ANYROUTER_BASE || "https://anyrouter.top";
const SESSION = process.env.ANYROUTER_SESSION || "";
const UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36";

function redact(text) {
  let out = String(text || "");
  if (SESSION) out = out.split(SESSION).join("<SESSION>");
  return out.length > 400 ? out.slice(0, 400) + "…" : out;
}

function solveChallenge(html) {
  const scripts = [...html.matchAll(/<script[^>]*>([\s\S]*?)<\/script>/gi)].map((m) => m[1]);
  if (!scripts.length) return null;
  const jar = [];
  const doc = {};
  Object.defineProperty(doc, "cookie", {
    get() { return jar.join("; "); },
    set(v) { const p = String(v).split(";")[0].trim(); if (p) jar.push(p); },
    configurable: true,
  });
  const loc = { href: "about:blank", reload() {}, replace() {}, assign() {} };
  doc.location = loc;
  const win = { document: doc, location: loc, navigator: { userAgent: UA }, setTimeout: () => 0, setInterval: () => 0, clearTimeout() {}, clearInterval() {}, alert() {} };
  win.window = win; win.self = win; win.top = win;
  for (const script of scripts) {
    const runner = new Function("document", "location", "window", "self", "top", "navigator", "setTimeout", "setInterval", "clearTimeout", "clearInterval", "alert", script);
    try {
      runner(doc, loc, win, win, win, win.navigator, win.setTimeout, win.setInterval, win.clearTimeout, win.clearInterval, win.alert);
    } catch (error) { /* the anti-debug trap throws a RangeError in V8; ignore */ }
  }
  return jar.join("; ") || null;
}

function headers(extra) {
  const base = {
    "user-agent": UA,
    accept: "application/json, text/plain, */*",
    "accept-language": "zh-CN,zh;q=0.9,en;q=0.8",
    origin: BASE,
    referer: BASE + "/",
    "x-requested-with": "XMLHttpRequest",
  };
  if (extra) Object.assign(base, extra);
  return base;
}

async function main() {
  if (!SESSION) { console.error("set ANYROUTER_SESSION first"); process.exit(1); }

  const challengeResp = await fetch(BASE + "/", { headers: headers(), redirect: "manual" });
  const challengeHtml = await challengeResp.text();
  const acw = solveChallenge(challengeHtml);
  console.log("challenge solved:", acw ? "yes (" + acw.split("=")[0] + ")" : "no");

  const cookieHeader = ["session=" + SESSION, acw].filter(Boolean).join("; ");
  const endpoints = [
    ["GET", "/api/user/self", true],
    ["GET", "/api/user/self", false],
    ["GET", "/api/user/dashboard", true],
    ["GET", "/api/user/amount", true],
    ["GET", "/api/user/token", true],
    ["GET", "/api/status", true],
    ["POST", "/api/user/sign_in", true],
  ];
  for (const [method, path, withSession] of endpoints) {
    const cookie = withSession ? cookieHeader : (acw || "");
    try {
      const resp = await fetch(BASE + path, { method, headers: headers({ cookie }), redirect: "manual" });
      const text = await resp.text();
      const isChallenge = /acw_sc__v2|arg1=/.test(text);
      console.log(`${method.padEnd(5)} ${path.padEnd(22)} session=${withSession ? "y" : "n"} -> ${resp.status} ${isChallenge ? "[challenge page]" : redact(text)}`);
    } catch (error) {
      console.log(`${method.padEnd(5)} ${path.padEnd(22)} session=${withSession ? "y" : "n"} -> ERR ${error.message}`);
    }
  }
}

main();
