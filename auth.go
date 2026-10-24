package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// handleAuthParse claims auth files that belong to AnyRouter. CPA asks the
// plugin about a file selected by its "type" field (or, when unset, offers the
// file to every plugin), so returning Handled=false for anything else is part
// of the contract.
func handleAuthParse(request []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return errorEnvelope("invalid_request", "malformed auth.parse request: "+errUnmarshal.Error(), http.StatusBadRequest), nil
		}
	}

	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider != "" && provider != providerKey {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	var file accountFile
	if len(req.RawJSON) > 0 {
		_ = json.Unmarshal(req.RawJSON, &file)
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" && req.Path != "" {
		fileName = filepath.Base(req.Path)
	}
	claimed := strings.EqualFold(strings.TrimSpace(file.Type), providerKey) ||
		strings.EqualFold(strings.TrimSpace(file.Provider), providerKey) ||
		strings.HasPrefix(strings.ToLower(fileName), authFilePrefix)
	if !claimed {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	label := firstNonEmpty(file.Remark, file.Note, strings.TrimSuffix(fileName, ".json"))
	auth := pluginapi.AuthData{
		Provider:    providerKey,
		ID:          firstNonEmpty(fileName, req.Path),
		FileName:    fileName,
		Label:       label,
		StorageJSON: append([]byte(nil), req.RawJSON...),
		// Metadata stays descriptive and minimal: the host copies every auth
		// file key into the runtime metadata itself, so repeating volatile
		// fields here would let a stale value be merged back into the file.
		Metadata: map[string]any{
			"type":   providerKey,
			"remark": label,
		},
	}
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: auth})
}

// handleAuthLoginStart answers the interactive-login probe. AnyRouter has no
// OAuth or device-code flow: an account is imported by pasting a browser Cookie,
// so the login step opens the site itself (where the user signs in) and the poll
// step explains how to finish the import in the panel.
//
// The URL must not be a hard-coded panel path: the host names the plugin instance
// after the library file, so the resource mount can carry a different id than the
// declared one. When the plugin has already served a request it knows that id and
// reports the exact panel path as metadata; otherwise the site URL is the only
// safe target.
func handleAuthLoginStart(request []byte) ([]byte, error) {
	cfg := currentConfig()
	loginURL := strings.TrimRight(firstNonEmpty(cfg.BaseURL, defaultBaseURL), "/") + "/"

	metadata := map[string]any{
		"flow":  "cookie-import",
		"steps": "在浏览器登录后，从 DevTools → Network 复制任意 api/user 请求的完整 Cookie 请求头，粘贴到插件面板的「新增账号」表单。",
	}
	if panelPath := knownPanelPath(); panelPath != "" {
		metadata["panel_path"] = panelPath
	}

	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  providerKey,
		URL:       loginURL,
		State:     "cookie-import",
		ExpiresAt: time.Now().Add(30 * time.Minute),
		Metadata:  metadata,
	})
}

// knownPanelPath returns the plugin's panel path when the host has revealed the
// instance id it mounted this plugin under, and "" otherwise.
func knownPanelPath() string {
	instanceID := knownResourceInstanceID()
	if instanceID == "" {
		return ""
	}
	return resourceMountPrefix + instanceID + "/panel"
}

func handleAuthLoginPoll(request []byte) ([]byte, error) {
	cfg := currentConfig()
	message := "AnyRouter 不支持交互式登录：请在 " +
		strings.TrimRight(firstNonEmpty(cfg.BaseURL, defaultBaseURL), "/") +
		" 登录后，把浏览器 Cookie 粘贴到插件面板的「新增账号」表单完成导入。"
	if panelPath := knownPanelPath(); panelPath != "" {
		message += "（面板：" + panelPath + "）"
	}
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusError,
		Message: message,
	})
}

// handleAuthRefresh is a no-op refresh: a cookie only changes when the user
// pastes a new one, and the host already owns the file contents.
func handleAuthRefresh(request []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return errorEnvelope("invalid_request", "malformed auth.refresh request: "+errUnmarshal.Error(), http.StatusBadRequest), nil
		}
	}
	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth: pluginapi.AuthData{
			Provider:    providerKey,
			ID:          req.AuthID,
			StorageJSON: req.StorageJSON,
			Metadata:    req.Metadata,
			Attributes:  req.Attributes,
		},
	})
}
