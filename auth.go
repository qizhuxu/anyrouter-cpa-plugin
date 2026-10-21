package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

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
// OAuth or device-code flow: accounts are imported by pasting a browser Cookie,
// so the start step points at the plugin panel and the poll step explains it.
func handleAuthLoginStart(request []byte) ([]byte, error) {
	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider: providerKey,
		URL:      "/v0/resource/plugins/" + pluginID + "/panel",
		State:    "cookie-import",
	})
}

func handleAuthLoginPoll(request []byte) ([]byte, error) {
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusError,
		Message: "AnyRouter 不支持交互式登录，请在插件面板粘贴浏览器 Cookie 导入账号。",
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
