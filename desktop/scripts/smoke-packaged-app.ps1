param(
  [string]$AppRoot = 'E:\MapOfflineBuild\dist\win-unpacked',
  [string]$ModelSource = 'A:\Map\desktop\offline-models\ollama\models'
)

$ErrorActionPreference = 'Stop'
if (-not (Test-Path -LiteralPath $ModelSource)) { throw "离线模型目录不存在：$ModelSource" }

$appExe = Get-ChildItem -LiteralPath $AppRoot -Filter '*.exe' |
  Where-Object { $_.Name -like '*蜀迹*' } | Select-Object -First 1
if (-not $appExe) { throw "在 $AppRoot 中找不到桌面应用程序。" }

$electronRunAsNode = $env:ELECTRON_RUN_AS_NODE
$previousModelsDir = $env:SHUJI_OLLAMA_MODELS_DIR
Remove-Item Env:ELECTRON_RUN_AS_NODE -ErrorAction SilentlyContinue
$env:SHUJI_OLLAMA_MODELS_DIR = $ModelSource
$process = Start-Process -FilePath $appExe.FullName -WorkingDirectory $AppRoot -PassThru
if ($null -ne $electronRunAsNode) { $env:ELECTRON_RUN_AS_NODE = $electronRunAsNode }
if ($null -ne $previousModelsDir) { $env:SHUJI_OLLAMA_MODELS_DIR = $previousModelsDir } else { Remove-Item Env:SHUJI_OLLAMA_MODELS_DIR -ErrorAction SilentlyContinue }
$logPath = Join-Path $env:APPDATA 'shuji-rongbao-desktop\logs\server.log'
$port = $null
try {
  for ($attempt = 0; $attempt -lt 180; $attempt++) {
    if ($process.HasExited) { throw "桌面程序提前退出。日志：$logPath" }
    if (Test-Path -LiteralPath $logPath) {
      $lines = Get-Content -LiteralPath $logPath -Tail 5000
      $line = $lines | Where-Object { $_ -match 'addr=http://127\.0\.0\.1:(\d+)' } | Select-Object -Last 1
      if ($line -match 'addr=http://127\.0\.0\.1:(\d+)') { $port = [int]$Matches[1]; break }
    }
    Start-Sleep -Seconds 1
  }
  if (-not $port) {
    if (Test-Path -LiteralPath $logPath) { Get-Content -LiteralPath $logPath -Tail 30 }
    throw '打包后的桌面应用未能启动本地服务。'
  }

  $page = Invoke-WebRequest -Uri "http://127.0.0.1:$port/static/shuji/" -UseBasicParsing -TimeoutSec 15
  if ($page.StatusCode -ne 200) { throw "游戏页面返回 HTTP $($page.StatusCode)" }

  $chatBody = @{
    question = '请简短介绍成都。'
    stream = $false
    top_k = 3
    use_tools = $false
    rewrite = $false
  } | ConvertTo-Json -Compress
  $chat = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$port/api/v1/chat" `
    -ContentType 'application/json' -Body $chatBody -TimeoutSec 300
  if (-not $chat.data.answer) { throw '打包应用的聊天接口没有返回回答。' }
  Write-Host '桌面版游戏页面 HTTP 200，文字聊天成功。'

  $imagePath = Join-Path (Split-Path -Parent $AppRoot) '..\..\Map\frontend\shuji\assets\prologue-hotpot.png'
  if (-not (Test-Path -LiteralPath $imagePath)) { $imagePath = 'A:\Map\frontend\shuji\assets\prologue-hotpot.png' }
  $image64 = [Convert]::ToBase64String([IO.File]::ReadAllBytes($imagePath))
  $visionBody = @{
    question = '请用一句话说说这张图里有什么。'
    images = @("data:image/png;base64,$image64")
    stream = $false
    top_k = 3
    use_tools = $false
    rewrite = $false
  } | ConvertTo-Json -Compress
  $vision = Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$port/api/v1/chat" `
    -ContentType 'application/json' -Body $visionBody -TimeoutSec 300
  if (-not $vision.data.answer) { throw '打包应用的图片识别接口没有返回回答。' }
  Write-Host "桌面版图片识别成功，回答模型：$($vision.data.model)"
} finally {
  if (-not $process.HasExited) {
    [void]$process.CloseMainWindow()
    if (-not $process.WaitForExit(30000)) {
      & taskkill.exe /PID $process.Id /T /F | Out-Null
      if (-not $process.WaitForExit(10000)) { throw '测试进程无法关闭。' }
    }
  }
}
