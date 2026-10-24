package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// hostHTTPResponse builds the host reply envelope for one HTTP call.
func hostHTTPResponse(t *testing.T, status int, contentType string, body []byte) json.RawMessage {
	t.Helper()
	raw, errMarshal := json.Marshal(map[string]any{
		"StatusCode": status,
		"Headers":    map[string][]string{"content-type": {contentType}},
		"Body":       base64.StdEncoding.EncodeToString(body),
	})
	if errMarshal != nil {
		t.Fatalf("marshal host response: %v", errMarshal)
	}
	return raw
}

func TestChallengePageCandidatesPreferRootWhenFresh(t *testing.T) {
	client := &anyrouterClient{cfg: Config{BaseURL: "https://anyrouter.top"}}
	target := "https://anyrouter.top/api/user/self"

	first := client.challengePageCandidates(target, false)
	if len(first) == 0 || first[0] != target {
		t.Fatalf("first solve should try the requested URL first, got %v", first)
	}
	for _, candidate := range first {
		if strings.Contains(candidate, "_t=") {
			t.Fatalf("a first solve must not cache-bust: %v", first)
		}
	}

	fresh := client.challengePageCandidates(target, true)
	if len(fresh) == 0 || !strings.HasPrefix(fresh[0], "https://anyrouter.top/?_t=") {
		t.Fatalf("a fresh solve should start at the site root with a cache buster, got %v", fresh)
	}
	for _, candidate := range fresh {
		if !strings.Contains(candidate, "_t=") {
			t.Fatalf("every fresh candidate needs a cache buster: %v", fresh)
		}
	}
}

// TestAPIRequestResolvesStaleInterstitialFromRoot reproduces the observed
// upstream behaviour: a cookie that worked seconds ago is refused, and the copy
// served on the API path carries a nonce the server rejects. The retry must solve
// again from the site root instead of the API path.
//
// The stub tells the two kinds of traffic apart by method: the plugin's API call
// is a POST here, while solving the interstitial is always a GET.
func TestAPIRequestResolvesStaleInterstitialFromRoot(t *testing.T) {
	fixture, errRead := os.ReadFile(filepath.Clean(fixturePath))
	if errRead != nil {
		t.Fatalf("read fixture: %v", errRead)
	}
	target := "https://retry.example.test/api/user/self"

	var mu sync.Mutex
	gets := make([]string, 0, 8)
	posts := 0
	logs := make([]string, 0, 8)
	stubHost(t, func(method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostHTTPDo {
			if raw, errMarshal := json.Marshal(payload); errMarshal == nil {
				mu.Lock()
				logs = append(logs, string(raw))
				mu.Unlock()
			}
			return json.RawMessage(`{"StatusCode":200}`), nil
		}
		request, okRequest := payload.(pluginapi.HTTPRequest)
		if !okRequest {
			t.Fatalf("unexpected payload type %T", payload)
		}
		if request.Method == http.MethodPost {
			mu.Lock()
			posts++
			call := posts
			mu.Unlock()
			if call == 1 {
				// First API call is answered with the interstitial, exactly as the
				// real endpoint does when the cached cookie went stale.
				return hostHTTPResponse(t, http.StatusOK, "text/html; charset=utf-8", fixture), nil
			}
			return hostHTTPResponse(t, http.StatusOK, "application/json",
				[]byte(`{"success":true,"data":{"quota":1234,"used_quota":56}}`)), nil
		}
		mu.Lock()
		gets = append(gets, request.URL)
		mu.Unlock()
		// Solving always receives the real captured interstitial.
		return hostHTTPResponse(t, http.StatusOK, "text/html; charset=utf-8", fixture), nil
	})

	client := &anyrouterClient{cfg: Config{
		BaseURL:          "https://retry.example.test",
		UserAgent:        "test-agent",
		ChallengeEnabled: true,
		ChallengeTTL:     time.Minute,
		QuotaPerUnit:     defaultQuotaPerUnit,
		Timeout:          5 * time.Second,
	}}
	challengeStore.invalidate("retry.example.test|" + client.proxyFor(nil).key())
	account := &Account{Name: "retry.json", File: accountFile{Cookie: "session=abc", Provider: pluginID, Type: pluginID}}

	resp, errRequest := client.apiRequest(http.MethodPost, target, account, []byte(`{}`))
	if errRequest != nil {
		t.Fatalf("apiRequest: %v", errRequest)
	}
	if !strings.Contains(string(resp.Body), `"quota"`) {
		t.Fatalf("final response is not the API payload: %s", resp.Body)
	}

	mu.Lock()
	defer mu.Unlock()
	if posts != 2 {
		t.Fatalf("API calls = %d, want 2 (one intercepted, one resolved); logs: %s", posts, strings.Join(logs, "\n"))
	}
	rootSolve := -1
	for index, url := range gets {
		if strings.HasPrefix(url, "https://retry.example.test/?_t=") {
			rootSolve = index
		}
	}
	if rootSolve < 0 {
		t.Fatalf("no fresh root solve with a cache buster among %v; logs: %s", gets, strings.Join(logs, "\n"))
	}
	for _, url := range gets[rootSolve+1:] {
		if strings.Contains(url, "/api/user/self") && strings.Contains(url, "_t=") {
			t.Fatalf("the retry solved from the API path instead of the root: %v", gets)
		}
	}
}
