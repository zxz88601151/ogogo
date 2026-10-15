#Requires -RunAsAdministrator
<#
  preflight.ps1  -  EduDesk Offline pre-go-live gate  (patch plan P0-1 / P0-2 / P0-4)

  Why this exists: the demo accounts (teacher01 / admin01 / academic01) and the
  placeholder signing secret are perfectly fine on a laptop and catastrophic on a
  corridor screen. Relying on "remember to change it" is not a control.
  This script makes the go/no-go decision explicit and machine-checked.

  Usage:
    .\preflight.ps1                              # check only
    .\preflight.ps1 -ServerRoot C:\EduDesk\server-go
    .\preflight.ps1 -SiteName "Demo-School"      # also require site customisation

  Exit code: 0 = all required checks PASS ; 1 = at least one FAIL (do NOT deploy).

  Output is a PASS/FAIL table. Nothing is modified - this is a read-only gate.
#>
[CmdletBinding()]
param(
    [string]$ServerRoot = '',
    [string]$SiteName   = '',
    [string[]]$DemoAccounts = @('teacher01', 'admin01', 'academic01'),
    [int]$MinSecretLen = 32,
    [string]$HealthUrl = 'http://127.0.0.1:8080/health'
)

$ErrorActionPreference = 'Continue'

# $PSCommandPath / $PSScriptRoot must be read at script scope: inside a function,
# $MyInvocation.MyCommand.Path is the *function's* invocation and comes back empty,
# which silently produced an empty server root during the first test run.
$ScriptDir = Split-Path -Parent $PSCommandPath
$script:Results = @()

function Add-Result {
    param(
        [string]$Id,
        [ValidateSet('PASS', 'FAIL', 'WARN')][string]$Status,
        [string]$Detail
    )
    $script:Results += [pscustomobject]@{ ID = $Id; Status = $Status; Detail = $Detail }
}

function Resolve-ServerRoot {
    param([string]$Explicit, [string]$BaseDir)
    if ($Explicit -ne '') { return $Explicit }
    # server-go sits next to this script's parent: implementation/deploy-windows -> implementation/server-go
    return (Join-Path (Split-Path -Parent $BaseDir) 'server-go')
}

$root   = Resolve-ServerRoot -Explicit $ServerRoot -BaseDir $ScriptDir
$users  = Join-Path $root 'data\users.json'
$secret = $env:EDUDESK_SECRET

Write-Host ''
Write-Host 'EduDesk preflight gate' -ForegroundColor Cyan
Write-Host ("  server root : {0}" -f $root)
Write-Host ''

# ---- P0-1  signing secret is not the placeholder ------------------------------
if ([string]::IsNullOrEmpty($secret)) {
    Add-Result 'P0-1.secret' 'FAIL' 'EDUDESK_SECRET is not set. Start with: $env:EDUDESK_SECRET = "<32+ random bytes>"'
} elseif ($secret -eq 'dev-only-secret-change-me') {
    Add-Result 'P0-1.secret' 'FAIL' 'EDUDESK_SECRET is still the shipped demo value'
} elseif ($secret.Length -lt $MinSecretLen) {
    Add-Result 'P0-1.secret' 'FAIL' ("EDUDESK_SECRET is {0} chars, minimum is {1}" -f $secret.Length, $MinSecretLen)
} else {
    Add-Result 'P0-1.secret' 'PASS' ("EDUDESK_SECRET length {0}" -f $secret.Length)
}

# ---- P0-1b  the *running* instance is not in dev mode -------------------------
# The env var check above only covers this shell. A Scheduled Task or service entry
# can still be launching the server with --dev, and nothing in the environment would
# reveal it. /health reports the running process's own mode.
try {
    $h = Invoke-RestMethod -Uri $HealthUrl -TimeoutSec 4
    if ($h.dev -eq $true) {
        Add-Result 'P0-1b.running-mode' 'FAIL' ("the running server reports dev=true (demo secret in use): {0}" -f $HealthUrl)
    } else {
        Add-Result 'P0-1b.running-mode' 'PASS' ("running server is not in dev mode (pid {0})" -f $h.pid)
    }
} catch {
    Add-Result 'P0-1b.running-mode' 'WARN' ("no server answering at {0} - cannot confirm the running mode" -f $HealthUrl)
}

