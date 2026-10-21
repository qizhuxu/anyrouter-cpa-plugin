package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// pluginRoutePrefix namespaces every management route this plugin owns. CPA
// matches declared routes exactly, so the prefix also documents ownership.
const pluginRoutePrefix = "/" + pluginID

// managementRegistration declares the browser resource and every management
// route. A path that is not declared here is answered by the host with a 404
// before the plugin ever sees it.
func managementRegistration() map[string]any {
	routes := []pluginapi.ManagementRoute{
		{Method: http.MethodGet, Path: pluginRoutePrefix + "/status"},
		{Method: http.MethodGet, Path: pluginRoutePrefix + "/accounts"},
		{Method: http.MethodGet, Path: pluginRoutePrefix + "/account"},
		{Method: http.MethodPost, Path: pluginRoutePrefix + "/account"},
		{Method: http.MethodPost, Path: pluginRoutePrefix + "/account/disable"},
		{Method: http.MethodPost, Path: pluginRoutePrefix + "/checkin"},
		{Method: http.MethodPost, Path: pluginRoutePrefix + "/balance"},
		{Method: http.MethodPost, Path: pluginRoutePrefix + "/challenge/refresh"},
		{Method: http.MethodPost, Path: pluginRoutePrefix + "/schedule"},
		{Method: http.MethodGet, Path: pluginRoutePrefix + "/events"},
	}
	resources := []pluginapi.ResourceRoute{
		{
			Path:        "/panel",
			Menu:        "AnyRouter",
			Description: "AnyRouter 账号、签到与余额面板（敏感操作走管理密钥）。",
		},
	}
	return map[string]any{
		"routes":    routes,
		"resources": resources,
	}
}

