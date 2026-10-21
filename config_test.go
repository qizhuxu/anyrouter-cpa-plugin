package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg, errLoad := loadConfig(nil)
	if errLoad != nil {
		t.Fatalf("loadConfig: %v", errLoad)
	}
	if cfg.BaseURL != defaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", cfg.BaseURL, defaultBaseURL)
	}
	if cfg.CheckinURL != defaultBaseURL+"/api/user/sign_in" {
		t.Fatalf("CheckinURL = %q", cfg.CheckinURL)
	}
	if cfg.BalanceURL != defaultBaseURL+"/api/user/self" {
		t.Fatalf("BalanceURL = %q", cfg.BalanceURL)
	}
	if !cfg.AutoCheckin || !cfg.ChallengeEnabled || !cfg.AutoBalance {
		t.Fatalf("boolean defaults must be true: %+v", cfg)
	}
	if cfg.CheckinTime != defaultCheckinTime {
		t.Fatalf("CheckinTime = %q", cfg.CheckinTime)
	}
	if cfg.Location == nil {
		t.Fatal("Location must resolve")
	}
}

func TestLoadConfigOverridesAndNormalisation(t *testing.T) {
	raw := []byte(`
enabled: true
priority: 5
base_url: "https://anyrouter.top/"
checkin_url: "https://blog.zhx47.top/anyrouter/api/user/sign_in"
auto_checkin: false
checkin_time: "07:30"
challenge_ttl_minutes: 5
request_timeout_seconds: 9
balance_refresh_minutes: 15
quota_per_unit: 1000
timezone: "Asia/Shanghai"
debug: true
`)
	cfg, errLoad := loadConfig(raw)
	if errLoad != nil {
		t.Fatalf("loadConfig: %v", errLoad)
	}
	if cfg.BaseURL != "https://anyrouter.top" {
		t.Fatalf("trailing slash must be trimmed, got %q", cfg.BaseURL)
	}
	if cfg.CheckinURL != "https://blog.zhx47.top/anyrouter/api/user/sign_in" {
		t.Fatalf("CheckinURL = %q", cfg.CheckinURL)
	}
	if cfg.BalanceURL != "https://anyrouter.top/api/user/self" {
		t.Fatalf("BalanceURL should derive from base_url, got %q", cfg.BalanceURL)
	}
	if cfg.AutoCheckin {
		t.Fatal("auto_checkin must be overridable to false")
	}
	if cfg.ChallengeTTL != 5*time.Minute || cfg.Timeout != 9*time.Second || cfg.BalanceInterval != 15*time.Minute {
		t.Fatalf("durations not applied: %+v", cfg)
	}
	if cfg.QuotaPerUnit != 1000 {
		t.Fatalf("QuotaPerUnit = %v", cfg.QuotaPerUnit)
	}
	if cfg.Timezone != "Asia/Shanghai" || cfg.Location.String() != "Asia/Shanghai" {
		t.Fatalf("timezone not applied: %q / %v", cfg.Timezone, cfg.Location)
	}
	if !cfg.Debug {
		t.Fatal("debug must be applied")
	}
}

func TestLoadConfigRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"bad scheme":     "base_url: \"ftp://anyrouter.top\"",
		"empty base":     "base_url: \"\"",
		"bad clock":      "checkin_time: \"25:99\"",
		"ttl range":      "challenge_ttl_minutes: 0",
		"timeout range":  "request_timeout_seconds: 0",
		"balance range":  "balance_refresh_minutes: 1",
		"quota per unit": "quota_per_unit: 0",
		"timezone":       "timezone: \"Mars/Olympus\"",
	}
	for name, raw := range cases {
		if _, errLoad := loadConfig([]byte(raw)); errLoad == nil {
			t.Fatalf("%s: expected an error", name)
		}
	}
}

func TestCheckinURLDefaultsToBaseWhenOnlyBaseChanges(t *testing.T) {
	cfg, errLoad := loadConfig([]byte("base_url: \"https://mirror.example.com/anyrouter\""))
	if errLoad != nil {
		t.Fatalf("loadConfig: %v", errLoad)
	}
	if cfg.CheckinURL != "https://mirror.example.com/anyrouter/api/user/sign_in" {
		t.Fatalf("CheckinURL = %q", cfg.CheckinURL)
	}
}