# ---- P0-2  demo accounts removed or disabled ----------------------------------
if (-not (Test-Path $users)) {
    Add-Result 'P0-2.accounts' 'FAIL' ("users.json not found at {0}" -f $users)
} else {
    try {
        # -Encoding UTF8: PS 5.1 would otherwise read users.json as code page 936 and
        # fail on the Chinese display names. See 06-validate-terminal-config.ps1.
        $parsed = Get-Content -Raw -Path $users -Encoding UTF8 | ConvertFrom-Json
        $hits = @()
        foreach ($a in $DemoAccounts) {
            $hit = $parsed | Where-Object { $_.username -eq $a }
            if ($hit) {
                $state = if ($hit.active) { 'ACTIVE' } else { 'inactive' }
                $hits += ("{0}({1})" -f $a, $state)
            }
        }
        $live = $parsed | Where-Object { $_.username -in $DemoAccounts -and $_.active -eq $true }
        if (@($live).Count -gt 0) {
            Add-Result 'P0-2.accounts' 'FAIL' ("demo accounts still active: {0}" -f ($hits -join ', '))
        } elseif (@($hits).Count -gt 0) {
            Add-Result 'P0-2.accounts' 'WARN' ("demo accounts present but inactive: {0} - delete them before go-live" -f ($hits -join ', '))
        } else {
            Add-Result 'P0-2.accounts' 'PASS' 'no demo accounts in users.json'
        }
    } catch {
        Add-Result 'P0-2.accounts' 'FAIL' ("users.json is not valid JSON: {0}" -f $_.Exception.Message)
    }
}

# ---- P0-4  time source is real NTP, not the CMOS default ----------------------
try {
    $wt = (w32tm /query /status 2>$null) -join "`n"
    if ($wt -match '(?m)^\s*Source:\s*(.+)$') {
        $src = $Matches[1].Trim()
        if ($src -match 'Local CMOS Clock|Free-running|^$') {
            Add-Result 'P0-4.ntp' 'FAIL' ("time source is not NTP: {0} - run 02-ntp.ps1 with a real server" -f $src)
        } else {
            Add-Result 'P0-4.ntp' 'PASS' ("time source: {0}" -f $src)
        }
    } else {
        Add-Result 'P0-4.ntp' 'WARN' 'could not read w32tm status (service may be stopped)'
    }
} catch {
    Add-Result 'P0-4.ntp' 'WARN' 'w32tm not available'
}

# ---- P0-6  the retired Node implementation is not deployable -----------------
$nodeSrv = Join-Path (Split-Path -Parent $root) 'server'
if (Test-Path $nodeSrv) {
    Add-Result 'P0-6.node-retired' 'FAIL' ("legacy Node implementation still present at {0} - archive it" -f $nodeSrv)
} else {
    Add-Result 'P0-6.node-retired' 'PASS' 'no live legacy Node implementation'
}

# ---- P1-7  revocation list is writable ---------------------------------------
# The server persists revoked token ids here; if the directory is read-only the
# list silently fails to save and logged-out tokens come back to life on restart.
$revDir = Join-Path $root 'data'
$revFile = Join-Path $revDir 'revocations.json'
if (-not (Test-Path $revDir)) {
    Add-Result 'P1-7.revocation' 'FAIL' ("data directory missing: {0}" -f $revDir)
} else {
    try {
        # .NET APIs, not Set-Content/Remove-Item: hostile-shell guards can veto the
        # cmdlets and would be misreported here as "directory not writable".
        $probe = Join-Path $revDir '.preflight-write-probe'
        [System.IO.File]::WriteAllText($probe, 'ok')
        [System.IO.File]::Delete($probe)
        Add-Result 'P1-7.revocation' 'PASS' 'data directory is writable (revocation list can persist)'
    } catch {
        Add-Result 'P1-7.revocation' 'FAIL' ("data directory is not writable: {0} ({1})" -f $revDir, $_.Exception.Message)
    }
}

# A corrupt list makes the server refuse to start, which is correct but should be
# caught here rather than during a 07:00 roll-out.
if (Test-Path $revFile) {
    try {
        $null = Get-Content -Raw -Path $revFile -Encoding UTF8 | ConvertFrom-Json
        $n = @((Get-Content -Raw -Path $revFile -Encoding UTF8 | ConvertFrom-Json).PSObject.Properties).Count
        Add-Result 'P1-7.revocation-file' 'PASS' ("revocations.json parseable ({0} entries)" -f $n)
    } catch {
        Add-Result 'P1-7.revocation-file' 'FAIL' 'revocations.json is corrupt - the server will refuse to start'
    }
} else {
    Add-Result 'P1-7.revocation-file' 'PASS' 'no revocations.json yet (nobody has logged out)'
}

