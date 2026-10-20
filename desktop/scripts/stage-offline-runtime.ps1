$ErrorActionPreference = 'Stop'

$desktopRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$ollamaCommand = Get-Command ollama -ErrorAction SilentlyContinue
if (-not $ollamaCommand) {
  throw '找不到 Ollama。请先安装 Ollama，并运行 ollama pull qwen2.5vl:7b 和 ollama pull bge-m3。'
}

$ollamaExe = $ollamaCommand.Source
$ollamaRoot = Split-Path -Parent $ollamaExe
$modelsRoot = $env:OLLAMA_MODELS
if (-not $modelsRoot) { $modelsRoot = Join-Path $env:USERPROFILE '.ollama\models' }
$modelsRoot = [IO.Path]::GetFullPath($modelsRoot)

$stageRoot = [IO.Path]::GetFullPath((Join-Path $desktopRoot 'offline-runtime\ollama'))
$bundleRoot = [IO.Path]::GetFullPath((Join-Path $desktopRoot 'offline-models\ollama'))
$stageRootCheck = $stageRoot.StartsWith($desktopRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)
$bundleRootCheck = $bundleRoot.StartsWith($desktopRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)
if (-not $stageRootCheck -or -not $bundleRootCheck) { throw '离线素材输出路径不在桌面项目目录内。' }
$stageModels = Join-Path $bundleRoot 'models'
$stageManifests = Join-Path $stageModels 'manifests\registry.ollama.ai\library'
$stageBlobs = Join-Path $stageModels 'blobs'
$sourceManifests = Join-Path $modelsRoot 'manifests\registry.ollama.ai\library'

if (-not (Test-Path -LiteralPath (Join-Path $ollamaRoot 'lib\ollama'))) {
  throw "Ollama 运行目录不完整，缺少 $ollamaRoot\lib\ollama"
}

foreach ($target in @($stageRoot, $bundleRoot)) {
  if (-not $target.StartsWith($desktopRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
    throw "拒绝清理项目目录之外的路径：$target"
  }
  if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
}

New-Item -ItemType Directory -Path $stageRoot -Force | Out-Null
New-Item -ItemType Directory -Path $stageManifests -Force | Out-Null
New-Item -ItemType Directory -Path $stageBlobs -Force | Out-Null
Copy-Item -LiteralPath $ollamaExe -Destination (Join-Path $stageRoot 'ollama.exe')

$runtimeLib = Join-Path $ollamaRoot 'lib\ollama'
$stageLib = Join-Path $stageRoot 'lib\ollama'
New-Item -ItemType Directory -Path $stageLib -Force | Out-Null
Get-ChildItem -LiteralPath $runtimeLib -File | Copy-Item -Destination $stageLib
# 保留 CPU 和 Vulkan 推理库；不把 1.7GB 的 NVIDIA CUDA 后端放进通用离线包。
Copy-Item -LiteralPath (Join-Path $runtimeLib 'vulkan') -Destination $stageLib -Recurse

$modelSpecs = @(
  @{ Name = 'qwen2.5vl'; Tag = '7b' },
  @{ Name = 'bge-m3'; Tag = 'latest' }
)
$copiedDigests = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
foreach ($model in $modelSpecs) {
  $relative = Join-Path $model.Name $model.Tag
  $manifestPath = Join-Path $sourceManifests $relative
  if (-not (Test-Path -LiteralPath $manifestPath)) {
    throw "缺少模型 $($model.Name):$($model.Tag)。请运行 ollama pull $($model.Name):$($model.Tag) 后重试。"
  }

  $destinationManifest = Join-Path $stageManifests $relative
  New-Item -ItemType Directory -Path (Split-Path -Parent $destinationManifest) -Force | Out-Null
  Copy-Item -LiteralPath $manifestPath -Destination $destinationManifest
  $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
  foreach ($descriptor in (@($manifest.config) + @($manifest.layers))) {
    if (-not $descriptor.digest) { continue }
    $digest = [string]$descriptor.digest
    if (-not $copiedDigests.Add($digest)) { continue }

    $hash = $digest -replace '^sha256:', ''
    $sourceBlob = Join-Path $modelsRoot "blobs\sha256-$hash"
    if (-not (Test-Path -LiteralPath $sourceBlob)) { throw "模型文件不完整，缺少 blobs\sha256-$hash" }
    Copy-Item -LiteralPath $sourceBlob -Destination (Join-Path $stageBlobs "sha256-$hash")
  }
}

$modelLicenses = Join-Path $bundleRoot 'licenses'
New-Item -ItemType Directory -Path $modelLicenses -Force | Out-Null
foreach ($model in $modelSpecs) {
  $manifestPath = Join-Path $sourceManifests (Join-Path $model.Name $model.Tag)
  $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
  $license = $manifest.layers | Where-Object { $_.mediaType -eq 'application/vnd.ollama.image.license' } | Select-Object -First 1
  if ($license) {
    $hash = ([string]$license.digest) -replace '^sha256:', ''
    Copy-Item -LiteralPath (Join-Path $stageBlobs "sha256-$hash") -Destination (Join-Path $modelLicenses "$($model.Name)-LICENSE.txt")
  }
}

$totalBytes = (Get-ChildItem -LiteralPath $stageRoot,$bundleRoot -Recurse -File | Measure-Object Length -Sum).Sum
Write-Host "离线运行时和模型素材已准备：$([math]::Round($totalBytes / 1GB, 2)) GB"
