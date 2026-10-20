# Windows 离线桌面版

桌面版由 Electron 窗口、Go 服务和内置 Ollama 组成。安装包内带有 `qwen2.5vl:7b`（同一个模型负责聊天和看图）及 `bge-m3:latest`（文本检索向量模型）；安装后不需要单独安装 Ollama、下载模型或连接互联网即可使用 AI 功能。

## 运行要求

- Windows 10 22H2 或 Windows 11，64 位
- 建议内存 16 GB；7B 模型会占用数 GB 内存
- 安装磁盘至少预留 12 GB；完整离线发行包约 7.69 GB（安装器约 0.57 GB，模型分卷约 7.13 GB）
- 为兼容不同显卡和显存容量，桌面版默认使用 CPU 推理；回答生成会比独显模式慢。建议 16 GB 内存，首次加载模型需等待。

## 下载安装与配置（用户）

打开 [GitHub v1.0.2 下载页](https://github.com/knoedele10/Map/releases/tag/v1.0.2)，在 **Assets** 中下载以下 6 个文件：

- `Shuji-Rongbao-Offline-Setup-1.0.2.exe`
- `Shuji-Rongbao-Offline-Models-1.0.2.json`（模型校验清单）
- `Shuji-Rongbao-Offline-Models-1.0.2.part01.bin`
- `Shuji-Rongbao-Offline-Models-1.0.2.part02.bin`
- `Shuji-Rongbao-Offline-Models-1.0.2.part03.bin`
- `Shuji-Rongbao-Offline-Models-1.0.2.part04.bin`

总下载量约 7.69 GB。请先把这 6 个文件全部下载到同一个文件夹，再双击运行安装器。**只下载或双击安装器不会自动从网上下载模型**；四个 `.bin` 分卷和 `.json` 清单必须放在安装器旁边。`blockmap` 是更新辅助文件，普通安装不需要下载。

安装程序会校验模型分卷，安装内置 Ollama CPU 运行时，并解压配置聊天/图片识别共用的 `qwen2.5vl:7b` 和文本检索用的 `bge-m3:latest`。安装完成后不需要另行安装 Ollama、下载模型或联网；安装时请保持安装窗口打开，并预留至少 12 GB 安装磁盘空间。

## 构建离线安装包

构建电脑需要 Windows x64、Go 1.25+、Node.js/npm、Ollama，以及联网下载模型（仅构建电脑需要联网）。先下载指定模型：

```powershell
ollama pull qwen2.5vl:7b
ollama pull bge-m3:latest
```

然后在项目目录执行：

```powershell
cd A:\Map\desktop
npm install
$env:MAP_DESKTOP_BUILD_OUTPUT = 'D:\MapDesktopBuild\dist'
npm run dist:win
```

脚本会从 Ollama 模型库复制所需模型、准备内置运行时、编译 Go 服务并生成 NSIS 安装程序及模型分卷。输出位置由 `MAP_DESKTOP_BUILD_OUTPUT` 指定；未设置时输出到 `desktop/dist`。输出目录包含 `Shuji-Rongbao-Offline-Setup-1.0.2.exe`、模型分卷和校验清单。请把输出目录中的所有文件放在同一文件夹，再双击安装器；安装器会校验并自动解压模型。缺少模型分卷时会在安装开始前提示。约 7 GB 的模型素材位于被 Git 忽略的 `offline-models/` 目录，不应提交到源码仓库。

## 验证离线模型素材

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/smoke-offline-runtime.ps1
```

此脚本会启动打包素材中的 Ollama，分别调用检索向量、文字聊天和图片理解接口，然后停止该测试进程。发布前还应在一台未安装 Ollama 的 Windows 电脑上断网安装并打开安装包，检查安装器、启动和实际游戏流程。

## 模型和运行时许可

- Qwen2.5-VL 7B：Apache License 2.0；随模型素材提供许可文本。
- BGE-M3：MIT；随模型素材提供上游许可文件。模型卡：[BAAI/bge-m3](https://huggingface.co/BAAI/bge-m3)。
- Ollama：MIT；安装包提供 `licenses/OLLAMA-LICENSE.txt`。
- Ollama 所带第三方组件的许可文件随运行时目录提供。

安装后关闭窗口会同时停止本应用启动的 Go 和 Ollama 子进程；应用使用独立本机端口，不会接管用户已运行的 Ollama 服务。
