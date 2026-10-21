const { app, BrowserWindow, dialog, shell } = require('electron');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const http = require('node:http');
const net = require('node:net');
const path = require('node:path');

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  let serverProcess;
  let ollamaProcess;
  let mainWindow;
  let serverLog;
  let restartMarker;
  let backendRestarting = false;
  let backendPort;
  let backendOllamaPort;
  let isQuitting = false;

  function runtimeLayout() {
    if (app.isPackaged) {
      const root = path.join(process.resourcesPath, 'runtime');
      const ollamaRoot = path.join(process.resourcesPath, 'offline-runtime', 'ollama');
      return {
        backendDir: path.join(root, 'backend'),
        serverExe: path.join(root, 'backend', 'server.exe'),
        configFile: path.join(root, 'backend', 'configs', 'config.yaml'),
        ollamaExe: path.join(ollamaRoot, 'ollama.exe'),
        ollamaModels: process.env.SHUJI_OLLAMA_MODELS_DIR || path.join(ollamaRoot, 'models'),
        ollamaLib: path.join(ollamaRoot, 'lib', 'ollama'),
      };
    }

    const repoRoot = path.resolve(__dirname, '..');
    return {
      backendDir: path.join(repoRoot, 'backend'),
      serverExe: path.join(__dirname, 'build', 'server.exe'),
      configFile: path.join(repoRoot, 'backend', 'configs', 'config.dev.yaml'),
      ollamaExe: null,
    };
  }

  function choosePort() {
    return new Promise((resolve, reject) => {
      const probe = net.createServer();
      probe.once('error', reject);
      probe.listen(0, '127.0.0.1', () => {
        const port = probe.address().port;
        probe.close((error) => error ? reject(error) : resolve(port));
      });
    });
  }

  function ensurePortAvailable(port) {
    return new Promise((resolve, reject) => {
      const probe = net.createServer();
      probe.once('error', () => reject(new Error(`本机端口 ${port} 已被占用，请关闭占用该端口的程序后重试。`)));
      probe.listen(port, '127.0.0.1', () => {
        probe.close((error) => error ? reject(error) : resolve());
      });
    });
  }

  function initLog() {
    const logDir = path.join(app.getPath('userData'), 'logs');
    fs.mkdirSync(logDir, { recursive: true });
    serverLog = fs.createWriteStream(path.join(logDir, 'server.log'), { flags: 'a' });
  }

  function getOllamaTags(port) {
    return new Promise((resolve) => {
      const request = http.get(`http://127.0.0.1:${port}/api/tags`, (response) => {
        let body = '';
        response.setEncoding('utf8');
        response.on('data', (chunk) => { body += chunk; });
        response.on('end', () => {
          try {
            const payload = JSON.parse(body);
            resolve((payload.models || []).map((model) => model.name));
          } catch {
            resolve([]);
          }
        });
      });
      request.setTimeout(1000, () => request.destroy());
      request.on('error', () => resolve([]));
    });
  }

  async function waitForOllama(port) {
    const requiredModels = ['qwen2.5vl:7b', 'bge-m3:latest'];
    for (let attempt = 0; attempt < 180; attempt += 1) {
      if (!ollamaProcess || ollamaProcess.exitCode !== null) return false;
      const tags = await getOllamaTags(port);
      const hasAllModels = requiredModels.every((required) => tags.includes(required));
      if (hasAllModels) return true;
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
    return false;
  }

  function startOllama(port) {
    const layout = runtimeLayout();
    for (const requiredPath of [layout.ollamaExe, layout.ollamaModels, layout.ollamaLib]) {
      if (!fs.existsSync(requiredPath)) throw new Error(`缺少内置 AI 运行文件：${requiredPath}`);
    }

    ollamaProcess = spawn(layout.ollamaExe, ['serve'], {
      cwd: path.dirname(layout.ollamaExe),
      env: {
        ...process.env,
        OLLAMA_HOST: `127.0.0.1:${port}`,
        OLLAMA_MODELS: layout.ollamaModels,
        OLLAMA_MAX_LOADED_MODELS: '1',
        OLLAMA_NUM_PARALLEL: '1',
        OLLAMA_VULKAN: '0',
        OLLAMA_NO_CLOUD: '1',
      },
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    ollamaProcess.stdout.pipe(serverLog);
    ollamaProcess.stderr.pipe(serverLog);
  }

  function isBackendReady(port) {
    return new Promise((resolve) => {
      const request = http.get(`http://127.0.0.1:${port}/api/v1/health`, (response) => {
        response.resume();
        resolve(response.statusCode >= 200 && response.statusCode < 500);
      });
      request.setTimeout(1000, () => request.destroy());
      request.on('error', () => resolve(false));
    });
  }

  async function waitForBackend(port) {
    for (let attempt = 0; attempt < 60; attempt += 1) {
      if (!serverProcess || serverProcess.exitCode !== null) return false;
      if (await isBackendReady(port)) return true;
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
    return false;
  }

  function startBackend(port, ollamaPort) {
    const layout = runtimeLayout();
    for (const requiredPath of [layout.serverExe, layout.configFile]) {
      if (!fs.existsSync(requiredPath)) throw new Error(`缺少运行文件：${requiredPath}`);
    }

    const userData = path.join(app.getPath('userData'), 'runtime');
    const dataDir = path.join(userData, 'data');
    const assetsDir = path.join(userData, 'assets');
    restartMarker = path.join(userData, 'service-control.request');
    fs.mkdirSync(dataDir, { recursive: true });
    fs.mkdirSync(assetsDir, { recursive: true });
    try { fs.rmSync(restartMarker, { force: true }); } catch {}

    serverProcess = spawn(layout.serverExe, ['-config', layout.configFile], {
      cwd: layout.backendDir,
      env: {
        ...process.env,
        RAG_SERVER_ADDR: `127.0.0.1:${port}`,
        ...(ollamaPort ? {
          RAG_EMBEDDING_BASE_URL: `http://127.0.0.1:${ollamaPort}`,
          RAG_CHAT_BASE_URL: `http://127.0.0.1:${ollamaPort}`,
          RAG_VISION_BASE_URL: `http://127.0.0.1:${ollamaPort}`,
        } : {}),
        RAG_STORAGE_DATA_DIR: dataDir,
        RAG_STORAGE_ASSETS_DIR: assetsDir,
        RAG_CONTROL_MARKER: restartMarker,
      },
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    const child = serverProcess;
    child.stdout.pipe(serverLog);
    child.stderr.pipe(serverLog);
    child.once('exit', async () => {
      if (serverProcess !== child || backendRestarting || !restartMarker || !fs.existsSync(restartMarker)) return;
      let action = '';
      try { action = fs.readFileSync(restartMarker, 'utf8').trim().split(/\s+/, 1)[0]; } catch {}
      if (action !== 'restart' || isQuitting) return;
      backendRestarting = true;
      try {
        startBackend(backendPort, backendOllamaPort);
        if (!await waitForBackend(backendPort)) throw new Error('服务重启后未能恢复，请查看 server.log。');
      } catch (error) {
        dialog.showErrorBox('服务重启失败', error.message || String(error));
      } finally {
        backendRestarting = false;
      }
    });
  }

  async function createWindow(port) {
    const origin = `http://127.0.0.1:${port}`;
    mainWindow = new BrowserWindow({
      width: 1440,
      height: 920,
      minWidth: 960,
      minHeight: 640,
      title: '蜀迹·蓉宝寻忆记',
      autoHideMenuBar: true,
      webPreferences: {
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
      },
    });

    mainWindow.webContents.setWindowOpenHandler(({ url }) => {
      if (url.startsWith('https://') || url.startsWith('http://')) shell.openExternal(url);
      return { action: 'deny' };
    });
    mainWindow.webContents.on('will-navigate', (event, url) => {
      if (!url.startsWith(origin)) {
        event.preventDefault();
        if (url.startsWith('https://') || url.startsWith('http://')) shell.openExternal(url);
      }
    });
    mainWindow.on('closed', () => { mainWindow = null; });
    await mainWindow.loadURL(`${origin}/static/shuji/`);
  }

  async function launch() {
    try {
      initLog();
      const port = 38180;
      await ensurePortAvailable(port);
      let ollamaPort;
      if (app.isPackaged) {
        ollamaPort = await choosePort();
        const layout = runtimeLayout();
        if (fs.existsSync(layout.ollamaExe) && fs.existsSync(layout.ollamaModels) && fs.existsSync(layout.ollamaLib)) {
          startOllama(ollamaPort);
          if (!await waitForOllama(ollamaPort)) {
            throw new Error('内置 Ollama 或模型未能启动。请查看应用日志中的 server.log。');
          }
        } else {
          ollamaPort = undefined;
        }
      }
      backendPort = port;
      backendOllamaPort = ollamaPort;
      startBackend(port, ollamaPort);
      if (!await waitForBackend(port)) {
        throw new Error('本地服务启动失败。请查看应用日志中的 server.log。');
      }
      await createWindow(port);
    } catch (error) {
      dialog.showErrorBox('蜀迹·蓉宝寻忆记启动失败', error.message || String(error));
      app.quit();
    }
  }

  app.whenReady().then(launch);
  app.on('second-instance', () => {
    if (!mainWindow) return;
    if (mainWindow.isMinimized()) mainWindow.restore();
    mainWindow.focus();
  });
  app.on('before-quit', () => {
    isQuitting = true;
    for (const child of [serverProcess, ollamaProcess]) {
      if (!child || child.exitCode !== null) continue;
      if (process.platform === 'win32') {
        spawn('taskkill.exe', ['/PID', String(child.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
      } else {
        child.kill('SIGTERM');
      }
    }
    if (serverLog) serverLog.end();
  });
  app.on('window-all-closed', () => app.quit());
}
