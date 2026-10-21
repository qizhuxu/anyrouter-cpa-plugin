package main

import (
	"strings"
	"sync"
	"time"
)

// backgroundState tracks the scheduler so the panel can report what is running.
var backgroundState struct {
	mu              sync.Mutex
	started         bool
	stopCh          chan struct{}
	lastTick        time.Time
	lastCheckinDate string
	lastError       string
}

// startBackground starts the single scheduler goroutine. Repeated calls (every
// plugin.register / plugin.reconfigure) are idempotent.
func startBackground() {
	backgroundState.mu.Lock()
	if backgroundState.started {
		backgroundState.mu.Unlock()
		return
	}
	stopCh := make(chan struct{}, 1)
	backgroundState.stopCh = stopCh
	backgroundState.started = true
	backgroundState.mu.Unlock()

	safeGo("scheduler", func() { schedulerLoop(stopCh) })
	hostLog("info", "后台调度已启动", nil)
}

// stopBackground stops the scheduler. The stop channel is buffered so a send is
// never dropped while the loop is busy; otherwise a restart would leave two
// loops running.
func stopBackground() {
	backgroundState.mu.Lock()
	started := backgroundState.started
	stopCh := backgroundState.stopCh
	backgroundState.started = false
	backgroundState.stopCh = nil
	backgroundState.mu.Unlock()
	if !started || stopCh == nil {
		return
	}
	select {
	case stopCh <- struct{}{}:
	default:
	}
	hostLog("info", "后台调度已停止", nil)
}

func schedulerLoop(stopCh chan struct{}) {
	// Give the host a moment to finish loading before the first sweep touches
	// the credential inventory.
	timer := time.NewTimer(5 * time.Second)
	select {
	case <-stopCh:
		timer.Stop()
		return
	case <-timer.C:
	}
	guardLoop("scheduler-first-tick", schedulerTick)

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			guardLoop("scheduler-tick", schedulerTick)
		}
	}
}

// schedulerTick runs the due automatic tasks. Everything is best-effort: a
// failure is logged and the next tick tries again.
func schedulerTick() {
	if !hostAvailable() {
		return
	}
	cfg := effectiveConfig()
	now := time.Now().In(cfg.Location)

	backgroundState.mu.Lock()
	backgroundState.lastTick = now
	backgroundState.mu.Unlock()

	if cfg.AutoBalance {
		accountsState.mu.RLock()
		lastBalance := accountsState.lastBalanceAt
		accountsState.mu.RUnlock()
		if lastBalance.IsZero() || now.Sub(lastBalance) >= cfg.BalanceInterval {
			selection := selectAccounts("", true)
			if len(selection) > 0 {
				results := refreshBalances(selection)
				for _, item := range results {
					level := "info"
					message := item.Message
					if item.OK {
						message = "自动刷新余额成功"
					} else {
						level = "warn"
					}
					recordEvent(level, "balance", item.Account, message)
				}
				hostLogf("info", "自动余额刷新：%s", summariseBalances(results))
			}
		}
	}

	if !cfg.AutoCheckin {
		return
	}
	if now.Format("15:04") < cfg.CheckinTime {
		return
	}
	today := now.Format("2006-01-02")

	backgroundState.mu.Lock()
	ranToday := backgroundState.lastCheckinDate == today
	backgroundState.mu.Unlock()
	if ranToday {
		return
	}

	selection := dueAccounts(cfg, now)
	if len(selection) == 0 {
		// Every enabled account already has today's result; remember that so the
		// remaining ticks stay cheap.
		backgroundState.mu.Lock()
		backgroundState.lastCheckinDate = today
		backgroundState.mu.Unlock()
		return
	}
	results := checkinAccounts(selection)
	for _, item := range results {
		level := "info"
		if !item.OK {
			level = "warn"
		}
		recordEvent(level, "checkin", item.Account, "自动签到："+item.Message)
	}
	summary := summariseCheckin(results)
	hostLogf("info", "自动签到：%s", summary)

	backgroundState.mu.Lock()
	backgroundState.lastCheckinDate = today
	if len(dueAccounts(cfg, now)) == 0 {
		backgroundState.lastError = ""
	} else {
		backgroundState.lastError = "部分账号今日签到未成功，将在下次运行时重试"
	}
	backgroundState.mu.Unlock()
}

// dueAccounts selects the enabled accounts that still need today's check-in.
// A failed attempt is retried at most every 30 minutes to avoid hammering the
// upstream when a cookie is broken.
func dueAccounts(cfg Config, now time.Time) []*Account {
	today := now.Format("2006-01-02")
	out := make([]*Account, 0, 4)
	for _, account := range selectAccounts("", true) {
		last := strings.TrimSpace(account.File.LastCheckinAt)
		if last != "" {
			if parsed, errParse := time.Parse(time.RFC3339, last); errParse == nil {
				local := parsed.In(cfg.Location)
				if local.Format("2006-01-02") == today {
					// Already handled today unless the attempt failed long enough ago.
					if account.File.LastCheckinStatus != checkinFailed {
						continue
					}
					if now.Sub(local) < 30*time.Minute {
						continue
					}
				}
			}
		}
		out = append(out, account)
	}
	return out
}

// nextCheckinTime reports the next scheduled automatic check-in for the panel.
func nextCheckinTime(cfg Config, now time.Time) string {
	if !cfg.AutoCheckin {
		return ""
	}
	parsed, errParse := time.Parse("15:04", cfg.CheckinTime)
	if errParse != nil {
		return ""
	}
	candidate := time.Date(now.Year(), now.Month(), now.Day(), parsed.Hour(), parsed.Minute(), 0, 0, now.Location())
	if !candidate.After(now) {
		candidate = candidate.Add(24 * time.Hour)
	}
	return candidate.Format(time.RFC3339)
}
