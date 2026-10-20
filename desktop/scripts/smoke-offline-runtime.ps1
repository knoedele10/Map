$ErrorActionPreference = 'Stop'

$desktopRoot = Split-Path -Parent $PSScriptRoot
$repoRoot = Split-Path -Parent $desktopRoot
$ollamaRoot = Join-Path $desktopRoot 'offline-runtime\ollama'
$portProbe = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
$portProbe.Start()
$port = $portProbe.LocalEndpoint.Port
$portProbe.Stop()
$env:OLLAMA_MODELS = Join-Path $desktopRoot 'offline-models\ollama\models'
$env:OLLAMA_HOST = "127.0.0.1:$port"
$env:OLLAMA_MAX_LOADED_MODELS = '1'
$env:OLLAMA_VULKAN = '0'
$logFile = Join-Path $ollamaRoot 'smoke-test.log'
$errorLogFile = Join-Path $ollamaRoot 'smoke-test-error.log'
$ollama = Start-Process -FilePath (Join-Path $ollamaRoot 'ollama.exe') -ArgumentList 'serve' `
  -WorkingDirectory $ollamaRoot -WindowStyle Hidden -PassThru `
  -RedirectStandardOutput $logFile -RedirectStandardError $errorLogFile

try {
  $baseUrl = "http://127.0.0.1:$port"
  $ready = $false
  for ($attempt = 0; $attempt -lt 120; $attempt++) {
    try {
      $tags = Invoke-RestMethod -Uri "$baseUrl/api/tags" -TimeoutSec 2
      $ready = $true
      break
    } catch {
      if ($ollama.HasExited) { throw "Bundled Ollama exited early. See $logFile" }
      Start-Sleep -Seconds 1
    }
  }
  if (-not $ready) { throw "Bundled Ollama did not become ready. See $logFile" }

  $names = @($tags.models | ForEach-Object { $_.name })
  foreach ($required in @('qwen2.5vl:7b', 'bge-m3:latest')) {
    if ($names -notcontains $required) { throw "Offline bundle is missing $required" }
  }

  $embedBody = @{ model = 'bge-m3:latest'; input = '成都武侯祠' } | ConvertTo-Json -Compress
  $embed = Invoke-RestMethod -Method Post -Uri "$baseUrl/api/embed" -ContentType 'application/json' -Body $embedBody -TimeoutSec 180
  $dimension = $embed.embeddings[0].Count
  if ($dimension -ne 1024) { throw "Unexpected embedding dimension: $dimension" }
  Write-Host "检索模型可用：bge-m3，向量维度 $dimension"

  $chatBody = @{
    model = 'qwen2.5vl:7b'
    messages = @(@{ role = 'user'; content = '只回复：聊天模型正常' })
    stream = $false
    options = @{ num_predict = 24 }
  } | ConvertTo-Json -Depth 8 -Compress
  $chat = Invoke-RestMethod -Method Post -Uri "$baseUrl/api/chat" -ContentType 'application/json' -Body $chatBody -TimeoutSec 300
  if (-not $chat.message.content) { throw 'The chat model returned an empty response.' }
  Write-Host "聊天模型可用：qwen2.5vl:7b（收到回答）"

  $imagePath = Join-Path $repoRoot 'frontend\shuji\assets\prologue-hotpot.png'
  $imageBase64 = [Convert]::ToBase64String([IO.File]::ReadAllBytes($imagePath))
  $visionBody = @{
    model = 'qwen2.5vl:7b'
    messages = @(@{ role = 'user'; content = '用一句话描述图片。'; images = @($imageBase64) })
    stream = $false
    options = @{ num_predict = 48 }
  } | ConvertTo-Json -Depth 8 -Compress
  $vision = Invoke-RestMethod -Method Post -Uri "$baseUrl/api/chat" -ContentType 'application/json' -Body $visionBody -TimeoutSec 300
  if (-not $vision.message.content) { throw 'The vision model returned an empty response.' }
  Write-Host '图片识别可用：qwen2.5vl:7b（收到回答）'
} finally {
  if (-not $ollama.HasExited) {
    & taskkill.exe /PID $ollama.Id /T /F | Out-Null
  }
}
