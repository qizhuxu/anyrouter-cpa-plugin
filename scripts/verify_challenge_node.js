// Cross-check helper: runs the captured AnyRouter (加速乐 / JSL) interstitial in
// Node with a stubbed document and prints the cookie it would install.
//
//   node scripts/verify_challenge_node.js testdata/challenge_jsl.html
//
// The Go side (challenge_test.go) must produce the same acw_sc__v2 value; the
// two engines disagreeing means the interpretation wrapper is wrong.
const fs = require("fs");

const file = process.argv[2] || "testdata/challenge_jsl.html";
const html = fs.readFileSync(file, "utf8");
const scripts = [...html.matchAll(/<script[^>]*>([\s\S]*?)<\/script>/gi)].map((match) => match[1]);

if (scripts.length === 0) {
  console.error("no <script> blocks found");
  process.exit(1);
}

const jar = [];
const doc = {};
Object.defineProperty(doc, "cookie", {
  get() {
    return jar.join("; ");
  },
  set(value) {
    const pair = String(value).split(";")[0].trim();
    if (pair) jar.push(pair);
  },
  configurable: true,
});
const loc = { href: "about:blank", reload() {}, replace() {}, assign() {} };
doc.location = loc;
doc.write = () => {};
doc.writeln = () => {};

const win = {
  document: doc,
  location: loc,
  navigator: { userAgent: "node", language: "zh-CN" },
  setTimeout: () => 0,
  setInterval: () => 0,
  clearTimeout: () => {},
  clearInterval: () => {},
  alert: () => {},
};
win.window = win;
win.self = win;
win.top = win;

scripts.forEach((script, index) => {
  const runner = new Function(
    "document", "location", "window", "self", "top", "navigator",
    "setTimeout", "setInterval", "clearTimeout", "clearInterval", "alert",
    script,
  );
  try {
    runner(doc, loc, win, win, win, win.navigator, win.setTimeout, win.setInterval,
      win.clearTimeout, win.clearInterval, win.alert);
  } catch (error) {
    console.error(`script ${index} threw: ${error && error.message}`);
  }
});

console.log(jar.join("; "));
