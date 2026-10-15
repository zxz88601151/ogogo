@echo off
REM ------------------------------------------------------------------
REM EduDesk watchdog recovery action
REM
REM Design 05 sec 2.1, row 4: when the watchdog service itself fails, the
REM Windows Service Recovery policy takes over:
REM   - 1st / 2nd failure: restart the service
REM   - 3rd failure: run this recovery script
REM
REM This script runs as LocalSystem (same account as the service). It only:
REM   1. appends an evidence line to the local log, and
REM   2. attempts to start the service again.
REM It does NOT fetch remote content and does NOT download anything
REM (design 05 sec 7 dependency blacklist).
REM
REM NOTE: this file must stay pure ASCII. cmd.exe reads it using the OEM
REM code page (936 on zh-CN), so non-ASCII comments get mangled and are
REM executed as commands.
REM ------------------------------------------------------------------

set LOGDIR=%ProgramData%\EduDesk\logs
if not exist "%LOGDIR%" mkdir "%LOGDIR%"

set LOG=%LOGDIR%\watchdog-recovery.log

echo [%DATE% %TIME%] recovery action invoked by SCM >> "%LOG%"
sc query EduDeskWatchdogSvc >> "%LOG%" 2>&1
echo [%DATE% %TIME%] attempting restart >> "%LOG%"
sc start EduDeskWatchdogSvc >> "%LOG%" 2>&1
echo [%DATE% %TIME%] recovery action finished >> "%LOG%"

exit /b 0
