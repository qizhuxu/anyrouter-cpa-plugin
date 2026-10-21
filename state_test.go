package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withStateFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	previous := configState.cfg
	applyConfig(Config{StateFile: path, BaseURL: defaultBaseURL, CheckinTime: "08:00", AutoCheckin: true, AutoBalance: true,
		BalanceInterval: 30 * time.Minute, Timezone: "Local", Location: time.Local, QuotaPerUnit: defaultQuotaPerUnit})
	pluginStateStore.mu.Lock()
	pluginStateStore.value = pluginState{}
	pluginStateStore.loaded = false
	pluginStateStore.path = ""
	pluginStateStore.loadErr = ""
	pluginStateStore.mu.Unlock()
	t.Cleanup(func() {
		applyConfig(previous)
		pluginStateStore.mu.Lock()
		pluginStateStore.value = pluginState{}
		pluginStateStore.loaded = false
		pluginStateStore.mu.Unlock()
	})
	return path
}

// TestPanelScheduleOverridesConfig pins the resolution: the panel override wins
// over config.yaml, and clearing it falls back to the configuration value.
func TestPanelScheduleOverridesConfig(t *testing.T) {
	withStateFile(t)

	if cfg := effectiveConfig(); !cfg.AutoCheckin || cfg.CheckinTime != "08:00" {
		t.Fatalf("expected the configured schedule, got %+v", cfg)
	}
	if source := scheduleSource(currentState()); source != "config" {
		t.Fatalf("source = %q, want config", source)
	}

	off := false
	later := "21:30"
	minutes := 45
	if errSave := savePluginState(currentConfig(), pluginState{
		AutoCheckin:    &off,
		CheckinTime:    &later,
		BalanceMinutes: &minutes,
	}); errSave != nil {
		t.Fatalf("savePluginState: %v", errSave)
	}

	cfg := effectiveConfig()
	if cfg.AutoCheckin {
		t.Fatal("panel override must disable auto check-in")
	}
	if cfg.CheckinTime != "21:30" {
		t.Fatalf("CheckinTime = %q, want 21:30", cfg.CheckinTime)
	}
	if cfg.BalanceInterval != 45*time.Minute {
		t.Fatalf("BalanceInterval = %v, want 45m", cfg.BalanceInterval)
	}
	if source := scheduleSource(currentState()); source != "panel" {
		t.Fatalf("source = %q, want panel", source)
	}
	// The remaining configuration stays untouched by the panel changes.
	if cfg.BaseURL != defaultBaseURL {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}

	// A reset returns to "follow the configuration".
	if errSave := savePluginState(currentConfig(), pluginState{}); errSave != nil {
		t.Fatalf("reset: %v", errSave)
	}
	if cfg := effectiveConfig(); !cfg.AutoCheckin || cfg.CheckinTime != "08:00" {
		t.Fatalf("reset must restore the configured schedule, got %+v", cfg)
	}
}

func TestPluginStatePersistsAndReloads(t *testing.T) {
	path := withStateFile(t)
	on := true
	timeValue := "07:15"
	if errSave := savePluginState(currentConfig(), pluginState{AutoCheckin: &on, CheckinTime: &timeValue}); errSave != nil {
		t.Fatalf("save: %v", errSave)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("state file not written: %v", errRead)
	}
	var decoded pluginState
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("state file is not valid JSON: %v", errUnmarshal)
	}
	if decoded.CheckinTime == nil || *decoded.CheckinTime != "07:15" || decoded.AutoCheckin == nil || !*decoded.AutoCheckin {
		t.Fatalf("unexpected persisted state: %+v", decoded)
	}
	// No temp file is left behind (atomic write).
	if _, errStat := os.Stat(path + ".tmp"); errStat == nil {
		t.Fatal("temp file left behind after an atomic write")
	}
	// Reloading from disk yields the same overrides.
	pluginStateStore.mu.Lock()
	pluginStateStore.loaded = false
	pluginStateStore.mu.Unlock()
	reloaded := currentState()
	if reloaded.CheckinTime == nil || *reloaded.CheckinTime != "07:15" {
		t.Fatalf("reload lost the override: %+v", reloaded)
	}
}

func TestSavePluginStateValidates(t *testing.T) {
	withStateFile(t)
	bad := "25:99"
	if errSave := savePluginState(currentConfig(), pluginState{CheckinTime: &bad}); errSave == nil {
		t.Fatal("invalid clock must be rejected")
	}
	low := 1
	if errSave := savePluginState(currentConfig(), pluginState{BalanceMinutes: &low}); errSave == nil {
		t.Fatal("out-of-range interval must be rejected")
	}
	high := 5000
	if errSave := savePluginState(currentConfig(), pluginState{BalanceMinutes: &high}); errSave == nil {
		t.Fatal("out-of-range interval must be rejected")
	}
}

func TestEffectiveConfigIgnoresBrokenOverrides(t *testing.T) {
	withStateFile(t)
	// A hand-edited state file with an invalid clock must not break scheduling.
	broken := "not-a-clock"
	pluginStateStore.mu.Lock()
	pluginStateStore.value = pluginState{CheckinTime: &broken}
	pluginStateStore.loaded = true
	pluginStateStore.mu.Unlock()
	if cfg := effectiveConfig(); cfg.CheckinTime != "08:00" {
		t.Fatalf("broken override must be ignored, got %q", cfg.CheckinTime)
	}
}

func TestScheduleSnapshotShape(t *testing.T) {
	withStateFile(t)
	snapshot := scheduleSnapshot()
	for _, key := range []string{"auto_checkin", "checkin_time", "auto_refresh_balance",
		"balance_refresh_minutes", "timezone", "next_checkin_at", "source", "state_file"} {
		if _, ok := snapshot[key]; !ok {
			t.Fatalf("schedule snapshot misses %q: %#v", key, snapshot)
		}
	}
	if snapshot["source"] != "config" {
		t.Fatalf("source = %v, want config", snapshot["source"])
	}
	if snapshot["next_checkin_at"] == "" {
		t.Fatal("next_checkin_at must be set while auto check-in is enabled")
	}
}
