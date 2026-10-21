package main

import (
	"fmt"
	"strings"
	"time"
)

// checkinResult is one account's outcome inside a sweep.
type checkinResult struct {
	Account string `json:"account"`
	Remark  string `json:"remark"`
	Status  string `json:"status"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	At      string `json:"at"`
}

// balanceResult is one account's balance outcome inside a sweep.
type balanceResult struct {
	Account   string  `json:"account"`
	Remark    string  `json:"remark"`
	OK        bool    `json:"ok"`
	Message   string  `json:"message"`
	Quota     float64 `json:"quota"`
	UsedQuota float64 `json:"used_quota"`
	Total     float64 `json:"total"`
	At        string  `json:"at"`
}

// checkinAccounts signs in the given accounts. An empty selection means "all
// enabled accounts". The busy flag keeps overlapping sweeps apart.
func checkinAccounts(selection []*Account) []checkinResult {
	cfg := currentConfig()
	client := newClient()
	results := make([]checkinResult, 0, len(selection))

	for _, account := range selection {
		if account == nil {
			continue
		}
		runtime := runtimeFor(account.Name)
		accountsState.mu.Lock()
		if runtime.Busy {
			accountsState.mu.Unlock()
			results = append(results, checkinResult{
				Account: account.Name,
				Remark:  account.displayName(),
				Status:  checkinUnknown,
				Message: "该账号正在执行其他任务，已跳过",
			})
			continue
		}
		runtime.Busy = true
		accountsState.mu.Unlock()

		outcome := client.checkin(account)

		withRuntime(account.Name, func(entry *accountRuntime) {
			entry.Busy = false
			entry.LastRunAt = outcome.At
			if outcome.Status != checkinSuccess && outcome.Status != checkinAlready {
				entry.LastError = outcome.Message
			} else {
				entry.LastError = ""
			}
		})

		// Persist the outcome so the panel still shows it after a restart. The
		// auth file is the only durable store available to a plugin.
		if errSave := persistCheckin(account, outcome); errSave != nil {
			hostLogf("warn", "保存签到结果失败 account=%s: %v", account.Name, errSave)
		}

		results = append(results, checkinResult{
			Account: account.Name,
			Remark:  account.displayName(),
			Status:  outcome.Status,
			OK:      outcome.OK,
			Message: outcome.Message,
			At:      outcome.At.Format(time.RFC3339),
		})
		if cfg.Debug {
			hostLogf("debug", "签到 account=%s status=%s message=%s", account.Name, outcome.Status, outcome.Message)
		}
	}

	summary := summariseCheckin(results)
	accountsState.mu.Lock()
	accountsState.lastCheckinAt = time.Now()
	accountsState.lastCheckin = summary
	accountsState.mu.Unlock()
	return results
}

func persistCheckin(account *Account, outcome checkinOutcome) error {
	file := account.File
	file.LastCheckinAt = outcome.At.Format(time.RFC3339)
	file.LastCheckinStatus = outcome.Status
	file.LastCheckinMessage = truncate(outcome.Message, 300)
	if errSave := saveAccountFile(account.Name, file); errSave != nil {
		return errSave
	}
	account.File = file
	return nil
}

func summariseCheckin(results []checkinResult) string {
	if len(results) == 0 {
		return "没有可签到的账号"
	}
	success, already, failed := 0, 0, 0
	for _, item := range results {
		switch item.Status {
		case checkinSuccess:
			success++
		case checkinAlready:
			already++
		default:
			failed++
		}
	}
	return fmt.Sprintf("签到 %d 个账号：成功 %d，已签到 %d，失败 %d", len(results), success, already, failed)
}

// refreshBalances reads the balance of the given accounts. An empty selection
// means "all enabled accounts".
func refreshBalances(selection []*Account) []balanceResult {
	client := newClient()
	results := make([]balanceResult, 0, len(selection))

	for _, account := range selection {
		if account == nil {
			continue
		}
		runtime := runtimeFor(account.Name)
		accountsState.mu.Lock()
		if runtime.Busy {
			accountsState.mu.Unlock()
			results = append(results, balanceResult{
				Account: account.Name,
				Remark:  account.displayName(),
				Message: "该账号正在执行其他任务，已跳过",
			})
			continue
		}
		runtime.Busy = true
		accountsState.mu.Unlock()

		info, errBalance := client.balance(account)

		withRuntime(account.Name, func(entry *accountRuntime) {
			entry.Busy = false
			entry.LastRunAt = time.Now()
			if errBalance != nil {
				entry.LastError = errBalance.Error()
				return
			}
			entry.Balance = info
			entry.BalanceAt = info.FetchedAt
			entry.LastError = ""
		})
		if errBalance == nil {
			// Persist the reading: the ledger must not show "未读取" after a reload.
			if errSave := persistBalance(account, info); errSave != nil {
				hostLogf("warn", "保存余额快照失败 account=%s: %v", account.Name, errSave)
			}
		}

		result := balanceResult{
			Account: account.Name,
			Remark:  account.displayName(),
			At:      time.Now().Format(time.RFC3339),
		}
		if errBalance != nil {
			result.Message = errBalance.Error()
			results = append(results, result)
			continue
		}
		result.OK = true
		result.Quota = info.Quota
		result.UsedQuota = info.UsedQuota
		result.Total = info.Total()
		results = append(results, result)
	}

	summary := summariseBalances(results)
	accountsState.mu.Lock()
	accountsState.lastBalanceAt = time.Now()
	accountsState.lastBalance = summary
	accountsState.mu.Unlock()
	return results
}

// persistBalance stores the latest reading in the account file so the ledger
// still has a figure after the plugin (or the page) restarts.
func persistBalance(account *Account, info *balanceInfo) error {
	snapshot := newBalanceSnapshot(info)
	if snapshot == nil {
		return nil
	}
	file := account.File
	file.Balance = snapshot
	if errSave := saveAccountFile(account.Name, file); errSave != nil {
		return errSave
	}
	account.File = file
	return nil
}

func summariseBalances(results []balanceResult) string {
	if len(results) == 0 {
		return "没有可查询的账号"
	}
	ok, failed := 0, 0
	for _, item := range results {
		if item.OK {
			ok++
		} else {
			failed++
		}
	}
	return fmt.Sprintf("余额刷新 %d 个账号：成功 %d，失败 %d", len(results), ok, failed)
}

// selectAccounts resolves a request into concrete accounts. An empty name means
// every enabled account; onlyEnabled filters out disabled ones.
func selectAccounts(name string, onlyEnabled bool) []*Account {
	all := accountsSnapshot()
	name = strings.TrimSpace(name)
	if name != "" {
		for _, account := range all {
			if account.Name == name {
				return []*Account{account}
			}
		}
		return nil
	}
	out := make([]*Account, 0, len(all))
	for _, account := range all {
		if onlyEnabled && !account.enabled() {
			continue
		}
		out = append(out, account)
	}
	return out
}

// accountView is the panel representation of an account. The cookie is never
// included here; a dedicated single-account route returns it on demand.
type accountView struct {
	Name               string       `json:"name"`
	AuthIndex          string       `json:"auth_index"`
	ID                 string       `json:"id"`
	Remark             string       `json:"remark"`
	Disabled           bool         `json:"disabled"`
	CookieHint         string       `json:"cookie_hint"`
	CookieFields       int          `json:"cookie_fields"`
	HasCookie          bool         `json:"has_cookie"`
	UserID             string       `json:"user_id"`
	UserIDSource       string       `json:"user_id_source"`
	ProxyURL           string       `json:"proxy_url"`
	Egress             string       `json:"egress"`
	BaseURL            string       `json:"base_url"`
	CheckinURL         string       `json:"checkin_url"`
	CreatedAt          string       `json:"created_at"`
	UpdatedAt          string       `json:"updated_at"`
	LastCheckinAt      string       `json:"last_checkin_at"`
	LastCheckinStatus  string       `json:"last_checkin_status"`
	LastCheckinMessage string       `json:"last_checkin_message"`
	Balance            *balanceView `json:"balance,omitempty"`
	LastError          string       `json:"last_error,omitempty"`
}

type balanceView struct {
	Quota        float64 `json:"quota"`
	UsedQuota    float64 `json:"used_quota"`
	Total        float64 `json:"total"`
	RequestCount int64   `json:"request_count"`
	Username     string  `json:"username"`
	DisplayName  string  `json:"display_name"`
	Group        string  `json:"group"`
	Dollars      float64 `json:"dollars"`
	UsedDollars  float64 `json:"used_dollars"`
	FetchedAt    string  `json:"fetched_at"`
	// Stale marks a figure restored from disk rather than read in this session.
	Stale bool `json:"stale,omitempty"`
}

func buildBalanceView(info *balanceInfo, cfg Config, fromDisk bool) *balanceView {
	if info == nil {
		return nil
	}
	perUnit := cfg.QuotaPerUnit
	if perUnit <= 0 {
		perUnit = defaultQuotaPerUnit
	}
	// A restored reading is only flagged when it is older than the configured
	// refresh interval: "came from disk" is not the same as "out of date", and
	// the ledger must not cry stale for a figure read a minute ago.
	interval := cfg.BalanceInterval
	if interval <= 0 {
		interval = time.Duration(defaultBalanceMin) * time.Minute
	}
	stale := fromDisk && !info.FetchedAt.IsZero() && time.Since(info.FetchedAt) > interval
	view := &balanceView{
		Quota:        info.Quota,
		UsedQuota:    info.UsedQuota,
		Total:        info.Total(),
		RequestCount: info.RequestCount,
		Username:     info.Username,
		DisplayName:  info.DisplayName,
		Group:        info.Group,
		Dollars:      info.Quota / perUnit,
		UsedDollars:  info.UsedQuota / perUnit,
		Stale:        stale,
	}
	if !info.FetchedAt.IsZero() {
		view.FetchedAt = info.FetchedAt.Format(time.RFC3339)
	}
	return view
}

func buildAccountViews(list []*Account, cfg Config) []accountView {
	views := make([]accountView, 0, len(list))
	for _, account := range list {
		view := accountView{
			Name:               account.Name,
			AuthIndex:          account.AuthIndex,
			ID:                 account.ID,
			Remark:             account.displayName(),
			Disabled:           account.File.Disabled,
			CookieHint:         maskSecret(account.File.Cookie),
			CookieFields:       cookieValueCount(account.File.Cookie),
			HasCookie:          strings.TrimSpace(account.File.Cookie) != "",
			UserID:             resolveUserID(account),
			UserIDSource:       userIDSource(account),
			ProxyURL:           account.File.ProxyURL,
			Egress:             newClient().proxyFor(account).describe(),
			BaseURL:            account.effectiveBase(cfg),
			CheckinURL:         account.effectiveCheckinURL(cfg),
			CreatedAt:          account.File.CreatedAt,
			UpdatedAt:          account.File.UpdatedAt,
			LastCheckinAt:      account.File.LastCheckinAt,
			LastCheckinStatus:  account.File.LastCheckinStatus,
			LastCheckinMessage: account.File.LastCheckinMessage,
		}
		accountsState.mu.RLock()
		if runtime, okRuntime := accountsState.runtime[account.Name]; okRuntime && runtime != nil {
			view.LastError = runtime.LastError
			if runtime.Balance != nil {
				view.Balance = buildBalanceView(runtime.Balance, cfg, false)
			}
		}
		accountsState.mu.RUnlock()
		// Fall back to the persisted reading so a fresh page shows the last known
		// figure instead of an empty slot; the panel marks it as stale.
		if view.Balance == nil && account.File.Balance != nil {
			if info := account.File.Balance.toInfo(); info != nil {
				view.Balance = buildBalanceView(info, cfg, true)
			}
		}
		views = append(views, view)
	}
	return views
}
