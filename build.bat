@echo off
chcp 65001 >nul
cd /d "%~dp0"

rem Refresh frontend cache-buster (?v=) using PowerShell (UTF-8 safe).
rem 2026-08-25 修复:缓存戳用 年月日时分(精确到分钟)——旧逻辑固定 yyyyMMdd+'x',
rem 同一天多次 build 戳不变,浏览器缓存旧 JS/CSS(改前端不生效的隐性根源);自定义戳(xbb 等)
rem 也因正则不匹配而永远不刷新。
powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$p='web\kb\index.html';" ^
  "$b=[IO.File]::ReadAllBytes((Resolve-Path $p));" ^
  "$s=[Text.Encoding]::UTF8.GetString($b);" ^
  "$v='?v=' + (Get-Date -Format 'yyyyMMddHHmm');" ^
  "$n=[regex]::Replace($s, '\?v=20\d{6,12}[a-z]*\d*', $v);" ^
  "if ($n -ne $s) { [IO.File]::WriteAllText((Resolve-Path $p), $n, [Text.UTF8Encoding]::new($false)); Write-Host 'refreshed ?v=' $v } else { Write-Host '?v up-to-date' }"

rem Compile as GUI subsystem (no terminal window on launch).
go build -ldflags "-H windowsgui -s -w" -o NiliX.exe .
if %errorlevel%==0 (
  echo BUILD OK: NiliX.exe
  exit /b 0
)
echo BUILD FAILED. See error above.
exit /b 1
