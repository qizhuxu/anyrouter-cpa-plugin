package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// AnyRouter sits behind a 加速乐 (JSL) interstitial: the first request answers
// with HTML whose script computes an "acw_sc__v2" cookie and reloads. The script
// is obfuscated with a string table, so the only faithful way to obtain the
// cookie is to execute it. goja (pure Go, ES5.1) runs the script with a stubbed
// document, which is also what the original Deno wrapper did with eval().

const (
	jslStandardKey     = "3000176000856006061501533003690027800375"
	challengeEvalLimit = 3 * time.Second
)

var (
	scriptTagPattern  = regexp.MustCompile(`(?is)<script[^>]*>(.*?)</script>`)
	arg1Pattern       = regexp.MustCompile(`(?i)\barg1\s*=\s*['"]([0-9A-Fa-f]{16,64})['"]`)
	hex40Pattern      = regexp.MustCompile(`[0-9A-Fa-f]{40}`)
	acwLiteralPattern = regexp.MustCompile(`acw_sc__v2=([0-9A-Fa-f]{16,64})`)
)

// jslUnboxOrder is the permutation table used by the canonical JSL script.
var jslUnboxOrder = []int{
	0xf, 0x23, 0x1d, 0x18, 0x21, 0x10, 0x1, 0x26, 0xa, 0x9,
	0x13, 0x1f, 0x28, 0x1b, 0x16, 0x17, 0x19, 0xd, 0x6, 0xb,
	0x27, 0x12, 0x14, 0x8, 0xe, 0x15, 0x20, 0x1a, 0x2, 0x1e,
	0x7, 0x4, 0x11, 0x5, 0x3, 0x1c, 0x22, 0x25, 0xc, 0x24,
}

// challengeEntry is one solved cookie set for one host.
type challengeEntry struct {
	Cookies  string
	Method   string
	SolvedAt time.Time
}

// challengeCache stores the solved cookie per host with a TTL.
type challengeCache struct {
	mu      sync.Mutex
	entries map[string]challengeEntry
}

func newChallengeCache() *challengeCache {
	return &challengeCache{entries: make(map[string]challengeEntry)}
}

var challengeStore = newChallengeCache()

func (c *challengeCache) get(host string, ttl time.Duration) (challengeEntry, bool) {
	if c == nil {
		return challengeEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[host]
	if !ok || entry.Cookies == "" {
		return challengeEntry{}, false
	}
	if ttl > 0 && time.Since(entry.SolvedAt) > ttl {
		return challengeEntry{}, false
	}
	return entry, true
}

func (c *challengeCache) put(host, cookies, method string) {
	if c == nil || host == "" {
		return
	}
	c.mu.Lock()
	c.entries[host] = challengeEntry{Cookies: cookies, Method: method, SolvedAt: time.Now()}
	c.mu.Unlock()
}

func (c *challengeCache) invalidate(host string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, host)
	c.mu.Unlock()
}

func (c *challengeCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = make(map[string]challengeEntry)
	c.mu.Unlock()
}

// snapshot reports cache age for the status route without exposing the cookie.
func (c *challengeCache) snapshot() map[string]any {
	out := make(map[string]any)
	if c == nil {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for host, entry := range c.entries {
		out[host] = map[string]any{
			"method":        entry.Method,
			"solved_at":     entry.SolvedAt.Format(time.RFC3339),
			"age_seconds":   int(time.Since(entry.SolvedAt).Seconds()),
			"cookie_fields": cookieValueCount(entry.Cookies),
		}
	}
	return out
}

// looksLikeChallenge reports whether a response body is the interstitial page
// rather than the JSON the API should have returned.
func looksLikeChallenge(body string) bool {
	if strings.TrimSpace(body) == "" {
		return false
	}
	lower := strings.ToLower(body)
	if strings.Contains(lower, "acw_sc__v2") {
		return true
	}
	// A JSL body always carries the obfuscated script plus the arg1 seed, and
	// never looks like JSON.
	return strings.Contains(lower, "arg1") && strings.Contains(lower, "<script")
}

// solveChallenge extracts the cookie set the interstitial would install.
func solveChallenge(htmlBody, userAgent string, timeout time.Duration) (string, string, error) {
	if strings.TrimSpace(htmlBody) == "" {
		return "", "", fmt.Errorf("empty challenge body")
	}
	scripts := extractScripts(htmlBody)
	var lastErr error
	for index, script := range scripts {
		cookies, errRun := runScriptCollectCookies(script, userAgent, timeout)
		if len(cookies) > 0 {
			joined := strings.Join(cookies, "; ")
			return joined, fmt.Sprintf("js#%d", index+1), nil
		}
		if errRun != nil {
			lastErr = errRun
		}
	}
	if match := acwLiteralPattern.FindStringSubmatch(htmlBody); match != nil {
		return "acw_sc__v2=" + match[1], "literal", nil
	}
	if cookie, ok := canonicalAcwScV2(scripts, htmlBody); ok {
		return cookie, "canonical", nil
	}
	if lastErr != nil {
		return "", "", fmt.Errorf("无法从挑战页取得 Cookie: %w", lastErr)
	}
	return "", "", fmt.Errorf("挑战页中没有可解析的脚本")
}

func extractScripts(htmlBody string) []string {
	matches := scriptTagPattern.FindAllStringSubmatch(htmlBody, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
			out = append(out, match[1])
		}
	}
	return out
}

