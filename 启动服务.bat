@echo off
cd /d "%~dp0"
start "" "NiliX.exe" -config settings.json -port 8787
echo 服务已启动: http://127.0.0.1:8787
