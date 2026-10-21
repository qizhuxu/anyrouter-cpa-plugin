#!/usr/bin/env bash
# Live verification checklist for the anyrouter CPA plugin (Linux / macOS).
#
#   ./scripts/verify_live.sh --base-url http://127.0.0.1:8319 --key <management key>
#
# Mirrors scripts/verify_live.ps1: the plugin must be loaded AND effective, the
# unauthenticated resource page must render, management routes must answer
# behind the key, the JSL challenge must be solvable through the host transport,
# and the account / check-in / balance / quota paths must execute.
#
# Requires: curl, python3 (JSON parsing). jq is used when available.
set -u

BASE_URL="http://127.0.0.1:8317"
KEY=""
PLUGIN_ID="anyrouter"
SKIP_MUTATIONS=0

while [ $# -gt 0 ]; do
  case "$1" in
    --base-url) BASE_URL="$2"; shift 2 ;;
    --key) KEY="$2"; shift 2 ;;
    --plugin-id) PLUGIN_ID="$2"; shift 2 ;;
    --skip-mutations) SKIP_MUTATIONS=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$KEY" ]; then
  echo "usage: $0 --key <management key> [--base-url URL] [--plugin-id ID] [--skip-mutations]" >&2
  exit 2
fi

FAILURES=0
AUTH_INDEX=""

say()  { printf '\n=== %s ===\n' "$1"; }
pass() { printf '  [PASS] %s %s\n' "$1" "${2:-}"; }
fail() { printf '  [FAIL] %s %s\n' "$1" "${2:-}"; FAILURES=$((FAILURES + 1)); }
check() { # check <name> <ok:0|1> [detail]
  if [ "$2" -eq 0 ]; then pass "$1" "${3:-}"; else fail "$1" "${3:-}"; fi
}

api() { # api <method> <path> [json-body] -> body on stdout, exit 0 even on HTTP errors
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -sS -X "$method" "$BASE_URL$path" \
      -H "Authorization: Bearer $KEY" -H "X-Management-Key: $KEY" \
      -H 'Content-Type: application/json' --data "$body" -w '\n%{http_code}'
  else
    curl -sS -X "$method" "$BASE_URL$path" \
      -H "Authorization: Bearer $KEY" -H "X-Management-Key: $KEY" -w '\n%{http_code}'
  fi
}

body_of() { printf '%s' "$1" | sed '$d'; }
code_of() { printf '%s' "$1" | tail -n 1; }

jget() { # jget <json> <python-expression on d>
  python3 -c "
import json,sys
try:
    d = json.loads(sys.argv[1])
except Exception:
    print(''); sys.exit(0)
print($2)
" "$1"
}

say "1. binary + plugin registration"
INFO=$(curl -sS -D /tmp/anyrouter_headers.$$ -o /tmp/anyrouter_plugins.$$ "$BASE_URL/v8/management/plugins" \
  -H "Authorization: Bearer $KEY" -H "X-Management-Key: $KEY" 2>/dev/null; cat /tmp/anyrouter_plugins.$$)
SUPPORT=$(grep -i '^x-cpa-support-plugin' /tmp/anyrouter_headers.$$ 2>/dev/null | tr -d '\r' | awk '{print $2}')
rm -f /tmp/anyrouter_headers.$$ /tmp/anyrouter_plugins.$$
[ "$SUPPORT" = "1" ]; check "binary supports native plugins" $? "(X-CPA-SUPPORT-PLUGIN=${SUPPORT:-missing})"
[ "$(jget "$INFO" "d.get('plugins_enabled')")" = "True" ]; check "global plugins.enabled" $?
ENTRY=$(jget "$INFO" "next((json.dumps(p) for p in d.get('plugins',[]) if p.get('id')=='$PLUGIN_ID'), '')")
[ -n "$ENTRY" ]; check "plugin discovered" $?
if [ -n "$ENTRY" ]; then
  for field in configured enabled registered effective_enabled; do
    [ "$(jget "$ENTRY" "d.get('$field')")" = "True" ]; check "$field" $?
  done
  echo "         path=$(jget "$ENTRY" "d.get('path')")"
  echo "         config_fields=$(jget "$ENTRY" "','.join(f['name'] for f in d.get('config_fields',[]))")"
  echo "         menus=$(jget "$ENTRY" "','.join(m.get('path','') for m in d.get('menus',[]))")"