// managementRequest is the inbound management payload. Field names match the
// host struct so the default JSON decoding works.
type managementRequest struct {
	Method         string              `json:"Method"`
	Path           string              `json:"Path"`
	Headers        http.Header         `json:"Headers"`
	Query          map[string][]string `json:"Query"`
	Body           []byte              `json:"Body"`
	HostCallbackID string              `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

func jsonResponse(status int, payload any) managementResponse {
	return managementResponse{
		StatusCode: status,
		Headers:    http.Header{"content-type": []string{"application/json; charset=utf-8"}},
		Body:       marshalJSON(payload),
	}
}

func jsonError(status int, code, message string) managementResponse {
	return jsonResponse(status, map[string]any{"error": code, "message": message})
}

func htmlResponse(status int, body string) managementResponse {
	return managementResponse{
		StatusCode: status,
		Headers:    http.Header{"content-type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
	}
}

// handleManagement dispatches one management or resource request.
func handleManagement(request []byte) ([]byte, error) {
	var req managementRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return errorEnvelope("invalid_request", "malformed management request: "+errUnmarshal.Error(), http.StatusBadRequest), nil
		}
	}
	path := normaliseRoutePath(req.Path)
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	resp, handled := dispatchManagement(method, path, req)
	if !handled {
		resp = jsonError(http.StatusNotFound, "not_found", "未知路径: "+req.Path)
	}
	return okEnvelope(resp)
}

// normaliseRoutePath strips the mount prefix so both the resource mount
// (/v0/resource/plugins/anyrouter/panel) and the management mount
// (/v0/management/anyrouter/status) resolve to the same local path.
func normaliseRoutePath(raw string) string {
	path := strings.TrimSpace(raw)
	if index := strings.Index(path, pluginRoutePrefix); index >= 0 {
		path = path[index+len(pluginRoutePrefix):]
	}
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func dispatchManagement(method, path string, req managementRequest) (managementResponse, bool) {
	switch path {
	case "/panel":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "面板仅支持 GET"), true
		}
		return htmlResponse(http.StatusOK, panelHTML()), true
	case "/status":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "状态接口仅支持 GET"), true
		}
		return handleStatus(), true
	case "/accounts":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "账号列表仅支持 GET"), true
		}
		return handleAccounts(), true
	case "/account":
		if method == http.MethodGet {
			return handleAccountDetail(req), true
		}
		if method == http.MethodPost {
			return handleAccountSave(req), true
		}
		return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "账号接口支持 GET/POST"), true
	case "/account/disable":
		if method != http.MethodPost {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "该接口仅支持 POST"), true
		}
		return handleAccountDisable(req), true
	case "/checkin":
		if method != http.MethodPost {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "签到仅支持 POST"), true
		}
		return handleCheckin(req), true
	case "/balance":
		if method != http.MethodPost {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "余额刷新仅支持 POST"), true
		}
		return handleBalance(req), true
	case "/schedule":
		if method != http.MethodPost {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "该接口仅支持 POST"), true
		}
		return handleScheduleSave(req), true
	case "/challenge/refresh":
		if method != http.MethodPost {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "该接口仅支持 POST"), true
		}
		return handleChallengeRefresh(), true
	case "/events":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "事件接口仅支持 GET"), true
		}
		return jsonResponse(http.StatusOK, map[string]any{"events": recentEvents(100)}), true
	default:
		return managementResponse{}, false
	}
}

// handleScheduleSave persists the panel-managed automatic-task settings. The
// host owns config.yaml and a plugin cannot rewrite it, so the overrides go to
// the plugin's own state file and take effect on the next scheduler tick.
func handleScheduleSave(req managementRequest) managementResponse {
	cfg := currentConfig()
	var body struct {
		AutoCheckin        *bool   `json:"auto_checkin"`
		CheckinTime        *string `json:"checkin_time"`
		AutoRefreshBalance *bool   `json:"auto_refresh_balance"`
		BalanceMinutes     *int    `json:"balance_refresh_minutes"`
		Reset              bool    `json:"reset"`
	}
	if len(req.Body) > 0 {
		if errUnmarshal := json.Unmarshal(req.Body, &body); errUnmarshal != nil {
			return jsonError(http.StatusBadRequest, "invalid_request", "请求体不是合法 JSON: "+errUnmarshal.Error())
		}
	}
	next := pluginState{}
	if !body.Reset {
		next.AutoCheckin = body.AutoCheckin
		next.AutoBalance = body.AutoRefreshBalance
		next.CheckinTime = body.CheckinTime
		next.BalanceMinutes = body.BalanceMinutes
	}
	if errSave := savePluginState(cfg, next); errSave != nil {
		return jsonError(http.StatusBadRequest, "invalid_request", errSave.Error())
	}
	snapshot := scheduleSnapshot()
	hostLogf("info", "自动任务设置已更新：签到=%v 时间=%v 余额=%v 间隔=%v 分钟（来源=%v）",
		snapshot["auto_checkin"], snapshot["checkin_time"], snapshot["auto_refresh_balance"],
		snapshot["balance_refresh_minutes"], snapshot["source"])
	recordEvent("info", "schedule", "", fmt.Sprintf("自动任务设置已保存：签到 %v @ %v（来源 %v）",
		snapshot["auto_checkin"], snapshot["checkin_time"], snapshot["source"]))
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "schedule": snapshot})
}

func handleStatus() managementResponse {
	// Effective (panel overrides applied) so the schedule the panel shows is the
	// schedule the scheduler will actually use.
	cfg := effectiveConfig()
	list, _ := accounts(5 * time.Second)

	accountsState.mu.RLock()
	lastCheckinAt := accountsState.lastCheckinAt
	lastCheckin := accountsState.lastCheckin
	lastBalanceAt := accountsState.lastBalanceAt
	lastBalance := accountsState.lastBalance
	loadErr := accountsState.loadErr
	accountsState.mu.RUnlock()

	enabled := 0
	for _, account := range list {
		if account.enabled() {
			enabled++
		}
	}
	scheduler := scheduleSnapshot()
	backgroundState.mu.Lock()
	// Runtime-only fields are merged into the same block the panel edits; the
	// schedule values themselves come from scheduleSnapshot().
	schedulerExtra := map[string]any{
		"started":            backgroundState.started,
		"last_tick":          formatTime(backgroundState.lastTick),
		"last_checkin_date":  backgroundState.lastCheckinDate,
		"last_checkin_error": backgroundState.lastError,
	}
	backgroundState.mu.Unlock()
	for key, value := range schedulerExtra {
		scheduler[key] = value
	}

	now := time.Now().In(cfg.Location)
	return jsonResponse(http.StatusOK, map[string]any{
		"plugin":          pluginID,
		"version":         pluginVersion,
		"host_available":  hostAvailable(),
		"now":             now.Format(time.RFC3339),
		"timezone":        cfg.Timezone,
		"account_count":   len(list),
		"enabled_count":   enabled,
		"load_error":      loadErr,
		"last_checkin_at": formatTime(lastCheckinAt),
		"last_checkin":    lastCheckin,
		"last_balance_at": formatTime(lastBalanceAt),
		"last_balance":    lastBalance,
		"next_checkin_at": nextCheckinTime(cfg, now),
		"egress":          newClient().proxyFor(nil).describe(),
		"egress_default":  newClient().proxyFor(nil).Mode == proxyModeHost,
		"challenge":       challengeStore.snapshot(),
		"config": map[string]any{
			"base_url":                cfg.BaseURL,
			"checkin_url":             cfg.CheckinURL,
			"balance_url":             cfg.BalanceURL,
			"challenge_enabled":       cfg.ChallengeEnabled,
			"challenge_ttl_minutes":   int(cfg.ChallengeTTL.Minutes()),
			"request_timeout_seconds": int(cfg.Timeout.Seconds()),
			"quota_per_unit":          cfg.QuotaPerUnit,
			"proxy_url":               cfg.ProxyURL,
			"timezone":                cfg.Timezone,
			"debug":                   cfg.Debug,
		},
		"schedule": scheduler,
		"events":   recentEvents(30),
	})
}

func handleAccounts() managementResponse {
	// Effective values, so the panel never shows "已关闭" next to a panel-enabled
	// schedule.
	cfg := effectiveConfig()
	list, errLoad := accounts(5 * time.Second)
	if errLoad != nil && len(list) == 0 {
		return jsonError(http.StatusBadGateway, "host_unavailable", "读取 CPA 凭证失败: "+errLoad.Error())
	}
	views := buildAccountViews(list, cfg)
	return jsonResponse(http.StatusOK, map[string]any{
		"accounts": views,
		"config": map[string]any{
			"base_url":                cfg.BaseURL,
			"checkin_url":             cfg.CheckinURL,
			"balance_url":             cfg.BalanceURL,
			"quota_per_unit":          cfg.QuotaPerUnit,
			"auto_checkin":            cfg.AutoCheckin,
			"checkin_time":            cfg.CheckinTime,
			"auto_refresh_balance":    cfg.AutoBalance,
			"balance_refresh_minutes": int(cfg.BalanceInterval.Minutes()),
			"timezone":                cfg.Timezone,
		},
		"count":  len(views),
		"events": recentEvents(20),
	})
}

// handleAccountDetail returns one account including its cookie so the panel can
// offer an edit form. It is a management route, so the host already enforced
// the management key.
func handleAccountDetail(req managementRequest) managementResponse {
	name := queryValue(req, "name")
	if name == "" {
		return jsonError(http.StatusBadRequest, "invalid_request", "缺少 name 参数")
	}
	if _, errLoad := accounts(2 * time.Second); errLoad != nil {
		return jsonError(http.StatusBadGateway, "host_unavailable", "读取 CPA 凭证失败: "+errLoad.Error())
	}
	account := findAccount(name)
	if account == nil {
		return jsonError(http.StatusNotFound, "not_found", "账号不存在: "+name)
	}
	cfg := currentConfig()
	view := buildAccountViews([]*Account{account}, cfg)[0]
	payload := map[string]any{
		"account":        view,
		"cookie":         account.File.Cookie,
		"user_id":        resolveUserID(account),
		"user_id_source": userIDSource(account),
		"proxy_url":      account.File.ProxyURL,
		"egress":         newClient().proxyFor(account).describe(),
		"base_url":       account.File.BaseURL,
		"checkin_url":    account.File.CheckinURL,
		"balance_url":    account.File.BalanceURL,
	}
	return jsonResponse(http.StatusOK, payload)
}

type accountSaveRequest struct {
	Name       string `json:"name"`
	Remark     string `json:"remark"`
	Cookie     string `json:"cookie"`
	UserID     string `json:"user_id"`
	ProxyURL   string `json:"proxy_url"`
	BaseURL    string `json:"base_url"`
	CheckinURL string `json:"checkin_url"`
	BalanceURL string `json:"balance_url"`
	Disabled   *bool  `json:"disabled"`
}

// userIDSource explains where the New-Api-User value came from, so the panel can
// tell a configured value apart from a derived one.
func userIDSource(account *Account) string {
	if account == nil {
		return "none"
	}
	if strings.TrimSpace(account.File.UserID) != "" {
		return "configured"
	}
	if sessionUserID(account.File.Cookie) != "" {
		return "derived from session cookie"
	}
	return "missing"
}

// handleAccountSave creates or updates an account. The auth file is the only
// durable storage a plugin can use, and writing it also refreshes the host's
// credential inventory.
func handleAccountSave(req managementRequest) managementResponse {
	var body accountSaveRequest
	if len(req.Body) > 0 {
		if errUnmarshal := json.Unmarshal(req.Body, &body); errUnmarshal != nil {
			return jsonError(http.StatusBadRequest, "invalid_request", "请求体不是合法 JSON: "+errUnmarshal.Error())
		}
	}
	body.Remark = strings.TrimSpace(body.Remark)
	body.Cookie = strings.TrimSpace(body.Cookie)
	if body.Cookie == "" {
		return jsonError(http.StatusBadRequest, "invalid_request", "cookie 不能为空")
	}
	if _, errLoad := accounts(1 * time.Second); errLoad != nil {
		return jsonError(http.StatusBadGateway, "host_unavailable", "读取 CPA 凭证失败: "+errLoad.Error())
	}

	name := strings.TrimSpace(body.Name)
	var file accountFile
	if name != "" {
		if existing := findAccount(name); existing != nil {
			file = existing.File
		} else {
			// A name that is not in the inventory yet creates a new account, so
			// callers (and restore scripts) can pin the file name explicitly.
			sanitized, errName := sanitizeAccountName(name)
			if errName != nil {
				return jsonError(http.StatusBadRequest, "invalid_request", errName.Error())
			}
			name = sanitized
			file = accountFile{CreatedAt: time.Now().Format(time.RFC3339)}
		}
	} else {
		name = newAccountName(body.Remark)
		file = accountFile{CreatedAt: time.Now().Format(time.RFC3339)}
	}
	if body.Remark != "" {
		file.Remark = body.Remark
		file.Note = body.Remark
	}
	file.Cookie = body.Cookie
	file.UserID = strings.TrimSpace(body.UserID)
	file.ProxyURL = strings.TrimSpace(body.ProxyURL)
	if errProxy := validateProxySetting(file.ProxyURL); errProxy != nil {
		return jsonError(http.StatusBadRequest, "invalid_request", "代理设置无效: "+errProxy.Error())
	}
	file.BaseURL = strings.TrimSpace(body.BaseURL)
	file.CheckinURL = strings.TrimSpace(body.CheckinURL)
	file.BalanceURL = strings.TrimSpace(body.BalanceURL)
	if body.Disabled != nil {
		file.Disabled = *body.Disabled
	}
	if errSave := saveAccountFile(name, file); errSave != nil {
		return jsonError(http.StatusBadGateway, "save_failed", "保存账号失败: "+errSave.Error())
	}
	hostLogf("info", "账号已保存 name=%s cookie_fields=%d", name, cookieValueCount(file.Cookie))
	recordEvent("info", "account", name, "账号已保存")
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "name": name})
}

type accountDisableRequest struct {
	Name     string `json:"name"`
	Disabled *bool  `json:"disabled"`
}

func handleAccountDisable(req managementRequest) managementResponse {
	var body accountDisableRequest
	if len(req.Body) > 0 {
		if errUnmarshal := json.Unmarshal(req.Body, &body); errUnmarshal != nil {
			return jsonError(http.StatusBadRequest, "invalid_request", "请求体不是合法 JSON: "+errUnmarshal.Error())
		}
	}
	if _, errLoad := accounts(1 * time.Second); errLoad != nil {
		return jsonError(http.StatusBadGateway, "host_unavailable", "读取 CPA 凭证失败: "+errLoad.Error())
	}
	account := findAccount(body.Name)
	if account == nil {
		return jsonError(http.StatusNotFound, "not_found", "账号不存在: "+strings.TrimSpace(body.Name))
	}
	disabled := !account.File.Disabled
	if body.Disabled != nil {
		disabled = *body.Disabled
	}
	file := account.File
	file.Disabled = disabled
	if errSave := saveAccountFile(account.Name, file); errSave != nil {
		return jsonError(http.StatusBadGateway, "save_failed", "保存账号失败: "+errSave.Error())
	}
	recordEvent("info", "account", account.Name, map[bool]string{true: "账号已停用", false: "账号已启用"}[disabled])
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "name": account.Name, "disabled": disabled})
}

type taskRequest struct {
	Name string `json:"name"`
}

func handleCheckin(req managementRequest) managementResponse {
	var body taskRequest
	if len(req.Body) > 0 {
		_ = json.Unmarshal(req.Body, &body)
	}
	if _, errLoad := accounts(1 * time.Second); errLoad != nil {
		return jsonError(http.StatusBadGateway, "host_unavailable", "读取 CPA 凭证失败: "+errLoad.Error())
	}
	selection := selectAccounts(body.Name, true)
	if len(selection) == 0 {
		return jsonError(http.StatusBadRequest, "no_accounts", "没有可签到的账号（检查账号是否存在或已停用）")
	}
	results := checkinAccounts(selection)
	for _, item := range results {
		level := "info"
		if !item.OK {
			level = "warn"
		}
		recordEvent(level, "checkin", item.Account, item.Message)
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"results": results,
		"summary": summariseCheckin(results),
	})
}

func handleBalance(req managementRequest) managementResponse {
	var body taskRequest
	if len(req.Body) > 0 {
		_ = json.Unmarshal(req.Body, &body)
	}
	if _, errLoad := accounts(1 * time.Second); errLoad != nil {
		return jsonError(http.StatusBadGateway, "host_unavailable", "读取 CPA 凭证失败: "+errLoad.Error())
	}
	selection := selectAccounts(body.Name, true)
	if len(selection) == 0 {
		return jsonError(http.StatusBadRequest, "no_accounts", "没有可查询的账号（检查账号是否存在或已停用）")
	}
	results := refreshBalances(selection)
	for _, item := range results {
		level := "info"
		message := item.Message
		if item.OK {
			message = "余额读取成功"
		} else {
			level = "warn"
		}
		recordEvent(level, "balance", item.Account, message)
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"results": results,
		"summary": summariseBalances(results),
	})
}

func handleChallengeRefresh() managementResponse {
	challengeStore.clear()
	cfg := currentConfig()
	client := newClient()
	cookie := client.ensureChallenge(cfg.BaseURL+"/", nil)
	if cookie == "" {
		recordEvent("warn", "challenge", "", "未能获取挑战 Cookie")
		return jsonResponse(http.StatusBadGateway, map[string]any{
			"ok":      false,
			"message": "未能从挑战页取得 Cookie，请检查 base_url 与代理设置。",
		})
	}
	recordEvent("info", "challenge", "", "挑战 Cookie 已刷新")
	return jsonResponse(http.StatusOK, map[string]any{
		"ok":     true,
		"fields": cookieValueCount(cookie),
		"cache":  challengeStore.snapshot(),
	})
}

func queryValue(req managementRequest, key string) string {
	if req.Query == nil {
		return ""
	}
	values := req.Query[key]
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
