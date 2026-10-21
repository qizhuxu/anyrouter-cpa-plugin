package main

// Host callbacks (plugin -> CPA) live here. The transport is swapped in by the
// cgo build (cabi.go); this file stays cgo free so the package still builds and
// unit tests still run with CGO_ENABLED=0.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// errHostUnavailable is returned when the plugin runs outside a CPA process
// (unit tests) or before cliproxy_plugin_init handed over the host table.
var errHostUnavailable = errors.New("host callbacks are unavailable")

// hostCallFunc performs one host RPC. cabi.go replaces it with the cgo
// implementation during cliproxy_plugin_init.
var hostCallFunc = func(method string, payload any) (json.RawMessage, error) {
	return nil, errHostUnavailable
}

// hostReady reports whether cliproxy_plugin_init already handed over the host
// function table. Background loops wait for it instead of failing once.
var hostReady atomic.Bool

func callHost(method string, payload any) (json.RawMessage, error) {
	if !hostReady.Load() {
		return nil, errHostUnavailable
	}
	return hostCallFunc(method, payload)
}

func hostAvailable() bool { return hostReady.Load() }

// hostLog writes a structured line to the CPA log. Never pass credential
// values in message or fields.
func hostLog(level, message string, fields map[string]any) {
	payload := map[string]any{
		"level":   level,
		"message": "[anyrouter] " + message,
	}
	if len(fields) > 0 {
		payload["fields"] = fields
	}
	_, _ = callHost(pluginabi.MethodHostLog, payload)
}

func hostLogf(level, format string, args ...any) {
	hostLog(level, fmt.Sprintf(format, args...), nil)
}

// hostHTTPDo performs an HTTP request through the host transport. Routing
// plugin traffic through the host keeps proxy settings, TLS policy and the
// request log in one place.
func hostHTTPDo(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	raw, errCall := callHost(pluginabi.MethodHostHTTPDo, req)
	if errCall != nil {
		return pluginapi.HTTPResponse{}, errCall
	}
	if len(raw) == 0 {
		return pluginapi.HTTPResponse{}, errors.New("host.http.do returned an empty response")
	}
	var resp pluginapi.HTTPResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return pluginapi.HTTPResponse{}, fmt.Errorf("decode host.http.do response: %w", errUnmarshal)
	}
	return resp, nil
}

// hostAuthList returns every credential the host knows about.
func hostAuthList() ([]pluginapi.HostAuthFileEntry, error) {
	raw, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, errCall
	}
	var resp struct {
		Files []pluginapi.HostAuthFileEntry `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.list response: %w", errUnmarshal)
	}
	return resp.Files, nil
}

// hostAuthGet returns the raw JSON of one physical auth file.
func hostAuthGet(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	raw, errCall := callHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return pluginapi.HostAuthGetResponse{}, errCall
	}
	var resp pluginapi.HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host.auth.get response: %w", errUnmarshal)
	}
	return resp, nil
}

// hostAuthSave writes an auth file into the host auth directory and reloads it
// as a credential. The name must end with ".json".
func hostAuthSave(name string, payload json.RawMessage) (pluginapi.HostAuthSaveResponse, error) {
	raw, errCall := callHost(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{
		Name: name,
		JSON: payload,
	})
	if errCall != nil {
		return pluginapi.HostAuthSaveResponse{}, errCall
	}
	var resp pluginapi.HostAuthSaveResponse
	if errUnmarshal := json.Unmarshal(raw, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthSaveResponse{}, fmt.Errorf("decode host.auth.save response: %w", errUnmarshal)
	}
	return resp, nil
}
