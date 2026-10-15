#Requires -RunAsAdministrator
<#
  04-assigned-access.ps1
  EduDesk Offline - Campus Display MVP  (design 05 section 1.1, decision D-06)

  Configures Windows Assigned Access (kiosk mode) so the terminal boots straight into
  the display and the user cannot reach the desktop, taskbar or browser chrome.

  Target: Windows 11 Pro / Enterprise (Assigned Access available).
  If the school standardizes on Education/Enterprise and wants a shell-level lock,
  use Shell Launcher instead (see README, D-06 alternatives).

  Usage:
    .\04-assigned-access.ps1 -KioskUser EduDeskKiosk -Profile classroom -TerminalId A2F-07
#>
param(
    [Parameter(Mandatory = $true)]
    [string]$KioskUser,

    [ValidateSet('classroom', 'corridor')]
    [string]$Profile = 'classroom',
    [string]$TerminalId = 'DEV-00',
    [int]$Port = 8080
)

$ErrorActionPreference = 'Stop'

Write-Host '[1/4] Checking Assigned Access cmdlet availability...'
$cmd = Get-Command Set-AssignedAccess -ErrorAction SilentlyContinue
if (-not $cmd) {
    Write-Warning 'Set-AssignedAccess cmdlet not found on this build.'
    Write-Warning 'Fallback: use Settings > Accounts > Other users > Set up a kiosk, OR switch to Shell Launcher.'
    Write-Warning 'Continuing will not change the system.'
    exit 1
}

Write-Host "[2/4] Ensuring kiosk user exists: $KioskUser"
if (-not (Get-LocalUser -Name $KioskUser -ErrorAction SilentlyContinue)) {
    Write-Warning "Local user '$KioskUser' does not exist. Create it first (with a password), then re-run:"
    Write-Warning "  New-LocalUser -Name $KioskUser -Password (Read-Host -AsSecureString) -PasswordNeverExpires"
    exit 1
}

Write-Host '[3/4] Reading browser AppUserModelId for kiosk app...'
# Edge stable AUMID; Chrome differs (Google Chrome). Adjust if using Chrome.
$aumid = 'MSEdge'
Write-Host "  using AUMID: $aumid"

Write-Host "[4/4] Applying Assigned Access: user=$KioskUser app=$aumid"
Set-AssignedAccess -AppUserModelId $aumid -UserName $KioskUser

Write-Host ''
Write-Host 'Applied. Verify by signing in as the kiosk user: it must boot directly into the display.'
Write-Host ''
Write-Host "NEXT: make the kiosk user auto-logon so no one types a password (design W-01)."
Write-Host "      Also point the browser start page at: http://127.0.0.1:$Port/?profile=$Profile&terminal=$TerminalId"
Write-Host '      (or rely on the logon Scheduled Task from 03-install-autostart.ps1).'
Write-Host ''
Write-Host 'TO UNDO: Set-AssignedAccess -AppUserModelId "" -UserName ""   (or run 99-uninstall.ps1)'
