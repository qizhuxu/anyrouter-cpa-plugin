package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// handleQuotaDescribe advertises which credential providers this plugin can
// report a balance for.
func handleQuotaDescribe() ([]byte, error) {
	return okEnvelope(pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{providerKey},
		DisplayName:        "AnyRouter",
		SupportsReset:      false,
	})
}

// handleQuotaFetch answers CPA's per-credential quota request, which is what
// makes the balance visible on the credential page.
//
// Failures return an empty response rather than an error envelope: a quota
// lookup must never turn into a visible failure of the credential.
func handleQuotaFetch(request []byte) ([]byte, error) {
	var req pluginapi.QuotaFetchRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return errorEnvelope("invalid_request", "malformed quota.fetch request: "+errUnmarshal.Error(), http.StatusBadRequest), nil
		}
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider != "" && provider != providerKey && provider != pluginID {
		return okEnvelope(pluginapi.QuotaFetchResponse{})
	}

	account := accountForQuota(req)
	if account == nil {
		hostLog("debug", "quota.fetch 未找到匹配账号，返回空结果", map[string]any{
			"auth_index": req.AuthIndex,
			"provider":   req.Provider,
		})
		return okEnvelope(pluginapi.QuotaFetchResponse{})
	}
	if !account.enabled() {
		return okEnvelope(pluginapi.QuotaFetchResponse{})
	}

	client := newClient()
	info, errBalance := client.balance(account)
	if errBalance != nil {
		hostLogf("warn", "额度查询失败 account=%s: %v", account.Name, errBalance)
		recordEvent("warn", "quota", account.Name, errBalance.Error())
		// A transient upstream failure must not blank the credential page: fall
		// back to the last persisted reading and say how old it is.
		if account.File.Balance != nil {
			if cached := account.File.Balance.toInfo(); cached != nil {
				note := "实时读取失败：" + truncate(errBalance.Error(), 120)
				if !cached.FetchedAt.IsZero() {
					note = "上次读取 " + cached.FetchedAt.Format("01-02 15:04") + "；" + note
				}
				return okEnvelope(quotaResponse(cached, currentConfig(), note))
			}
		}
		return okEnvelope(pluginapi.QuotaFetchResponse{})
	}

	withRuntime(account.Name, func(entry *accountRuntime) {
		entry.Balance = info
		entry.BalanceAt = info.FetchedAt
		entry.LastError = ""
	})
	if persisted := findAccount(account.Name); persisted != nil {
		if errSave := persistBalance(persisted, info); errSave != nil {
			hostLogf("warn", "保存余额快照失败 account=%s: %v", account.Name, errSave)
		}
	}
	return okEnvelope(quotaResponse(info, currentConfig(), ""))
}

// accountForQuota resolves the credential the host asked about. The host passes
// the raw auth file JSON in StorageJSON when an auth_index was supplied, which
// is the authoritative source; the cached inventory is only a fallback.
func accountForQuota(req pluginapi.QuotaFetchRequest) *Account {
	if len(req.StorageJSON) > 0 {
		var file accountFile
		if errUnmarshal := json.Unmarshal(req.StorageJSON, &file); errUnmarshal == nil {
			name := quotaMetadataString(req.Metadata, "name")
			account := &Account{
				Name:      firstNonEmpty(name, req.AuthID, req.AuthIndex, "anyrouter-account"),
				AuthIndex: req.AuthIndex,
				ID:        req.AuthID,
				File:      file,
			}
			if !strings.HasSuffix(strings.ToLower(account.Name), ".json") {
				account.Name += ".json"
			}
			return account
		}
	}
	if strings.TrimSpace(req.AuthIndex) != "" {
		for _, account := range accountsSnapshot() {
			if account.AuthIndex == req.AuthIndex || account.ID == req.AuthID {
				return account
			}
		}
	}
	return nil
}

// MetadataString reads one string value out of the request metadata map.
func quotaMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, okValue := metadata[key]
	if !okValue {
		return ""
	}
	asString, okString := value.(string)
	if !okString {
		return ""
	}
	return strings.TrimSpace(asString)
}

// quotaResponse maps a balance onto the normalised shape CPA renders. note is
// appended to the bucket description, e.g. to mark a stale fallback reading.
func quotaResponse(info *balanceInfo, cfg Config, note string) pluginapi.QuotaFetchResponse {
	if info == nil {
		return pluginapi.QuotaFetchResponse{}
	}
	perUnit := cfg.QuotaPerUnit
	if perUnit <= 0 {
		perUnit = defaultQuotaPerUnit
	}
	remaining := info.Quota / perUnit
	used := info.UsedQuota / perUnit
	total := remaining + used

	fraction := 0.0
	if total > 0 {
		fraction = remaining / total
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}

	plan := firstNonEmpty(info.Group, providerKey)
	description := "剩余 / 总量"
	if strings.TrimSpace(note) != "" {
		description = description + "（" + strings.TrimSpace(note) + "）"
	}
	resp := pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{Plan: plan, TierName: info.DisplayName},
		Summary: []pluginapi.QuotaMetric{
			{Key: "remaining", Label: "剩余额度", Value: round2(remaining), Format: "currency", Currency: "USD"},
			{Key: "used", Label: "已用额度", Value: round2(used), Format: "currency", Currency: "USD"},
			{Key: "requests", Label: "请求次数", Value: float64(info.RequestCount), Unit: "次", Format: "number"},
			{Key: "remaining_raw", Label: "剩余额度(原始)", Value: info.Quota, Unit: "quota", Format: "number"},
		},
		Groups: []pluginapi.QuotaGroup{{
			DisplayName: "AnyRouter 额度",
			Buckets: []pluginapi.QuotaBucket{{
				Window:            "total",
				RemainingFraction: fraction,
				Description:       description,
			}},
		}},
	}
	return resp
}

// handleQuotaReset reports that resetting is not supported.
func handleQuotaReset(request []byte) ([]byte, error) {
	return okEnvelope(pluginapi.QuotaResetResponse{
		Success: false,
		Message: "AnyRouter 不支持重置额度",
	})
}

func round2(value float64) float64 {
	if value < 0 {
		value = 0
	}
	return float64(int64(value*100+0.5)) / 100
}
