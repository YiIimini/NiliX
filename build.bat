@echo off
cd /d "%~dp0"
echo 正在编译 NiliX（GUI 子系统 · 无终端窗口）...
go build -ldflags "-H windowsgui -s -w" -o NiliX.exe .
if %errorlevel%==0 (
  echo 编译成功: NiliX.exe
  timeout /t 2 /nobreak >nul
  exit /b 0
)
echo 编译失败，请检查错误信息。
pause
