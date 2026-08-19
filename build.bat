@echo off
cd /d "%~dp0"
echo 正在自动刷新前端版本参数 (?v=)...
powershell -NoProfile -Command "$p='web\kb\index.html'; $s=Get-Content $p -Raw; $v='?v=' + (Get-Date -Format 'yyyyMMdd') + 'x'; $n=[regex]::Replace($s, '\?v=2026\d{6}[a-z]\d*', $v); if ($n -ne $s) { Set-Content $p $n -NoNewline -Encoding UTF8; Write-Host '已刷新 ?v=' $v } else { Write-Host '?v 格式未识别(跳过)' }"
echo 正在编译 NiliX（GUI 子系统 · 无终端窗口）...
go build -ldflags "-H windowsgui -s -w" -o NiliX.exe .
if %errorlevel%==0 (
  echo 编译成功: NiliX.exe
  timeout /t 2 /nobreak >nul
  exit /b 0
)
echo 编译失败，请检查错误信息。
pause