# ---- P1-9  transport is not plaintext on an untrusted segment -----------------
$cert = Get-ChildItem -Path (Join-Path $root 'tls') -Filter '*.crt' -ErrorAction SilentlyContinue
if ($cert) {
    Add-Result 'P1-9.tls' 'PASS' ("certificate found: {0}" -f $cert[0].Name)
} else {
    Add-Result 'P1-9.tls' 'WARN' 'no certificate under server-go\tls - either pass -tls-cert/-tls-key or front it with the Caddyfile.sample'
}

# ---- P1-10  watchdog is installed as a real Windows service ------------------
$svc = Get-Service -Name 'EduDeskWatchdogSvc' -ErrorAction SilentlyContinue
if ($svc) {
    Add-Result 'P1-10.watchdog' 'PASS' ("service state: {0}" -f $svc.Status)
} else {
    Add-Result 'P1-10.watchdog' 'FAIL' 'EduDeskWatchdogSvc is not installed - run edudesk-watchdog.exe install'
}

# ---- P1-11  terminal config is present and well-formed -----------------------
$tcfg = Join-Path $root 'data\terminal-config.json'
if (-not (Test-Path $tcfg)) {
    Add-Result 'P1-11.terminal-config' 'WARN' ("no terminal-config.json at {0} - validate with 06-validate-terminal-config.ps1" -f $tcfg)
} else {
    try {
        $c = Get-Content -Raw -Path $tcfg -Encoding UTF8 | ConvertFrom-Json
        if ($c.profile -notin @('classroom', 'corridor')) {
            Add-Result 'P1-11.terminal-config' 'FAIL' ("profile must be classroom|corridor, got '{0}'" -f $c.profile)
        } elseif ([string]::IsNullOrWhiteSpace($c.terminal_id)) {
            Add-Result 'P1-11.terminal-config' 'FAIL' 'terminal_id is empty'
        } else {
            Add-Result 'P1-11.terminal-config' 'PASS' ("{0} / {1}" -f $c.profile, $c.terminal_id)
        }
    } catch {
        Add-Result 'P1-11.terminal-config' 'FAIL' 'terminal-config.json is not valid JSON'
    }
}

# ---- P0-5  content operations owner is recorded ------------------------------
$ownerFile = Join-Path $root 'data\content-owner.txt'
if (Test-Path $ownerFile) {
    $name = (Get-Content -Raw -Path $ownerFile -Encoding UTF8).Trim()
    if ($name.Length -gt 0) {
        Add-Result 'P0-5.content-owner' 'PASS' ("recorded owner: {0}" -f $name)
    } else {
        Add-Result 'P0-5.content-owner' 'FAIL' 'content-owner.txt is empty'
    }
} else {
    Add-Result 'P0-5.content-owner' 'FAIL' ("no content owner recorded at {0} - this is a Gate G-0-e prerequisite" -f $ownerFile)
}

# ---- optional: site customisation -------------------------------------------
if ($SiteName -ne '') {
    $cfg = Join-Path $root 'data\site.json'
    if ((Test-Path $cfg) -and ((Get-Content -Raw $cfg -Encoding UTF8) -match [regex]::Escape($SiteName))) {
        Add-Result 'site.customised' 'PASS' ("site name present: {0}" -f $SiteName)
    } else {
        Add-Result 'site.customised' 'FAIL' ("site.json does not mention '{0}'" -f $SiteName)
    }
}

# ---- report -----------------------------------------------------------------
# Format-Table straight into the host interleaves with Write-Host and shreds the
# layout, so serialise to a string first.
$table = $script:Results | Format-Table -AutoSize ID, Status, Detail | Out-String -Width 200
Write-Host $table
$fails = @($script:Results | Where-Object { $_.Status -eq 'FAIL' })
$warns = @($script:Results | Where-Object { $_.Status -eq 'WARN' })
$passes = @($script:Results | Where-Object { $_.Status -eq 'PASS' })

Write-Host ("{0} PASS / {1} WARN / {2} FAIL" -f $passes.Count, $warns.Count, $fails.Count)
if ($fails.Count -gt 0) {
    Write-Host 'GO-LIVE BLOCKED: resolve every FAIL above.' -ForegroundColor Red
    exit 1
}
if ($warns.Count -gt 0) {
    Write-Host 'Preflight passed with warnings - acknowledge them in writing before go-live.' -ForegroundColor Yellow
}
Write-Host 'Preflight passed.' -ForegroundColor Green
exit 0
