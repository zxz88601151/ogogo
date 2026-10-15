#Requires -Version 5.1
<#
  06-validate-terminal-config.ps1  -  EduDesk terminal config validator (patch plan P1-11)

  Why this exists: profile and terminal id used to be typed by hand into a scheduled
  task argument. A typo there is invisible until someone notices a corridor screen
  showing classroom content. This validates the config BEFORE the task is registered,
  and can also prove the server actually answers for that profile.

  Usage:
    .\06-validate-terminal-config.ps1 -ConfigPath ..\server-go\data\terminal-config.json
    .\06-validate-terminal-config.ps1 -ConfigPath <path> -CheckServer
    .\06-validate-terminal-config.ps1 -ConfigPath ..\server-go\data\terminal-config.json

  Exit code: 0 = config is valid ; 1 = at least one hard error.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ConfigPath,

    [switch]$CheckServer,
    [int]$MinPollSec = 30,
    [int]$MaxPollSec = 600
)

$ErrorActionPreference = 'Continue'
$errors   = @()
$warnings = @()

if (-not (Test-Path $ConfigPath)) {
    Write-Host ("FAIL: config not found: {0}" -f $ConfigPath) -ForegroundColor Red
    exit 1
}

try {
    # -Encoding UTF8 is mandatory: Windows PowerShell 5.1 defaults to the ANSI code
    # page (936 here), which mangles the Chinese fields and then "fails to parse"
    # valid JSON. Explicit UTF8 also strips a BOM if an editor added one.
    $cfg = Get-Content -Raw -Path $ConfigPath -Encoding UTF8 | ConvertFrom-Json
} catch {
    Write-Host ("FAIL: not valid JSON: {0}" -f $_.Exception.Message) -ForegroundColor Red
    exit 1
}

# ---- profile -----------------------------------------------------------------
if ([string]::IsNullOrWhiteSpace($cfg.profile)) {
    $errors += 'profile is missing'
} elseif ($cfg.profile -notin @('classroom', 'corridor')) {
    $errors += ("profile must be classroom|corridor, got '{0}'" -f $cfg.profile)
}

# ---- terminal_id -------------------------------------------------------------
if ([string]::IsNullOrWhiteSpace($cfg.terminal_id)) {
    $errors += 'terminal_id is missing'
} elseif ($cfg.terminal_id -eq 'DEV-00') {
    $warnings += 'terminal_id is still the template placeholder DEV-00'
} elseif ($cfg.terminal_id -notmatch '^[A-Za-z0-9]{1,6}-\d{2,3}$') {
    $warnings += ("terminal_id '{0}' does not look like <building><floor>-<nn> (e.g. A2F-07)" -f $cfg.terminal_id)
}

# ---- server_url --------------------------------------------------------------
if ([string]::IsNullOrWhiteSpace($cfg.server_url)) {
    $errors += 'server_url is missing'
} else {
    $uri = $null
    if (-not [Uri]::TryCreate($cfg.server_url, [UriKind]::Absolute, [ref]$uri)) {
        $errors += ("server_url is not an absolute URL: {0}" -f $cfg.server_url)
    } elseif ($uri.Scheme -notin @('http', 'https')) {
        $errors += ("server_url scheme must be http|https, got '{0}'" -f $uri.Scheme)
    } elseif ($uri.Scheme -eq 'http' -and $uri.Host -notin @('127.0.0.1', 'localhost', '::1')) {
        # plaintext to a remote host is exactly what P1-9 is about
        $warnings += ("server_url is plaintext http to a remote host ({0}); prefer https or a reverse proxy" -f $uri.Host)
    }
}

# ---- poll_sec ----------------------------------------------------------------
if ($null -eq $cfg.poll_sec) {
    $warnings += 'poll_sec missing; terminal default (60s) will be used'
} elseif ($cfg.poll_sec -lt $MinPollSec -or $cfg.poll_sec -gt $MaxPollSec) {
    $errors += ("poll_sec {0} is outside [{1}, {2}]" -f $cfg.poll_sec, $MinPollSec, $MaxPollSec)
}

# ---- kiosk -------------------------------------------------------------------
if ($null -eq $cfg.kiosk) {
    $warnings += 'kiosk missing; defaulting to true'
}

# ---- optional live probe -----------------------------------------------------
if ($CheckServer -and $errors.Count -eq 0) {
    $probe = $cfg.server_url.TrimEnd('/')
    try {
        $h = Invoke-RestMethod -Uri "$probe/health" -TimeoutSec 5
        if ($h.status -ne 'ok') {
            $errors += ("/health returned status '{0}'" -f $h.status)
        } else {
            Write-Host ("  /health ok (pid {0})" -f $h.pid) -ForegroundColor DarkGray
            try {
                $b = Invoke-RestMethod -Uri ("{0}/bundle?profile={1}" -f $probe, $cfg.profile) -TimeoutSec 8
                if (-not $b.content_hash) {
                    $errors += '/bundle did not carry content_hash (server may be pre-patch build)'
                } else {
                    $n = @($b.items).Count
                    Write-Host ("  /bundle ok: profile={0} items={1} ttl={2}s" -f $cfg.profile, $n, $b.ttl_sec) -ForegroundColor DarkGray
                    if ($n -eq 0) { $warnings += 'bundle for this profile currently has zero items (screen will show the empty-state copy)' }
                }
            } catch {
                $errors += ("/bundle?profile={0} failed: {1}" -f $cfg.profile, $_.Exception.Message)
            }
        }
    } catch {
        $errors += ("cannot reach {0}/health : {1}" -f $probe, $_.Exception.Message)
    }
}

# ---- report ------------------------------------------------------------------
Write-Host ''
Write-Host ("terminal config: {0}" -f $ConfigPath) -ForegroundColor Cyan
Write-Host ("  terminal_id = {0}" -f $cfg.terminal_id)
Write-Host ("  profile     = {0}" -f $cfg.profile)
Write-Host ("  server_url  = {0}" -f $cfg.server_url)
Write-Host ("  poll_sec    = {0}" -f $cfg.poll_sec)

foreach ($w in $warnings) { Write-Host ("WARN: {0}" -f $w) -ForegroundColor Yellow }
foreach ($e in $errors)   { Write-Host ("FAIL: {0}" -f $e) -ForegroundColor Red }

if ($errors.Count -gt 0) {
    Write-Host 'CONFIG INVALID' -ForegroundColor Red
    exit 1
}
Write-Host 'CONFIG OK' -ForegroundColor Green
exit 0