func TestClassifySignIn(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus string
		wantOK     bool
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":"unauthorized"}`, checkinFailed, false},
		{"fresh success", http.StatusOK, `{"message":"签到成功，获得 $25 额度","success":true}`, checkinSuccess, true},
		{"already", http.StatusOK, `{"message":"","success":true}`, checkinAlready, true},
		{"failure message", http.StatusOK, `{"message":"Cookie 已过期","success":false}`, checkinFailed, false},
		{"server error", http.StatusInternalServerError, `{"message":"boom"}`, checkinFailed, false},
		{"challenge html", http.StatusOK, `<html><script>var arg1='ABCD';acw_sc__v2</script></html>`, checkinFailed, false},
		{"not json", http.StatusOK, `ok`, checkinUnknown, false},
		{"no success field", http.StatusOK, `{"message":"嗯"}`, checkinUnknown, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			outcome := classifySignIn(testCase.status, []byte(testCase.body))
			if outcome.Status != testCase.wantStatus || outcome.OK != testCase.wantOK {
				t.Fatalf("got status=%q ok=%t message=%q", outcome.Status, outcome.OK, outcome.Message)
			}
			if strings.TrimSpace(outcome.Message) == "" {
				t.Fatal("message must never be empty")
			}
		})
	}
}

func TestParseBalance(t *testing.T) {
	body := []byte(`{"success":true,"message":"","data":{"quota":500000,"used_quota":250000,"request_count":12,"username":"tester","display_name":"测试号","group":"default"}}`)
	info, errParse := parseBalance(body)
	if errParse != nil {
		t.Fatalf("parseBalance: %v", errParse)
	}
	if info.Quota != 500000 || info.UsedQuota != 250000 || info.RequestCount != 12 {
		t.Fatalf("unexpected numbers: %+v", info)
	}
	if info.DisplayName != "测试号" || info.Username != "tester" || info.Group != "default" {
		t.Fatalf("unexpected identity: %+v", info)
	}
	if info.Total() != 750000 {
		t.Fatalf("Total = %v", info.Total())
	}
}

func TestParseBalanceTopLevelFields(t *testing.T) {
	info, errParse := parseBalance([]byte(`{"quota":10,"used_quota":5}`))
	if errParse != nil {
		t.Fatalf("parseBalance: %v", errParse)
	}
	if info.Quota != 10 || info.UsedQuota != 5 {
		t.Fatalf("unexpected: %+v", info)
	}
}

func TestParseBalanceFailureAndGarbage(t *testing.T) {
	if _, errParse := parseBalance([]byte(`{"success":false,"message":"无效的令牌"}`)); errParse == nil {
		t.Fatal("success=false must be an error")
	}
	if _, errParse := parseBalance([]byte(`<html>nope</html>`)); errParse == nil {
		t.Fatal("non JSON must be an error")
	}
}

func TestQuotaResponseMapping(t *testing.T) {
	cfg := Config{QuotaPerUnit: 500000}
	info := &balanceInfo{Quota: 250000, UsedQuota: 250000, RequestCount: 3, Group: "default", DisplayName: "测试号"}
	resp := quotaResponse(info, cfg, "")
	if resp.Subscription == nil || resp.Subscription.Plan != "default" {
		t.Fatalf("subscription = %+v", resp.Subscription)
	}
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 1 {
		t.Fatalf("groups = %+v", resp.Groups)
	}
	if fraction := resp.Groups[0].Buckets[0].RemainingFraction; fraction != 0.5 {
		t.Fatalf("remaining fraction = %v, want 0.5", fraction)
	}
	metrics := map[string]float64{}
	for _, metric := range resp.Summary {
		metrics[metric.Key] = metric.Value
	}
	if metrics["remaining"] != 0.5 || metrics["used"] != 0.5 {
		t.Fatalf("metrics = %+v", metrics)
	}
	// CPA drops summary entries without a valid ISO currency or a numeric value.
	for _, metric := range resp.Summary {
		if metric.Format == "currency" && metric.Currency != "USD" {
			t.Fatalf("metric %s has currency %q", metric.Key, metric.Currency)
		}
	}
}

func TestQuotaResponseZeroTotalDoesNotDivideByZero(t *testing.T) {
	resp := quotaResponse(&balanceInfo{}, Config{QuotaPerUnit: 500000}, "")
	if resp.Groups[0].Buckets[0].RemainingFraction != 0 {
		t.Fatalf("fraction = %v, want 0", resp.Groups[0].Buckets[0].RemainingFraction)
	}
}

