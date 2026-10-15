#Requires -RunAsAdministrator
<#
  02-ntp.ps1
  EduDesk Offline - Campus Display MVP
  Design doc 05 section 3: sync local system clock to the school NTP server so that
  content staleness (ttl / max_offline_hours) is trustworthy.

  The terminal must NEVER trust an unsynced clock for staleness decisions.
  Usage:
    .\02-ntp.ps1 -NtpServer ntp.school.local
#>
param(
    [Parameter(Mandatory = $true)]
    [string]$NtpServer
)

$ErrorActionPreference = 'Stop'

Write-Host "[1/4] Configuring w32time to sync from $NtpServer ..."
# Manual peer list, no domain hierarchy sync.
w32tm /config /manualpeerlist:$NtpServer /syncfromflags:manual /reliable:yes /update

Write-Host '[2/4] Restarting Windows Time service...'
Set-Service -Name w32time -StartupType Automatic
Restart-Service -Name w32time -Force

Write-Host '[3/4] Forcing immediate resync...'
w32tm /resync /force

Write-Host '[4/4] Current status:'
w32tm /query /status

Write-Host ''
Write-Host 'NOTE: In a domain-joined terminal, prefer the default domain hierarchy'
Write-Host '      (omit /syncfromflags:manual) and let the DC provide time.'
Write-Host 'Expected drift after sync: < 1s. The terminal flags drift > 60s in the UI.'
