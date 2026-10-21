const crypto = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { Arch, Platform, build } = require('electron-builder');

const desktopRoot = path.resolve(__dirname, '..');
const packagePath = path.join(desktopRoot, 'package.json');
const originalPackageJSON = fs.readFileSync(packagePath, 'utf8');
const packageInfo = JSON.parse(originalPackageJSON);
const modelRoot = path.join(desktopRoot, 'offline-models', 'ollama');
const maxPartBytes = 1_900_000_000;
const coreOnly = process.argv[2] === 'core' || process.env.MAP_DESKTOP_FLAVOR === 'core';
const defaultOutput = path.join(desktopRoot, coreOnly ? 'dist-core' : 'dist');
const output = path.resolve(process.env.MAP_DESKTOP_BUILD_OUTPUT || defaultOutput);

async function createModelParts() {
  const baseName = `Shuji-Rongbao-Offline-Models-${packageInfo.version}`;
  const tarExe = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'tar.exe');
  if (!fs.existsSync(path.join(modelRoot, 'models')) || !fs.existsSync(path.join(modelRoot, 'licenses'))) {
    throw new Error('离线模型素材未准备好，请先运行 scripts/stage-offline-runtime.ps1。');
  }

  const tar = spawn(tarExe, ['-cf', '-', '-C', modelRoot, 'models', 'licenses'], {
    stdio: ['ignore', 'pipe', 'inherit'],
    windowsHide: true,
  });
  const tarExit = new Promise((resolve, reject) => {
    tar.once('error', reject);
    tar.once('close', resolve);
  });

  const parts = [];
  let partIndex = 0;
  let partBytes = 0;
  let partFd;
  let partHash;
  let partName;
  const closePart = () => {
    if (partFd === undefined) return;
    fs.closeSync(partFd);
    parts.push({ name: partName, bytes: partBytes, sha256: partHash.digest('hex') });
    partFd = undefined;
  };
  const startPart = () => {
    partIndex += 1;
    partBytes = 0;
    partName = `${baseName}.part${String(partIndex).padStart(2, '0')}.bin`;
    partFd = fs.openSync(path.join(output, partName), 'w');
    partHash = crypto.createHash('sha256');
  };

  let totalBytes = 0;
  try {
    for await (const chunk of tar.stdout) {
      let offset = 0;
      while (offset < chunk.length) {
        if (partFd === undefined) startPart();
        const length = Math.min(chunk.length - offset, maxPartBytes - partBytes);
        let written = 0;
        while (written < length) {
          written += fs.writeSync(partFd, chunk, offset + written, length - written);
        }
        partHash.update(chunk.subarray(offset, offset + length));
        offset += length;
        partBytes += length;
        totalBytes += length;
        if (partBytes === maxPartBytes) closePart();
      }
    }
    closePart();
    const exitCode = await tarExit;
    if (exitCode !== 0) throw new Error(`生成模型归档失败，tar.exe 退出码：${exitCode}`);
  } finally {
    if (tar.exitCode === null) tar.kill();
    if (partFd !== undefined) fs.closeSync(partFd);
  }

  if (parts.length !== 4) {
    throw new Error(`模型包切成了 ${parts.length} 卷，当前安装器要求 4 卷。请先调整分卷规则。`);
  }
  const manifest = {
    version: packageInfo.version,
    totalBytes,
    parts,
  };
  fs.writeFileSync(path.join(output, `${baseName}.json`), `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(`模型包已生成：${parts.length} 个分卷，共 ${(totalBytes / 1e9).toFixed(2)} GB`);
}

async function main() {
  const baseResources = packageInfo.build.extraResources;
  const resources = coreOnly
    ? baseResources.filter((resource) => !resource.from.includes('offline-runtime/ollama') &&
      !resource.from.includes('unpack-models.exe'))
    : baseResources;
  const buildConfig = {
    directories: { output },
    nsis: {
      ...packageInfo.build.nsis,
      include: coreOnly ? path.join(desktopRoot, 'scripts', 'installer-core.nsh') : path.join(desktopRoot, 'scripts', 'installer-models.nsh'),
    },
    win: {
      ...packageInfo.build.win,
      artifactName: coreOnly
        ? 'Shuji-Rongbao-Core-Setup-' + packageInfo.version + '.' + '${ext}'
        : 'Shuji-Rongbao-Offline-AI-Setup-' + packageInfo.version + '.' + '${ext}',
    },
  };
  if (coreOnly) {
    packageInfo.build.extraResources = resources;
    packageInfo.build.nsis.include = buildConfig.nsis.include;
    packageInfo.build.win.artifactName = buildConfig.win.artifactName;
    fs.writeFileSync(packagePath, JSON.stringify(packageInfo, null, 2) + '\n');
  }
  try {
    await build({
      targets: Platform.WINDOWS.createTarget(['nsis'], Arch.x64),
      config: buildConfig,
    });
  } finally {
    if (coreOnly) fs.writeFileSync(packagePath, originalPackageJSON);
  }
  if (!coreOnly) await createModelParts();
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
