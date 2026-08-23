# EP01 自动渲染推进：assets → render → qc → assemble（后台运行，日志到 run_ep01.log）
$ErrorActionPreference = 'Continue'
$base = 'http://127.0.0.1:8787'
$log = 'C:\Mi\Ai\WorkBench\NiliX\run_ep01.log'
function L($m) { ("[{0}] {1}" -f (Get-Date -Format 'HH:mm:ss'), $m) | Tee-Object -FilePath $log -Append }

try {
  $html = (Invoke-WebRequest -Uri "$base/" -TimeoutSec 10 -UseBasicParsing).Content
  $tok = ([regex]::Match($html, 'NILIX_TOKEN\s*=\s*"([0-9a-f]{32})"')).Groups[1].Value
  if (-not $tok) { L 'token 提取失败'; exit 1 }
  $h = @{ 'X-NiliX-Token' = $tok }
  $cfg = 'C:\Mi\Ai\WorkBench\NiliX\manju\人间回收站\config.json'
  $enc = [uri]::EscapeDataString($cfg)

  function Trigger($ph) {
    $b = @{ config = $cfg; phase = $ph; episode = 'EP01' } | ConvertTo-Json
    $by = [System.Text.Encoding]::UTF8.GetBytes($b)
    $r = Invoke-RestMethod -Uri "$base/api/manju/run" -Method Post -Headers $h -ContentType 'application/json; charset=utf-8' -Body $by -TimeoutSec 20
    L ("触发阶段 {0} → {1}" -f $ph, ($r.stage))
  }
  function GetStage {
    $s = Invoke-RestMethod -Uri "$base/api/manju/status?config=$enc" -Method Get -Headers $h -TimeoutSec 15
    return $s
  }

  $order = @('assets', 'render', 'qc', 'assemble')
  Trigger 'assets'
  $deadline = (Get-Date).AddHours(3)
  foreach ($ph in $order) {
    L "等待阶段完成: $ph"
    while ((Get-Date) -lt $deadline) {
      $s = GetStage
      if ($s.running) { Start-Sleep -Seconds 30; continue }
      # 不 running：确认已推进到当前阶段（完成）或更后
      if ($s.stage -eq $ph) { break }
      Start-Sleep -Seconds 15
    }
    if ((Get-Date) -ge $deadline) { L "⏰ 超时于 $ph"; exit 2 }
    L "✅ 阶段完成: $ph"
    $idx = [array]::IndexOf($order, $ph)
    if ($idx -ge 0 -and $idx -lt $order.Count - 1) { Trigger $order[$idx + 1] }
  }
  L '🎉 EP01 全部阶段完成'
} catch {
  L ("❌ 异常: " + $_.Exception.Message)
  exit 3
}
