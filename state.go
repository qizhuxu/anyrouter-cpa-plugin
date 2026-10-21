package main

// Plugin-owned state for settings the panel manages at runtime.
//
// The host owns config.yaml and a plugin has no callback to rewrite it, so panel
// changes live in a small JSON file of its own. A nil field means "follow the
// plugin configuration"; a set field overrides it for the scheduler and for what
// the panel reports. The file is written atomically (temp + rename) and is safe
// for concurrent readers.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// pluginState holds the panel-managed overrides.
type pluginState struct {
	AutoCheckin    *bool   `json:"auto_checkin,omitempty"`
	CheckinTime    *string `json:"checkin_time,omitempty"`
	AutoBalance    *bool   `json:"auto_refresh_balance,omitempty"`
	BalanceMinutes *int    `json:"balance_refresh_minutes,omitempty"`
	UpdatedAt      string  `json:"updated_at,omitempty"`
}

// anyOverride reports whether the panel overrides anything.
func (s pluginState) anyOverride() bool {
	return s.AutoCheckin != nil || s.CheckinTime != nil || s.AutoBalance != nil || s.BalanceMinutes != nil
}

var pluginStateStore struct {
	mu      sync.RWMutex
	value   pluginState
	loaded  bool
	path    string
	loadErr string
}

// stateFilePath resolves where the override file lives: the configured
// state_file when set (in Docker point it at a mounted directory), otherwise
// <user config dir>/anyrouter/state.json, otherwise the working directory.
func stateFilePath(cfg Config) string {
	if configured := strings.TrimSpace(cfg.StateFile); configured != "" {
		return configured
	}
	if dir, errDir := os.UserConfigDir(); errDir == nil && dir != "" {
		return filepath.Join(dir, pluginID, "state.json")
	}
	if wd, errWd := os.Getwd(); errWd == nil && wd != "" {
		return filepath.Join(wd, pluginID+"-state.json")
	}
	return pluginID + "-state.json"
}

// loadPluginState reads the override file. A missing file is not an error; a
// malformed one is reported and ignored so a bad edit cannot break scheduling.
func loadPluginState(cfg Config) pluginState {
	path := stateFilePath(cfg)
	raw, errRead := os.ReadFile(path)
	pluginStateStore.mu.Lock()
	pluginStateStore.path = path
	pluginStateStore.loaded = true
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			pluginStateStore.loadErr = errRead.Error()
		} else {
			pluginStateStore.loadErr = ""
		}
		pluginStateStore.value = pluginState{}
		pluginStateStore.mu.Unlock()
		return pluginState{}
	}
	var parsed pluginState
	if errUnmarshal := json.Unmarshal(raw, &parsed); errUnmarshal != nil {
		pluginStateStore.loadErr = "state file is not valid JSON: " + errUnmarshal.Error()
		pluginStateStore.value = pluginState{}
		pluginStateStore.mu.Unlock()
		return pluginState{}
	}
	pluginStateStore.loadErr = ""
	pluginStateStore.value = parsed
	pluginStateStore.mu.Unlock()
	return parsed
}

// currentState returns the cached overrides, loading them once on first use.
func currentState() pluginState {
	pluginStateStore.mu.RLock()
	loaded := pluginStateStore.loaded
	value := pluginStateStore.value
	pluginStateStore.mu.RUnlock()
	if loaded {
		return value
	}
	return loadPluginState(currentConfig())
}

func stateFileInfo() (path, loadErr string) {
	pluginStateStore.mu.RLock()
	defer pluginStateStore.mu.RUnlock()
	return pluginStateStore.path, pluginStateStore.loadErr
}

// savePluginState validates and persists the overrides atomically.
func savePluginState(cfg Config, next pluginState) error {
	if next.CheckinTime != nil {
		value := strings.TrimSpace(*next.CheckinTime)
		if _, errClock := time.Parse("15:04", value); errClock != nil {
			return fmt.Errorf("checkin_time 必须为 HH:MM 格式，当前为 %q", value)
		}
		next.CheckinTime = &value
	}
	if next.BalanceMinutes != nil {
		if *next.BalanceMinutes < 5 || *next.BalanceMinutes > 1440 {
			return fmt.Errorf("balance_refresh_minutes 必须在 5-1440 之间，当前为 %d", *next.BalanceMinutes)
		}
	}
	next.UpdatedAt = time.Now().Format(time.RFC3339)

	path := stateFilePath(cfg)
	raw, errMarshal := json.MarshalIndent(next, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
			return fmt.Errorf("创建状态目录失败: %w", errMkdir)
		}
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
		return fmt.Errorf("写入状态文件失败: %w", errWrite)
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交状态文件失败: %w", errRename)
	}
	pluginStateStore.mu.Lock()
	pluginStateStore.value = next
	pluginStateStore.loaded = true
	pluginStateStore.path = path
	pluginStateStore.loadErr = ""
	pluginStateStore.mu.Unlock()
	return nil
}

// effectiveConfig returns the plugin configuration with the panel overrides
// applied. The scheduler and every schedule-related read use this so a panel
// change takes effect on the next tick without a restart.
func effectiveConfig() Config {
	cfg := currentConfig()
	state := currentState()
	if state.AutoCheckin != nil {
		cfg.AutoCheckin = *state.AutoCheckin
	}
	if state.CheckinTime != nil {
		if _, errClock := time.Parse("15:04", *state.CheckinTime); errClock == nil {
			cfg.CheckinTime = *state.CheckinTime
		}
	}
	if state.AutoBalance != nil {
		cfg.AutoBalance = *state.AutoBalance
	}
	if state.BalanceMinutes != nil && *state.BalanceMinutes >= 5 && *state.BalanceMinutes <= 1440 {
		cfg.BalanceInterval = time.Duration(*state.BalanceMinutes) * time.Minute
	}
	return cfg
}

// scheduleSource tells the panel where the effective schedule comes from.
func scheduleSource(state pluginState) string {
	if state.anyOverride() {
		return "panel"
	}
	return "config"
}

// scheduleSnapshot is the panel-facing view of the automatic tasks.
func scheduleSnapshot() map[string]any {
	cfg := effectiveConfig()
	state := currentState()
	path, loadErr := stateFileInfo()
	now := time.Now().In(cfg.Location)
	return map[string]any{
		"auto_checkin":            cfg.AutoCheckin,
		"checkin_time":            cfg.CheckinTime,
		"auto_refresh_balance":    cfg.AutoBalance,
		"balance_refresh_minutes": int(cfg.BalanceInterval.Minutes()),
		"timezone":                cfg.Timezone,
		"now":                     now.Format(time.RFC3339),
		"next_checkin_at":         nextCheckinTime(cfg, now),
		"source":                  scheduleSource(state),
		"state_file":              path,
		"state_file_error":        loadErr,
		"updated_at":              state.UpdatedAt,
	}
}
