@echo off
rem NiliX build script.
rem
rem [ENCODING RULE - do not break] Keep this file ASCII-only.
rem cmd.exe parses .bat files with the system codepage (GBK on zh-CN Windows),
rem so UTF-8 Chinese comments here get mis-decoded and corrupt multi-line caret
rem continuations - comment fragments become bogus commands (repro: cmd /c from
rem Git Bash). Chinese docs live in tools\build_refresh_version.ps1 (UTF-8 BOM).
rem See tools\build_refresh_version.ps1 header for the full history.

cd /d "%~dp0"

rem Step 1: refresh frontend cache-buster (?v=) - minutes-precision timestamp,
rem so browsers force-reload JS/CSS on every build. Old fixed-date stamps kept
rem serving stale JS on same-day rebuilds.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0tools\build_refresh_version.ps1"
if errorlevel 1 (
  echo VERSION REFRESH FAILED.
  exit /b 1
)

rem Step 2: compile as GUI subsystem (no terminal window on launch).
go build -ldflags "-H windowsgui -s -w" -o NiliX.exe .
if %errorlevel%==0 (
  echo BUILD OK: NiliX.exe
  exit /b 0
)
echo BUILD FAILED. See error above.
exit /b 1
