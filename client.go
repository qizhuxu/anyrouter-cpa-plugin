package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// anyrouterClient talks to AnyRouter through the host HTTP callback so the
// configured upstream proxy, TLS policy and request log all still apply.
type anyrouterClient struct {
	cfg Config
}

func newClient() *anyrouterClient {
	return &anyrouterClient{cfg: currentConfig()}
}

// checkinStatus values reported by classifySignIn.
const (
	checkinSuccess = "success"
	checkinAlready = "already"
	checkinFailed  = "failed"
	checkinUnknown = "unknown"
)

// checkinOutcome is the classified result of one sign-in attempt.
type checkinOutcome struct {
	Status     string
	OK         bool
	Message    string
	HTTPStatus int
	Raw        string
	At         time.Time
}

// balanceInfo is a normalised view of the upstream account payload.
type balanceInfo struct {
	Quota        float64
	UsedQuota    float64
	RequestCount int64
	Username     string
	DisplayName  string
	Email        string
	Group        string
	Extra        map[string]any
	FetchedAt    time.Time
}

// Total returns quota + used_quota, the cycle capacity when upstream exposes no
// explicit total.
func (b *balanceInfo) Total() float64 {
	if b == nil {
		return 0
	}
	return b.Quota + b.UsedQuota
}

// proxyFor resolves the egress for one account: the account value wins, then the
// plugin-wide value, otherwise the host transport.
func (c *anyrouterClient) proxyFor(account *Account) proxySetting {
	if account != nil {
		if setting, errParse := parseProxySetting(account.File.ProxyURL); errParse == nil && setting.Mode != proxyModeHost {
			return setting
		}
	}
	if setting, errParse := parseProxySetting(c.cfg.ProxyURL); errParse == nil {
		return setting
	}
	return proxySetting{Mode: proxyModeHost}
}

// do performs one upstream call, either through the host transport (default:
// inherits CPA proxy + request log) or through the plugin-owned client when a
// proxy_url was configured explicitly.
func (c *anyrouterClient) do(req pluginapi.HTTPRequest, account *Account) (pluginapi.HTTPResponse, error) {
	setting := c.proxyFor(account)
	if setting.Mode == proxyModeHost {
		return c.doWithTimeout(req)
	}
	noteOwnTransport(setting)
	return c.doDirect(req, setting)
}

// doDirect performs the request with a plugin-owned client bound to the resolved
// proxy. Used only when proxy_url/direct is configured explicitly.
func (c *anyrouterClient) doDirect(req pluginapi.HTTPRequest, setting proxySetting) (pluginapi.HTTPResponse, error) {
	transport, errTransport := setting.transport()
	if errTransport != nil {
		return pluginapi.HTTPResponse{}, errTransport
	}
	defer transport.CloseIdleConnections()

	timeout := c.cfg.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultTimeoutSec) * time.Second
	}
	client := &http.Client{Timeout: timeout, Transport: transport}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, errRequest := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if errRequest != nil {
		return pluginapi.HTTPResponse{}, errRequest
	}
	for key, values := range req.Headers {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return pluginapi.HTTPResponse{}, errDo
	}
	defer func() { _ = resp.Body.Close() }()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, maxDirectBodyBytes))
	if errRead != nil {
		return pluginapi.HTTPResponse{}, errRead
	}
	return pluginapi.HTTPResponse{StatusCode: resp.StatusCode, Headers: resp.Header, Body: raw}, nil
}

// maxDirectBodyBytes caps plugin-owned reads (JSON payloads; the challenge page
// is a few KB).
const maxDirectBodyBytes = 8 << 20

// doWithTimeout runs one host HTTP call under the configured deadline. The host
// callback has no timeout parameter of its own, so the plugin stops waiting
// itself; the buffered channel lets the host side finish without leaking.
func (c *anyrouterClient) doWithTimeout(req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	timeout := c.cfg.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultTimeoutSec) * time.Second
	}
	type outcome struct {
		resp pluginapi.HTTPResponse
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, errCall := hostHTTPDo(req)
		done <- outcome{resp: resp, err: errCall}
	}()
	select {
	case result := <-done:
		return result.resp, result.err
	case <-time.After(timeout):
		return pluginapi.HTTPResponse{}, fmt.Errorf("请求超时（%s）: %s %s", timeout, req.Method, req.URL)
	}
}

// resolveUserID returns the new-api user id for an account: the explicitly
// configured value wins, otherwise it is derived from the session cookie.
func resolveUserID(account *Account) string {
	if account == nil {
		return ""
	}
	if explicit := strings.TrimSpace(account.File.UserID); explicit != "" {
		return explicit
	}
	return sessionUserID(account.File.Cookie)
}

