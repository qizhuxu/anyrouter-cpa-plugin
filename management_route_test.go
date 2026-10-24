package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

// TestNormaliseRoutePath pins the mount parsing. The host mounts the plugin's
// resource under its own *instance* id, which can differ from the declared id
// (a renamed config key or a differently named library), so the segment must be
// treated as opaque. A substring search would slice "anyrouter-linux-amd64" in
// the middle and 404 the panel - the regression this test locks down.
func TestNormaliseRoutePathMounts(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		// management mount: the plugin declares these paths itself
		{"/v0/management/anyrouter/status", "/status"},
		{"/v0/management/anyrouter/account/disable", "/account/disable"},
		{"/v0/management/anyrouter", "/"},
		{"/v0/management/anyrouter/", "/"},
		// resource mount: the host inserts its instance id
		{"/v0/resource/plugins/anyrouter/panel", "/panel"},
		{"/v0/resource/plugins/anyrouter-linux-amd64/panel", "/panel"},
		{"/v0/resource/plugins/anyrouter-linux-arm64/panel", "/panel"},
		{"/v0/resource/plugins/anyrouter.v2/panel", "/panel"},
		{"/v0/resource/plugins/AnyRouter/panel", "/panel"},
		{"/v0/resource/plugins/plugin-7f3a/panel", "/panel"},
		{"/v0/resource/plugins/anyrouter", "/"},
		{"/v0/resource/plugins/anyrouter/", "/"},
		// instance id in front of the declared management path
		{"/v0/management/anyrouter-linux-amd64/status", "/status"},
		// legacy mount and already-stripped paths
		{"/plugins/anyrouter/status", "/status"},
		{"/anyrouter/status", "/status"},
		{"/status", "/status"},
		{"/panel", "/panel"},
		{"status", "/status"},
		{"", "/"},
		{"/", "/"},
		// trailing slash and unknown shapes
		{"/v0/resource/plugins/anyrouter/panel/", "/panel"},
		{"/v0/resource/plugins/anyrouter/nope", "/nope"},
	}
	for _, testCase := range cases {
		if got := normaliseRoutePath(testCase.raw); got != testCase.want {
			t.Errorf("normaliseRoutePath(%q) = %q, want %q", testCase.raw, got, testCase.want)
		}
	}
}

func TestResourceMountInstanceID(t *testing.T) {
	cases := map[string]string{
		"/v0/resource/plugins/anyrouter/panel":             "anyrouter",
		"/v0/resource/plugins/anyrouter-linux-amd64/panel": "anyrouter-linux-amd64",
		"/v0/resource/plugins/anyrouter":                   "anyrouter",
		"/v0/management/anyrouter/status":                  "",
		"/panel":                                           "",
	}
	for raw, want := range cases {
		if got := resourceMountInstanceID(raw); got != want {
			t.Errorf("resourceMountInstanceID(%q) = %q, want %q", raw, got, want)
		}
	}
}

// managementResult decodes one plugin management response envelope.
func managementResult(t *testing.T, raw []byte) (int, []byte) {
	t.Helper()
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       string `json:"Body"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("envelope is not JSON: %v (%s)", errUnmarshal, raw)
	}
	if !envelope.OK {
		t.Fatalf("envelope not ok: %s", raw)
	}
	body, errDecode := base64.StdEncoding.DecodeString(envelope.Result.Body)
	if errDecode != nil {
		t.Fatalf("body is not base64: %v", errDecode)
	}
	return envelope.Result.StatusCode, body
}

// TestPanelServedUnderRenamedInstanceID walks the real dispatch path with the
// instance id the host derived from a differently named library.
func TestPanelServedUnderRenamedInstanceID(t *testing.T) {
	for _, path := range []string{
		"/v0/resource/plugins/anyrouter/panel",
		"/v0/resource/plugins/anyrouter-linux-amd64/panel",
		"/v0/resource/plugins/anyrouter", // mount root: the panel is the only resource
	} {
		payload, errMarshal := json.Marshal(managementRequest{Method: http.MethodGet, Path: path})
		if errMarshal != nil {
			t.Fatalf("marshal request: %v", errMarshal)
		}
		raw, errHandle := handleManagement(payload)
		if errHandle != nil {
			t.Fatalf("handleManagement(%s): %v", path, errHandle)
		}
		status, body := managementResult(t, raw)
		if status != http.StatusOK {
			t.Fatalf("handleManagement(%s) status = %d, want 200 (%s)", path, status, body)
		}
		if len(body) == 0 || !containsFold(string(body), "<!doctype html") {
			t.Fatalf("handleManagement(%s) did not return the panel HTML", path)
		}
	}
}

// TestUnknownPathStillReported: genuinely unknown routes keep the JSON 404.
func TestUnknownPathStillReported(t *testing.T) {
	payload, _ := json.Marshal(managementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/anyrouter-linux-amd64/nope"})
	raw, errHandle := handleManagement(payload)
	if errHandle != nil {
		t.Fatalf("handleManagement: %v", errHandle)
	}
	status, _ := managementResult(t, raw)
	if status != http.StatusNotFound {
		t.Fatalf("unknown resource path status = %d, want 404", status)
	}
}

func containsFold(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			match := true
			for j := 0; j < len(needle); j++ {
				left, right := haystack[i+j], needle[j]
				if 'A' <= left && left <= 'Z' {
					left += 'a' - 'A'
				}
				if 'A' <= right && right <= 'Z' {
					right += 'a' - 'A'
				}
				if left != right {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
		return false
	})()
}
