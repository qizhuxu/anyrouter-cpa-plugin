package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// withAuthConfig installs a config for one test and clears the remembered
// instance id, which is process state.
func withAuthConfig(t *testing.T, cfg Config) {
	t.Helper()
	previous := configState.cfg
	applyConfig(cfg)
	rememberResourceInstanceID("")
	t.Cleanup(func() {
		applyConfig(previous)
		rememberResourceInstanceID("")
	})
}

func decodeAuthEnvelope(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var envelope struct {
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("envelope is not JSON: %v (%s)", errUnmarshal, raw)
	}
	if !envelope.OK {
		t.Fatalf("envelope not ok: %s", raw)
	}
	return envelope.Result
}

// TestAuthLoginStartOpensTheSite: the interactive login must send the browser to
// AnyRouter itself, never to a hard-coded panel path (the host mounts the panel
// under the library-derived instance id).
func TestAuthLoginStartOpensTheSite(t *testing.T) {
	withAuthConfig(t, Config{BaseURL: defaultBaseURL, Timezone: "Local", Location: time.Local})

	raw, errHandle := handleAuthLoginStart(nil)
	if errHandle != nil {
		t.Fatalf("handleAuthLoginStart: %v", errHandle)
	}
	result := decodeAuthEnvelope(t, raw)
	if result["URL"] != "https://anyrouter.top/" {
		t.Fatalf("URL = %v, want the AnyRouter site root", result["URL"])
	}
	if result["State"] != "cookie-import" {
		t.Fatalf("State = %v", result["State"])
	}
	if _, ok := result["ExpiresAt"]; !ok {
		t.Fatalf("ExpiresAt missing: %#v", result)
	}
	metadata, _ := result["Metadata"].(map[string]any)
	if metadata == nil || metadata["flow"] != "cookie-import" {
		t.Fatalf("metadata = %#v, want flow=cookie-import", result["Metadata"])
	}
	if _, exists := metadata["panel_path"]; exists {
		t.Fatalf("panel_path must stay absent before the host reveals the instance id: %#v", metadata)
	}
}

func TestAuthLoginStartHonoursConfiguredBaseURL(t *testing.T) {
	withAuthConfig(t, Config{BaseURL: "https://mirror.example.com/anyrouter", Timezone: "Local", Location: time.Local})
	raw, errHandle := handleAuthLoginStart(nil)
	if errHandle != nil {
		t.Fatalf("handleAuthLoginStart: %v", errHandle)
	}
	if got := decodeAuthEnvelope(t, raw)["URL"]; got != "https://mirror.example.com/anyrouter/" {
		t.Fatalf("URL = %v, want the configured base URL with a trailing slash", got)
	}
}

// TestAuthLoginReportsTheRealPanelPath: once the host has dispatched a resource
// request, the plugin knows the instance id it was mounted under and can report
// the exact panel path in the login flow.
func TestAuthLoginReportsTheRealPanelPath(t *testing.T) {
	withAuthConfig(t, Config{BaseURL: defaultBaseURL, Timezone: "Local", Location: time.Local})

	payload, _ := json.Marshal(managementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/anyrouter-linux-amd64/panel"})
	if _, errHandle := handleManagement(payload); errHandle != nil {
		t.Fatalf("handleManagement: %v", errHandle)
	}

	startRaw, _ := handleAuthLoginStart(nil)
	start := decodeAuthEnvelope(t, startRaw)
	metadata, _ := start["Metadata"].(map[string]any)
	if metadata["panel_path"] != "/v0/resource/plugins/anyrouter-linux-amd64/panel" {
		t.Fatalf("panel_path = %v, want the renaming instance's real path", metadata["panel_path"])
	}
	// The user-facing target stays the site itself.
	if start["URL"] != "https://anyrouter.top/" {
		t.Fatalf("URL = %v, want the site", start["URL"])
	}

	pollRaw, _ := handleAuthLoginPoll(nil)
	poll := decodeAuthEnvelope(t, pollRaw)
	if poll["Status"] != string(pluginapi.AuthLoginStatusError) {
		t.Fatalf("poll status = %v", poll["Status"])
	}
	message, _ := poll["Message"].(string)
	for _, want := range []string{"https://anyrouter.top", "/v0/resource/plugins/anyrouter-linux-amd64/panel", "Cookie"} {
		if !strings.Contains(message, want) {
			t.Fatalf("poll message %q misses %q", message, want)
		}
	}
}
