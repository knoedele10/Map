# 打包「蜀迹 · 蓉宝知识库问答」自包含发行版 exe。
#
#   powershell -ExecutionPolicy Bypass -File scripts\build-exe.ps1
#
# 会把前端、语料、预建索引、配置全部内嵌进单个 exe，产物在 ..\dist\。
# 构建完成后清空暂存目录（只留 .gitkeep），避免 go test 反复打包 31MB 资源。
param(
    [string]$OutDir = "",
    [string]$Version = "",
    # 要打进发行包的前端目录。默认仓库顶层的 frontend/；
    # 换前端时只要指到新目录，后端代码一行都不用改。
    [string]$WebDir = ""
)
$ErrorActionPreference = "Stop"
$server = Split-Path -Parent $PSScriptRoot
$repo   = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $server))
if (-not $OutDir) { $OutDir = Join-Path (Split-Path -Parent $server) "dist" }
if (-not $Version) { $Version = "1.0.0+" + (Get-Date -Format "yyyyMMdd-HHmmss") }
if (-not $WebDir) {
    # 兼容两种布局：主仓库（frontend 在 3 层之上）与本交付包（frontend 在 2 层之上，即 <包>/frontend）
    foreach ($cand in @((Join-Path $repo "frontend"), (Join-Path (Split-Path -Parent (Split-Path -Parent $server)) "frontend"))) {
        if (Test-Path -LiteralPath $cand) { $WebDir = $cand; break }
    }
    if (-not $WebDir) { $WebDir = Join-Path $repo "frontend" }
}

Set-Location $server
$stage = Join-Path $server "cmd\shuji\assets"

Write-Host "[1/4] 暂存资源 -> $stage"
Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
foreach ($d in @("server", "docs\shu")) {
    New-Item -ItemType Directory -Force (Join-Path $stage $d) | Out-Null
}

# 将 server 的所有正式文件纳入发行包：运行时资源直接使用，源码、契约、
# 评测集与部署资料随包释放，便于审计和维护。排除下载产生的 (1)/(2) 重复副本、
# 当前暂存目录以及旧的独立 server.exe（本发行版自身已经编译了同一服务端）。
Get-ChildItem $server -Recurse -File |
    Where-Object {
        -not $_.FullName.StartsWith($stage, [System.StringComparison]::OrdinalIgnoreCase) -and
        $_.Name -notmatch ' \([12]\)\.[^.]+$' -and
        $_.FullName -ne (Join-Path $server "bin\server.exe")
    } |
    ForEach-Object {
        $rel = $_.FullName.Substring($server.Length).TrimStart('\')
        $target = Join-Path (Join-Path $stage "server") $rel
        New-Item -ItemType Directory -Force (Split-Path -Parent $target) | Out-Null
        Copy-Item -LiteralPath $_.FullName -Destination $target
    }
# 前端已搬出后端源码树（见仓库顶层 frontend/），上面的递归暂存只覆盖 server/，
# 所以这里显式把前端目录放进发行包的 server/web —— 发行版解包后 web_dir 仍是 "web"。
if (-not (Test-Path -LiteralPath $WebDir)) { throw "前端目录不存在: $WebDir" }
$webStage = Join-Path $stage "server\web"
New-Item -ItemType Directory -Force $webStage | Out-Null
Copy-Item -Recurse -Force (Join-Path $WebDir "*") $webStage
Write-Host "      前端已暂存: $WebDir"

# 将资料目录中的所有受支持文档一并写入单文件发行版。
# 本项目的运行配置从 ../docs/shu 读取，因此统一释放到 assets/docs/shu。
Get-ChildItem "..\..\data\corpus" -Recurse -File |
    Where-Object { $_.Extension.ToLowerInvariant() -in @(".md", ".txt", ".docx", ".pdf", ".xlsx", ".csv", ".html", ".png", ".jpg", ".jpeg") } |
    ForEach-Object {
        # Go embed 的路径校验不接受间隔号等标点；只规范内嵌副本名，源文档不改。
        $safeName = $_.Name.Replace("·", "-").Replace("龙宝", "蓉宝")
        Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $stage "docs\shu\$safeName")
    }

# Go embed 不跨越嵌套 go.mod 模块边界；暂时改名，启动器解包时恢复原名。
$embeddedGoMod = Join-Path $stage "server\go.mod"
if (Test-Path $embeddedGoMod) {
    Move-Item -LiteralPath $embeddedGoMod -Destination ($embeddedGoMod + ".bundle")
}

$manifest = Get-ChildItem (Join-Path $stage "server") -Recurse -File |
    ForEach-Object {
        $relative = $_.FullName.Substring((Join-Path $stage "server").Length).TrimStart('\')
        if ($relative -eq 'go.mod.bundle') { 'go.mod' } else { $relative }
    } |
    Sort-Object
$manifest | Set-Content -Path (Join-Path $stage "server\BUNDLE-MANIFEST.txt") -Encoding UTF8

$staged = (Get-ChildItem $stage -Recurse -File | Measure-Object -Property Length -Sum).Sum
Write-Host ("      已暂存 {0:N1} MB" -f ($staged / 1MB))

Write-Host "[2/4] 编译 (版本 $Version)"
New-Item -ItemType Directory -Force $OutDir | Out-Null
$exe = Join-Path $OutDir "蜀迹-蓉宝知识库.exe"
# 暂存目录会在每次构建后清空；-a 强制重编译，避免 Go 构建缓存复用上一次空 assets。
# windowsgui 隐藏命令行窗口；双击后只出现独立游戏窗口。
go build -a -trimpath -ldflags "-s -w -H=windowsgui -X main.bundleVersion=$Version" -o $exe ./cmd/shuji
if ($LASTEXITCODE -ne 0) { throw "go build 失败" }

Write-Host "[3/4] 清理暂存目录"
Remove-Item -Recurse -Force $stage
New-Item -ItemType Directory -Force $stage | Out-Null
Set-Content -Path (Join-Path $stage ".gitkeep") -Value "" -NoNewline

Write-Host "[4/4] 完成"
$size = (Get-Item $exe).Length
Write-Host ("      产物: {0}" -f $exe)
Write-Host ("      体积: {0:N1} MB" -f ($size / 1MB))


