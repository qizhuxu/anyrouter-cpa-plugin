package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// safeGo starts a background goroutine that contains panics. A panic escaping
// into the host fuses the plugin, after which CPA silently skips every
// capability it declared.
func safeGo(loopName string, fn func()) {
	go func() {
		defer recoverPanic(loopName)
		fn()
	}()
}

// guardLoop contains a panic for a single iteration of a long running loop.
func guardLoop(loopName string, fn func()) {
	defer recoverPanic(loopName)
	fn()
}

func recoverPanic(where string) {
	if recovered := recover(); recovered != nil {
		fmt.Fprintf(os.Stderr, "anyrouter: recovered panic in %s: %v\n%s\n", where, recovered, debug.Stack())
	}
}

// guardRPC wraps one inbound RPC. It converts a panic into an error envelope
// instead of letting it reach the host.
func guardRPC(method string, fn func() ([]byte, error)) (out []byte, errOut error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(os.Stderr, "anyrouter: recovered panic in %s: %v\n%s\n", method, recovered, debug.Stack())
			out = errorEnvelope("plugin_panic", fmt.Sprintf("%s panicked: %v", method, recovered), 500)
			errOut = nil
		}
	}()
	return fn()
}

// maskSecret keeps a short prefix and suffix of a credential so the panel can
// identify it without ever showing the full value.
func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 10 {
		return strings.Repeat("*", len(value))
	}
	return value[:4] + strings.Repeat("*", 8) + value[len(value)-4:]
}

// cookieValueCount reports how many "k=v" pairs a Cookie header carries. Only
// the count is ever logged or returned; values stay inside the plugin.
func cookieValueCount(cookie string) int {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return 0
	}
	count := 0
	for _, part := range strings.Split(cookie, ";") {
		if strings.TrimSpace(part) != "" {
			count++
		}
	}
	return count
}

// truncate limits free text that goes back to the panel or the host log.
func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