// sessionUserID extracts the numeric user id new-api expects in the
// New-Api-User header from the AnyRouter session cookie.
//
// The cookie is base64 of "<number>|<base64(gob session map)>|<signature>", and
// the gob map carries the id as nint. Anything unexpected yields "" and the
// caller simply omits the header (the user can then set it by hand).
func sessionUserID(cookie string) string {
	payload := sessionGobPayload(cookie)
	if len(payload) == 0 {
		return ""
	}
	var session map[interface{}]interface{}
	if errDecode := gob.NewDecoder(bytes.NewReader(payload)).Decode(&session); errDecode != nil {
		return ""
	}
	switch typed := session["id"].(type) {
	case int:
		if typed > 0 {
			return strconv.Itoa(typed)
		}
	case int64:
		if typed > 0 {
			return strconv.FormatInt(typed, 10)
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed != "" && isDigits(trimmed) {
			return trimmed
		}
	}
	return ""
}

// sessionGobPayload unwraps the cookie down to the gob encoded session map.
func sessionGobPayload(cookie string) []byte {
	value := ""
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "session=") {
			value = strings.TrimSpace(strings.TrimPrefix(part, "session="))
		}
	}
	if value == "" {
		return nil
	}
	outer := decodeBase64Loose(value)
	if len(outer) == 0 || len(outer) > 8192 {
		return nil
	}
	first := bytes.IndexByte(outer, '|')
	if first < 0 {
		return nil
	}
	rest := outer[first+1:]
	if end := bytes.IndexByte(rest, '|'); end >= 0 {
		rest = rest[:end]
	}
	if len(rest) == 0 || len(rest) > 8192 {
		return nil
	}
	return decodeBase64Loose(string(rest))
}

// decodeBase64Loose accepts padded and unpadded standard/URL-safe base64.
func decodeBase64Loose(value string) []byte {
	for _, decoder := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if out, errDecode := decoder.DecodeString(value); errDecode == nil && len(out) > 0 {
			return out
		}
	}
	return nil
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// headers builds the request headers for one upstream call.
func (c *anyrouterClient) headers(account *Account, target string, challenge string, withBody bool) http.Header {
	header := http.Header{}
	header.Set("user-agent", c.cfg.UserAgent)
	header.Set("accept", "application/json, text/plain, */*")
	header.Set("accept-language", "zh-CN,zh;q=0.9,en;q=0.8")
	header.Set("cache-control", "no-cache")
	header.Set("pragma", "no-cache")

	base := c.cfg.BaseURL
	if account != nil {
		base = account.effectiveBase(c.cfg)
	}
	header.Set("origin", base)
	referer := base + "/"
	if parsed, errParse := url.Parse(target); errParse == nil && parsed.Path != "" {
		referer = parsed.Scheme + "://" + parsed.Host + "/"
	}
	header.Set("referer", referer)

	// The session cookie the user pasted, plus the anti-bot cookie, joined with
	// the canonical "; " separator. Trailing separators in the pasted value are
	// trimmed so the header never contains ";;".
	parts := make([]string, 0, 2)
	if account != nil {
		if session := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(account.File.Cookie), ";")); session != "" {
			parts = append(parts, session)
		}
	}
	if challenge != "" {
		parts = append(parts, strings.TrimSpace(strings.TrimRight(challenge, ";")))
	}
	if len(parts) > 0 {
		header.Set("cookie", strings.Join(parts, "; "))
	}
	// new-api (AnyRouter runs on it) rejects most dashboard endpoints unless the
	// caller repeats the numeric user id in this header, even with a valid
	// session cookie. Check-in does not need it; the balance endpoint does.
	if account != nil {
		if userID := resolveUserID(account); userID != "" {
			header.Set("New-Api-User", userID)
		}
	}
	if withBody {
		header.Set("content-type", "application/json")
	}
	header.Set("x-requested-with", "XMLHttpRequest")
	return header
}

