package main

import (
	"sync"
	"time"
)

// eventLog is a bounded in-memory ring of plugin events. It gives the panel and
// the operator a check-in / balance history without writing any credential
// material to disk.
type eventEntry struct {
	At      string `json:"at"`
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Account string `json:"account,omitempty"`
	Message string `json:"message"`
}

var events = struct {
	mu      sync.Mutex
	entries []eventEntry
	limit   int
}{limit: 200}

// recordEvent appends one event, dropping the oldest beyond the limit.
func recordEvent(level, kind, account, message string) {
	events.mu.Lock()
	defer events.mu.Unlock()
	events.entries = append(events.entries, eventEntry{
		At:      time.Now().Format(time.RFC3339),
		Level:   level,
		Kind:    kind,
		Account: account,
		Message: truncate(message, 400),
	})
	if len(events.entries) > events.limit {
		events.entries = append([]eventEntry(nil), events.entries[len(events.entries)-events.limit:]...)
	}
}

// recentEvents returns the newest entries first.
func recentEvents(limit int) []eventEntry {
	events.mu.Lock()
	defer events.mu.Unlock()
	if limit <= 0 || limit > len(events.entries) {
		limit = len(events.entries)
	}
	out := make([]eventEntry, 0, limit)
	for index := len(events.entries) - 1; index >= 0 && len(out) < limit; index-- {
		out = append(out, events.entries[index])
	}
	return out
}