// runScriptCollectCookies executes one script block with a stubbed document and
// collects every document.cookie assignment it performs.
func runScriptCollectCookies(script, userAgent string, timeout time.Duration) ([]string, error) {
	return runScriptCollectCookiesLimit(script, userAgent, timeout, challengeStackLimit)
}

// challengeStackLimit bounds the interpreter call depth. The JSL payload embeds
// an anti-debugging routine that deliberately recurses without a base case and
// relies on the engine throwing a stack overflow, which the payload's own
// try/catch swallows. goja grows goroutine stacks dynamically, so without a
// hard call-depth limit that routine spins instead of unwinding; the value is
// far above what the challenge itself needs.
const challengeStackLimit = 2048

func runScriptCollectCookiesLimit(script, userAgent string, timeout time.Duration, stackLimit int) ([]string, error) {
	if timeout <= 0 {
		timeout = challengeEvalLimit
	}
	vm := goja.New()
	if stackLimit > 0 {
		vm.SetMaxCallStackSize(stackLimit)
	}
	var mu sync.Mutex
	jar := make([]string, 0, 2)

	setCookie := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		value := strings.TrimSpace(call.Arguments[0].String())
		if value == "" {
			return goja.Undefined()
		}
		mu.Lock()
		jar = append(jar, value)
		mu.Unlock()
		return goja.Undefined()
	}
	if errSet := vm.Set("__anyrouterSetCookie", setCookie); errSet != nil {
		return nil, errSet
	}

	done := make(chan error, 1)
	go func() {
		_, errRun := vm.RunString(challengeWrapper(stripAntiDebugTrap(script), userAgent))
		done <- errRun
	}()

	var errRun error
	select {
	case errRun = <-done:
	case <-time.After(timeout):
		vm.Interrupt("challenge evaluation timed out")
		<-done
		errRun = fmt.Errorf("挑战脚本执行超过 %s", timeout)
	}

	mu.Lock()
	collected := normaliseCookies(jar)
	mu.Unlock()
	if len(collected) == 0 && errRun != nil {
		return nil, errRun
	}
	return collected, errRun
}

// normaliseCookies trims attributes and de-duplicates while preserving order.
func normaliseCookies(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		pair := strings.TrimSpace(strings.SplitN(item, ";", 2)[0])
		if pair == "" || !strings.Contains(pair, "=") {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(strings.SplitN(pair, "=", 2)[0]))
		if _, exists := seen[key]; exists {
			out = replaceCookie(out, key, pair)
			continue
		}
		seen[key] = struct{}{}
		out = append(out, pair)
	}
	return out
}

func replaceCookie(list []string, key, pair string) []string {
	for i, item := range list {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(item, "=", 2)[0]), key) {
			list[i] = pair
		}
	}
	return list
}

// antiDebugTrapPattern matches the devtools trap the JSL payload plants right
// before it computes the cookie: a ternary whose else branch calls a helper that
// recurses without a base case, e.g. "?D('0'):b();".
var antiDebugTrapPattern = regexp.MustCompile(`\?[A-Za-z_$][A-Za-z0-9_$]*\((?:'0'|"0")\):[A-Za-z_$][A-Za-z0-9_$]*\(\)\s*;?`)

// stripAntiDebugTrap neutralises that trap. In a browser the runaway recursion
// ends in a catchable RangeError, but goja reports it as an uncatchable
// *StackOverflowError that aborts the whole script, so the cookie would never be
// computed. Only the trap ternary is rewritten; the string table rotation and
// the cookie computation are left untouched.
func stripAntiDebugTrap(script string) string {
	if !strings.Contains(script, "acw_sc__v2") || !strings.Contains(script, "RegExp") {
		return script
	}
	location := antiDebugTrapPattern.FindStringIndex(script)
	if location == nil {
		return script
	}
	return script[:location[0]] + "?0:0;" + script[location[1]:]
}

