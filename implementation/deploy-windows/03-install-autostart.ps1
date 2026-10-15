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
#>
param(
    [ValidateSet('classroom', 'corridor')]
    [string]$Profile = 'classroom',
    [string]$TerminalId = 'DEV-00',
    [int]$Port = 8080,
    [string]$DailyRestart = ''          # e.g. '05:00' ; empty = skip
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

Write-Host "[1/2] Registering logon autostart task: $taskName"
$act = New-ScheduledTaskAction -Execute 'powershell.exe' `
    -Argument ("-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"{0}`" -Profile {1} -TerminalId {2} -Port {3}" -f $launcher, $Profile, $TerminalId, $Port)
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