fi
for field in supports_oauth supports_quota; do
  [ "$(jget "$ENTRY" "d.get('$field')")" = "True" ]; check "$field" $?
done

say "2. resource page (no management key) and auth boundary"
PANEL_CODE=$(curl -sS -o /tmp/anyrouter_panel.$$ -w '%{http_code}' "$BASE_URL/v0/resource/plugins/$PLUGIN_ID/panel")
PANEL_BYTES=$(wc -c < /tmp/anyrouter_panel.$$ | tr -d ' ')
[ "$PANEL_CODE" = "200" ]; check "panel served without key" $? "(HTTP $PANEL_CODE, $PANEL_BYTES bytes)"
grep -q '账号台账' /tmp/anyrouter_panel.$$ && ! grep -q '<script src' /tmp/anyrouter_panel.$$
check "panel is self contained html" $?
rm -f /tmp/anyrouter_panel.$$
NOKEY_CODE=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE_URL/v0/management/$PLUGIN_ID/status")
[ "$NOKEY_CODE" = "401" ]; check "management route rejects missing key" $? "(HTTP $NOKEY_CODE)"

say "3. status"
STATUS_RAW=$(api GET "/v0/management/$PLUGIN_ID/status")
STATUS=$(body_of "$STATUS_RAW")
[ "$(code_of "$STATUS_RAW")" = "200" ]; check "status reachable" $?
[ "$(jget "$STATUS" "d.get('host_available')")" = "True" ]; check "host callbacks available" $?
[ "$(jget "$STATUS" "d.get('config',{}).get('base_url')")" = "https://anyrouter.top" ] 2>/dev/null
check "base_url from config" $? "($(jget "$STATUS" "d.get('config',{}).get('base_url')"))"
echo "         timezone=$(jget "$STATUS" "d.get('timezone')") now=$(jget "$STATUS" "d.get('now')")"

say "4. JSL challenge through the host transport"
CH=$(body_of "$(api POST "/v0/management/$PLUGIN_ID/challenge/refresh" '{}')")
[ "$(jget "$CH" "d.get('ok')")" = "True" ]; check "challenge cookie obtained" $? "($(jget "$CH" "d.get('message')"))"
echo "         fields=$(jget "$CH" "d.get('fields')") cache=$(jget "$CH" "json.dumps(d.get('cache'))")"

