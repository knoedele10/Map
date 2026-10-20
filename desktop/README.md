# Windows 桌面版

Electron 在独立窗口中显示现有 Web 页面，并作为子进程启动 Go 后端。关闭窗口时会停止后端。索引和上传资源保存在当前 Windows 用户的应用数据目录中。

## 开发环境

- Windows x64
- Go 1.25 或更新版本
- Node.js 与 npm
- Ollama 与项目所需模型（运行 AI 功能时）

## 本地运行

在此目录执行：

```powershell
npm install
npm start
```

## 生成 Windows 安装程序

```powershell
npm run dist:win
```

NSIS 安装包生成在 `desktop/dist/`。安装包包含 Go 服务、前端、知识库语料和运行配置，不包含 Ollama 或模型权重。使用者需单独安装 Ollama 与项目配置的模型，才能使用检索、问答和图片识别功能。
