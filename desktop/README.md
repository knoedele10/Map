# Windows 桌面版

项目提供两个 Windows x64 安装版本：不带 AI 资源的纯本体版，以及包含 Ollama 和模型、可断网使用的完整离线 AI 版。

## 纯本体版

下载 Shuji-Rongbao-Core-Setup-1.0.3.exe（约 557 MB）并双击安装。此版本不内置 Ollama 或 AI 模型。地图、剧情和本机游戏功能可直接使用；问答与图片识别需要自行配置 Ollama 服务及模型。

## 本地大模型离线版

桌面版由 Electron 窗口、Go 服务和内置 Ollama 组成。安装包内带有 `qwen2.5vl:7b`（同一个模型负责聊天和看图）及 `bge-m3:latest`（文本检索向量模型）；安装后不需要单独安装 Ollama、下载模型或连接互联网即可使用 AI 功能。

桌面端服务固定使用 `127.0.0.1:38180`。启动桌面程序后，地图窗口会自动打开；需要知识库控制台时，在本机浏览器访问 `http://127.0.0.1:38180/`。如果端口已被其他程序占用，请先关闭占用程序再启动桌面端。

## 运行要求

- Windows 10 22H2 或 Windows 11，64 位
- 建议内存 16 GB；7B 模型会占用数 GB 内存
- 纯本体安装器约 557 MB；完整离线发行包约 7.69 GB，安装 AI 版建议预留至少 12 GB 磁盘空间
- 为兼容不同显卡和显存容量，桌面版默认使用 CPU 推理；回答生成会比独显模式慢。建议 16 GB 内存，首次加载模型需等待。

## 下载安装与配置（用户）

v1.0.3 当前下载页只提供纯本体版：打开 [GitHub v1.0.3 下载页](https://github.com/knoedele10/Map/releases/tag/v1.0.3)，下载 Shuji-Rongbao-Core-Setup-1.0.3.exe 并双击安装。该安装器不包含 Ollama 或 AI 模型。

完整离线 AI 版的构建方式见下文；本次发布未上传其安装器和模型分卷。

## 构建安装包

纯本体版只需编译 Go 服务并打包：

```powershell
cd A:\Map\desktop
npm install
npm run dist:core
```

完整离线版构建电脑还需准备 Ollama 和指定模型，按下列步骤打包：

构建电脑需要 Windows x64、Go 1.25+、Node.js/npm、Ollama，以及联网下载模型（仅构建电脑需要联网）。先下载指定模型：

```powershell
ollama pull qwen2.5vl:7b
ollama pull bge-m3:latest
```

然后在项目目录执行：

```powershell
cd A:\Map\desktop
npm install
npm run dist:core
$env:MAP_DESKTOP_BUILD_OUTPUT = 'D:\MapDesktopBuild\dist'
npm run dist:win
```

dist:core 会生成纯本体安装器；dist:win 会从 Ollama 模型库复制模型、准备内置运行时并生成本地大模型安装器、模型分卷和校验清单。默认输出分别位于 desktop/dist-core 和 desktop/dist；设置 MAP_DESKTOP_BUILD_OUTPUT 可指定自定义输出目录。模型素材位于被 Git 忽略的 offline-models/ 目录，不应提交到源码仓库。

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
