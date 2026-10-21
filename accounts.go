package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// authFilePrefix namespaces the auth files this plugin owns inside the host
// auth directory. CPA derives the credential id from the file name minus the
// extension, so the prefix also keeps the inventory easy to recognise.
const authFilePrefix = "anyrouter-"

// accountFile is the on-disk format of one AnyRouter account. It is an
// ordinary CPA auth file whose "type" makes the host ask this plugin to parse
// it, which is what puts the account into the credential list and makes the
// quota provider reachable per credential.
type accountFile struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	// Remark is the user-facing name; Note mirrors it for host-side displays.
	Remark string `json:"remark,omitempty"`
	Note   string `json:"note,omitempty"`
	// Cookie is the browser Cookie header value of a logged-in AnyRouter session.
	Cookie string `json:"cookie"`
	// UserID is the new-api "New-Api-User" header value. AnyRouter rejects the
	// balance endpoint without it; when empty the plugin derives it from the
	// session cookie.
	UserID string `json:"user_id,omitempty"`
	// ProxyURL overrides the plugin-wide proxy for this account. Empty inherits
	// the plugin value; direct/none bypass every proxy.
	ProxyURL string `json:"proxy_url,omitempty"`
	// Disabled lets the host skip the credential and stops automatic check-in.
	Disabled bool `json:"disabled,omitempty"`
	// Per-account endpoint overrides; empty means "use the plugin default".
	BaseURL    string `json:"base_url,omitempty"`
	CheckinURL string `json:"checkin_url,omitempty"`
	BalanceURL string `json:"balance_url,omitempty"`
	// Timestamps are informational and survive plugin restarts.
	CreatedAt          string `json:"created_at,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
	LastCheckinAt      string `json:"last_checkin_at,omitempty"`
	LastCheckinStatus  string `json:"last_checkin_status,omitempty"`
	LastCheckinMessage string `json:"last_checkin_message,omitempty"`
	// Balance keeps the last reading so the ledger shows a figure right after a
	// page reload or a plugin restart instead of "未读取". It is refreshed at most
	// once per balance_refresh_minutes.
	Balance *balanceSnapshot `json:"balance,omitempty"`
}

// balanceSnapshot is the persisted form of one balance reading.
type balanceSnapshot struct {
	Quota       float64 `json:"quota"`
	Used        float64 `json:"used_quota"`
	Total       float64 `json:"total"`
	Requests    int64   `json:"request_count"`
	Username    string  `json:"username,omitempty"`
	DisplayName string  `json:"display_name,omitempty"`
	Group       string  `json:"group,omitempty"`
	FetchedAt   string  `json:"fetched_at,omitempty"`
}

// toInfo converts a snapshot back into the runtime shape.
func (s *balanceSnapshot) toInfo() *balanceInfo {
	if s == nil {
		return nil
	}
	info := &balanceInfo{
		Quota:        s.Quota,
		UsedQuota:    s.Used,
		RequestCount: s.Requests,
		Username:     s.Username,
		DisplayName:  s.DisplayName,
		Group:        s.Group,
	}
	if parsed, errParse := time.Parse(time.RFC3339, s.FetchedAt); errParse == nil {
		info.FetchedAt = parsed
	}
	return info
}

func newBalanceSnapshot(info *balanceInfo) *balanceSnapshot {
	if info == nil {
		return nil
	}
	return &balanceSnapshot{
		Quota:       info.Quota,
		Used:        info.UsedQuota,
		Total:       info.Total(),
		Requests:    info.RequestCount,
		Username:    info.Username,
		DisplayName: info.DisplayName,
		Group:       info.Group,
		FetchedAt:   info.FetchedAt.Format(time.RFC3339),
	}
}

// Account is the runtime view of an accountFile plus the host credential ids.
type Account struct {
	Name      string
	AuthIndex string
	ID        string
	File      accountFile
}

// Remark returns the display name, falling back to the file name.
func (a *Account) displayName() string {
	if a == nil {
		return ""
	}
	if strings.TrimSpace(a.File.Remark) != "" {
		return strings.TrimSpace(a.File.Remark)
	}
	if strings.TrimSpace(a.File.Note) != "" {
		return strings.TrimSpace(a.File.Note)
	}
	return a.Name
}

func (a *Account) effectiveBase(cfg Config) string {
	if a != nil && strings.TrimSpace(a.File.BaseURL) != "" {
		return strings.TrimSpace(a.File.BaseURL)
	}
	return cfg.BaseURL
}

func (a *Account) effectiveCheckinURL(cfg Config) string {
	if a != nil && strings.TrimSpace(a.File.CheckinURL) != "" {
		return strings.TrimSpace(a.File.CheckinURL)
	}
	return cfg.CheckinURL
}

func (a *Account) effectiveBalanceURL(cfg Config) string {
	if a != nil && strings.TrimSpace(a.File.BalanceURL) != "" {
		return strings.TrimSpace(a.File.BalanceURL)
	}
	return cfg.BalanceURL
}

func (a *Account) enabled() bool { return a != nil && !a.File.Disabled }

// accountRuntime keeps everything that does not belong in the auth file:
// balances are volatile and are re-read on demand.
type accountRuntime struct {
	Balance   *balanceInfo
	BalanceAt time.Time
	LastError string
	Busy      bool
	LastRunAt time.Time
}

// accountsState caches the host inventory and the volatile per-account state.
var accountsState struct {
	mu            sync.RWMutex
	accounts      []*Account
	loadedAt      time.Time
	loadErr       string
	runtime       map[string]*accountRuntime
	lastCheckinAt time.Time
	lastCheckin   string
	lastBalanceAt time.Time
	lastBalance   string
}

func ensureMapsLocked() {
	if accountsState.runtime == nil {
		accountsState.runtime = make(map[string]*accountRuntime)
	}
}

func runtimeFor(name string) *accountRuntime {
	accountsState.mu.Lock()
	defer accountsState.mu.Unlock()
	ensureMapsLocked()
	entry, ok := accountsState.runtime[name]
	if !ok {
		entry = &accountRuntime{}
		accountsState.runtime[name] = entry
	}
	return entry
}

func withRuntime(name string, fn func(*accountRuntime)) {
	accountsState.mu.Lock()
	defer accountsState.mu.Unlock()
	ensureMapsLocked()
	entry, ok := accountsState.runtime[name]
	if !ok {
		entry = &accountRuntime{}
		accountsState.runtime[name] = entry
	}
	fn(entry)
}

// loadAccounts rebuilds the account snapshot from the host credential
// inventory. Failure keeps the previous snapshot so a transient host error does
// not empty the panel.
func loadAccounts() ([]*Account, error) {
	files, errList := hostAuthList()
	if errList != nil {
		accountsState.mu.Lock()
		accountsState.loadErr = errList.Error()
		accountsState.mu.Unlock()
		return nil, errList
	}

	accounts := make([]*Account, 0, len(files))
	for _, entry := range files {
		if !isAnyrouterEntry(entry) {
			continue
		}
		account := &Account{
			Name:      entry.Name,
			AuthIndex: entry.AuthIndex,
			ID:        entry.ID,
			File: accountFile{
				Type:     providerKey,
				Provider: providerKey,
				Remark:   firstNonEmpty(entry.Note, entry.Label),
				Disabled: entry.Disabled,
			},
		}
		if strings.TrimSpace(entry.AuthIndex) != "" {
			resp, errGet := hostAuthGet(entry.AuthIndex)
			if errGet != nil {
				hostLogf("warn", "读取账号 %s 失败: %v", entry.Name, errGet)
			} else if len(resp.JSON) > 0 {
				var parsed accountFile
				if errUnmarshal := json.Unmarshal(resp.JSON, &parsed); errUnmarshal != nil {
					hostLogf("warn", "解析账号 %s 失败: %v", entry.Name, errUnmarshal)
				} else {
					account.File = parsed
					if account.File.Type == "" {
						account.File.Type = providerKey
					}
					if account.File.Provider == "" {
						account.File.Provider = providerKey
					}
				}
			}
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })

	accountsState.mu.Lock()
	accountsState.accounts = accounts
	accountsState.loadedAt = time.Now()
	accountsState.loadErr = ""
	ensureMapsLocked()
	// Drop runtime entries whose account disappeared.
	alive := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		alive[account.Name] = struct{}{}
	}
	for name := range accountsState.runtime {
		if _, ok := alive[name]; !ok {
			delete(accountsState.runtime, name)
		}
	}
	accountsState.mu.Unlock()
	return accounts, nil
}

// isAnyrouterEntry decides whether a host credential belongs to this plugin.
// The provider keys are authoritative; the file name prefix is the fallback for
// entries the host reports without provider metadata.
func isAnyrouterEntry(entry pluginapi.HostAuthFileEntry) bool {
	if strings.EqualFold(strings.TrimSpace(entry.Provider), providerKey) ||
		strings.EqualFold(strings.TrimSpace(entry.Type), providerKey) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(entry.Name)), authFilePrefix)
}

// accounts returns the cached inventory, reloading it when older than maxAge.
func accounts(maxAge time.Duration) ([]*Account, error) {
	accountsState.mu.RLock()
	cached := accountsState.accounts
	loadedAt := accountsState.loadedAt
	accountsState.mu.RUnlock()
	if cached != nil && time.Since(loadedAt) < maxAge {
		return cached, nil
	}
	fresh, errLoad := loadAccounts()
	if errLoad != nil {
		if cached != nil {
			return cached, nil
		}
		return nil, errLoad
	}
	return fresh, nil
}

func accountsSnapshot() []*Account {
	accountsState.mu.RLock()
	defer accountsState.mu.RUnlock()
	return append([]*Account(nil), accountsState.accounts...)
}

func findAccount(name string) *Account {
	name = strings.TrimSpace(name)
	for _, account := range accountsSnapshot() {
		if account.Name == name {
			return account
		}
	}
	return nil
}

// saveAccountFile persists one account. The host writes the file, reloads it as
// a credential and reports the resolved path.
func saveAccountFile(name string, file accountFile) error {
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		name += ".json"
	}
	file.Type = providerKey
	file.Provider = providerKey
	if file.Note == "" {
		file.Note = file.Remark
	}
	file.UpdatedAt = time.Now().Format(time.RFC3339)
	if file.CreatedAt == "" {
		file.CreatedAt = file.UpdatedAt
	}
	raw, errMarshal := json.MarshalIndent(file, "", "  ")
	if errMarshal != nil {
		return errMarshal
	}
	if _, errSave := hostAuthSave(name, raw); errSave != nil {
		return errSave
	}
	// Force the next read to come from the host again.
	accountsState.mu.Lock()
	accountsState.loadedAt = time.Time{}
	accountsState.mu.Unlock()
	return nil
}

// newAccountName builds a unique file name from the remark.
func newAccountName(remark string) string {
	slug := slugify(remark)
	return authFilePrefix + slug + "-" + randomHex(3) + ".json"
}

// sanitizeAccountName validates a caller supplied auth file name. The host
// writes the file into its auth directory, so path separators and traversal
// must be rejected before the name reaches host.auth.save.
func sanitizeAccountName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("账号文件名不能为空")
	}
	if strings.ContainsAny(name, `\/:*?"<>|`) || strings.Contains(name, "..") {
		return "", fmt.Errorf("账号文件名包含非法字符: %q", raw)
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		name += ".json"
	}
	if len(name) > 128 {
		return "", fmt.Errorf("账号文件名过长: %d 字符", len(name))
	}
	return name, nil
}

func slugify(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	var out strings.Builder
	for _, r := range text {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == '-' || r == '_':
			out.WriteRune('-')
		case r > 127:
			// Non-ASCII remarks are common (Chinese); keep them out of the file
			// name and fall back to a neutral prefix instead.
			return "account"
		}
	}
	slug := strings.Trim(out.String(), "-")
	if slug == "" {
		return "account"
	}
	if len(slug) > 24 {
		slug = slug[:24]
	}
	return slug
}

func randomHex(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, errRead := rand.Read(buf); errRead != nil {
		return fmt.Sprintf("%06x", time.Now().UnixNano()&0xffffff)
	}
	return hex.EncodeToString(buf)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