func TestNormaliseRoutePath(t *testing.T) {
	cases := map[string]string{
		"/v0/management/anyrouter/accounts":                "/accounts",
		"/v0/management/anyrouter/status/":                 "/status",
		"/v0/resource/plugins/anyrouter/panel":             "/panel",
		"/v0/management/anyrouter":                         "/",
		"/v0/management/anyrouter/account/disable":         "/account/disable",
		"/v0/management/plugin/anyrouter/../anyrouter/x/y": "/../anyrouter/x/y",
	}
	for input, want := range cases {
		if got := normaliseRoutePath(input); got != want {
			t.Fatalf("normaliseRoutePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestManagementRegistrationDeclaresEveryRoute(t *testing.T) {
	raw, errMarshal := json.Marshal(managementRegistration())
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	var decoded struct {
		Routes []struct {
			Method string
			Path   string
		}
		Resources []struct {
			Path string
			Menu string
		}
	}
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal: %v", errUnmarshal)
	}
	if len(decoded.Routes) == 0 {
		t.Fatal("no routes declared")
	}
	seen := map[string]bool{}
	for _, route := range decoded.Routes {
		if !strings.HasPrefix(route.Path, pluginRoutePrefix+"/") {
			t.Fatalf("route %q must live under %s", route.Path, pluginRoutePrefix)
		}
		if route.Method == "" {
			t.Fatalf("route %q has no method", route.Path)
		}
		key := strings.ToUpper(route.Method) + " " + route.Path
		if seen[key] {
			t.Fatalf("duplicate route %s", key)
		}
		seen[key] = true
	}
	if len(decoded.Resources) != 1 {
		t.Fatalf("resources = %+v", decoded.Resources)
	}
	if decoded.Resources[0].Path == "/" || decoded.Resources[0].Path == "" {
		t.Fatal("a resource path of / is rejected by the host")
	}
	if decoded.Resources[0].Menu == "" {
		t.Fatal("the resource needs a menu label")
	}
}

func TestDispatchRejectsUnknownPathAndWrongMethod(t *testing.T) {
	resp, handled := dispatchManagement(http.MethodGet, "/nope", managementRequest{})
	if handled {
		t.Fatal("unknown paths must not be handled")
	}
	_ = resp
	if _, handled := dispatchManagement(http.MethodPost, "/panel", managementRequest{}); !handled {
		t.Fatal("the panel route must be handled")
	}
	resp, handled = dispatchManagement(http.MethodPost, "/panel", managementRequest{})
	if !handled || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("panel POST status = %d (handled=%t)", resp.StatusCode, handled)
	}
}

func TestPanelHTMLIsSelfContained(t *testing.T) {
	html := panelHTML()
	// The guard is about *loading* external things. A literal http:// example in a
	// help text is not a resource reference, so the patterns below target the
	// loading forms only.
	for _, forbidden := range []string{
		`<script src`, `<link rel="stylesheet"`, `<link rel='stylesheet'`,
		`@import`, `url(http`, `src="http`, `src='http`, `href="http`, `href='http`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("panel must not load external resources, found %q", forbidden)
		}
	}
	if !strings.Contains(html, "/v0/management/anyrouter") {
		t.Fatal("panel must call the management routes")
	}
	if !strings.Contains(html, "Authorization") {
		t.Fatal("panel must send the management key")
	}
}

// TestPanelStructure pins the parts of the redesign that are easy to lose in a
// later edit: the inlined layers, the accessibility hooks, the motion guard and
// the view states reachable through the address.
func TestPanelStructure(t *testing.T) {
	html := panelHTML()
	required := map[string]string{
		"inlined stylesheet":    ":root{",
		"design tokens":         "--accent:",
		"tabular figures":       "font-variant-numeric:tabular-nums",
		"reduced motion guard":  "prefers-reduced-motion",
		"live region":           `aria-live="polite"`,
		"dialog semantics":      `role="dialog"`,
		"keyboard focus ring":   ":focus-visible",
		"narrow layout":         "max-width:900px",
		"state via address":     `params.get("state")`,
		"first paint animation": "data-enter",
		"row busy state":        `data-busy`,
		"inline delete confirm": "state.deleteArmed",
		"user id field":         `id="f-userid"`,
		"cookie help text":      "开发者工具",
	}
	for label, needle := range required {
		if !strings.Contains(html, needle) {
			t.Fatalf("panel is missing %s (%q)", label, needle)
		}
	}
	if strings.Contains(html, "onclick=") {
		t.Fatal("panel must not use inline event handlers")
	}
	if strings.Contains(html, " ") {
		t.Fatal("panel must not rely on non-breaking spaces for layout")
	}
}

func TestMaskAndSlugHelpers(t *testing.T) {
	if got := maskSecret("session=abcdefghijklmnop"); !strings.HasPrefix(got, "sess") || !strings.HasSuffix(got, "mnop") {
		t.Fatalf("maskSecret = %q", got)
	}
	if got := maskSecret("short"); got != "*****" {
		t.Fatalf("maskSecret(short) = %q", got)
	}
	if got := slugify("主号"); got != "account" {
		t.Fatalf("slugify(主号) = %q", got)
	}
	if got := slugify("Main Acc"); got != "mainacc" {
		t.Fatalf("slugify(Main Acc) = %q", got)
	}
	name := newAccountName("main")
	if !strings.HasPrefix(name, authFilePrefix) || !strings.HasSuffix(name, ".json") {
		t.Fatalf("newAccountName = %q", name)
	}
	if got := cookieValueCount("a=1; b=2;"); got != 2 {
		t.Fatalf("cookieValueCount = %d", got)
	}
}

