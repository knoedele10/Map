param(
  [ValidateSet('local','deepseek')] [string]$Chat = 'local',
  [switch]$NoBrowser,
  [switch]$Ingest
)
$ErrorActionPreference = 'Stop'
$projectRoot = $PSScriptRoot
$runtimeDir = Join-Path $projectRoot 'data/runtime'
New-Item -ItemType Directory -Path $runtimeDir,(Join-Path $runtimeDir 'tmp'),(Join-Path $runtimeDir 'go-cache') -Force | Out-Null
$env:GOCACHE = Join-Path $runtimeDir 'go-cache'
$env:TEMP = Join-Path $runtimeDir 'tmp'
$env:TMP = $env:TEMP
# The console writes each provider group to a separate local-only env file.
foreach ($envFile in @('.env.chat.local', '.env.embedding.local', '.env.vision.local')) {
  $fullEnvPath = Join-Path (Join-Path $projectRoot 'config') $envFile
  if (-not (Test-Path -LiteralPath $fullEnvPath)) { continue }
  foreach ($line in Get-Content -LiteralPath $fullEnvPath) {
    if ($line -notmatch '^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$') { continue }
    $name = $Matches[1]
    $rawValue = $Matches[2]
    try { $value = ConvertFrom-Json -InputObject $rawValue -ErrorAction Stop }
    catch { throw "本机模型配置文件格式有误：$envFile" }
    Set-Item -Path "Env:$name" -Value ([string]$value)
  }
}
try {
  $existing = Invoke-RestMethod 'http://127.0.0.1:8090/api/v1/health' -TimeoutSec 2
  if ($existing.data.version -eq '0.2.0') {
    Write-Host '项目已经在运行：http://127.0.0.1:8090/static/shuji/'
    if (-not $NoBrowser) { Start-Process 'http://127.0.0.1:8090/static/shuji/' }
    return
  }
} catch {}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw '找不到 Go，请安装 Go 1.25 或更新版本。' }
$ollama = Get-Command ollama -ErrorAction SilentlyContinue
if (-not $ollama) { throw '找不到 Ollama。请安装并准备 bge-m3、qwen2.5:7b 和 qwen2.5vl-16k:latest，或使用 RAG_* 环境变量配置已有模型服务。' }
try { $null = Invoke-RestMethod 'http://127.0.0.1:11434/api/tags' -TimeoutSec 3 }
catch {
  $env:OLLAMA_NOPRUNE = 'true'
  Start-Process -FilePath $ollama.Source -ArgumentList 'serve' -WindowStyle Hidden
  $ready = $false
  for ($attempt = 0; $attempt -lt 30; $attempt++) {
    Start-Sleep -Milliseconds 500
    try { $null = Invoke-RestMethod 'http://127.0.0.1:11434/api/tags' -TimeoutSec 2; $ready = $true; break } catch {}
  }
  if (-not $ready) { throw 'Ollama 启动失败，请检查 11434 端口。' }
}
if ($Chat -eq 'deepseek') {
  if (-not $env:RAG_CHAT_API_KEY) { $env:RAG_CHAT_API_KEY = $env:DEEPSEEK_API_KEY }
  if (-not $env:RAG_CHAT_API_KEY) { throw '请先设置 DEEPSEEK_API_KEY 或 RAG_CHAT_API_KEY。' }
  $env:RAG_CHAT_PROVIDER = 'openai'
  $env:RAG_CHAT_BASE_URL = 'https://api.deepseek.com/v1'
  $env:RAG_CHAT_MODEL = 'deepseek-chat'
  # Text and vision adapters must use their own provider credentials.
  $env:RAG_CHAT_VISION_MODEL = ''
}
Set-Location (Join-Path $projectRoot 'backend')
Write-Host '地图: http://127.0.0.1:8090/static/shuji/'
Write-Host '知识库控制台: http://127.0.0.1:8090/'
Write-Host 'Ctrl+C 停止后端。首次使用在控制台点击“增量入库”。'
if (-not $NoBrowser) {
  $openBrowserJob = Start-Job -ScriptBlock {
    for ($attempt = 0; $attempt -lt 120; $attempt++) {
      try {
        $null = Invoke-RestMethod 'http://127.0.0.1:8090/api/v1/health' -TimeoutSec 3
        Start-Process 'http://127.0.0.1:8090/static/shuji/'
        break
      } catch { Start-Sleep -Seconds 1 }
    }
  }
}
$serverArgs = @('run','./cmd/server','-config',(Join-Path $projectRoot 'backend/configs/config.dev.yaml'))
if ($Ingest) { $serverArgs += '-ingest' }
$controlMarker = Join-Path $runtimeDir 'service-control.request'
$env:RAG_CONTROL_MARKER = $controlMarker
while ($true) {
	if (Test-Path -LiteralPath $controlMarker) { Remove-Item -LiteralPath $controlMarker -Force }
  & go @serverArgs
  $serverExitCode = $LASTEXITCODE
	if (Test-Path -LiteralPath $controlMarker) {
		$controlAction = (Get-Content -LiteralPath $controlMarker -Raw).Trim().Split(' ')[0]
		Remove-Item -LiteralPath $controlMarker -Force
		if ($controlAction -eq 'restart') {
			Write-Host '服务正在重新启动…'
			$serverArgs = @($serverArgs | Where-Object { $_ -ne '-ingest' })
			Start-Sleep -Seconds 1
			continue
		}
		if ($controlAction -eq 'stop') {
			Write-Host '服务已停止。再次启动请运行 启动项目.cmd。'
			break
		}
  }
  if ($serverExitCode -ne 0) { exit $serverExitCode }
  break
}
