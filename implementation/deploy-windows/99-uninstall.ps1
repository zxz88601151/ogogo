#Requires -RunAsAdministrator
<#
  99-uninstall.ps1
  EduDesk Offline - Campus Display MVP
  Rolls back the terminal-side deployment done by 01..04.

  It does NOT touch the server or any content. It only removes:
    - Scheduled tasks created by 03-install-autostart.ps1
    - Assigned Access configuration (if present)

  Usage:
    .\99-uninstall.ps1 -TerminalId A2F-07
#>
param(
    [string]$TerminalId = 'DEV-00',
    [switch]$RemoveKioskUser
)

$ErrorActionPreference = 'Continue'

Write-Host "[1/3] Removing Scheduled Tasks matching EduDesk-*$TerminalId ..."
Get-ScheduledTask -TaskName "EduDesk-*$TerminalId" -ErrorAction SilentlyContinue | ForEach-Object {
    Write-Host "  removing task: $($_.TaskName)"
    Unregister-ScheduledTask -TaskName $_.TaskName -Confirm:$false
}

Write-Host '[2/3] Clearing Assigned Access (if configured)...'
if (Get-Command Set-AssignedAccess -ErrorAction SilentlyContinue) {
    try {
        Set-AssignedAccess -AppUserModelId '' -UserName '' -ErrorAction Stop
        Write-Host '  Assigned Access cleared.'
    } catch {
        Write-Host "  no Assigned Access to clear, or manual reset needed: $($_.Exception.Message)"
    }
} else {
    Write-Host '  Set-AssignedAccess not available on this build; skip.'
}

Write-Host '[3/3] Note: power/screensaver policy from 01 is NOT reverted (safe defaults).'
if ($RemoveKioskUser) {
    Write-Host '  (-RemoveKioskUser not implemented automatically; remove the local user manually if needed.)'
}

Write-Host ''
Write-Host 'Stopping any running display/server processes...'
Get-Process -Name edudesk -ErrorAction SilentlyContinue | Stop-Process -Force
Get-Process -Name msedge, chrome -ErrorAction SilentlyContinue | Where-Object {
    $_.MainWindowTitle -like '*EduDesk*' -or $true
} | ForEach-Object { } # left intentionally conservative: do not kill user's browser silently
Write-Host 'Done. (Browser processes were NOT force-closed to avoid disrupting the operator.)'
