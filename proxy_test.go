package main

import (
	"strings"
	"testing"
)

func TestParseProxySetting(t *testing.T) {
	cases := []struct {
		value string
		mode  string
	}{
		{"", proxyModeHost},
		{"   ", proxyModeHost},
		{"direct", proxyModeDirect},
		{"DIRECT", proxyModeDirect},
		{"none", proxyModeDirect},
		{"http://127.0.0.1:10808", proxyModeURL},
		{"https://proxy.example.com:8443", proxyModeURL},
		{"socks5://127.0.0.1:1080", proxyModeURL},
		{"socks5h://user:pass@127.0.0.1:1080", proxyModeURL},
	}
	for _, testCase := range cases {
		setting, errParse := parseProxySetting(testCase.value)
		if errParse != nil {
			t.Fatalf("parseProxySetting(%q): %v", testCase.value, errParse)
		}
		if setting.Mode != testCase.mode {
			t.Fatalf("parseProxySetting(%q) mode = %q, want %q", testCase.value, setting.Mode, testCase.mode)
		}
	}
}

func TestParseProxySettingRejectsUnsupportedValues(t *testing.T) {
	for _, value := range []string{"ftp://proxy:21", "http://", "socks://127.0.0.1:1080", "://nope"} {
		if _, errParse := parseProxySetting(value); errParse == nil {
			t.Fatalf("parseProxySetting(%q) must fail", value)
		}
	}
}

func TestProxyRedactionHidesCredentials(t *testing.T) {
	setting, errParse := parseProxySetting("socks5h://alice:s3cret@127.0.0.1:1080")
	if errParse != nil {
		t.Fatalf("parse: %v", errParse)
	}
	description := setting.describe()
	if strings.Contains(description, "s3cret") || strings.Contains(description, "alice") {
		t.Fatalf("describe() leaked proxy credentials: %s", description)
	}
}

// TestProxyPrecedence pins the resolution order: account value, then the
// plugin-wide value, then the host transport.
func TestProxyPrecedence(t *testing.T) {
	client := &anyrouterClient{cfg: Config{ProxyURL: "http://127.0.0.1:10808"}}

	if setting := client.proxyFor(nil); setting.key() != "url:http://127.0.0.1:10808" {
		t.Fatalf("plugin-level proxy not used: %s", setting.key())
	}
	account := &Account{File: accountFile{ProxyURL: "socks5://127.0.0.1:1080"}}
	if setting := client.proxyFor(account); setting.key() != "url:socks5://127.0.0.1:1080" {
		t.Fatalf("account proxy must win: %s", setting.key())
	}
	// direct/none on the account bypasses the plugin-wide proxy entirely.
	account.File.ProxyURL = "direct"
	if setting := client.proxyFor(account); setting.Mode != proxyModeDirect {
		t.Fatalf("account direct must win: %+v", setting)
	}
	// no plugin value and no account value -> host transport
	plain := &anyrouterClient{cfg: Config{}}
	if setting := plain.proxyFor(&Account{}); setting.Mode != proxyModeHost {
		t.Fatalf("expected host transport, got %+v", setting)
	}
}

// TestChallengeCacheKeySeparatesEgress: the anti-bot cookie is issued per egress
// IP, so accounts with different proxies must not share a cached cookie.
func TestChallengeCacheKeySeparatesEgress(t *testing.T) {
	direct := &anyrouterClient{cfg: Config{ProxyURL: "direct"}}
	proxied := &anyrouterClient{cfg: Config{ProxyURL: "http://127.0.0.1:10808"}}
	hostOnly := &anyrouterClient{cfg: Config{}}

	keys := map[string]string{
		"direct":  "anyrouter.top|" + direct.proxyFor(nil).key(),
		"proxied": "anyrouter.top|" + proxied.proxyFor(nil).key(),
		"host":    "anyrouter.top|" + hostOnly.proxyFor(nil).key(),
	}
	if keys["direct"] == keys["proxied"] || keys["direct"] == keys["host"] || keys["proxied"] == keys["host"] {
		t.Fatalf("egress keys must differ: %#v", keys)
	}
}

func TestTransportSelection(t *testing.T) {
	httpSetting, _ := parseProxySetting("http://127.0.0.1:10808")
	transport, errTransport := httpSetting.transport()
	if errTransport != nil {
		t.Fatalf("http proxy transport: %v", errTransport)
	}
	if transport.Proxy == nil {
		t.Fatal("http proxy must set transport.Proxy")
	}

	socksSetting, _ := parseProxySetting("socks5://127.0.0.1:1080")
	socksTransport, errSocks := socksSetting.transport()
	if errSocks != nil {
		t.Fatalf("socks5 transport: %v", errSocks)
	}
	if socksTransport.DialContext == nil || socksTransport.Proxy != nil {
		t.Fatal("socks5 must use a custom dialer, not transport.Proxy")
	}

	directSetting, _ := parseProxySetting("direct")
	directTransport, errDirect := directSetting.transport()
	if errDirect != nil {
		t.Fatalf("direct transport: %v", errDirect)
	}
	if directTransport.Proxy != nil || directTransport.DialContext != nil {
		t.Fatal("direct must not install any proxy")
	}
}

func TestLoadConfigProxyValidation(t *testing.T) {
	cfg, errLoad := loadConfig([]byte("proxy_url: \"socks5://127.0.0.1:1080\""))
	if errLoad != nil {
		t.Fatalf("loadConfig: %v", errLoad)
	}
	if cfg.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("ProxyURL = %q", cfg.ProxyURL)
	}
	if _, errBad := loadConfig([]byte("proxy_url: \"ftp://127.0.0.1:21\"")); errBad == nil {
		t.Fatal("invalid proxy scheme must be rejected")
	}
}
