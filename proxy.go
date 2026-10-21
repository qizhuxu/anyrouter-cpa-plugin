package main

// Proxy handling for the plugin's own upstream calls.
//
// CPA already routes plugin traffic through its own proxy settings: every call
// made with host.http.do honours requests.proxy-url (verified in
// internal/pluginhost/http_bridge.go, priority: request override -> credential ->
// config). That stays the default and the recommended path.
//
// Because the host has no per-call proxy field for plugin HTTP, an explicit
// plugin- or account-level proxy_url is served by a plugin-owned HTTP client.
// Only that explicit case leaves the host transport; the log says so once per
// process, and the keywords direct/none mean "bypass every proxy".

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

const (
	proxyModeHost   = "host"
	proxyModeDirect = "direct"
	proxyModeURL    = "url"
)

// proxySetting is one resolved proxy decision.
type proxySetting struct {
	Mode string
	URL  *url.URL
	Raw  string
}

// key identifies the egress in the challenge cache: the anti-bot cookie is
// issued per egress IP, so a cookie solved through one proxy must never be
// reused on another.
func (p proxySetting) key() string {
	if p.Mode == proxyModeURL && p.URL != nil {
		return p.Mode + ":" + p.URL.String()
	}
	return p.Mode
}

func (p proxySetting) describe() string {
	switch p.Mode {
	case proxyModeDirect:
		return "直连（忽略所有代理）"
	case proxyModeURL:
		if p.URL != nil {
			return "代理 " + redactProxy(p.URL.String())
		}
	}
	return "宿主 transport（继承 CPA 的 requests.proxy-url）"
}

// redactProxy hides credentials inside a proxy URL before it reaches a log.
func redactProxy(raw string) string {
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.User == nil {
		return raw
	}
	parsed.User = url.UserPassword("***", "***")
	return parsed.String()
}

// parseProxySetting resolves one configuration value. An empty value means
// "host transport"; direct/none mean "no proxy at all".
func parseProxySetting(raw string) (proxySetting, error) {
	value := strings.TrimSpace(raw)
	switch strings.ToLower(value) {
	case "":
		return proxySetting{Mode: proxyModeHost}, nil
	case "direct", "none":
		return proxySetting{Mode: proxyModeDirect, Raw: value}, nil
	}
	parsed, errParse := url.Parse(value)
	if errParse != nil {
		return proxySetting{}, errParse
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return proxySetting{}, fmt.Errorf("仅支持 http / https / socks5 / socks5h，当前为 %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return proxySetting{}, fmt.Errorf("缺少主机名")
	}
	return proxySetting{Mode: proxyModeURL, URL: parsed, Raw: value}, nil
}

func validateProxySetting(raw string) error {
	_, errParse := parseProxySetting(raw)
	return errParse
}

// transport builds the transports used by the plugin-owned client.
func (p proxySetting) transport() (*http.Transport, error) {
	transport := &http.Transport{
		MaxIdleConns:          4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if p.Mode != proxyModeURL || p.URL == nil {
		return transport, nil
	}
	switch strings.ToLower(p.URL.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(p.URL)
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if p.URL.User != nil {
			password, _ := p.URL.User.Password()
			auth = &proxy.Auth{User: p.URL.User.Username(), Password: password}
		}
		dialer, errDialer := proxy.SOCKS5("tcp", p.URL.Host, auth, proxy.Direct)
		if errDialer != nil {
			return nil, fmt.Errorf("构建 SOCKS5 拨号器失败: %w", errDialer)
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			if contextDialer, okDialer := dialer.(proxy.ContextDialer); okDialer {
				return contextDialer.DialContext(ctx, network, address)
			}
			return dialer.Dial(network, address)
		}
	}
	return transport, nil
}

// directClientWarnOnce keeps the "left the host transport" notice to one line
// per process instead of one per request.
var directClientWarnOnce sync.Once

func noteOwnTransport(setting proxySetting) {
	directClientWarnOnce.Do(func() {
		hostLogf("info", "已按插件配置使用自有 HTTP 客户端（%s）：此路径不经过宿主的请求日志与传输策略，未配置 proxy_url 时应留空以走宿主 transport", setting.describe())
	})
}
