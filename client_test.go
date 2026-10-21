package main

import (
	"bytes"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// stubHost swaps the host callback transport for the duration of one test.
func stubHost(t *testing.T, handler func(method string, payload any) (json.RawMessage, error)) *[]string {
	t.Helper()
	previous := hostCallFunc
	seen := make([]string, 0, 4)
	var mu sync.Mutex
	hostCallFunc = func(method string, payload any) (json.RawMessage, error) {
		mu.Lock()
		seen = append(seen, method)
		mu.Unlock()
		return handler(method, payload)
	}
	hostReady.Store(true)
	t.Cleanup(func() {
		hostCallFunc = previous
		hostReady.Store(false)
	})
	return &seen
}

func TestDoWithTimeoutReportsTimeout(t *testing.T) {
	stubHost(t, func(method string, payload any) (json.RawMessage, error) {
		time.Sleep(400 * time.Millisecond)
		return json.RawMessage(`{"StatusCode":200}`), nil
	})
	client := &anyrouterClient{cfg: Config{Timeout: 30 * time.Millisecond}}
	start := time.Now()
	_, errDo := client.doWithTimeout(pluginapi.HTTPRequest{Method: "GET", URL: "https://anyrouter.top/"})
	if errDo == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(errDo.Error(), "超时") {
		t.Fatalf("error = %v, want a timeout message", errDo)
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("timeout was not enforced promptly: %s", elapsed)
	}
}

func TestDoWithTimeoutReturnsResponse(t *testing.T) {
	stubHost(t, func(method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatalf("unexpected host method %q", method)
		}
		raw, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			t.Fatalf("marshal payload: %v", errMarshal)
		}
		// The wire contract: the host decodes Go field names.
		for _, key := range []string{`"Method"`, `"URL"`, `"Headers"`} {
			if !strings.Contains(string(raw), key) {
				t.Fatalf("host.http.do payload misses %s: %s", key, string(raw))
			}
		}
		return json.RawMessage(`{"StatusCode":200,"Headers":{"content-type":["application/json"]},"Body":"eyJvayI6dHJ1ZX0="}`), nil
	})
	client := &anyrouterClient{cfg: Config{Timeout: time.Second}}
	resp, errDo := client.doWithTimeout(pluginapi.HTTPRequest{Method: "GET", URL: "https://anyrouter.top/"})
	if errDo != nil {
		t.Fatalf("doWithTimeout: %v", errDo)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Fatalf("body = %q, want the base64 decoded payload", string(resp.Body))
	}
}

func TestHostAuthHelpersDecodeResponses(t *testing.T) {
	stubHost(t, func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostAuthList:
			return json.RawMessage(`{"files":[{"name":"anyrouter-a.json","auth_index":"idx-1","type":"anyrouter","provider":"anyrouter"},{"name":"codex.json","auth_index":"idx-2","type":"codex"}]}`), nil
		case pluginabi.MethodHostAuthGet:
			return json.RawMessage(`{"auth_index":"idx-1","name":"anyrouter-a.json","json":{"type":"anyrouter","cookie":"session=1"}}`), nil
		case pluginabi.MethodHostAuthSave:
			return json.RawMessage(`{"name":"anyrouter-a.json","path":"/auths/anyrouter-a.json"}`), nil
		default:
			return nil, errHostUnavailable
		}
	})

	files, errList := hostAuthList()
	if errList != nil {
		t.Fatalf("hostAuthList: %v", errList)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	anyrouterCount := 0
	for _, entry := range files {
		if isAnyrouterEntry(entry) {
			anyrouterCount++
		}
	}
	if anyrouterCount != 1 {
		t.Fatalf("isAnyrouterEntry matched %d entries, want 1", anyrouterCount)
	}

	got, errGet := hostAuthGet("idx-1")
	if errGet != nil {
		t.Fatalf("hostAuthGet: %v", errGet)
	}
	var file accountFile
	if errUnmarshal := json.Unmarshal(got.JSON, &file); errUnmarshal != nil {
		t.Fatalf("decode auth json: %v", errUnmarshal)
	}
	if file.Cookie != "session=1" {
		t.Fatalf("cookie = %q", file.Cookie)
	}

	saved, errSave := hostAuthSave("anyrouter-a.json", json.RawMessage(`{"type":"anyrouter"}`))
	if errSave != nil {
		t.Fatalf("hostAuthSave: %v", errSave)
	}
	if saved.Path == "" {
		t.Fatal("save response path is empty")
	}
}

