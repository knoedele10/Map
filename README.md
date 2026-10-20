# 蜀迹 · 蓉宝寻忆记

项目由 Go 后端、静态前端和本机知识库组成。除浏览器访问的源码运行方式外，`desktop/` 还提供 Electron 桌面窗口外壳；v1.0.1 Windows 安装包可从 [GitHub Releases](https://github.com/knoedele10/Map/releases/tag/v1.0.1) 下载，也可以自行从源码构建。

## 最新更新：v1.0.1

- 整理后端、前端、配置和桌面端目录结构。
- 更新完整前端资源，加入背景音乐和美食小游戏资源。
- 添加 Electron Windows 桌面外壳、Docker 构建文件和打包说明。
- 修复桌面端启动入口：打开后直接进入蜀迹地图游戏，而不是后台控制台。
- 下载 Windows 安装包：[蜀迹·蓉宝寻忆记 v1.0.1](https://github.com/knoedele10/Map/releases/tag/v1.0.1)。

从源码重新打包 Windows 安装程序：

```powershell
cd A:\Map\desktop
npm install
npm run dist:win
```

安装包输出在 `desktop/dist/Shuji-Rongbao-Setup-1.0.1.exe`。需要 Windows x64、Go 1.25+、Node.js 和 npm。Ollama 与项目使用的模型需要另行安装。完整更新记录见 [`docs/更新通知.md`](docs/更新通知.md)。

## 启动

双击 `启动项目.cmd`，或在 PowerShell 中执行：

```powershell
cd A:\Map
.\start.ps1
```

服务控制台顶部的“重启服务”按钮可应用刚保存的模型配置；网页重启功能由此启动脚本托管，直接运行后端程序时按钮会提示不支持。

- 地图游戏：http://127.0.0.1:8090/static/shuji/
- 知识库控制台：http://127.0.0.1:8090/
- 服务状态：http://127.0.0.1:8090/api/v1/health

需要 Go 1.25+ 和 Ollama。启动脚本会启动已安装的 Ollama，不会下载模型。停止服务后改 Go 代码，再重新运行启动脚本；改前端后浏览器按 `Ctrl+F5`。

桌面版开发与安装包构建说明见 [`desktop/README.md`](desktop/README.md)。桌面外壳在独立窗口运行同一套 Go 服务和前端；Ollama 与模型仍需单独安装。

## 当前本机模型

运行配置位于 `backend/configs/config.dev.yaml`；根目录 `config/` 保存环境变量示例和本机模型密钥文件：

| 用途 | 模型 |
| --- | --- |
| Embedding 检索 | Ollama `bge-m3:latest`，1024 维 |
| Chat 对话 | Ollama `qwen2.5:7b` |
| Vision 识图与图片说明 | Ollama `qwen2.5vl-16k:latest` |

控制台的“模型与 API 配置”支持两种模式：两个 API 时 Chat 与 Vision 共用同一模型/API，Embedding 单独配置；三个 API 时三者分别配置。配置分别保存在 `config/.env.chat.local`、`config/.env.embedding.local`、`config/.env.vision.local`，密钥不会回显。保存后重启服务生效。Embedding 模型或维度变化后需要清空重建索引。

## 功能

- 五章剧情、离线地图、景点卡片、蓉宝互动、小游戏、文物三维模型和本机存档。
- 地图问答、拍照识景、控制台多图问答和答案来源。
- 控制台资料上传、索引管理、增量入库与重建、混合检索调试、模型状态和问答记录。
- 六组照片资料已按杜甫草堂、都江堰、剑门关、金沙遗址、武侯祠分类，另设其他地标；每类含目录检索索引，并有五处景点对比说明。

游戏为本机游客存档，没有在线账号系统。批量上传后统一入库；删除索引不会删除 `data/corpus/` 中的原始资料。

## 目录

```text
A:\Map\
├── backend/                 Go API、服务与检索实现（独立 Go module）
│   ├── cmd/                 服务端和命令行程序入口
│   ├── internal/            后端内部模块
│   ├── configs/             配置模板与景点数据
│   ├── deploy/              数据库部署文件
│   └── docs/、eval/、scripts/
├── frontend/                控制台、地图游戏和静态资源
│   └── shuji/               蜀迹游戏页面、脚本、样式与媒体资源
├── desktop/                 Electron 桌面外壳与 Windows 打包配置
├── data/
│   ├── corpus/              当前知识库语料（含分类景点图片）
│   └── runtime/             索引快照、日志、临时文件和缓存
├── config/                  环境变量示例和本机密钥文件
├── scripts/                 检查、前端资源检查和 HTTP 联调脚本
├── docs/                    联调记录和截图
├── Dockerfile               Docker 镜像构建
├── .dockerignore            Docker 构建上下文排除项
├── .gitattributes            Windows 安装包的 Git LFS 规则
├── start.ps1                本地启动入口
└── 启动项目.cmd             Windows 双击启动入口
```

运行中生成的索引、日志与缓存统一写入 `data/runtime/`；前端检查脚本也将临时文件写在该目录。

本机若有旧版提取备份，可自行放在 `archive/`；它不属于当前源码，也不参与构建或运行。新增的景点 Markdown 来源于原项目 `sites.json`，保留原出处说明，没有额外联网核验。

运行配置、模板和景点数据统一放在 `backend/configs/`；活动配置使用其中的 `sites.json`。根目录 `config/` 只存环境变量示例和本机密钥。Electron、Node 依赖和打包结果分别位于 `desktop/`、`desktop/node_modules/` 和 `desktop/dist/`；Node 依赖与中间构建文件不提交，当前 Windows 安装包通过 Git LFS 单独跟踪。

## 检查与排障

```powershell
# 后端检查与前端脚本/本地资源检查
.\scripts\check.ps1

# 服务启动后做真实 HTTP、检索、上传和删除联调
python .\scripts\smoke.py

# 当前景点检索黄金集
cd .\backend
go run ./cmd/ragctl eval -base http://127.0.0.1:8090 -golden ./eval/golden-connected.yaml
```

文件索引与缓存均保存在 `data/runtime/`。当前使用文件存储；未连接真实 PostgreSQL。详细联调范围见 [`docs/联调记录.md`](docs/联调记录.md)。

