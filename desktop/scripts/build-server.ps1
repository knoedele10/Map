$ErrorActionPreference = 'Stop'
$desktopRoot = Split-Path -Parent $PSScriptRoot
$repoRoot = Split-Path -Parent $desktopRoot
$buildDir = Join-Path $desktopRoot 'build'
$serverExe = Join-Path $buildDir 'server.exe'
$unpackerExe = Join-Path $buildDir 'unpack-models.exe'

New-Item -ItemType Directory -Path $buildDir -Force | Out-Null
Push-Location (Join-Path $repoRoot 'backend')
try {
  & go build -trimpath -ldflags='-s -w' -o $serverExe ./cmd/server
  if ($LASTEXITCODE -ne 0) { throw "Go 编译失败，退出码：$LASTEXITCODE" }
  & go build -trimpath -ldflags='-s -w' -o $unpackerExe ./cmd/model-unpacker
  if ($LASTEXITCODE -ne 0) { throw "模型安装工具编译失败，退出码：$LASTEXITCODE" }
} finally {
  Pop-Location
}

Write-Host "桌面版后端已生成：$serverExe"
Write-Host "模型安装工具已生成：$unpackerExe"