if [ "$SKIP_MUTATIONS" -eq 0 ]; then
  NAME="anyrouter-verify-$(date +%s).json"
  say "5. account import (writes a CPA auth file)"
  SAVED=$(body_of "$(api POST "/v0/management/$PLUGIN_ID/account" \
    "{\"name\":\"$NAME\",\"remark\":\"verify-account\",\"cookie\":\"session=verify-placeholder-cookie\"}")")
  [ "$(jget "$SAVED" "d.get('ok')")" = "True" ]; check "account saved" $? "($(jget "$SAVED" "d.get('name')"))"
  NAME=$(jget "$SAVED" "d.get('name')")

  ACCOUNTS=$(body_of "$(api GET "/v0/management/$PLUGIN_ID/accounts")")
  [ "$(jget "$ACCOUNTS" "any(a['name']=='$NAME' for a in d.get('accounts',[]))")" = "True" ]
  check "account listed" $?
  [ "$(jget "$ACCOUNTS" "any(a.get('cookie') for a in d.get('accounts',[]))")" = "False" ]
  check "cookie never returned in the list" $?
  DETAIL=$(body_of "$(api GET "/v0/management/$PLUGIN_ID/account?name=$NAME")")
  [ "$(jget "$DETAIL" "d.get('cookie')")" = "session=verify-placeholder-cookie" ]
  check "single account exposes the cookie for editing" $?

  say "6. credential claimed by auth_provider"
  FILES=$(body_of "$(api GET "/v0/management/auth-files")")
  MINE=$(jget "$FILES" "next((json.dumps(f) for f in d.get('files',[]) if f.get('name')=='$NAME'), '')")
  [ -n "$MINE" ]; check "auth file visible to CPA" $?
  if [ -n "$MINE" ]; then
    PROVIDER=$(jget "$MINE" "d.get('provider')")
    AUTH_INDEX=$(jget "$MINE" "d.get('auth_index')")
    echo "         provider=$PROVIDER type=$(jget "$MINE" "d.get('type')") auth_index=$AUTH_INDEX disabled=$(jget "$MINE" "d.get('disabled')")"
    [ "$PROVIDER" = "$PLUGIN_ID" ]; check "provider reported as ${PLUGIN_ID}" $?
  fi
  AUTH_DIR=$(python3 -c "
import json,sys
print(json.loads(sys.argv[1]).get('auth_dir',''))
" "$(jget "$STATUS" "json.dumps({'auth_dir': d.get('config',{}).get('auth_dir','')})")" 2>/dev/null)
  if [ -n "${AUTH_PATH:-}" ] && [ -f "$AUTH_PATH" ]; then
    MODE=$(stat -c '%a' "$AUTH_PATH" 2>/dev/null || stat -f '%Lp' "$AUTH_PATH")
    [ "$MODE" = "600" ]; check "auth file written 0600" $? "(mode $MODE)"
  fi

  say "7. check-in path (placeholder cookie must fail cleanly)"
  CHECKIN=$(body_of "$(api POST "/v0/management/$PLUGIN_ID/checkin" "{\"name\":\"$NAME\"}")")
  [ "$(jget "$CHECKIN" "len(d.get('results',[]))")" = "1" ]; check "check-in executed" $?
  echo "         summary=$(jget "$CHECKIN" "d.get('summary')")"
  echo "         status=$(jget "$CHECKIN" "d['results'][0]['status']") message=$(jget "$CHECKIN" "d['results'][0]['message']")"

  say "8. balance path"
  BAL=$(body_of "$(api POST "/v0/management/$PLUGIN_ID/balance" "{\"name\":\"$NAME\"}")")
  echo "         summary=$(jget "$BAL" "d.get('summary')")"
  echo "         message=$(jget "$BAL" "d['results'][0]['message'] if d.get('results') else ''")"

  say "9. quota provider reachable for the credential"
  if [ -n "$AUTH_INDEX" ]; then
    QCODE=$(curl -sS -o /tmp/anyrouter_quota.$$ -w '%{http_code}' \
      "$BASE_URL/v8/management/plugins/$PLUGIN_ID/quota?auth_index=$AUTH_INDEX" \
      -H "Authorization: Bearer $KEY" -H "X-Management-Key: $KEY")
    echo "         HTTP $QCODE body=$(head -c 200 /tmp/anyrouter_quota.$$)"
    [ "$QCODE" = "200" ]; check "quota route answered" $?
    rm -f /tmp/anyrouter_quota.$$
  else
    fail "quota route" "(no auth_index captured)"
  fi

  say "10. disable / enable"
  OFF=$(body_of "$(api POST "/v0/management/$PLUGIN_ID/account/disable" "{\"name\":\"$NAME\",\"disabled\":true}")")
  [ "$(jget "$OFF" "d.get('disabled')")" = "True" ]; check "account disabled" $?
  ON=$(body_of "$(api POST "/v0/management/$PLUGIN_ID/account/disable" "{\"name\":\"$NAME\",\"disabled\":false}")")
  [ "$(jget "$ON" "d.get('disabled')")" = "False" ]; check "account enabled" $?

  say "11. events"
  EV=$(body_of "$(api GET "/v0/management/$PLUGIN_ID/events")")
  [ "$(jget "$EV" "len(d.get('events',[]))>0")" = "True" ]; check "events recorded" $?
  jget "$EV" "'\n'.join('         [%s] %s %s - %s' % (e.get('at'), e.get('kind'), e.get('account'), e.get('message')) for e in d.get('events',[])[:6])"
fi

printf '\n'
if [ "$FAILURES" -eq 0 ]; then
  echo "ALL CHECKS PASSED"
  exit 0
fi
echo "$FAILURES CHECK(S) FAILED"
exit 1
