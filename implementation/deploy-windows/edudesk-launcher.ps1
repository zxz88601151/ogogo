#Requires -RunAsAdministrator
<#
  edudesk-launcher.ps1
  EduDesk Offline - Campus Display MVP  (D-06 fallback deployment)

  Implements design doc 05:
    - section 1.2  W-01 auto start / W-02 fullscreen / W-03 fast first paint /
                   W-04 never blank / W-05 regain foreground / W-09 8h heartbeat
    - section 2.1  local watchdog: health timeout -> restart, 3 crashes in 5 min -> FALLBACK

  LOCAL ONLY:
    * starts the local edudesk server (serves 127.0.0.1), and
    * opens the system browser in kiosk mode against 127.0.0.1.
  It does NOT embed Chromium/WebView and does NOT pull remote HTML (design 05 section 7).

  Usage:
    .\edudesk-launcher.ps1 -Profile classroom -TerminalId A2F-07
    .\edudesk-launcher.ps1 -Profile corridor  -TerminalId B1-02
    .\edudesk-launcher.ps1 -Config ..\server-go\data\terminal-config.json

  Config (P1-11):
    -Config reads terminal-config.json so that profile / terminal id / port live in
    one validated file rather than in a scheduled-task argument string.

  Signing secret (P0-1):
    If EDUDESK_SECRET is a strong value (>= 32 chars) it is handed to the server.
    Otherwise the launcher starts the server in --dev mode and logs a loud warning:
    a display that refuses to boot is worse than a demo secret on an unprovisioned
    box. The *gate* against shipping a demo secret is preflight.ps1, not the runtime.
#>
param(
    [ValidateSet('classroom', 'corridor')]
    [string]$Profile = 'classroom',

    [string]$TerminalId = 'DEV-00',
    [int]$Port = 8080,
    [int]$HealthIntervalSec = 15,
    [int]$HeartbeatEverySec = 28800,   # 8h, design W-09
    [string]$Secret = '',
    [string]$Config = '',
    [string]$ServerUrl = ''
)

$ErrorActionPreference = 'Continue'
$root    = Split-Path -Parent $MyInvocation.MyCommand.Path
$srvDir  = Join-Path (Split-Path -Parent $root) 'server-go'
$srvExe  = Join-Path $srvDir 'edudesk.exe'
$logDir  = Join-Path $root 'logs'
$logFile = Join-Path $logDir ("launcher-{0:yyyyMMdd}.log" -f (Get-Date))

# ---- P1-11: load terminal-config.json if provided ---------------------------
if ($Config -ne '') {
    if (-not (Test-Path $Config)) {
        Write-Host ("FATAL: -Config not found: {0}" -f $Config) -ForegroundColor Red
        exit 1
    }
    try {
        $cfgObj = Get-Content -Raw -Path $Config -Encoding UTF8 | ConvertFrom-Json
        if ($cfgObj.profile)    { $Profile    = $cfgObj.profile }
        if ($cfgObj.terminal_id){ $TerminalId = $cfgObj.terminal_id }
        if ($cfgObj.server_url) { $ServerUrl  = $cfgObj.server_url }
    } catch {
        Write-Host ("FATAL: -Config is not valid JSON: {0}" -f $_.Exception.Message) -ForegroundColor Red
        exit 1
    }
}
if ($ServerUrl -eq '') { $ServerUrl = "http://127.0.0.1:$Port" }
$base = $ServerUrl.TrimEnd('/')

if (-not (Test-Path $logDir)) { New-Item -ItemType Directory -Path $logDir -Force | Out-Null }

function Write-Log([string]$msg) {
    $line = "[{0:yyyy-MM-dd HH:mm:ss}] {1}" -f (Get-Date), $msg
    Write-Host $line
    Add-Content -Path $logFile -Value $line -Encoding UTF8
}

function Test-Health {
    try {
        $r = Invoke-WebRequest -Uri "$base/health" -TimeoutSec 4 -UseBasicParsing
        return ($r.StatusCode -eq 200)
    } catch { return $false }
}

function Get-BundleInfo {
    try {
        $b = Invoke-RestMethod -Uri "$base/bundle?profile=$Profile" -TimeoutSec 5
        return $b.bundle_id
    } catch { return 'n/a' }
}

