@echo off
cd /d "%~dp0"

echo ==========================================================
echo   蜀迹 . 成都城脉地图  ---  龙宝知识库问答服务端
echo ==========================================================
echo.

if not exist "bin\server.exe" goto nobin

curl -s --max-time 3 http://127.0.0.1:11434/api/tags >nul 2>&1
if errorlevel 1 goto noollama
echo [OK]   Ollama 在线
goto startserver

:noollama
echo [警告] 连不上 Ollama 127.0.0.1:11434，检索会失败。
echo        请另开一个窗口执行:  ollama serve
echo.

:startserver
echo [..]   启动服务端 127.0.0.1:8080
start "ragkb-server" /min "bin\server.exe"

set /a n=0
:wait
set /a n+=1
curl -s -o nul --max-time 2 http://127.0.0.1:8080/api/v1/health >nul 2>&1
if not errorlevel 1 goto ready
if %n% geq 30 goto failed
timeout /t 1 /nobreak >nul
goto wait

:ready
echo [OK]   服务已就绪
echo.
echo ----------------------------------------------------------
echo   页面:  http://127.0.0.1:8080/static/shuji/
echo   健康:  http://127.0.0.1:8080/api/v1/health
echo ----------------------------------------------------------
echo.
echo   龙宝默认是睡着的，要先解锁能力才能提问。
echo   页面打开后按 F12，在 Console 里粘贴下面这行并回车：
echo.
echo   localStorage.setItem('shuji-prototype-v1', JSON.stringify({done:['wuhou','dufu','dujiangyan','sanxingdui','jinsha','leshan','jianmen','sansu'], main:5, prologue:true})); location.reload();
echo.
echo   这会把 8 个景点标记为已通关，解锁全部 8 种问答能力。
echo   只想解锁一两个，就只留对应的 id：
echo     wuhou=三国  dufu=诗词  dujiangyan=水利  sanxingdui=文物
echo     jinsha=金器  leshan=造像  jianmen=地理  sansu=家风
echo.
start "" "http://127.0.0.1:8080/static/shuji/"
echo   浏览器已打开。关掉最小化的 ragkb-server 窗口即可停止服务。
echo.
pause
exit /b 0

:failed
echo [错误] 服务 30 秒内没起来，端口 8080 可能被占用。
pause
exit /b 1

:nobin
echo [错误] 找不到 bin\server.exe
echo        请先执行:  go build -o bin\server.exe ./cmd/server
pause
exit /b 1
