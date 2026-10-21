package main

import (
	"encoding/json"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	pluginID      = "anyrouter"
	pluginVersion = "0.1.0"
	pluginAuthor  = "qizhuxu"
	// pluginRepo must not be empty: the host rejects a registration whose
	// metadata carries no repository URL (internal/pluginhost validPlugin).
	pluginRepo  = "https://github.com/qizhuxu/anyrouter-cpa-plugin"
	providerKey = "anyrouter"
)

// lifecycleRequest is the payload of plugin.register / plugin.reconfigure.
type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

// handleMethod dispatches every inbound RPC. Every method implied by a declared
// capability is covered; the default branch answers unknown_method instead of
// failing silently.
func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handleLifecycle(request)
	case pluginabi.MethodPluginQuiesce:
		// Acknowledge only: CPA owns the request lifecycle and there is nothing
		// to drain. The scheduler keeps running so an in-flight check-in can
		// finish and be reported.
		return okEnvelope(map[string]any{})
	case pluginabi.MethodPluginShutdown:
		stopBackground()
		return okEnvelope(map[string]any{})
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(map[string]any{"identifier": providerKey})
	case pluginabi.MethodAuthParse:
		return handleAuthParse(request)
	case pluginabi.MethodAuthLoginStart:
		return handleAuthLoginStart(request)
	case pluginabi.MethodAuthLoginPoll:
		return handleAuthLoginPoll(request)
	case pluginabi.MethodAuthRefresh:
		return handleAuthRefresh(request)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration())
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	case pluginabi.MethodQuotaIdentifier:
		return okEnvelope(map[string]any{"identifier": providerKey})
	case pluginabi.MethodQuotaDescribe:
		return handleQuotaDescribe()
	case pluginabi.MethodQuotaFetch:
		return handleQuotaFetch(request)
	case pluginabi.MethodQuotaReset:
		return handleQuotaReset(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, http.StatusNotImplemented), nil
	}
}

// handleLifecycle decodes the plugin configuration, swaps it in atomically and
// makes sure the background scheduler is running. plugin.register and
// plugin.reconfigure share the same response.
func handleLifecycle(request []byte) ([]byte, error) {
	var req lifecycleRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return errorEnvelope("invalid_request", "malformed lifecycle request: "+errUnmarshal.Error(), http.StatusBadRequest), nil
		}
	}
	cfg, errConfig := loadConfig(req.ConfigYAML)
	if errConfig != nil {
		hostLogf("warn", "invalid configuration, keeping the previous one: %v", errConfig)
		return errorEnvelope("invalid_config", errConfig.Error(), http.StatusBadRequest), nil
	}
	applyConfig(cfg)
	loadPluginState(cfg)
	hostLogf("info", "configured base_url=%s checkin_url=%s auto_checkin=%t checkin_time=%s challenge=%t",
		cfg.BaseURL, cfg.CheckinURL, cfg.AutoCheckin, cfg.CheckinTime, cfg.ChallengeEnabled)
	startBackground()
	return okEnvelope(registration())
}

// registration is the plugin.register / plugin.reconfigure response.
func registration() map[string]any {
	return map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"metadata": pluginapi.Metadata{
			Name:             pluginID,
			Version:          pluginVersion,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepo,
			Logo:             "",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "base_url",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     defaultBaseURL,
					Description: "AnyRouter 站点根地址，用于挑战页与余额查询。",
				},
				{
					Name:        "checkin_url",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "签到接口地址，留空则使用 {base_url}/api/user/sign_in。",
				},
				{
					Name:        "balance_url",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "余额接口地址，留空则使用 {base_url}/api/user/self。",
				},
				{
					Name:        "user_agent",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     defaultUserAgent,
					Description: "请求使用的 User-Agent。",
				},
				{
					Name:        "challenge_enabled",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "是否自动执行加速乐（JSL）挑战页并缓存 acw_sc__v2 Cookie。",
				},
				{
					Name:        "challenge_ttl_minutes",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Default:     defaultChallengeTTLMin,
					Description: "挑战 Cookie 的缓存分钟数。",
				},
				{
					Name:        "request_timeout_seconds",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Default:     defaultTimeoutSec,
					Description: "单次上游请求超时秒数。",
				},
				{
					Name:        "auto_checkin",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "是否每天自动签到。",
				},
				{
					Name:        "checkin_time",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     defaultCheckinTime,
					Description: "每日自动签到时间，格式 HH:MM。",
				},
				{
					Name:        "auto_refresh_balance",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "是否定时刷新账号余额。",
				},
				{
					Name:        "balance_refresh_minutes",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Default:     defaultBalanceMin,
					Description: "余额刷新间隔分钟数。",
				},
				{
					Name:        "quota_per_unit",
					Type:        pluginapi.ConfigFieldTypeNumber,
					Default:     defaultQuotaPerUnit,
					Description: "额度换算比例，new-api 默认为 500000 额度 = 1 美元。",
				},
				{
					Name:        "timezone",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     defaultTimezone,
					Description: "自动任务使用的时区，Local 表示跟随主机，也可填 Asia/Shanghai。",
				},
				{
					Name:        "state_file",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "面板托管设置的状态文件路径；留空为 <用户配置目录>/anyrouter/state.json，Docker 里建议指向挂载目录。",
				},
				{
					Name:        "proxy_url",
					Type:        pluginapi.ConfigFieldTypeString,
					Default:     "",
					Description: "AnyRouter 专用代理（http/https/socks5/socks5h）；留空跟随宿主的 requests.proxy-url，direct 表示强制直连。",
				},
				{
					Name:        "debug",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Default:     false,
					Description: "输出更详细的插件日志（不包含凭证内容）。",
				},
			},
		},
		"capabilities": map[string]any{
			"auth_provider":  true,
			"management_api": true,
			"quota_provider": true,
		},
	}
}
