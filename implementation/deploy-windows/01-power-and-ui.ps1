#Requires -RunAsAdministrator
<#
  01-power-and-ui.ps1
  EduDesk Offline - Campus Display MVP
  Implements design doc 05 section 1.2 requirements W-06 (no sleep / screen saver / monitor off)
  and section 5 (screen protection / power policy).

  Local only. No remote content fetch. No external dependency.
  Run once per terminal as Administrator.
#>

$ErrorActionPreference = 'Stop'

Write-Host '[1/5] Disabling monitor / standby / hibernate / disk timeouts (AC power)...'
powercfg /change monitor-timeout-ac 0
powercfg /change standby-timeout-ac 0
powercfg /change hibernate-timeout-ac 0
powercfg /change disk-timeout-ac 0

Write-Host '[2/5] Disabling USB selective suspend (AC)...'
# USB selective suspend setting GUID (2a737441-1930-4402-8d77-b2bebba308a3)
powercfg /setacvalueindex SCHEME_CURRENT 2a737441-1930-4402-8d77-b2bebba308a3 48e6b7a6-50f5-4782-a5d4-53bb8f07e226 0
powercfg /setactive SCHEME_CURRENT

Write-Host '[3/5] Disabling screen saver...'
$desk = 'HKCU:\Control Panel\Desktop'
if (Test-Path $desk) {
    Set-ItemProperty -Path $desk -Name 'ScreenSaveActive'   -Value '0' -Type String
    Set-ItemProperty -Path $desk -Name 'ScreenSaveTimeOut'  -Value '0' -Type String
}
# Also disable screen saver policy machine-wide so any kiosk user is covered
$pol = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\Control Panel\Desktop'
if (-not (Test-Path $pol)) { New-Item -Path $pol -Force | Out-Null }
Set-ItemProperty -Path $pol -Name 'ScreenSaveActive'  -Value '0' -Type String
Set-ItemProperty -Path $pol -Name 'ScreenSaveTimeOut' -Value '0' -Type String
Set-ItemProperty -Path $pol -Name 'SCRNSAVE.EXE'      -Value ''  -Type String

Write-Host '[4/5] Disabling lock screen / fast user switching prompts...'
$sys = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System'
if (-not (Test-Path $sys)) { New-Item -Path $sys -Force | Out-Null }
# NoLockScreen is a Windows Server / Enterprise policy; harmless if unsupported.
Set-ItemProperty -Path $sys -Name 'DisableLockWorkstation' -Value 1 -Type DWord

Write-Host '[5/5] Done. Verify with: powercfg /query SCHEME_CURRENT SUB_SLEEP'
Write-Host 'NOTE: BIOS "AC Recovery = Power On" (design W-08) must be set in firmware; not scriptable from OS.'
