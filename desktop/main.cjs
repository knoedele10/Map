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
  let mainWindow;
  let serverLog;

  function runtimeLayout() {
    if (app.isPackaged) {
      const root = path.join(process.resourcesPath, 'runtime');
      return {
        backendDir: path.join(root, 'backend'),
        serverExe: path.join(root, 'backend', 'server.exe'),
        configFile: path.join(root, 'backend', 'configs', 'config.yaml'),
      };
    }

    const repoRoot = path.resolve(__dirname, '..');
    return {
      backendDir: path.join(repoRoot, 'backend'),
      serverExe: path.join(__dirname, 'build', 'server.exe'),
      configFile: path.join(repoRoot, 'backend', 'configs', 'config.dev.yaml'),
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

  function startBackend(port) {
    const layout = runtimeLayout();
    for (const requiredPath of [layout.serverExe, layout.configFile]) {
      if (!fs.existsSync(requiredPath)) throw new Error(`缺少运行文件：${requiredPath}`);
    }

    const userData = path.join(app.getPath('userData'), 'runtime');
    const dataDir = path.join(userData, 'data');
    const assetsDir = path.join(userData, 'assets');
    const logDir = path.join(app.getPath('userData'), 'logs');
    fs.mkdirSync(dataDir, { recursive: true });
    fs.mkdirSync(assetsDir, { recursive: true });
    fs.mkdirSync(logDir, { recursive: true });
    serverLog = fs.createWriteStream(path.join(logDir, 'server.log'), { flags: 'a' });

    serverProcess = spawn(layout.serverExe, ['-config', layout.configFile], {
      cwd: layout.backendDir,
      env: {
        ...process.env,
        RAG_SERVER_ADDR: `127.0.0.1:${port}`,
        RAG_STORAGE_DATA_DIR: dataDir,
        RAG_STORAGE_ASSETS_DIR: assetsDir,
      },
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    serverProcess.stdout.pipe(serverLog);
    serverProcess.stderr.pipe(serverLog);
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
    await mainWindow.loadURL(`${origin}/`);
  }

  async function launch() {
    try {
      const port = await choosePort();
      startBackend(port);
      if (!await waitForBackend(port)) {
        throw new Error('本地服务未能启动。请确认 Ollama 已安装并运行，再查看应用日志。');
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
    if (serverProcess && serverProcess.exitCode === null) serverProcess.kill();
    if (serverLog) serverLog.end();
  });
  app.on('window-all-closed', () => app.quit());
}
