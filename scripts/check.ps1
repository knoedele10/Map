$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$env:GOCACHE = Join-Path $root 'data/runtime/go-cache'
$env:TEMP = Join-Path $root 'data/runtime/tmp'
$env:TMP = $env:TEMP
New-Item -ItemType Directory -Path $env:GOCACHE,$env:TEMP -Force | Out-Null
Push-Location (Join-Path $root 'backend')
try {
  & go test ./internal/... ./cmd/server ./cmd/ragctl
  if ($LASTEXITCODE -ne 0) { throw 'Backend checks failed' }
} finally { Pop-Location }
& python (Join-Path $root 'scripts/check_frontend.py')
if ($LASTEXITCODE -ne 0) { throw 'Frontend checks failed' }
Write-Host 'PASS: backend and frontend checks'
