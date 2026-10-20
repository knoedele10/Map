# Go 后端

`backend/` 是独立的 Go module，为控制台、蜀迹地图和 Electron 桌面版提供同一套 HTTP 服务与检索能力。项目总览、启动方式和桌面打包说明见仓库根目录的 [README](../README.md)。

## 入口

| 入口 | 用途 |
| --- | --- |
| `cmd/server` | 开发和部署用 HTTP 服务；前端由它托管 |
| `cmd/ragctl` | 资料入库、索引迁移和检索评测 |
| `cmd/shuji` | 可选的自包含 Go 桌面发行版构建入口；Electron 是当前推荐的桌面打包方式 |

Electron 打包会编译 `cmd/server`，并把产物放到 `desktop/build/`。Docker 镜像也编译 `cmd/server`，构建定义在仓库根目录的 `Dockerfile`。

## 目录

```text
backend/
├── cmd/                 程序入口
├── internal/            API、应用启动、配置、RAG、模型和存储实现
├── configs/             配置示例、开发配置和景点数据
├── deploy/              数据库结构
├── docs/                API 与接口契约
├── eval/                检索评测集
├── scripts/             可选的自包含 Go EXE 构建脚本
├── go.mod, go.sum       Go 依赖
└── openapi.yaml         OpenAPI 描述
```

持久化索引、上传资料和运行日志放在仓库级 `data/runtime/`；知识库原始资料放在 `data/corpus/`。这两处路径由配置文件设置，不要把运行数据放进 `backend/` 源码目录。

## 本地开发

推荐从仓库根目录运行 `.\start.ps1`，它会加载本机模型配置并使用正确的工作目录。需要手动运行后端时：

```powershell
cd A:\Map\backend
go run ./cmd/server -config configs/config.dev.yaml
```

配置文件里的相对路径以 `backend/` 为工作目录解析。提交前端、后端检查入口在仓库根目录：

```powershell
cd A:\Map
.\scripts\check.ps1
```

## 配置与接口

- [开发配置](configs/config.dev.yaml) 面向本机运行；本机凭据应放在仓库根目录 `config/` 的忽略文件中。
- [配置模板](configs/config.example.yaml) 用于了解所有可配项；不要把真实 API Key 写入模板。
- [OpenAPI](openapi.yaml)、[API 文档](docs/API.md) 和 [接口契约](docs/CONTRACT.md) 描述 HTTP 请求与响应。
- 文件存储是默认后端；PostgreSQL/pgvector 的表结构和切换方式见 `deploy/` 与根 README。

`backend/data/`、`backend/data-pg/`、`backend/server.exe` 是本地运行或构建产生的旧路径，已由 `.gitignore` 排除；当前运行数据以根目录 `data/runtime/` 为准。
