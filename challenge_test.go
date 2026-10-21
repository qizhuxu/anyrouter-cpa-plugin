package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const fixturePath = "testdata/challenge_jsl.html"

var acwValuePattern = regexp.MustCompile(`^acw_sc__v2=([0-9A-Fa-f]{40})$`)

// TestSolveChallengeFixture runs the real AnyRouter interstitial captured from
// https://anyrouter.top/ through the goja interpreter.
func TestSolveChallengeFixture(t *testing.T) {
	body, errRead := os.ReadFile(filepath.Clean(fixturePath))
	if errRead != nil {
		t.Fatalf("read fixture: %v", errRead)
	}
	html := string(body)
	if !looksLikeChallenge(html) {
		t.Fatalf("fixture is not recognised as a challenge page")
	}

	cookies, method, errSolve := solveChallenge(html, defaultUserAgent, 3*time.Second)
	if errSolve != nil {
		t.Fatalf("solveChallenge: %v", errSolve)
	}
	t.Logf("solved via %s: %s", method, cookies)
	if !strings.Contains(cookies, "acw_sc__v2=") {
		t.Fatalf("cookie set %q does not contain acw_sc__v2", cookies)
	}
	for _, pair := range strings.Split(cookies, ";") {
		pair = strings.TrimSpace(pair)
		if strings.HasPrefix(pair, "acw_sc__v2=") && !acwValuePattern.MatchString(pair) {
			t.Fatalf("acw_sc__v2 value is not 40 hex chars: %q", pair)
		}
	}
}

// TestSolveChallengePrefersJS documents the strategy order: executing the page
// script wins over the algorithmic fallback.
func TestSolveChallengePrefersJS(t *testing.T) {
	body, errRead := os.ReadFile(filepath.Clean(fixturePath))
	if errRead != nil {
		t.Fatalf("read fixture: %v", errRead)
	}
	_, method, errSolve := solveChallenge(string(body), defaultUserAgent, 3*time.Second)
	if errSolve != nil {
		t.Fatalf("solveChallenge: %v", errSolve)
	}
	if !strings.HasPrefix(method, "js") {
		t.Fatalf("method = %q, want the js evaluation path", method)
	}
}

// TestCanonicalFallbackConsistency checks that the algorithmic fallback agrees
// with the interpreter on this page. The AnyRouter interstitial obfuscates its
// XOR key inside a string table, so a mismatch here means the documented
// standard key is not the one this page uses and the fallback must be treated
// as best-effort only.
func TestCanonicalFallbackConsistency(t *testing.T) {
	body, errRead := os.ReadFile(filepath.Clean(fixturePath))
	if errRead != nil {
		t.Fatalf("read fixture: %v", errRead)
	}
	html := string(body)

	jsCookie, _, errSolve := solveChallenge(html, defaultUserAgent, 3*time.Second)
	if errSolve != nil {
		t.Fatalf("solveChallenge: %v", errSolve)
	}
	jsValue := ""
	for _, pair := range strings.Split(jsCookie, ";") {
		pair = strings.TrimSpace(pair)
		if strings.HasPrefix(pair, "acw_sc__v2=") {
			jsValue = strings.TrimPrefix(pair, "acw_sc__v2=")
		}
	}
	canonicalCookie, okCanonical := canonicalAcwScV2(extractScripts(html), html)
	if !okCanonical {
		t.Fatalf("canonical fallback did not produce a cookie")
	}
	canonicalValue := strings.TrimPrefix(canonicalCookie, "acw_sc__v2=")
	if !strings.EqualFold(jsValue, canonicalValue) {
		t.Fatalf("fallback mismatch: js=%s canonical=%s (the page uses a site specific XOR key)", jsValue, canonicalValue)
	}
}

// TestUnsboxPermutation pins the JSL unboxing table against a hand computed
// vector: feeding it the identity sequence must return the table order.
func TestUnsboxPermutation(t *testing.T) {
	identity := make([]byte, len(jslUnboxOrder))
	for index := range identity {
		identity[index] = byte('a' + index%26)
	}
	got := unsbox(string(identity))
	if len(got) != len(jslUnboxOrder) {
		t.Fatalf("unsbox length = %d, want %d", len(got), len(jslUnboxOrder))
	}
	for slot, position := range jslUnboxOrder {
		if position < 1 || position > len(identity) {
			t.Fatalf("table entry %d out of range: %d", slot, position)
		}
		want := identity[position-1]
		if got[slot] != want {
			t.Fatalf("unsbox[%d] = %q, want %q (position %d)", slot, got[slot], want, position)
		}
	}
}

func TestHexXorPadding(t *testing.T) {
	if got := hexXor("ff00", "0102"); got != "fe02" {
		t.Fatalf("hexXor = %q, want fe02", got)
	}
	// A single digit XOR result must be zero padded, never shortened.
	if got := hexXor("0f", "00"); got != "0f" {
		t.Fatalf("hexXor = %q, want 0f", got)
	}
}

func TestChallengeCacheTTL(t *testing.T) {
	cache := newChallengeCache()
	cache.put("anyrouter.top", "acw_sc__v2=abc", "js#1")
	if _, ok := cache.get("anyrouter.top", time.Minute); !ok {
		t.Fatal("fresh entry should be returned")
	}
	cache.mu.Lock()
	entry := cache.entries["anyrouter.top"]
	entry.SolvedAt = time.Now().Add(-2 * time.Hour)
	cache.entries["anyrouter.top"] = entry
	cache.mu.Unlock()
	if _, ok := cache.get("anyrouter.top", time.Minute); ok {
		t.Fatal("expired entry must not be returned")
	}
	cache.invalidate("anyrouter.top")
	if _, ok := cache.get("anyrouter.top", time.Minute); ok {
		t.Fatal("invalidated entry must not be returned")
	}
}

func TestNormaliseCookiesReplacesDuplicates(t *testing.T) {
	got := normaliseCookies([]string{"acw_sc__v2=aaa; path=/", "acw_sc__v2=bbb", "other=1"})
	if len(got) != 2 {
		t.Fatalf("got %v, want two cookie pairs", got)
	}
	if got[0] != "acw_sc__v2=bbb" || got[1] != "other=1" {
		t.Fatalf("got %v, want the last acw value kept", got)
	}
}