func TestAuthParseClaimsOnlyAnyrouterFiles(t *testing.T) {
	raw, errMarshal := json.Marshal(accountFile{
		Type:   providerKey,
		Remark: "主号",
		Cookie: "session=1; other=2",
	})
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	request, _ := json.Marshal(map[string]any{
		"Provider": providerKey,
		"FileName": "anyrouter-main-abc123.json",
		"RawJSON":  raw,
	})
	encoded, errHandle := handleAuthParse(request)
	if errHandle != nil {
		t.Fatalf("handleAuthParse: %v", errHandle)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Handled bool `json:"Handled"`
			Auth    struct {
				Provider string `json:"Provider"`
				Label    string `json:"Label"`
			} `json:"Auth"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(encoded, &env); errUnmarshal != nil {
		t.Fatalf("unmarshal: %v", errUnmarshal)
	}
	if !env.OK || !env.Result.Handled {
		t.Fatalf("expected the file to be claimed: %s", string(encoded))
	}
	if env.Result.Auth.Provider != providerKey || env.Result.Auth.Label != "主号" {
		t.Fatalf("unexpected auth: %+v", env.Result.Auth)
	}
	if strings.Contains(string(encoded), "session=1") {
		t.Fatal("auth.parse must not expose the cookie outside StorageJSON")
	}
}

func TestAuthParseDeclinesForeignFiles(t *testing.T) {
	request, _ := json.Marshal(map[string]any{
		"Provider": "codex",
		"FileName": "codex-user.json",
		"RawJSON":  []byte(`{"type":"codex","access_token":"x"}`),
	})
	encoded, errHandle := handleAuthParse(request)
	if errHandle != nil {
		t.Fatalf("handleAuthParse: %v", errHandle)
	}
	if !strings.Contains(string(encoded), `"Handled":false`) {
		t.Fatalf("foreign providers must not be claimed: %s", string(encoded))
	}
}

func TestDueAccounts(t *testing.T) {
	cfg := Config{AutoCheckin: true, Location: time.Local}
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.Local)
	today := now.Format(time.RFC3339)
	yesterday := now.Add(-24 * time.Hour).Format(time.RFC3339)

	accountsState.mu.Lock()
	accountsState.accounts = []*Account{
		{Name: "anyrouter-a.json", File: accountFile{LastCheckinAt: today, LastCheckinStatus: checkinSuccess}},
		{Name: "anyrouter-b.json", File: accountFile{LastCheckinAt: yesterday}},
		{Name: "anyrouter-c.json", File: accountFile{Disabled: true}},
		{Name: "anyrouter-d.json", File: accountFile{LastCheckinAt: now.Add(-5 * time.Minute).Format(time.RFC3339), LastCheckinStatus: checkinFailed}},
	}
	accountsState.mu.Unlock()

	due := dueAccounts(cfg, now)
	names := make([]string, 0, len(due))
	for _, account := range due {
		names = append(names, account.Name)
	}
	if len(names) != 1 || names[0] != "anyrouter-b.json" {
		t.Fatalf("due = %v, want only the account without today's result", names)
	}

	// A failed attempt older than the retry window becomes due again.
	accountsState.mu.Lock()
	accountsState.accounts = []*Account{
		{Name: "anyrouter-d.json", File: accountFile{LastCheckinAt: now.Add(-45 * time.Minute).Format(time.RFC3339), LastCheckinStatus: checkinFailed}},
	}
	accountsState.mu.Unlock()
	if due := dueAccounts(cfg, now); len(due) != 1 {
		t.Fatalf("a stale failure must be retried, got %d", len(due))
	}

	accountsState.mu.Lock()
	accountsState.accounts = nil
	accountsState.mu.Unlock()
}

func TestNextCheckinTime(t *testing.T) {
	cfg := Config{AutoCheckin: true, CheckinTime: "08:00", Location: time.Local}
	morning := time.Date(2026, 10, 21, 6, 0, 0, 0, time.Local)
	next := nextCheckinTime(cfg, morning)
	if !strings.HasPrefix(next, "2026-10-21T08:00:00") {
		t.Fatalf("next = %q", next)
	}
	evening := time.Date(2026, 10, 21, 22, 0, 0, 0, time.Local)
	if next := nextCheckinTime(cfg, evening); !strings.HasPrefix(next, "2026-10-22T08:00:00") {
		t.Fatalf("next = %q", next)
	}
	if next := nextCheckinTime(Config{CheckinTime: "08:00"}, morning); next != "" {
		t.Fatalf("auto check-in disabled must report no schedule, got %q", next)
	}
}