function Start-Server {
    if (-not (Test-Path $srvExe)) {
        Write-Log "FATAL: server exe not found at $srvExe"
        return $false
    }
    # Only the local-server topology starts a process here. When -Config points at a
    # LAN server (P1-9 / multi-terminal), there is nothing local to launch.
    if ($base -notmatch '^https?://(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$') {
        Write-Log "INFO: server_url is remote ($base); not starting a local server"
        return $false
    }

    # ---- P0-1 secret contract ----
    $effectiveSecret = $Secret
    if ($effectiveSecret -eq '') { $effectiveSecret = $env:EDUDESK_SECRET }
    $argList = @('-addr', ":$Port", '-web', $srvDir)
    if ($effectiveSecret -ne '' -and $effectiveSecret.Length -ge 32 -and $effectiveSecret -ne 'dev-only-secret-change-me') {
        $argList += @('-secret', $effectiveSecret)
        Write-Log ("starting server: {0} -addr :{1}  [production secret, {2} bytes]" -f $srvExe, $Port, $effectiveSecret.Length)
    } else {
        # Degrade with a loud warning instead of refusing to boot: a black corridor
        # screen is a worse outcome than a demo secret on a not-yet-provisioned box.
        # preflight.ps1 is the gate that blocks this from reaching go-live.
        $argList += @('-dev')
        Write-Log 'WARNING: no strong EDUDESK_SECRET -> starting server with --dev (demo secret). DO NOT go live in this mode.'
    }

    Start-Process -FilePath $srvExe `
        -ArgumentList $argList `
        -WorkingDirectory $srvDir -WindowStyle Hidden | Out-Null
    # wait up to 30s for health
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Seconds 1
        if (Test-Health) { Write-Log 'server is healthy'; return $true }
    }
    Write-Log 'WARN: server did not become healthy within 30s'
    return $false
}

function Find-Browser {
    $candidates = @(
        "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe",
        "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe",
        "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
        "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe"
    )
    foreach ($c in $candidates) { if ($c -and (Test-Path $c)) { return $c } }
    return $null
}

function Start-Kiosk {
    $browser = Find-Browser
    if (-not $browser) { Write-Log 'FATAL: no Edge/Chrome found for kiosk mode'; return $false }
    $url = "$base/?profile=$Profile&terminal=$TerminalId"
    $args = @(
        '--kiosk', $url,
        '--no-first-run', '--no-default-browser-check',
        '--disable-features=Translate,msEdgeTranslate',
        '--disable-pinch', '--overscroll-history-navigation=0',
        '--edge-kiosk-type=fullscreen', '--kiosk-idle-timeout-minutes=0'
    )
    Write-Log "launching kiosk: $browser $($args -join ' ')"
    Start-Process -FilePath $browser -ArgumentList $args | Out-Null
    return $true
}

function Get-KioskProcess {
    $names = @('msedge', 'chrome')
    foreach ($n in $names) {
        $p = Get-Process -Name $n -ErrorAction SilentlyContinue
        if ($p) { return $p[0] }
    }
    return $null
}

function Show-Fallback {
    $fb = Join-Path $root 'fallback.html'
    if (Test-Path $fb) {
        Write-Log "entering FALLBACK screen: $fb"
        Start-Process $fb | Out-Null
    } else {
        Write-Log 'FALLBACK requested but fallback.html missing'
    }
}

# ---------------- main ----------------
Write-Log "=== EduDesk launcher start (profile=$Profile terminal=$TerminalId server=$base) ==="

if (-not (Test-Health)) { Start-Server | Out-Null }
else { Write-Log 'server already healthy; reusing' }

if (-not (Start-Kiosk)) { Write-Log 'kiosk launch failed; aborting'; exit 1 }

$crashTimes = New-Object System.Collections.ArrayList
$lastHeartbeat = Get-Date
$startedAt = Get-Date

while ($true) {
    Start-Sleep -Seconds $HealthIntervalSec

    # 1) server health
    if (-not (Test-Health)) {
        Write-Log 'health check failed'
        $fails = 0
        for ($i = 0; $i -lt 3; $i++) {
            Start-Sleep -Seconds 2
            if (Test-Health) { break }
            $fails++
        }
        if ($fails -ge 3) {
            Write-Log '3 consecutive health failures -> restarting server'
            [void]$crashTimes.Add((Get-Date))
            # prune to 5 min window
            $cut = (Get-Date).AddMinutes(-5)
            $recent = @($crashTimes | Where-Object { $_ -gt $cut })
            if ($recent.Count -ge 3) {
                Write-Log 'CRASH STORM: >=3 restarts in 5 min -> FALLBACK, stop loop'
                Show-Fallback
                exit 2
            }
            Start-Server | Out-Null
            Start-Kiosk | Out-Null
        }
    }

    # 2) kiosk process alive (W-05)
    if (-not (Get-KioskProcess)) {
        Write-Log 'kiosk process gone -> relaunching'
        Start-Kiosk | Out-Null
    }

    # 3) heartbeat (W-09)
    $since = ((Get-Date) - $lastHeartbeat).TotalSeconds
    if ($since -ge $HeartbeatEverySec) {
        try {
            $proc = Get-Process -Name edudesk -ErrorAction SilentlyContinue | Select-Object -First 1
            $memMB = if ($proc) { [math]::Round($proc.WorkingSet64 / 1MB, 1) } else { 'n/a' }
        } catch { $memMB = 'n/a' }
        $upMin = [math]::Round(((Get-Date) - $startedAt).TotalMinutes, 0)
        $bid = Get-BundleInfo
        Write-Log "HEARTBEAT uptime_min=$upMin mem_mb=$memMB bundle_id=$bid state=$(if(Test-Health){'NORMAL'}else{'OFFLINE'}) profile=$Profile"
        $lastHeartbeat = Get-Date
    }
}
