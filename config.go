package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	// Embed the IANA time zone database so config values such as
	// "Asia/Shanghai" resolve on hosts that have no zoneinfo (notably Windows).
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

const (
	defaultBaseURL         = "https://anyrouter.top"
	defaultUserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
	defaultChallengeTTLMin = 30
	defaultTimeoutSec      = 20
	defaultCheckinTime     = "08:00"
	defaultBalanceMin      = 30
	defaultQuotaPerUnit    = 500000.0
	defaultTimezone        = "Local"
)

// Config is the resolved plugin configuration. Fields are always populated:
// absent YAML keys fall back to the defaults above.
type Config struct {
	BaseURL          string
	CheckinURL       string
	BalanceURL       string
	UserAgent        string
	ChallengeEnabled bool
	ChallengeTTL     time.Duration
	Timeout          time.Duration
	AutoCheckin      bool
	CheckinTime      string
	AutoBalance      bool
	BalanceInterval  time.Duration
	QuotaPerUnit     float64
	Timezone         string
	Location         *time.Location
	Debug            bool
	// StateFile overrides where the panel-managed state lives. In Docker point it
	// at a mounted directory; empty uses <user config dir>/anyrouter/state.json.
	StateFile string
	// ProxyURL is the plugin-wide proxy for AnyRouter traffic. Empty means "use
	// the host transport", which honours CPA's requests.proxy-url. The keywords
	// direct/none mean "do not use any proxy".
	ProxyURL string
}

// configDoc uses pointers so "key absent" and "key set to zero" stay
// distinguishable: decoding into plain values would silently turn an omitted
// boolean into false.
type configDoc struct {
	BaseURL      *string  `yaml:"base_url"`
	CheckinURL   *string  `yaml:"checkin_url"`
	BalanceURL   *string  `yaml:"balance_url"`
	UserAgent    *string  `yaml:"user_agent"`
	ChallengeOn  *bool    `yaml:"challenge_enabled"`
	ChallengeTTL *int     `yaml:"challenge_ttl_minutes"`
	TimeoutSec   *int     `yaml:"request_timeout_seconds"`
	AutoCheckin  *bool    `yaml:"auto_checkin"`
	CheckinTime  *string  `yaml:"checkin_time"`
	AutoBalance  *bool    `yaml:"auto_refresh_balance"`
	BalanceMin   *int     `yaml:"balance_refresh_minutes"`
	QuotaPerUnit *float64 `yaml:"quota_per_unit"`
	Timezone     *string  `yaml:"timezone"`
	Debug        *bool    `yaml:"debug"`
	ProxyURL     *string  `yaml:"proxy_url"`
	StateFile    *string  `yaml:"state_file"`
}