// ensureChallenge returns the cached anti-bot cookie for the target host,
// solving the interstitial when the cache is cold.
//
// The challenge is solved from the failing request's own URL first and only
// then from the site root: the original Deno wrapper did the same, and some
// 加速乐 deployments hand out a challenge bound to the requested path, so a
// cookie solved from "/" is not accepted everywhere.
func (c *anyrouterClient) ensureChallenge(target string, account *Account, fresh bool) string {
	if !c.cfg.ChallengeEnabled {
		return ""
	}
	parsed, errParse := url.Parse(target)
	if errParse != nil || parsed.Host == "" {
		return ""
	}
	cacheKey := parsed.Host + "|" + c.proxyFor(account).key()
	if !fresh {
		if entry, ok := challengeStore.get(cacheKey, c.cfg.ChallengeTTL); ok {
			return entry.Cookies
		}
	}

	for _, pageURL := range c.challengePageCandidates(target, fresh) {
		headers := c.headers(account, pageURL, "", false)
		if fresh {
			// A rejected cookie means the page we solved from is stale; ask the
			// CDN for a current interstitial instead of a cached copy.
			headers.Set("Cache-Control", "no-cache")
			headers.Set("Pragma", "no-cache")
		}
		resp, errDo := c.do(pluginapi.HTTPRequest{
			Method:  http.MethodGet,
			URL:     pageURL,
			Headers: headers,
		}, account)
		if errDo != nil {
			hostLogf("warn", "获取挑战页失败 (%s): %v", pageURL, errDo)
			continue
		}
		body := string(resp.Body)
		if !looksLikeChallenge(body) {
			hostLogf("debug", "挑战页未命中 (%s, status=%d, %d 字节)", pageURL, resp.StatusCode, len(body))
			continue
		}
		cookies, method, errSolve := solveChallenge(body, c.cfg.UserAgent, challengeEvalLimit)
		if errSolve != nil {
			hostLogf("warn", "解析挑战页失败 (%s): %v", pageURL, errSolve)
			continue
		}
		challengeStore.put(cacheKey, cookies, method)
		hostLogf("info", "已获取挑战 Cookie (host=%s, 出口=%s, 来源=%s, 方式=%s, 字段数=%d%s)",
			parsed.Host, c.proxyFor(account).describe(), pageURL, method, cookieValueCount(cookies),
			map[bool]string{true: "，主动刷新"}[fresh])
		return cookies
	}
	return ""
}