// challengeWrapper runs the challenge script with the browser globals it
// touches. Using a JS defined accessor keeps the Go side free of goja's
// property-descriptor API and mirrors the original eval() wrapper.
func challengeWrapper(script, userAgent string) string {
	var out strings.Builder
	out.WriteString("(function(){\n")
	out.WriteString("var __jar = [];\n")
	out.WriteString("function __norm(v){ return String(v).split(';')[0].trim(); }\n")
	out.WriteString("var __doc = {};\n")
	out.WriteString("Object.defineProperty(__doc, 'cookie', {\n")
	out.WriteString("  get: function(){ return __jar.join('; '); },\n")
	out.WriteString("  set: function(v){ var p = __norm(v); if (p) { __jar.push(p); try { __anyrouterSetCookie(p); } catch (e) {} } },\n")
	out.WriteString("  configurable: true\n")
	out.WriteString("});\n")
	out.WriteString("var __loc = { href: 'about:blank', reload: function(){}, replace: function(){}, assign: function(){}, toString: function(){ return 'about:blank'; } };\n")
	out.WriteString("__doc.location = __loc;\n")
	out.WriteString("__doc.write = function(){}; __doc.writeln = function(){};\n")
	out.WriteString("__doc.readyState = 'complete';\n")
	out.WriteString("__doc.createElement = function(){ return {}; };\n")
	out.WriteString("__doc.getElementsByTagName = function(){ return []; };\n")
	out.WriteString("var __console = { log: function(){}, info: function(){}, warn: function(){}, error: function(){}, debug: function(){}, trace: function(){} };\n")
	out.WriteString("var __nav = { userAgent: " + strconv.Quote(userAgent) + ", language: 'zh-CN', languages: ['zh-CN','zh'], platform: 'Win32' };\n")
	out.WriteString("var __noop = function(){ return 0; };\n")
	out.WriteString("var __win = { document: __doc, location: __loc, navigator: __nav, console: __console,\n")
	out.WriteString("  setTimeout: __noop, setInterval: __noop, clearTimeout: __noop, clearInterval: __noop,\n")
	out.WriteString("  alert: __noop, atob: function(s){ return s; }, btoa: function(s){ return s; } };\n")
	out.WriteString("__win.window = __win; __win.self = __win; __win.top = __win; __win.parent = __win; __win.globalThis = __win;\n")
	out.WriteString("var window = __win, self = __win, top = __win, globalThis = __win;\n")
	out.WriteString("var document = __doc, location = __loc, navigator = __nav, console = __console;\n")
	out.WriteString("var setTimeout = __noop, setInterval = __noop, clearTimeout = __noop, clearInterval = __noop, alert = __noop;\n")
	out.WriteString("var atob = __win.atob, btoa = __win.btoa;\n")
	out.WriteString("try {\n")
	out.WriteString(script)
	out.WriteString("\n} catch (__e) { }\n")
	out.WriteString("})();\n")
	return out.String()
}

// canonicalAcwScV2 implements the published JSL algorithm as a fallback for the
// case where executing the script is impossible. The XOR key is taken from the
// page when it appears verbatim, otherwise the widely used standard key is
// assumed.
func canonicalAcwScV2(scripts []string, htmlBody string) (string, bool) {
	haystack := htmlBody
	if len(scripts) > 0 {
		haystack = strings.Join(scripts, "\n")
	}
	match := arg1Pattern.FindStringSubmatch(haystack)
	if match == nil {
		return "", false
	}
	arg1 := strings.ToUpper(match[1])
	key := ""
	for _, candidate := range hex40Pattern.FindAllString(haystack, -1) {
		if !strings.EqualFold(candidate, arg1) {
			key = strings.ToLower(candidate)
			break
		}
	}
	if key == "" {
		key = jslStandardKey
	}
	unboxed := unsbox(arg1)
	if len(unboxed) < 2 {
		return "", false
	}
	return "acw_sc__v2=" + hexXor(unboxed, key), true
}

// unsbox reorders arg1 through the fixed permutation table.
func unsbox(arg1 string) string {
	out := make([]byte, len(jslUnboxOrder))
	for index := 0; index < len(arg1) && index < len(jslUnboxOrder); index++ {
		for slot, position := range jslUnboxOrder {
			if position == index+1 {
				out[slot] = arg1[index]
			}
		}
	}
	return string(out)
}

// hexXor XORs two hex strings two digits at a time and returns hex.
func hexXor(left, right string) string {
	var out strings.Builder
	for index := 0; index+1 < len(left) && index+1 < len(right); index += 2 {
		a, errA := strconv.ParseUint(left[index:index+2], 16, 16)
		b, errB := strconv.ParseUint(right[index:index+2], 16, 16)
		if errA != nil || errB != nil {
			break
		}
		out.WriteString(fmt.Sprintf("%02x", byte(a)^byte(b)))
	}
	return out.String()
}
