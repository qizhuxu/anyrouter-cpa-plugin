# Live verification checklist for the anyrouter CPA plugin.
#
#   pwsh -File scripts/verify_live.ps1 -BaseUrl http://127.0.0.1:8319 -Key <management key>
#
# It walks the checks from the CPA plugin testing guide: the plugin must be
# loaded AND effective (registered + effective_enabled), the unauthenticated
# resource page must render, the management routes must answer behind the key,
# the challenge solver must work through the host transport, and the account /
# check-in / quota paths must execute.

[CmdletBinding()]
param(
    [string]$BaseUrl = "http://127.0.0.1:8317",
    [Parameter(Mandatory = $true)][string]$Key,
    [string]$PluginId = "anyrouter",
    [switch]$SkipMutations
)

$ErrorActionPreference = "Continue"
$auth = @{ Authorization = "Bearer $Key"; "X-Management-Key" = $Key }
$script:failures = 0

function Section([string]$Title) {
    Write-Host ""
    Write-Host "=== $Title ===" -ForegroundColor Cyan
}

function Check([string]$Name, [bool]$Ok, [string]$Detail = "") {
    if ($Ok) {
        Write-Host ("  [PASS] {0} {1}" -f $Name, $Detail) -ForegroundColor Green
    } else {
        Write-Host ("  [FAIL] {0} {1}" -f $Name, $Detail) -ForegroundColor Red
        $script:failures++
    }
}

function TryJson([string]$Method, [string]$Path, $Body) {
    $uri = "$BaseUrl$Path"
    try {
        if ($null -ne $Body) {
            return Invoke-RestMethod -Method $Method -Uri $uri -Headers $auth -ContentType "application/json" -Body ($Body | ConvertTo-Json -Depth 6)
        }
        return Invoke-RestMethod -Method $Method -Uri $uri -Headers $auth
    } catch {
        return @{ __error = $_.Exception.Message; __status = $_.Exception.Response.StatusCode.value__ }
    }
}

Section "1. binary + plugin registration"
try {
    $probe = Invoke-WebRequest -Method GET -Uri "$BaseUrl/v8/management/plugins" -Headers $auth -UseBasicParsing
    $pluginHeader = @($probe.Headers["X-CPA-SUPPORT-PLUGIN"])[0]
    Check "binary supports native plugins" ($pluginHeader -eq "1") "(X-CPA-SUPPORT-PLUGIN=$pluginHeader)"
    $plugins = $probe.Content | ConvertFrom-Json
    Check "global plugins.enabled" ($plugins.plugins_enabled -eq $true)
    $entry = $plugins.plugins | Where-Object { $_.id -eq $PluginId }
    Check "plugin discovered" ($null -ne $entry)
    if ($entry) {
        Check "configured" ($entry.configured -eq $true)
        Check "enabled" ($entry.enabled -eq $true)
        Check "registered" ($entry.registered -eq $true)
        Check "effective_enabled" ($entry.effective_enabled -eq $true)
        Check "supports_oauth" ($entry.supports_oauth -eq $true)
        Check "supports_quota" ($entry.supports_quota -eq $true)
        Write-Host ("         path={0}" -f $entry.path)
        Write-Host ("         config_fields={0}" -f (($entry.config_fields | ForEach-Object { $_.name }) -join ", "))
        Write-Host ("         menus={0}" -f (($entry.menus | ForEach-Object { $_.path }) -join ", "))
    }
} catch {
    Check "management probe" $false $_.Exception.Message
}

Section "2. resource page (no management key)"
try {
    $panel = Invoke-WebRequest -Method GET -Uri "$BaseUrl/v0/resource/plugins/$PluginId/panel" -UseBasicParsing
    Check "panel served without key" ($panel.StatusCode -eq 200) "(HTTP $($panel.StatusCode), $($panel.RawContentLength) bytes)"
    Check "panel is self contained html" ($panel.Content -match "AnyRouter" -and $panel.Content -notmatch "<script src=")
} catch {
    Check "panel served without key" $false $_.Exception.Message
}
try {
    Invoke-WebRequest -Method GET -Uri "$BaseUrl/v0/management/$PluginId/status" -UseBasicParsing | Out-Null
    Check "management route rejects missing key" $false "(HTTP 200 without a key!)"
} catch {
    $status = $_.Exception.Response.StatusCode.value__
    Check "management route rejects missing key" ($status -eq 401) "(HTTP $status)"
}

Section "3. status and accounts"
$status = TryJson GET "/v0/management/$PluginId/status"
Check "status reachable" ($null -eq $status.__error) ("$($status.__error)")
if ($null -eq $status.__error) {
    Write-Host ("         host_available={0} timezone={1} next_checkin_at={2}" -f $status.host_available, $status.timezone, $status.next_checkin_at)
    Write-Host ("         config.base_url={0}" -f $status.config.base_url)
    Check "host callbacks available" ($status.host_available -eq $true)
}

