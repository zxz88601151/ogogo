#Requires -RunAsAdministrator
<#
  03-install-autostart.ps1
  EduDesk Offline - Campus Display MVP  (design 05 W-01 / W-07 / W-10)

  Registers a Scheduled Task that launches edudesk-launcher.ps1 at user logon,
  and (optionally) a daily maintenance restart task at 05:00 (design W-07).

  It deliberately does NOT do anything remote and does NOT run a remote-content
  PowerShell task (design 05 section 7 forbids that persistence pattern).

  Usage:
    .\03-install-autostart.ps1 -Profile classroom -TerminalId A2F-07
    .\03-install-autostart.ps1 -Profile corridor  -TerminalId B1-02 -DailyRestart 05:00
    .\03-install-autostart.ps1 -Config ..\server-go\data\terminal-config.json -DailyRestart 05:00

  Preferred form (P1-11): -Config. Profile and terminal id then live in one JSON file
  that 06-validate-terminal-config.ps1 checks, instead of being typed into the task
  argument string where a typo is invisible until a screen shows the wrong content.
#>
param(
    [ValidateSet('classroom', 'corridor')]
    [string]$Profile = 'classroom',
    [string]$TerminalId = 'DEV-00',
    [int]$Port = 8080,
    [string]$DailyRestart = '',          # e.g. '05:00' ; empty = skip
    [string]$Config = ''                 # path to terminal-config.json (P1-11)
)

$ErrorActionPreference = 'Stop'
$here    = Split-Path -Parent $MyInvocation.MyCommand.Path
$launcher = Join-Path $here 'edudesk-launcher.ps1'
$taskName = "EduDesk-Display-$TerminalId"

if (-not (Test-Path $launcher)) { throw "launcher not found: $launcher" }

function New-EduTask {
    param([string]$Name, [object]$Action, [object]$Trigger, [string]$Desc)
    $existing = Get-ScheduledTask -TaskName $Name -ErrorAction SilentlyContinue
    if ($existing) { Unregister-ScheduledTask -TaskName $Name -Confirm:$false }
    $set = New-ScheduledTaskSettingsSet `
        -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit ([TimeSpan]::Zero)
    Register-ScheduledTask -TaskName $Name -Action $Action -Trigger $Trigger `
        -Settings $set -RunLevel Highest -Description $Desc | Out-Null
}

# ---- P1-11: validate the config first, and register the task against it -------
$launchArgs = ''
if ($Config -ne '') {
    $validator = Join-Path $here '06-validate-terminal-config.ps1'
    if (-not (Test-Path $validator)) { throw "validator not found: $validator" }
    Write-Host "[0/2] Validating terminal config: $Config"
    & $validator -ConfigPath $Config
    if ($LASTEXITCODE -ne 0) { throw "terminal config validation FAILED - fix it before registering autostart" }
    # read the effective profile/id back out for task naming only
    $cfgObj = Get-Content -Raw -Path $Config -Encoding UTF8 | ConvertFrom-Json
    $Profile    = $cfgObj.profile
    $TerminalId = $cfgObj.terminal_id
    $launchArgs = '-Config "{0}"' -f (Resolve-Path $Config).Path
} else {
    Write-Host '[0/2] No -Config given; profile/terminal id will be baked into the task arguments (legacy form).'
    $launchArgs = '-Profile {0} -TerminalId {1} -Port {2}' -f $Profile, $TerminalId, $Port
}

Write-Host "[1/2] Registering logon autostart task: $taskName"
$act = New-ScheduledTaskAction -Execute 'powershell.exe' `
    -Argument ("-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"{0}`" {1}" -f $launcher, $launchArgs)
$trg = New-ScheduledTaskTrigger -AtLogOn
New-EduTask -Name $taskName -Action $act -Trigger $trg `
    -Desc "EduDesk kiosk display autostart (profile=$Profile terminal=$TerminalId)."

if ($DailyRestart) {
    $rtName = "EduDesk-Maintenance-$TerminalId"
    Write-Host "[2/2] Registering daily maintenance restart at $DailyRestart : $rtName"
    $rst = New-ScheduledTaskAction -Execute 'shutdown.exe' -Argument '/r /t 0 /f'
    $rtr = New-ScheduledTaskTrigger -Daily -At $DailyRestart
    New-EduTask -Name $rtName -Action $rst -Trigger $rtr `
        -Desc "EduDesk daily maintenance reboot (design W-07) for terminal $TerminalId."
} else {
    Write-Host '[2/2] Skipped daily restart (pass -DailyRestart 05:00 to enable, design W-07).'
}

Write-Host ''
Write-Host 'Registered tasks:'
Get-ScheduledTask -TaskName "EduDesk-*$TerminalId" | Format-Table TaskName, State -AutoSize

Write-Host 'IMPORTANT (W-01): a Scheduled Task still needs a user session to render the screen.'
Write-Host 'Choose ONE of:'
Write-Host '  a) Assigned Access kiosk user (run 04-assigned-access.ps1) - recommended, and'
Write-Host '     configure Windows auto-logon for that kiosk account;'
Write-Host '  b) Windows auto-logon (AutoAdminLogon). WARNING: it stores the password in the'
Write-Host '     registry (HKLM\...\Winlogon\DefaultPassword). Treat as a decision (D-06), not a default.'
