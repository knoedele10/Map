@echo off
chcp 65001 >nul
cd /d "%~dp0"
echo 重建蜀地语料索引（docs/shu/*.md，按 ability 打标）
echo 需要 Ollama 在线（用 bge-m3 算向量）。
echo.
go run ./cmd/ragctl ingest -rebuild
echo.
pause