// loadConfig turns the host-provided plugins.configs.<id> YAML node into a
// validated Config. Every plugin-owned field is validated here; ConfigFields in
// the registration response only drives the management UI form.
func loadConfig(raw []byte) (Config, error) {
	cfg := Config{
		BaseURL:          defaultBaseURL,
		UserAgent:        defaultUserAgent,
		ChallengeEnabled: true,
		ChallengeTTL:     time.Duration(defaultChallengeTTLMin) * time.Minute,
		Timeout:          time.Duration(defaultTimeoutSec) * time.Second,
		AutoCheckin:      true,
		CheckinTime:      defaultCheckinTime,
		AutoBalance:      true,
		BalanceInterval:  time.Duration(defaultBalanceMin) * time.Minute,
		QuotaPerUnit:     defaultQuotaPerUnit,
		Timezone:         defaultTimezone,
	}

	var doc configDoc
	if len(strings.TrimSpace(string(raw))) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &doc); errUnmarshal != nil {
			return Config{}, fmt.Errorf("解析插件配置失败: %w", errUnmarshal)
		}
	}
	if doc.BaseURL != nil {
		cfg.BaseURL = strings.TrimSpace(*doc.BaseURL)
	}
	if doc.CheckinURL != nil {
		cfg.CheckinURL = strings.TrimSpace(*doc.CheckinURL)
	}
	if doc.BalanceURL != nil {
		cfg.BalanceURL = strings.TrimSpace(*doc.BalanceURL)
	}
	if doc.UserAgent != nil {
		cfg.UserAgent = strings.TrimSpace(*doc.UserAgent)
	}
	if doc.ChallengeOn != nil {
		cfg.ChallengeEnabled = *doc.ChallengeOn
	}
	if doc.ChallengeTTL != nil {
		if *doc.ChallengeTTL < 1 || *doc.ChallengeTTL > 1440 {
			return Config{}, fmt.Errorf("challenge_ttl_minutes 必须在 1-1440 之间，当前为 %d", *doc.ChallengeTTL)
		}
		cfg.ChallengeTTL = time.Duration(*doc.ChallengeTTL) * time.Minute
	}
	if doc.TimeoutSec != nil {
		if *doc.TimeoutSec < 1 || *doc.TimeoutSec > 300 {
			return Config{}, fmt.Errorf("request_timeout_seconds 必须在 1-300 之间，当前为 %d", *doc.TimeoutSec)
		}
		cfg.Timeout = time.Duration(*doc.TimeoutSec) * time.Second
	}
	if doc.AutoCheckin != nil {
		cfg.AutoCheckin = *doc.AutoCheckin
	}
	if doc.CheckinTime != nil {
		cfg.CheckinTime = strings.TrimSpace(*doc.CheckinTime)
	}
	if doc.AutoBalance != nil {
		cfg.AutoBalance = *doc.AutoBalance
	}
	if doc.BalanceMin != nil {
		if *doc.BalanceMin < 5 || *doc.BalanceMin > 1440 {
			return Config{}, fmt.Errorf("balance_refresh_minutes 必须在 5-1440 之间，当前为 %d", *doc.BalanceMin)
		}
		cfg.BalanceInterval = time.Duration(*doc.BalanceMin) * time.Minute
	}
	if doc.QuotaPerUnit != nil {
		if *doc.QuotaPerUnit <= 0 {
			return Config{}, fmt.Errorf("quota_per_unit 必须大于 0，当前为 %v", *doc.QuotaPerUnit)
		}
		cfg.QuotaPerUnit = *doc.QuotaPerUnit
	}
	if doc.Timezone != nil {
		cfg.Timezone = strings.TrimSpace(*doc.Timezone)
	}
	if doc.Debug != nil {
		cfg.Debug = *doc.Debug
	}
	if doc.ProxyURL != nil {
		cfg.ProxyURL = strings.TrimSpace(*doc.ProxyURL)
	}
	if errProxy := validateProxySetting(cfg.ProxyURL); errProxy != nil {
		return Config{}, fmt.Errorf("proxy_url 无效: %w", errProxy)
	}
	if doc.StateFile != nil {
		cfg.StateFile = strings.TrimSpace(*doc.StateFile)
	}

	if cfg.BaseURL == "" {
		return Config{}, fmt.Errorf("base_url 不能为空")
	}
	normalizedBase, errBase := normalizeBaseURL(cfg.BaseURL)
	if errBase != nil {
		return Config{}, fmt.Errorf("base_url 无效: %w", errBase)
	}
	cfg.BaseURL = normalizedBase

	if cfg.CheckinURL == "" {
		cfg.CheckinURL = cfg.BaseURL + "/api/user/sign_in"
	} else if _, errCheckin := normalizeAbsoluteURL(cfg.CheckinURL); errCheckin != nil {
		return Config{}, fmt.Errorf("checkin_url 无效: %w", errCheckin)
	}
	if cfg.BalanceURL == "" {
		cfg.BalanceURL = cfg.BaseURL + "/api/user/self"
	} else if _, errBalance := normalizeAbsoluteURL(cfg.BalanceURL); errBalance != nil {
		return Config{}, fmt.Errorf("balance_url 无效: %w", errBalance)
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = defaultUserAgent
	}
	if _, errClock := time.Parse("15:04", cfg.CheckinTime); errClock != nil {
		return Config{}, fmt.Errorf("checkin_time 必须为 HH:MM 格式，当前为 %q", cfg.CheckinTime)
	}
	location, errLocation := resolveLocation(cfg.Timezone)
	if errLocation != nil {
		return Config{}, errLocation
	}
	cfg.Location = location
	return cfg, nil
}

// resolveLocation accepts "Local" plus any IANA zone name.
func resolveLocation(name string) (*time.Location, error) {
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	location, errLoad := time.LoadLocation(name)
	if errLoad != nil {
		return nil, fmt.Errorf("timezone %q 无法解析: %w", name, errLoad)
	}
	return location, nil
}

// normalizeBaseURL validates an http(s) base URL and strips any trailing slash.
func normalizeBaseURL(raw string) (string, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil {
		return "", errParse
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("仅支持 http/https，当前为 %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("缺少主机名")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func normalizeAbsoluteURL(raw string) (string, error) {
	parsed, errParse := url.Parse(strings.TrimSpace(raw))
	if errParse != nil {
		return "", errParse
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("仅支持 http/https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("缺少主机名")
	}
	return parsed.String(), nil
}

// configState holds the live configuration and the runtime caches. One mutex
// guards the account snapshot; the configuration itself is swapped wholesale.
var configState struct {
	mu       sync.RWMutex
	cfg      Config
	loaded   bool
	loadedAt time.Time
}

func applyConfig(cfg Config) {
	configState.mu.Lock()
	configState.cfg = cfg
	configState.loaded = true
	configState.loadedAt = time.Now()
	configState.mu.Unlock()
}

func currentConfig() Config {
	configState.mu.RLock()
	cfg := configState.cfg
	configState.mu.RUnlock()
	if !configState.loaded {
		fallback, errFallback := loadConfig(nil)
		if errFallback == nil {
			return fallback
		}
	}
	return cfg
}