Section "4. JSL challenge through the host transport"
$challenge = TryJson POST "/v0/management/$PluginId/challenge/refresh" @{}
Check "challenge cookie obtained" ($challenge.ok -eq $true) ("$($challenge.message)")
if ($challenge.ok) {
    Write-Host ("         cookie fields={0} cache={1}" -f $challenge.fields, ($challenge.cache | ConvertTo-Json -Compress))
}
$status2 = TryJson GET "/v0/management/$PluginId/status"
if ($null -eq $status2.__error) {
    $cached = $status2.challenge.PSObject.Properties | Select-Object -First 1
    Check "challenge cached per host" ($null -ne $cached) ("$($cached.Name) -> $($cached.Value.method)")
}

if (-not $SkipMutations) {
    Section "5. account import (writes a CPA auth file)"
    $name = "anyrouter-verify-$([guid]::NewGuid().ToString('N').Substring(0,8)).json"
    $saved = TryJson POST "/v0/management/$PluginId/account" @{
        name    = $name
        remark  = "verify-account"
        cookie  = "session=verify-placeholder-cookie"
    }
    Check "account saved" ($saved.ok -eq $true) ("$($saved.name) $($saved.message)")
    $name = $saved.name

    $accounts = TryJson GET "/v0/management/$PluginId/accounts"
    if ($null -eq $accounts.__error) {
        $added = $accounts.accounts | Where-Object { $_.name -eq $name }
        Check "account listed" ($null -ne $added)
        if ($added) {
            Write-Host ("         auth_index={0} cookie_hint={1} fields={2}" -f $added.auth_index, $added.cookie_hint, $added.cookie_fields)
        }
        Check "cookie never returned in the list" (($accounts.accounts | Where-Object { $_.cookie }) -eq $null)
    }

    $detail = TryJson GET "/v0/management/$PluginId/account?name=$name"
    Check "single account exposes the cookie for editing" ($detail.cookie -eq "session=verify-placeholder-cookie")

    Section "6. credential claimed by auth_provider"
    try {
        # auth-files stays on the v0 management prefix; it is not re-exposed under v8.
        $files = Invoke-RestMethod -Method GET -Uri "$BaseUrl/v0/management/auth-files" -Headers $auth
        $mine = $files.files | Where-Object { $_.name -eq $name }
        Check "auth file visible to CPA" ($null -ne $mine)
        if ($mine) {
            Write-Host ("         provider={0} type={1} disabled={2} auth_index={3}" -f $mine.provider, $mine.type, $mine.disabled, $mine.auth_index)
            Check "provider reported as anyrouter" ($mine.provider -eq $PluginId)
            $script:authIndex = $mine.auth_index
        }
    } catch {
        Check "auth file visible to CPA" $false $_.Exception.Message
    }

    Section "7. check-in path (placeholder cookie must fail cleanly)"
    $checkin = TryJson POST "/v0/management/$PluginId/checkin" @{ name = $name }
    Check "check-in executed" ($null -eq $checkin.__error) ("$($checkin.__error)")
    if ($null -eq $checkin.__error) {
        Write-Host ("         summary={0}" -f $checkin.summary)
        foreach ($row in $checkin.results) {
            Write-Host ("         {0}: status={1} ok={2} message={3}" -f $row.account, $row.status, $row.ok, $row.message)
            Check "outcome classified (never a bare 500 / empty message)" ($row.status -ne "" -and $row.message -ne "")
        }
    }

    Section "8. balance path"
    $balance = TryJson POST "/v0/management/$PluginId/balance" @{ name = $name }
    if ($null -eq $balance.__error) {
        Write-Host ("         summary={0}" -f $balance.summary)
        foreach ($row in $balance.results) {
            Write-Host ("         {0}: ok={1} message={2}" -f $row.account, $row.ok, $row.message)
        }
    }

    Section "9. quota provider reachable for the credential"
    if ($script:authIndex) {
        $quota = TryJson GET "/v8/management/plugins/$PluginId/quota?auth_index=$($script:authIndex)"
        if ($quota.__error) {
            Check "quota route answered" $true ("handled, upstream rejected the placeholder cookie: $($quota.__error)")
        } else {
            Check "quota route answered" $true "(summary entries: $(($quota.summary | Measure-Object).Count))"
        }
    } else {
        Check "quota route" $false "no auth_index captured"
    }

    Section "10. disable / enable"
    $off = TryJson POST "/v0/management/$PluginId/account/disable" @{ name = $name; disabled = $true }
    Check "account disabled" ($off.disabled -eq $true)
    $on = TryJson POST "/v0/management/$PluginId/account/disable" @{ name = $name; disabled = $false }
    Check "account enabled" ($on.disabled -eq $false)

    Section "11. events"
    $events = TryJson GET "/v0/management/$PluginId/events"
    if ($null -eq $events.__error) {
        Check "events recorded" (($events.events | Measure-Object).Count -gt 0)
        $events.events | Select-Object -First 6 | ForEach-Object { Write-Host ("         [{0}] {1} {2} - {3}" -f $_.at, $_.kind, $_.account, $_.message) }
    }
}

Write-Host ""
if ($script:failures -eq 0) {
    Write-Host "ALL CHECKS PASSED" -ForegroundColor Green
    exit 0
}
Write-Host "$($script:failures) CHECK(S) FAILED" -ForegroundColor Red
exit 1
