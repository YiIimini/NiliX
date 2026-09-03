# NiliX 构建辅助:刷新前端资产缓存戳 ?v=(web/kb/index.html)
# 由 build.bat 每次构建前调用。
#
# 为什么独立成文件(2026-09-03 根治):
#   旧版 build.bat 把这段 PowerShell 用 ^ 多行续行内嵌在 bat 里,且 bat 带 UTF-8 中文注释。
#   cmd 按系统代码页(中文系统=GBK)解析 bat,UTF-8 中文注释被误读后破坏续行结构,
#   注释碎片被当成命令执行——用户双击偶尔异常,Git Bash(cmd //c)下必现乱码报错。
#   bat 必须保持纯 ASCII;中文说明只放本文件(见下方编码要求)。
#
# 编码要求(勿破坏):
#   本文件必须保存为 UTF-8 with BOM——Windows PowerShell 5.1 对无 BOM 脚本按 ANSI
#   解析,中文注释/字符串会坏。用 Python 写入时 encoding='utf-8-sig'。
#   控制台输出消息一律英文(代码页不定,防输出乱码);中文只留在注释里。
#
# 语义与旧版一致:把 index.html 里所有 ?v=20xxxxxxxxxx[..] 统一替换为当前时间戳
# (yyyyMMddHHmm,精确到分钟),配合服务端 no-cache 让浏览器强刷 JS/CSS。

$ErrorActionPreference = 'Stop'

# 项目根 = 本脚本(tools/)的上一级,不依赖调用时的工作目录
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
$p = Join-Path $root 'web\kb\index.html'
if (-not (Test-Path $p)) { Write-Host ("REFRESH FAILED: index.html not found: " + $p); exit 1 }

$v = '?v=' + (Get-Date -Format 'yyyyMMddHHmm')
# 无 BOM UTF-8 显式读写(index.html 本体无 BOM,写回保持一致)
$s = [IO.File]::ReadAllText($p, [Text.UTF8Encoding]::new($false))
$hits = [regex]::Matches($s, '\?v=20\d{6,12}[a-z]*\d*').Count
if ($hits -lt 1) { Write-Host "REFRESH FAILED: no ?v= tokens matched in index.html"; exit 1 }
$n = [regex]::Replace($s, '\?v=20\d{6,12}[a-z]*\d*', $v)
[IO.File]::WriteAllText($p, $n, [Text.UTF8Encoding]::new($false))
Write-Host ("refreshed ?v= -> " + $v + " (" + $hits + " tokens)")
exit 0