func TestSessionUserIDDerivation(t *testing.T) {
	// Build a session cookie in the shape AnyRouter serves:
	//   session = base64("<number>|<base64(gob session map)>|<signature>")
	session := map[interface{}]interface{}{
		"id":       123456,
		"username": "tester",
		"role":     1,
		"status":   1,
	}
	var blob bytes.Buffer
	if errEncode := gob.NewEncoder(&blob).Encode(session); errEncode != nil {
		t.Fatalf("gob encode: %v", errEncode)
	}
	middle := base64.RawURLEncoding.EncodeToString(blob.Bytes())
	outer := []byte("1790619872|" + middle + "|0123456789abcdef")
	cookie := "session=" + base64.RawURLEncoding.EncodeToString(outer)

	if got := sessionUserID(cookie); got != "123456" {
		t.Fatalf("sessionUserID = %q, want 123456", got)
	}

	account := &Account{File: accountFile{Cookie: cookie}}
	if got := resolveUserID(account); got != "123456" {
		t.Fatalf("resolveUserID = %q, want the derived id", got)
	}
	// An explicit value always wins over the derived one.
	account.File.UserID = "999"
	if got := resolveUserID(account); got != "999" {
		t.Fatalf("resolveUserID = %q, want the configured id", got)
	}

	// The header is attached when an id is known, and the value must match the
	// session user, so both sources are exercised here.
	client := &anyrouterClient{cfg: Config{BaseURL: "https://anyrouter.top", UserAgent: "ua"}}
	account.File.UserID = ""
	header := client.headers(account, "https://anyrouter.top/api/user/self", "", false)
	if header.Get("New-Api-User") != "123456" {
		t.Fatalf("New-Api-User = %q", header.Get("New-Api-User"))
	}
}

func TestSessionUserIDRejectsUnexpectedShapes(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"no session field":  "other=1",
		"not base64":        "session=!!!!",
		"no separator":      "session=" + base64.RawURLEncoding.EncodeToString([]byte("just text")),
		"middle not base64": "session=" + base64.RawURLEncoding.EncodeToString([]byte("1790619872|!!!!|sig")),
		"gob without id":    "session=" + base64.RawURLEncoding.EncodeToString([]byte("1790619872|"+base64.RawURLEncoding.EncodeToString([]byte("not-gob"))+"|sig")),
	}
	for name, cookie := range cases {
		if got := sessionUserID(cookie); got != "" {
			t.Fatalf("%s: sessionUserID = %q, want empty", name, got)
		}
	}
}

func TestHeadersOmitUserIDWhenUnknown(t *testing.T) {
	client := &anyrouterClient{cfg: Config{BaseURL: "https://anyrouter.top", UserAgent: "ua"}}
	account := &Account{File: accountFile{Cookie: "session=opaque"}}
	header := client.headers(account, "https://anyrouter.top/api/user/self", "", false)
	if got := header.Get("New-Api-User"); got != "" {
		t.Fatalf("New-Api-User = %q, want it omitted when the id is unknown", got)
	}
}

func TestRedactCredentialsRemovesCookieValues(t *testing.T) {
	account := &Account{File: accountFile{Cookie: "session=supersecretvalue; other=alsosecret"}}
	text := "denied for session=supersecretvalue and alsosecret plus acw_sc__v2=deadbeefcafe"
	redacted := redactCredentials(text, account)
	for _, secret := range []string{"supersecretvalue", "alsosecret", "deadbeefcafe"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted text still contains %q: %s", secret, redacted)
		}
	}
}

func TestHeadersCarrySessionAndChallengeCookies(t *testing.T) {
	client := &anyrouterClient{cfg: Config{UserAgent: "ua-test", BaseURL: "https://anyrouter.top"}}
	account := &Account{File: accountFile{Cookie: "session=abc"}}
	header := client.headers(account, "https://anyrouter.top/api/user/self", "acw_sc__v2=deadbeef", false)
	cookie := header.Get("cookie")
	if !strings.Contains(cookie, "session=abc") || !strings.Contains(cookie, "acw_sc__v2=deadbeef") {
		t.Fatalf("cookie header = %q", cookie)
	}
	if header.Get("user-agent") != "ua-test" {
		t.Fatalf("user-agent = %q", header.Get("user-agent"))
	}
	if header.Get("origin") != "https://anyrouter.top" {
		t.Fatalf("origin = %q", header.Get("origin"))
	}

	// A session cookie that already ends with a separator must not produce ";;".
	account.File.Cookie = "session=abc;"
	header = client.headers(account, "https://anyrouter.top/api/user/self", "acw_sc__v2=deadbeef", false)
	if got := header.Get("cookie"); got != "session=abc; acw_sc__v2=deadbeef" {
		t.Fatalf("cookie header = %q", got)
	}
}

func TestStripAntiDebugTrap(t *testing.T) {
	payload := `var arg1='AA';function f(){(function(){var B=new RegExp('x'),D=b('toString');!B.test(D+'y')?D('0'):b();})();}for(var m=[1],p='k';m.length;){document.cookie='acw_sc__v2='+p;}`
	patched := stripAntiDebugTrap(payload)
	if strings.Contains(patched, ":b();") {
		t.Fatalf("trap survived: %s", patched)
	}
	if !strings.Contains(patched, "?0:0;") {
		t.Fatalf("trap was not neutralised in place: %s", patched)
	}
	if !strings.Contains(patched, "acw_sc__v2=") {
		t.Fatal("the cookie computation must be preserved")
	}
	// Unrelated scripts are never rewritten.
	plain := `var x = cond ? f('0') : g();`
	if got := stripAntiDebugTrap(plain); got != plain {
		t.Fatalf("non-challenge script was modified: %s", got)
	}
}