// withCacheBuster appends a unique query parameter so an intermediate cache
// serves a fresh interstitial instead of the copy whose nonce was rejected.
func withCacheBuster(raw string) string {
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Host == "" {
		return raw
	}
	query := parsed.Query()
	query.Set("_t", strconv.FormatInt(time.Now().UnixNano(), 10))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// challengePageCandidates lists the URLs to solve the interstitial from.
//
// On a first solve the requested URL usually answers with the interstitial
// itself, so it is tried first. After a rejection that page is known to be stale
// - the site root serves a current nonce, and the API path keeps returning a copy
// whose computed value the server refuses - so a fresh solve starts at the root
// and busts the CDN cache.
func (c *anyrouterClient) challengePageCandidates(target string, fresh bool) []string {
	parsed, errParse := url.Parse(target)
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	hostRoot := ""
	if errParse == nil && parsed.Host != "" {
		hostRoot = parsed.Scheme + "://" + parsed.Host + "/"
	}
	root := base + "/"
	candidates := []string{target}
	if fresh {
		candidates = []string{root, hostRoot, target}
	} else {
		candidates = append(candidates, hostRoot, root)
	}
	if fresh {
		for index := range candidates {
			candidates[index] = withCacheBuster(candidates[index])
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, exists := seen[candidate]; exists {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

// apiRequest performs one API call, retrying once after re-solving the
// challenge when the upstream answers with the interstitial instead of JSON.
func (c *anyrouterClient) apiRequest(method, target string, account *Account, body []byte) (pluginapi.HTTPResponse, error) {
	parsed, _ := url.Parse(target)
	host := ""
	if parsed != nil {
		host = parsed.Host
	}

	cacheKey := host + "|" + c.proxyFor(account).key()

	// The interstitial is served intermittently: a cookie that worked seconds ago
	// can be refused, and the copy served on the API path may carry a stale nonce.
	// Retry a couple of times, each time solving fresh from the site root.
	const attempts = 3
	var lastResp pluginapi.HTTPResponse
	for attempt := 0; attempt < attempts; attempt++ {
		fresh := attempt > 0
		challenge := c.ensureChallenge(target, account, fresh)
		resp, errDo := c.do(pluginapi.HTTPRequest{
			Method:  method,
			URL:     target,
			Headers: c.headers(account, target, challenge, len(body) > 0),
			Body:    body,
		}, account)
		if errDo != nil {
			return pluginapi.HTTPResponse{}, errDo
		}
		lastResp = resp
		if !c.cfg.ChallengeEnabled || !looksLikeChallenge(string(resp.Body)) {
			return resp, nil
		}
		if attempt < attempts-1 {
			hostLogf("info", "响应为挑战页，刷新挑战 Cookie 后重试（第 %d 次，host=%s）", attempt+1, host)
			challengeStore.invalidate(cacheKey)
			// Gentle backoff: the site challenges on request rate as well as on stale
			// cookies, so hammering the retry would make it worse.
			time.Sleep(time.Duration(400*(attempt+1)) * time.Millisecond)
		}
	}
	return lastResp, nil
}

// checkin performs POST /api/user/sign_in for one account.
func (c *anyrouterClient) checkin(account *Account) checkinOutcome {
	target := account.effectiveCheckinURL(c.cfg)
	resp, errDo := c.apiRequest(http.MethodPost, target, account, nil)
	if errDo != nil {
		return checkinOutcome{
			Status:  checkinFailed,
			Message: "请求异常: " + errDo.Error(),
			At:      time.Now(),
		}
	}
	outcome := classifySignIn(resp.StatusCode, resp.Body)
	if c.cfg.Debug {
		hostLogf("debug", "签到返回 account=%s status=%d content_type=%q outcome=%s body=%s",
			account.Name, resp.StatusCode, resp.Headers.Get("content-type"), outcome.Status,
			truncate(redactCredentials(string(resp.Body), account), 400))
	}
	return outcome
}

// classifySignIn maps an upstream sign-in response onto a status. The upstream
// answers 200 with success=true both for a fresh check-in and for "already
// checked in", and 401 when the session cookie expired.
func classifySignIn(statusCode int, body []byte) checkinOutcome {
	now := time.Now()
	raw := truncate(string(body), 2000)
	outcome := checkinOutcome{HTTPStatus: statusCode, Raw: raw, At: now}

	if statusCode == http.StatusUnauthorized {
		outcome.Status = checkinFailed
		detail := truncate(extractUpstreamMessage(body), 160)
		if detail == "" {
			outcome.Message = "签到接口拒绝该 Cookie（HTTP 401），请在面板更新该账号的 Cookie"
		} else {
			outcome.Message = "签到接口拒绝该 Cookie（HTTP 401）：" + detail
		}
		return outcome
	}
	if statusCode < 200 || statusCode >= 300 {
		outcome.Status = checkinFailed
		outcome.Message = fmt.Sprintf("签到失败 HTTP %d", statusCode)
		if snippet := extractUpstreamMessage(body); snippet != "" {
			outcome.Message += ": " + snippet
		}
		return outcome
	}
	if looksLikeChallenge(string(body)) {
		outcome.Status = checkinFailed
		outcome.Message = "被挑战页拦截，未能完成签到（可尝试关闭代理或降低频率）"
		return outcome
	}

	var payload struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		outcome.Status = checkinUnknown
		outcome.Message = "响应不是 JSON: " + truncate(string(body), 200)
		return outcome
	}
	message := strings.TrimSpace(payload.Message)
	if payload.Success == nil {
		outcome.Status = checkinUnknown
		if message != "" {
			outcome.Message = message
		} else {
			outcome.Message = "响应缺少 success 字段: " + truncate(string(body), 200)
		}
		return outcome
	}
	if *payload.Success {
		if message == "" {
			outcome.Status = checkinAlready
			outcome.Message = "今日已签到"
			outcome.OK = true
			return outcome
		}
		outcome.Status = checkinSuccess
		outcome.Message = message
		outcome.OK = true
		return outcome
	}
	outcome.Status = checkinFailed
	if message != "" {
		outcome.Message = message
	} else {
		outcome.Message = "签到失败: " + truncate(string(body), 200)
	}
	return outcome
}

// extractUpstreamMessage digs a human readable message out of an arbitrary
// upstream error body without assuming a schema.
func extractUpstreamMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal == nil {
		if strings.TrimSpace(payload.Message) != "" {
			return truncate(payload.Message, 200)
		}
		switch typed := payload.Error.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return truncate(typed, 200)
			}
		case map[string]any:
			if message, okMessage := typed["message"].(string); okMessage && strings.TrimSpace(message) != "" {
				return truncate(message, 200)
			}
		}
	}
	return truncate(string(body), 200)
}

// balance reads GET /api/user/self and normalises the payload.
func (c *anyrouterClient) balance(account *Account) (*balanceInfo, error) {
	target := account.effectiveBalanceURL(c.cfg)
	resp, errDo := c.apiRequest(http.MethodGet, target, account, nil)
	if errDo != nil {
		return nil, fmt.Errorf("请求余额接口失败: %w", errDo)
	}
	body := resp.Body
	cfg := currentConfig()
	if cfg.Debug {
		hostLogf("debug", "余额接口返回 account=%s status=%d content_type=%q body=%s",
			account.Name, resp.StatusCode, resp.Headers.Get("content-type"), truncate(redactCredentials(string(body), account), 400))
	}
	if looksLikeChallenge(string(body)) {
		return nil, fmt.Errorf("被挑战页拦截（HTTP %d）：上游仍要求完成挑战，可点“刷新挑战 Cookie”后重试", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		detail := truncate(extractUpstreamMessage(body), 160)
		switch {
		case strings.Contains(detail, "New-Api-User"):
			return nil, fmt.Errorf("站点要求 New-Api-User 头（用户 ID），当前值未被接受：%s。请在面板该账号的「用户 ID」里填写正确值", detail)
		case detail == "":
			return nil, fmt.Errorf("余额接口拒绝该 Cookie（HTTP 401）：登录态可能已过期，请在面板更新该账号的 Cookie")
		default:
			return nil, fmt.Errorf("余额接口拒绝该 Cookie（HTTP 401）：%s", detail)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("余额接口返回 HTTP %d: %s", resp.StatusCode, truncate(extractUpstreamMessage(body), 200))
	}
	info, errParse := parseBalance(body)
	if errParse != nil {
		return nil, errParse
	}
	return info, nil
}

// redactCredentials strips every cookie value that came from the account (and
// any acw value) out of a diagnostic string, so upstream bodies can be logged
// or surfaced without leaking the session.
func redactCredentials(text string, account *Account) string {
	out := text
	if account != nil {
		for _, part := range strings.Split(account.File.Cookie, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			out = strings.ReplaceAll(out, part, "<COOKIE>")
			if index := strings.Index(part, "="); index > 0 {
				out = strings.ReplaceAll(out, part[index+1:], "<COOKIE-VALUE>")
			}
		}
	}
	acwPattern := regexp.MustCompile(`acw_sc__v2=[^;"\s]+`)
	out = acwPattern.ReplaceAllString(out, "acw_sc__v2=<REDACTED>")
	return out
}

// parseBalance accepts the new-api shaped payload
// {"success":true,"data":{"quota":...,"used_quota":...}} and tolerates the
// fields living at the top level.
func parseBalance(body []byte) (*balanceInfo, error) {
	var envelopePayload struct {
		Success *bool           `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(body, &envelopePayload); errUnmarshal != nil {
		return nil, fmt.Errorf("余额响应不是 JSON: %s", truncate(string(body), 200))
	}
	if envelopePayload.Success != nil && !*envelopePayload.Success {
		message := strings.TrimSpace(envelopePayload.Message)
		if message == "" {
			message = truncate(string(body), 200)
		}
		return nil, fmt.Errorf("余额接口返回失败: %s", message)
	}

	source := body
	if len(envelopePayload.Data) > 0 && string(envelopePayload.Data) != "null" {
		source = envelopePayload.Data
	}
	var data map[string]any
	if errData := json.Unmarshal(source, &data); errData != nil {
		return nil, fmt.Errorf("余额数据无法解析: %s", truncate(string(source), 200))
	}

	info := &balanceInfo{
		Quota:     numberField(data, "quota"),
		UsedQuota: numberField(data, "used_quota"),
		Username:  stringField(data, "username"),
		Email:     stringField(data, "email"),
		Group:     stringField(data, "group"),
		Extra:     map[string]any{},
		FetchedAt: time.Now(),
	}
	info.DisplayName = firstNonEmpty(stringField(data, "display_name"), stringField(data, "displayName"), info.Username)
	if count, okCount := data["request_count"]; okCount {
		if asNumber, okNumber := toFloat(count); okNumber {
			info.RequestCount = int64(asNumber)
		}
	}
	// Keep a few extra counters for the panel without persisting the raw payload.
	for _, key := range []string{"aff_quota", "aff_history_quota", "aff_count", "used_quota", "quota"} {
		if value, okValue := data[key]; okValue {
			info.Extra[key] = value
		}
	}
	return info, nil
}

func numberField(data map[string]any, key string) float64 {
	value, okValue := data[key]
	if !okValue {
		return 0
	}
	asNumber, okNumber := toFloat(value)
	if !okNumber {
		return 0
	}
	return asNumber
}

func stringField(data map[string]any, key string) string {
	value, okValue := data[key]
	if !okValue {
		return ""
	}
	asString, okString := value.(string)
	if !okString {
		return ""
	}
	return strings.TrimSpace(asString)
}

func toFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		asFloat, errFloat := typed.Float64()
		if errFloat != nil {
			return 0, false
		}
		return asFloat, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		var parsed float64
		if _, errScan := fmt.Sscanf(trimmed, "%f", &parsed); errScan != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
