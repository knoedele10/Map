# 多模态 RAG 知识库 · API 接入文档

> 面向**前端开发者与接口使用者**的权威接入说明。所有路径、字段名、错误码、SSE 事件名均与冻结契约 [CONTRACT.md](CONTRACT.md)（v2）逐字一致；若发现出入，以契约与后端实现为准。

- **Base URL**：`http://127.0.0.1:8080`
- **API 前缀**：`/api/v1`
- **OpenAPI 规范**：[openapi.yaml](../openapi.yaml)，运行中的服务也可 `GET /openapi.yaml`
- **服务版本**：`0.2.0`

---

## 1. 基础约定

### 1.1 统一响应信封

除 **SSE** 与 **二进制资源** 外，所有响应都是这个信封：

```json
{ "code": 0, "message": "ok", "data": { }, "request_id": "9f2c1a4b7e0d3355" }
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `code` | int | `0` 成功；非 0 为业务错误码（见 1.4）。 |
| `message` | string | 人类可读说明；失败时是失败原因。 |
| `data` | object \| null | 业务数据；失败时为 `null`。 |
| `request_id` | string | 每请求唯一，同时回写响应头 `X-Request-Id`。 |

失败示例：

```json
{"code":40001,"message":"question 不能为空","data":null,"request_id":"9f2c1a4b7e0d3355"}
```

### 1.2 认证

- 配置了 `server.auth_token` 时，**除 `GET /api/v1/health` 外的所有 `/api/v1` 接口**都需要：

  ```http
  Authorization: Bearer <token>
  ```

  缺失或错误 → **HTTP 401** + `code=40100`。
- 未配置 token 时不校验（默认，便于本地联调）。
- 是否需要鉴权先读 `GET /api/v1/config` 的 `auth_required`。
- `GET /`（Demo 页）与 `GET /openapi.yaml` 不在 `/api/v1` 下，不参与鉴权。

### 1.3 跨域（CORS）

- `server.cors_origins=["*"]` 允许任意来源；否则按白名单回显 `Origin`。
- 允许头：`Authorization, Content-Type, X-Request-Id`；暴露头：`X-Request-Id`。
- SSE 响应头带 `X-Accel-Buffering: no`（防反向代理缓冲），并每 15s 发 `: ping` 保活。

### 1.4 错误码

| code | HTTP | 含义 | 前端建议 |
| --- | --- | --- | --- |
| `0` | 200 | 成功 | 正常处理。 |
| `40001` | 400 | 参数校验失败 | 展示 `message`，让用户改输入。 |
| `40100` | 401 | 未认证 / token 错误 | 提示配置或刷新 token，不要盲目重试。 |
| `40300` | 403 | 无权限 | 提示无访问权限。 |
| `40400` | 404 | 资源不存在 | 文档 / 任务 / 资产已不存在。 |
| `40900` | 409 | 状态冲突（如入库任务已在运行） | 提示"已有任务在跑"，转为轮询现有任务。 |
| `42900` | 429 | 触发限流 | 退避重试（见第 9 节）。 |
| `50000` | 500 | 服务内部错误 | 记录 `request_id` 并反馈。 |
| `50200` | 502 | 上游模型服务错误（Ollama / Chat API） | 可退避重试；持续失败查上游 Key / 服务。 |
| `50300` | 503 | 依赖未就绪（模型缺失、索引为空） | 提示先入库 / 拉模型，用 `/health` 定位。 |

### 1.5 时间与命名

- 时间一律 **RFC3339**，例如 `2026-10-10T13:22:00+08:00`。
- JSON 字段一律 **snake_case**。
- 请求体上限 32 MiB（上传接口按 `ingest.max_file_mb`）。

### 1.6 接口总览

| 方法 | 路径 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/health` | 否 | 健康检查 |
| GET | `/api/v1/config` | 是 | 前端可见配置 |
| GET | `/api/v1/stats` | 是 | 索引统计 + 可过滤元数据取值（facets） |
| POST | `/api/v1/chat` | 是 | 问答（SSE 流式 / 一次性 JSON） |
| POST | `/api/v1/search` | 是 | 纯检索（调试与评测） |
| POST | `/api/v1/retrieve` | 是 | 同 `/search`，保留别名 |
| POST | `/api/v1/assets` | 是 | 上传资料（multipart）→ asset_id |
| POST | `/api/v1/documents/upload` | 是 | 同 `/assets`，保留别名 |
| GET | `/api/v1/assets/:doc_id/:name` | 是 | 读取图片等二进制资源 |
| POST | `/api/v1/ingest` | 是 | 触发入库任务 |
| GET | `/api/v1/ingest/jobs` | 是 | 任务列表 |
| GET | `/api/v1/ingest/jobs/:id` | 是 | 任务详情与进度 |
| POST | `/api/v1/ingest/jobs/:id/cancel` | 是 | 取消任务 |
| GET | `/api/v1/documents` | 是 | 文档列表 |
| DELETE | `/api/v1/documents/:id` | 是 | 删除文档及其索引 |
| GET | `/api/v1/sites` | 是 | 景点列表 |
| GET | `/api/v1/sites/:id` | 是 | 单个景点结构化数据 |
| GET | `/api/v1/traces` | 是 | 最近问答链路留痕 |
| GET | `/openapi.yaml` | 否 | OpenAPI 3.1 规范 |
| GET | `/` | 否 | 内置 Demo 页面 |

> "鉴权 = 是"指**配置了 `auth_token` 之后**需要带 `Authorization`；未配置时全部放行。

---

## 2. 数据类型

### 2.1 Chunk（检索单元 / 引用出处）

```json
{
  "id": "c1a2b3c4d5e6f708",
  "doc_id": "9a8b7c6d5e4f3021",
  "source": "四川地方志/乐山大佛.md",
  "index": 3,
  "text": "乐山大佛通高 71 米……",
  "modality": "text",
  "asset_url": "",
  "locator": "第 3 块",
  "dynasty": "唐",
  "place": "乐山",
  "topic": "水利",
  "source_type": "地方志",
  "credibility": "high",
  "abilities": ["水利", "文物"],
  "tags": ["大佛", "石刻"],
  "hash": "3f1a9c77b2e04d18",
  "meta": { "page": "2" },
  "created_at": "2026-10-10T13:22:00+08:00"
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | 块 ID，稳定唯一。 |
| `doc_id` | string | 所属文档 ID。 |
| `source` | string | 相对 `docs_dir` 的路径（含子目录，正斜杠分隔）。 |
| `index` | int | 块在文档内的序号。 |
| `text` | string | 块正文；图片块是**图注**，表格块是整表文本。 |
| `modality` | string | `text` \| `table` \| `image`，前端据此选渲染方式。 |
| `asset_url` | string | 仅 `modality=="image"` 时非空，是可直接放进 `<img src>` 的相对路径。 |
| `locator` | string | 人类可读位置，如 `第 3 块`、`第 2 页`。 |
| `dynasty` | string | 朝代，如 `唐`。未标注为空串。 |
| `place` | string | 地点，如 `乐山`。 |
| `topic` | string | 主题，如 `水利`。 |
| `source_type` | string | 来源类型，如 `地方志`、`正史`、`博物馆`。 |
| `credibility` | string | `high` \| `medium` \| `low`。 |
| `abilities` | string[] | 关联玩法能力（`水利`/`文物`…）。未解锁的碎片不进上下文（见 2.2）。 |
| `tags` | string[] | 自由标签。 |
| `hash` | string | 内容指纹，用于幂等去重。 |
| `meta` | object\<string,string\> | 零散属性（页码、工作表名、图注模型等）。 |
| `created_at` | string | RFC3339 时间。 |

> 除 `meta` 外，所有元数据字段都可能为空字符串 / 空数组（未标注）。

### 2.2 Filter（检索过滤：跨维度 AND / 同维度 OR）

```json
{
  "doc_ids": [], "modalities": ["text","table"],
  "dynasty": ["唐"], "place": ["乐山"], "topic": ["水利"],
  "source_type": ["地方志"], "credibility": ["high"],
  "abilities": ["水利"], "tags": []
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `doc_ids` | string[] | 限定文档 ID。 |
| `modalities` | string[] | 限定模态。 |
| `dynasty` / `place` / `topic` / `source_type` / `credibility` | string[] | 元数据维度。 |
| `abilities` | string[] | **能力门禁**，语义见下。 |
| `tags` | string[] | 与块 `tags` 有交集即命中。 |

**abilities 的门禁语义**：过滤集非空时，**没有标注 `abilities` 的块视为通用内容，始终可检索**；**标注了 `abilities` 的块必须与过滤集有交集**才可检索。

**组合语义**：不同维度之间 AND，同一维度多个值之间 OR。

### 2.3 Doc（已入库文档）

```json
{
  "id": "9a8b7c6d5e4f3021",
  "title": "乐山大佛",
  "source": "四川地方志/乐山大佛.md",
  "modality": "text",
  "size": 4821,
  "chunks": 7,
  "assets": ["img_001.png"],
  "status": "indexed",
  "error": "",
  "dynasty": "唐", "place": "乐山", "topic": "水利",
  "source_type": "地方志", "credibility": "high",
  "abilities": ["水利"], "tags": [],
  "fingerprint": "3f1a9c77b2e04d18",
  "updated_at": "2026-10-10T13:22:00+08:00"
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | string | 文档 ID。 |
| `title` | string | 标题（可空）。 |
| `source` | string | 相对 `docs_dir` 的路径。 |
| `modality` | string | 文档主模态。 |
| `size` | int | 文件字节数。 |
| `chunks` | int | 切出的块数。 |
| `assets` | string[] | 关联资产文件名列表。 |
| `status` | string | `indexed` \| `failed` \| `pending`。 |
| `error` | string | 失败原因；成功为空串。 |
| `dynasty`…`abilities`/`tags` | — | 同 Chunk 的元数据。 |
| `fingerprint` | string | 内容指纹，增量入库据此判断是否改动。 |
| `updated_at` | string | RFC3339 时间。 |

### 2.4 Hit（检索命中）

```json
{
  "chunk": { "...Chunk..." },
  "score": 0.7134,
  "dense_score": 0.6812,
  "sparse_score": 8.914,
  "rank": 1,
  "from": "both"
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `chunk` | Chunk | 命中的块。 |
| `score` | number | 融合后归一化到 0~1 的排序分。 |
| `dense_score` | number | 稠密（余弦）相似度。 |
| `sparse_score` | number | BM25 原始分（无界，仅展示与调试）。 |
| `rank` | int | 排名，从 1 开始；与答案里的 `[1]` `[2]` 一一对应。 |
| `from` | string | 来自哪一路：`dense` \| `sparse` \| `both`。 |

### 2.5 Site（结构化景点数据）

```json
{
  "id": "leshan-buddha",
  "name": "乐山大佛",
  "aliases": ["凌云大佛", "嘉定大佛"],
  "dynasty": "唐", "place": "乐山", "topic": "水利", "ability": "水利",
  "summary": "世界最大的石刻弥勒佛坐像。",
  "description": "开凿于唐开元元年（713）……",
  "facts": { "高度": "71 米", "开凿年代": "713—803 年", "主持": "海通禅师" },
  "interpretation": "民间传说中的『自目可剜，佛财难得』属演绎，正史无载。",
  "sources": [ { "title": "《嘉州凌云寺大弥勒石像记》", "license": "公有领域" } ],
  "lat": 29.545, "lng": 103.769,
  "tags": ["石刻", "世界遗产"]
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` / `name` / `aliases` | string / string / string[] | 标识与别名；`GET /sites/:id` 支持用任一名称查询。 |
| `dynasty` / `place` / `topic` / `ability` | string | 分类与玩法能力。 |
| `summary` / `description` | string | 简介与详述。 |
| `facts` | object\<string,string\> | 结构化字段（高度 / 年代 / 主持…）。 |
| `interpretation` | string | 演绎说明，与史实分开。 |
| `sources` | SourceRef[] | 出处，`{ title, url?, license?, note? }`。 |
| `lat` / `lng` | number | 坐标。 |
| `tags` | string[] | 标签。 |

### 2.6 StructuredAnswer（`answer_format=structured`）

```json
{
  "history": "乐山大佛通高 71 米，开凿于唐开元元年至贞元十九年 [1]。",
  "sources": ["[1] 《嘉州凌云寺大弥勒石像记》—— 地方志，可信度 high"],
  "interpretation": "关于海通禅师自剜双目的说法出自民间传说，史料无明确记载。",
  "confidence": "high",
  "unknown": false
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `history` | string | **只写**有资料支撑的史实。 |
| `sources` | string[] | 带编号的引用说明，与 `sources` 数组下标对应。 |
| `interpretation` | string | 无史料支撑的推断 / 传说 / 演绎；没有就留空字符串。 |
| `confidence` | string | `high` \| `medium` \| `low`。 |
| `unknown` | bool | `true` 表示知识库里查不到（此时 `history` 写"知识库里没有找到相关内容"）。 |

### 2.7 其他公共结构

| 名称 | 字段 |
| --- | --- |
| Usage | `prompt_tokens` int、`completion_tokens` int、`total_tokens` int |
| ChatMessage | `role` string（`user`/`assistant`/`system`）、`content` string |
| Progress | `current` int、`total` int、`percent` int |
| JobStats | `files_total`、`files_changed`、`files_skipped`、`chunks_added`、`chunks_removed`、`chunks_deduped`、`assets`、`elapsed_ms`（均 int） |
| Facets | `dynasty`、`place`、`topic`、`source_type`、`credibility`、`abilities`（均为 string[]） |
| SourceRef | `title`、`url`、`license`、`note`（均 string） |

---

## 3. 接口详解

### 3.1 健康检查

```http
GET /api/v1/health
```

- **鉴权**：否。**请求**：无参数。**HTTP 恒为 200**。

```json
{
  "code": 0, "message": "ok", "request_id": "...",
  "data": {
    "status": "ok",
    "version": "0.2.0",
    "uptime_s": 128,
    "embedding": { "ok": true, "provider": "ollama", "model": "bge-m3", "dim": 1024, "detail": "" },
    "chat":      { "ok": true, "provider": "openai", "model": "deepseek-chat", "detail": "" },
    "vision":    { "ok": false, "provider": "openai", "model": "gpt-4o-mini", "detail": "未启用" },
    "index":     { "chunks": 42, "docs": 3, "dim": 1024, "backend": "file" }
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `status` | string | `ok`（依赖全通）\| `degraded`（能跑但依赖不全）。 |
| `version` | string | 服务版本。 |
| `uptime_s` | int | 已运行秒数。 |
| `embedding` / `chat` / `vision` | ComponentHealth | `{ ok, provider, model, dim?, detail }`；`detail` 为空表示正常，否则是原因。 |
| `index` | object | `{ chunks, docs, dim, backend }`。 |

> 依赖不全也用 200 + `degraded` 表达，**不要靠状态码判断依赖**。

### 3.2 前端可见配置

```http
GET /api/v1/config
```

- **鉴权**：是。

```json
{
  "code": 0, "message": "ok", "request_id": "...",
  "data": {
    "version": "0.2.0",
    "auth_required": false,
    "embedding_model": "bge-m3",
    "chat_model": "deepseek-chat",
    "chat_provider": "openai",
    "vision_enabled": false,
    "retrieval": { "top_k": 4, "candidate_k": 20, "hybrid_alpha": 0.6, "fusion": "rrf", "min_score": 0.0, "rewrite": true },
    "chunking": { "size": 600, "overlap": 90 },
    "upload": { "max_mb": 32, "accept": [".txt",".md",".docx",".pdf",".xlsx",".csv",".html",".png",".jpg"] },
    "features": { "stream": true, "structured_answer": true, "history": true, "filters": true, "abilities": true, "tools": true, "traces": true }
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `auth_required` | bool | `true` 时除 health 外都要带 token。 |
| `embedding_model` | string | 当前嵌入模型。 |
| `chat_model` / `chat_provider` | string | 当前生成模型与供应商。 |
| `vision_enabled` | bool | 是否启用图片图注。 |
| `retrieval` | object | `{ top_k, candidate_k, hybrid_alpha, fusion, min_score, rewrite }`，用于初始化 UI 默认值。 |
| `chunking` | object | `{ size, overlap }`。 |
| `upload` | object | `{ max_mb, accept }`。 |
| `features` | object | 能力开关，前端据此隐藏不可用 UI。 |

- **错误码**：40100、50000。

### 3.3 索引统计

```http
GET /api/v1/stats
```

- **鉴权**：是。

```json
{
  "code": 0, "message": "ok", "request_id": "...",
  "data": {
    "backend": "file", "chunks": 42, "docs": 3, "dim": 1024,
    "modalities": { "text": 40, "table": 1, "image": 1 },
    "assets": 1,
    "data_dir": "D:\\Deepseek\\RAG\\server\\data",
    "facets": {
      "dynasty": ["唐","宋"], "place": ["乐山","成都"],
      "topic": ["水利","诗词"], "abilities": ["水利","文物"],
      "source_type": ["地方志","正史"], "credibility": ["high","medium"]
    }
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `backend` | string | `file` \| `pgvector`。 |
| `chunks` / `docs` / `dim` | int | 块数 / 文档数 / 向量维度。 |
| `modalities` | object | 各模态块数。 |
| `assets` | int | 资产文件数。 |
| `data_dir` | string | 索引落盘目录（绝对路径）。 |
| `facets` | Facets | 索引里出现过的元数据取值，**前端据此生成筛选器，无需硬编码词典**。 |

- **错误码**：40100、50000。

### 3.4 问答

```http
POST /api/v1/chat
Content-Type: application/json
```

- **鉴权**：是。

| 字段 | 类型 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `question` | string | **是** | — | 1~2000 字。 |
| `stream` | bool | 否 | `true` | `false` 时返回一次性 JSON。 |
| `answer_format` | string | 否 | `text` | `text` 或 `structured`（史实边界强约束）。 |
| `top_k` | int | 否 | 服务端配置 | 1~20。 |
| `temperature` | float | 否 | 服务端配置 | 0~2。 |
| `session_id` | string | 否 | `""` | 传入则服务端保存多轮上下文；也可每轮自带 `history`。 |
| `history` | ChatMessage[] | 否 | `[]` | 最多 20 轮，超出按 token 预算从最旧开始裁剪。 |
| `filters` | Filter | 否 | `{}` | 见 2.2。 |
| `abilities` | string[] | 否 | `[]` | 便捷写法，等价于 `filters.abilities`。 |
| `use_tools` | bool | 否 | `true` | 是否启用服务端只读工具预取（景点数据）。 |
| `rewrite` | bool | 否 | 服务端配置 | 多轮时把"它有多高"改写成独立问题再检索。 |

请求示例：

```json
{
  "question": "它有多高？",
  "stream": true,
  "answer_format": "text",
  "top_k": 4,
  "temperature": 0.1,
  "session_id": "s_7f3a91c2",
  "history": [ { "role": "user", "content": "乐山大佛在哪？" }, { "role": "assistant", "content": "在四川乐山。" } ],
  "filters": { "dynasty": ["唐"], "abilities": ["水利"] },
  "use_tools": true,
  "rewrite": true
}
```

#### 非流式响应（`stream: false`）

```json
{
  "code": 0, "message": "ok", "request_id": "...",
  "data": {
    "answer": "乐山大佛通高 71 米 [1]。",
    "structured": null,
    "sources": [ { "...Hit..." } ],
    "sites": [ { "...Site..." } ],
    "usage": { "prompt_tokens": 812, "completion_tokens": 96, "total_tokens": 908 },
    "elapsed_ms": 3421,
    "ttft_ms": 812,
    "model": "deepseek-chat",
    "rewritten_question": "乐山大佛有多高",
    "request_id": "9f2c1a4b7e0d3355"
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `answer` | string | 最终答案文本（含 `[n]` 引用编号）。 |
| `structured` | StructuredAnswer \| null | 仅 `answer_format=structured` 且解析成功时非 null。 |
| `sources` | Hit[] | 命中的检索片段，下标与 `[n]` 对应。 |
| `sites` | Site[] | 工具预取命中的景点；未命中为空数组。 |
| `usage` | Usage | token 用量。 |
| `elapsed_ms` / `ttft_ms` | int | 总耗时 / 首 token 耗时（毫秒）。 |
| `model` | string | 实际使用的生成模型。 |
| `rewritten_question` | string | 多轮改写后的问题；未改写为空串。 |

- **错误码**：40001、40100、50200、50300、50000。

### 3.5 纯检索

```http
POST /api/v1/search
```

请求：

```json
{ "query": "大佛高度", "top_k": 6, "filters": { "dynasty": ["唐"] }, "abilities": [] }
```

| 字段 | 类型 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `query` | string | **是** | — | 检索语句。 |
| `top_k` | int | 否 | 服务端 `retrieval.top_k` | 返回条数。 |
| `filters` | Filter | 否 | `{}` | 见 2.2。 |
| `abilities` | string[] | 否 | `[]` | 等价于 `filters.abilities`。 |

响应 `data`：

```json
{
  "hits": [ { "...Hit..." } ],
  "elapsed_ms": 37,
  "candidates": { "dense": 20, "sparse": 20 }
}
```

- `POST /api/v1/retrieve` 与 `/search` **完全等价**（保留别名），请求/响应同上。
- **错误码**：40001、40100、50300、50000。

### 3.6 上传资料

```http
POST /api/v1/assets
Content-Type: multipart/form-data
```

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | file | **是** | 单文件。 |
| `source` | text | 否 | 目标相对路径；缺省用上传文件名。 |
| `ingest` | text | 否 | `true` 时上传后立即入库（异步）。 |
| `dynasty` / `place` / `topic` / `source_type` / `credibility` | text | 否 | 元数据。 |
| `abilities` | text | 否 | 逗号分隔。 |

响应 `data`：

```json
{ "asset_id": "9a8b7c6d5e4f3021", "doc_id": "9a8b7c6d5e4f3021", "source": "乐山大佛.md", "size": 10240, "job_id": "j_7f3a91c2" }
```

- `asset_id` 与 `doc_id` **同值**（上传即登记一份文档）。
- `POST /api/v1/documents/upload` 与 `/assets` 等价（保留别名）。
- 超限 / 扩展名不允许 → `40001`。
- **错误码**：40001、40100、40900、42900、50000。

### 3.7 读取二进制资源

```http
GET /api/v1/assets/:doc_id/:name
```

- **路径参数**：`doc_id`（文档 ID）、`name`（资产文件名，如 `img_001.png`）。
- **响应**：直接返回二进制内容，`Content-Type` 按扩展名推断，`Cache-Control: public, max-age=86400`。
- 图片引用里的 `asset_url` 就是这个路径，可直接放进 `<img src>`。
- **错误码**：40100、40400、50000。

### 3.8 触发入库

```http
POST /api/v1/ingest
```

请求：

```json
{ "mode": "incremental", "paths": [], "rebuild": false }
```

| 字段 | 类型 | 必填 | 默认 | 说明 |
| --- | --- | --- | --- | --- |
| `mode` | string | 否 | `incremental` | `incremental` \| `rebuild`。 |
| `paths` | string[] | 否 | `[]` | 限定只处理这些相对路径。 |
| `rebuild` | bool | 否 | `false` | `true` 等价于 `mode:"rebuild"`。 |

响应 `data`：`{ "job_id": "j_7f3a91c2", "status": "running" }`

- 同一时刻只允许一个入库任务；已有任务运行 → **HTTP 409 + `code=40900`**。
- **错误码**：40001、40100、40900、50000。

### 3.9 任务详情与进度

```http
GET /api/v1/ingest/jobs/:id
```

```json
{
  "code": 0, "message": "ok", "request_id": "...",
  "data": {
    "job_id": "j_7f3a91c2", "mode": "incremental", "status": "running",
    "progress": { "current": 12, "total": 30, "percent": 40 },
    "message": "向量化 12/30",
    "logs": ["[1/3] 处理 乐山大佛.md", "  切出 7 块"],
    "stats": { "files_total": 3, "files_changed": 2, "files_skipped": 1, "chunks_added": 18, "chunks_removed": 0, "chunks_deduped": 0, "assets": 1, "elapsed_ms": 5120 },
    "error": "",
    "started_at": "2026-10-10T13:22:00+08:00",
    "finished_at": null
  }
}
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `job_id` / `mode` | string | 任务 ID / `incremental` \| `rebuild`。 |
| `status` | string | `running` \| `succeeded` \| `failed` \| `cancelled`。 |
| `progress` | Progress | `{ current, total, percent }`。 |
| `message` | string | 当前阶段简述。 |
| `logs` | string[] | 最近日志，最多 200 行。 |
| `stats` | JobStats | 文件与块统计。 |
| `error` | string | 失败原因。 |
| `started_at` / `finished_at` | string \| null | RFC3339；运行中 `finished_at` 为 `null`。 |

> 建议每 **500~1000ms** 轮询一次，直到 `status != "running"`。

- **错误码**：40100、40400、50000。

### 3.10 任务列表

```http
GET /api/v1/ingest/jobs
```

响应 `data`：`{ "jobs": [ ...同上... ] }`，按启动时间**倒序**，最多 20 条。

- **错误码**：40100、50000。

### 3.11 取消任务

```http
POST /api/v1/ingest/jobs/:id/cancel
```

响应 `data`：`{ "job_id": "...", "status": "cancelling" }`

- 取消是**异步**的：立即返回 `cancelling`，之后状态转为 `cancelled`，继续用 3.9 轮询确认。
- 任务不存在 → `40400`。
- **错误码**：40100、40400、50000。

### 3.12 文档列表

```http
GET /api/v1/documents
```

响应 `data`：`{ "documents": [ ...Doc... ], "total": 3 }`

- **错误码**：40100、50000。

### 3.13 删除文档

```http
DELETE /api/v1/documents/:id
```

删除该文档的索引块、资产文件与 manifest 记录（**不删除** `docs_dir` 里的原文件）。

响应 `data`：`{ "doc_id": "...", "removed_chunks": 7, "removed_assets": 0 }`

- **错误码**：40100、40400、40900、50000。

### 3.14 景点列表

```http
GET /api/v1/sites
```

响应 `data`：`{ "sites": [ ...Site... ], "total": 2 }`

- 数据来源 `configs/sites.json`；文件不存在时返回**空列表**，服务照常运行。
- **错误码**：40100、50000。

### 3.15 单个景点

```http
GET /api/v1/sites/:id
```

响应 `data`：`{ ...Site... }`

- 支持用 `id`、`name` 或 `aliases` 里的任一名称查询。
- 不存在 → `40400`。
- **错误码**：40100、40400、50000。

### 3.16 问答链路留痕

```http
GET /api/v1/traces
```

响应 `data`：`{ "traces": [ ... ], "total": 12 }`，最近 100 条内存留痕，**倒序**。

```json
{
  "request_id": "9f2c...", "at": "2026-10-10T13:22:00+08:00",
  "question": "它有多高？", "rewritten_question": "乐山大佛有多高",
  "hits": [ { "chunk_id": "c1a2...", "source": "乐山大佛.md", "score": 0.71, "from": "both" } ],
  "sites": ["leshan-buddha"],
  "prompt_chars": 1820, "answer_chars": 96,
  "model": "deepseek-chat", "elapsed_ms": 3421, "ttft_ms": 812,
  "usage": { "prompt_tokens": 812, "completion_tokens": 96, "total_tokens": 908 },
  "error": ""
}
```

用途：出问题时能复现"当时检索到了什么、prompt 多长、模型回了什么"。`hits[].chunk_id` 可再去 `/search` 或用文档列表定位原文。

- **错误码**：40100、50000。

### 3.17 规范与 Demo 页

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/openapi.yaml` | 返回 OpenAPI 3.1 规范文本（`application/yaml`）。 |
| GET | `/` | 内置 Demo 页面（`web/index.html`，`text/html`）。 |

两者都不需要鉴权。

---

## 4. SSE 事件流

### 4.1 时序图

```text
前端                                              服务端
 │  POST /api/v1/chat  { stream: true }
 │────────────────────────────────────────────────►│
 │                                                 │ 1. 检索：嵌入 → 稠密 + BM25 → RRF 融合
 │                                                 │ 2. 工具预取（景点数据，可选）
 │  event: meta                                    │
 │◄────────────────────────────────────────────────│  data: {request_id, model, rewritten_question}
 │  event: sites                                   │
 │◄────────────────────────────────────────────────│  data: {"sites":[Site,...]}   ← 仅命中时出现
 │  event: sources                                 │
 │◄────────────────────────────────────────────────│  data: {"sources":[Hit,...]}
 │  （先渲染出处，答案还没开始）                     │
 │                                                 │ 3. 调 Chat API 流式生成
 │  event: delta                                   │
 │◄────────────────────────────────────────────────│  data: {"content":"乐山大佛"}
 │  event: delta                                   │
 │◄────────────────────────────────────────────────│  data: {"content":"通高 71 米"}
 │            ...（0~N 次 delta）...                │
 │  event: structured                              │
 │◄────────────────────────────────────────────────│  data: {history, sources, interpretation, ...}
 │  （仅 answer_format=structured 且 JSON 解析成功） │
 │  event: done                                    │
 │◄────────────────────────────────────────────────│  data: {answer, usage, elapsed_ms, ttft_ms, model}
 │  （服务端关闭连接）                              │
 │                                                 │
 │            ── 出错时（done 之前任意时刻）──      │
 │  event: error                                   │
 │◄────────────────────────────────────────────────│  data: {"code":50200,"message":"上游模型服务错误: ..."}
 │  （服务端关闭连接）                              │
 │                                                 │
 │  : ping                                         │
 │◄────────────────────────────────────────────────│  每 15s 一条注释行保活（无 event/data，忽略）
```

### 4.2 事件一览

| 事件名 | 出现次数 | 顺序 | data 结构 |
| --- | --- | --- | --- |
| `meta` | 恰好 1 次 | **永远第一个** | `{ request_id, model, rewritten_question }` |
| `sites` | 0 或 1 次 | meta 之后、sources 之前 | `{ sites: Site[] }` |
| `sources` | 恰好 1 次 | delta 之前 | `{ sources: Hit[] }` |
| `delta` | 0~N 次 | sources 之后、done 之前 | `{ content: string }` |
| `structured` | 0 或 1 次 | delta 之后、done 之前 | StructuredAnswer |
| `done` | 恰好 1 次 | 最后一个（正常结束） | `{ answer, usage, elapsed_ms, ttft_ms, model }` |
| `error` | 0 或 1 次 | done 之前任意时刻，之后关闭 | `{ code, message }` |
| `: ping` | 每 15s | 任意时刻 | 注释行，**无 data**，解析时忽略 |

约定：

- 事件之间用**空行**（`\n\n`）分隔；`data` 行是**单行 JSON**（内部换行已转义）。
- `meta` 永远第一个；`sites` 仅在启用工具且命中景点时出现；`structured` 仅在 `answer_format=structured` 且 JSON 解析成功时出现。
- `delta` 是**原始**模型输出片段。`structured` 模式下 delta 里是 JSON 文本的增量，前端可以只显示"正在生成"，等 `structured` 事件到了再渲染结构化卡片。
- `done` 之前任何时刻都可能出现 `error`；出错后服务端关闭连接。
- 客户端断开时服务端取消上游请求（`context` 取消）。

### 4.3 各事件 data 结构

```jsonc
// event: meta
{ "request_id": "9f2c1a4b7e0d3355", "model": "deepseek-chat", "rewritten_question": "乐山大佛有多高" }

// event: sites
{ "sites": [ { "id": "leshan-buddha", "name": "乐山大佛", "facts": { "高度": "71 米" } } ] }

// event: sources
{ "sources": [ { "chunk": { "...Chunk..." }, "score": 0.7134, "dense_score": 0.6812, "sparse_score": 8.914, "rank": 1, "from": "both" } ] }

// event: delta
{ "content": "乐山大佛" }

// event: structured
{ "history": "...", "sources": ["..."], "interpretation": "", "confidence": "high", "unknown": false }

// event: done
{ "answer": "乐山大佛通高 71 米 [1]。", "usage": { "prompt_tokens": 812, "completion_tokens": 96, "total_tokens": 908 }, "elapsed_ms": 3421, "ttft_ms": 812, "model": "deepseek-chat" }

// event: error
{ "code": 50200, "message": "上游模型服务错误: ..." }
```

---

## 5. 前端代码

> **不要用 `EventSource`**：它只支持 GET，不能带请求体，也不能自定义 `Authorization` 头，而 `POST /api/v1/chat` 三样都需要。请用 `fetch` + `ReadableStream` 手动解析。

### 5.1 代码 ①：原生 fetch + ReadableStream 解析 SSE

关键点：

- SSE 的**网络分块边界不等于事件边界**，必须维护 buffer，按 `"\n\n"` 切块，最后一段不完整要留到下一轮。
- 建连前的错误（401 / 400）走普通 JSON 信封；建连后的错误走 `error` 事件，**HTTP 仍是 200**。
- 用 `AbortController` 支持"停止生成"；用 `performance.now()` 计算 **TTFT**（首 token 耗时）。

```js
/**
 * 流式问答。
 * @param {object}   opts  { question, answerFormat, topK, filters, abilities, history, sessionId, token, baseUrl, signal }
 * @param {function} onEvent (eventName, data, meta) => void
 *        eventName: "meta" | "sites" | "sources" | "delta" | "structured" | "done" | "error"
 * @returns {Promise<{ answer: string, sources: any[], structured: any, ttftMs: number, elapsedMs: number }>}
 */
async function ragChatStream(opts, onEvent) {
  var baseUrl = opts.baseUrl || "http://127.0.0.1:8080";
  var t0 = performance.now();
  var ttftMs = 0;
  var answer = "";
  var sources = [];
  var structured = null;

  var res = await fetch(baseUrl + "/api/v1/chat", {
    method: "POST",
    headers: Object.assign(
      { "Content-Type": "application/json" },
      opts.token ? { Authorization: "Bearer " + opts.token } : {}
    ),
    body: JSON.stringify({
      question: opts.question,
      stream: true,
      answer_format: opts.answerFormat || "text",
      top_k: opts.topK != null ? opts.topK : 4,
      filters: opts.filters || {},
      abilities: opts.abilities || [],
      history: opts.history || [],
      session_id: opts.sessionId || "",
    }),
    signal: opts.signal,
  });

  // 鉴权 / 参数校验失败走普通 JSON 信封，不是 SSE
  if (!res.ok) {
    var errBody = await res.json().catch(function () { return {}; });
    var e = new Error(errBody.message || ("HTTP " + res.status));
    e.code = errBody.code;
    e.requestId = errBody.request_id || res.headers.get("X-Request-Id");
    throw e;
  }

  var reader = res.body.getReader();
  var decoder = new TextDecoder("utf-8");
  var buffer = "";
  var sawDone = false;

  for (;;) {
    var step = await reader.read();
    if (step.done) break;

    buffer += decoder.decode(step.value, { stream: true });

    // 按空行切事件；最后一段可能不完整，留到下一轮
    var blocks = buffer.split("\n\n");
    buffer = blocks.pop();

    for (var i = 0; i < blocks.length; i++) {
      var block = blocks[i];
      if (!block.trim()) continue;
      if (block.charAt(0) === ":") continue; // ": ping" 保活注释

      var evMatch = block.match(/^event:\s*(.+)$/m);
      var ev = evMatch ? evMatch[1].trim() : "message";
      var dataMatch = block.match(/^data:\s*(.+)$/m);
      var raw = dataMatch ? dataMatch[1] : "{}";

      var data;
      try {
        data = JSON.parse(raw);
      } catch (_) {
        continue; // 半截 JSON 直接跳过，避免整个流崩掉
      }

      if (ev === "delta" && ttftMs === 0) {
        ttftMs = Math.round(performance.now() - t0); // 首 token 耗时
      }
      if (ev === "delta") answer += data.content || "";
      if (ev === "sources") sources = data.sources || [];
      if (ev === "structured") structured = data;
      if (ev === "done") {
        sawDone = true;
        if (typeof data.answer === "string") answer = data.answer; // 用最终答案校正
      }

      onEvent(ev, data, { ttftMs: ttftMs });
    }
  }

  if (!sawDone) {
    var err = new Error("连接中断，未收到 done 事件");
    err.code = -1;
    throw err;
  }
  return { answer: answer, sources: sources, structured: structured, ttftMs: ttftMs, elapsedMs: Math.round(performance.now() - t0) };
}

// ---- 用法：带"停止生成" ----
var controller = new AbortController();
var streamed = "";

ragChatStream(
  { question: "乐山大佛有多高？", topK: 4, answerFormat: "text", signal: controller.signal },
  function (ev, data) {
    if (ev === "meta") console.log("request_id", data.request_id, "改写后", data.rewritten_question);
    else if (ev === "sites") console.log("工具命中景点", data.sites);
    else if (ev === "sources") renderSources(data.sources);
    else if (ev === "delta") { streamed += data.content; renderAnswer(streamed); }
    else if (ev === "structured") renderStructured(data);
    else if (ev === "done") console.log("用量", data.usage, "总耗时", data.elapsed_ms, "TTFT", data.ttft_ms);
    else if (ev === "error") console.error("服务端错误", data.code, data.message);
  }
).catch(function (e) {
  if (e.name === "AbortError") return; // 用户主动停止，不算错误
  console.error("请求失败", e.code, e.message, e.requestId);
});

// 用户点"停止"：
// controller.abort();  // 服务端会收到断开并取消上游请求
```

### 5.2 代码 ②：React Hook `useRagChat`

```jsx
import { useCallback, useRef, useState } from "react";

/**
 * useRagChat —— useChat 风格的流式问答 Hook。
 * 返回 { ask, stop, reset, answer, sources, sites, structured, status, error, ttftMs, usage }。
 * status: "idle" | "retrieving" | "streaming" | "done" | "error"
 */
export function useRagChat({ baseUrl = "http://127.0.0.1:8080", token = "" } = {}) {
  const [answer, setAnswer] = useState("");
  const [sources, setSources] = useState([]);
  const [sites, setSites] = useState([]);
  const [structured, setStructured] = useState(null);
  const [usage, setUsage] = useState(null);
  const [ttftMs, setTtftMs] = useState(0);
  const [status, setStatus] = useState("idle");
  const [error, setError] = useState(null);

  const abortRef = useRef(null);
  const answerRef = useRef("");

  const stop = useCallback(() => {
    if (abortRef.current) abortRef.current.abort();
    abortRef.current = null;
    setStatus((s) => (s === "streaming" || s === "retrieving" ? "idle" : s));
  }, []);

  const reset = useCallback(() => {
    stop();
    answerRef.current = "";
    setAnswer("");
    setSources([]);
    setSites([]);
    setStructured(null);
    setUsage(null);
    setTtftMs(0);
    setError(null);
    setStatus("idle");
  }, [stop]);

  const ask = useCallback(
    async (question, { topK = 4, filters = {}, abilities = [], history = [], answerFormat = "text", sessionId = "" } = {}) => {
      if (abortRef.current) abortRef.current.abort();
      const ac = new AbortController();
      abortRef.current = ac;

      answerRef.current = "";
      setAnswer("");
      setSources([]);
      setSites([]);
      setStructured(null);
      setUsage(null);
      setTtftMs(0);
      setError(null);
      setStatus("retrieving");

      const t0 = performance.now();
      let firstDelta = true;

      try {
        const res = await fetch(baseUrl + "/api/v1/chat", {
          method: "POST",
          headers: Object.assign(
            { "Content-Type": "application/json" },
            token ? { Authorization: "Bearer " + token } : {}
          ),
          body: JSON.stringify({
            question,
            stream: true,
            answer_format: answerFormat,
            top_k: topK,
            filters,
            abilities,
            history,
            session_id: sessionId,
          }),
          signal: ac.signal,
        });

        if (!res.ok) {
          const body = await res.json().catch(() => ({}));
          const err = new Error(body.message || ("HTTP " + res.status));
          err.code = body.code;
          err.requestId = body.request_id || res.headers.get("X-Request-Id");
          throw err;
        }

        const reader = res.body.getReader();
        const decoder = new TextDecoder("utf-8");
        let buffer = "";
        let sawDone = false;

        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;

          buffer += decoder.decode(value, { stream: true });
          const blocks = buffer.split("\n\n");
          buffer = blocks.pop();

          for (const block of blocks) {
            if (!block.trim() || block.startsWith(":")) continue;
            const ev = (block.match(/^event:\s*(.+)$/m) || [])[1]?.trim() || "message";
            const raw = (block.match(/^data:\s*(.+)$/m) || [])[1] || "{}";

            let data;
            try {
              data = JSON.parse(raw);
            } catch {
              continue;
            }

            if (ev === "meta") {
              // data.rewritten_question / data.model 可存起来展示
            } else if (ev === "sites") {
              setSites(data.sites || []);
            } else if (ev === "sources") {
              setSources(data.sources || []);
              setStatus("streaming");
            } else if (ev === "delta") {
              if (firstDelta) {
                firstDelta = false;
                setTtftMs(Math.round(performance.now() - t0));
              }
              answerRef.current += data.content || "";
              setAnswer(answerRef.current);
            } else if (ev === "structured") {
              setStructured(data);
            } else if (ev === "done") {
              sawDone = true;
              if (typeof data.answer === "string") {
                answerRef.current = data.answer;
                setAnswer(data.answer);
              }
              setUsage(data.usage || null);
              setStatus("done");
            } else if (ev === "error") {
              sawDone = true;
              setError({ code: data.code, message: data.message });
              setStatus("error");
            }
          }
        }

        if (!sawDone) {
          setError({ code: -1, message: "连接中断，未收到 done 事件" });
          setStatus("error");
        }
      } catch (e) {
        if (e.name === "AbortError") return; // 主动停止，不算错误
        setError({ code: e.code || -1, message: e.message, requestId: e.requestId });
        setStatus("error");
      }
    },
    [baseUrl, token]
  );

  return { ask, stop, reset, answer, sources, sites, structured, usage, ttftMs, status, error };
}
```

---

## 6. 引用渲染

答案里的 `[1]` `[2]` 与 `sources` 数组**下标一一对应（从 1 开始）**，可做成可点击跳转。

### 6.1 文本卡片（`modality === "text"`）

```jsx
function TextCard({ hit, onCiteClick }) {
  const c = hit.chunk;
  const summary = c.text.length > 200 ? c.text.slice(0, 200) + "…" : c.text;
  return (
    <div className="cite-card" id={"cite-" + hit.rank} onClick={() => onCiteClick?.(hit.rank)}>
      <div className="cite-head">
        <span className="cite-src">{c.source}</span>
        <span className="cite-loc">{c.locator}</span>
        <span className="cite-score">{hit.score.toFixed(3)}</span>
        {c.credibility ? <span className="cite-cred">{c.credibility}</span> : null}
      </div>
      <p className="cite-text">{summary}</p>
    </div>
  );
}
```

### 6.2 表格卡片（`modality === "table"`）

表格块的 `text` 是按换行拆行、按竖线拆列的纯文本：

```jsx
function TableCard({ hit }) {
  const rows = hit.chunk.text
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => line.split("|").map((cell) => cell.trim()));

  return (
    <div className="cite-card" id={"cite-" + hit.rank}>
      <div className="cite-head">
        <span className="cite-src">{hit.chunk.source}</span>
        <span className="cite-score">{hit.score.toFixed(3)}</span>
      </div>
      <table className="cite-table">
        <tbody>
          {rows.map((cells, i) => (
            <tr key={i}>
              {cells.map((cell, j) => (
                <td key={j}>{cell}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
```

### 6.3 图片卡片（`modality === "image"`）

`asset_url` 是相对路径，需拼上 Base URL：

```jsx
function ImageCard({ hit, baseUrl = "http://127.0.0.1:8080" }) {
  const c = hit.chunk;
  const src = c.asset_url ? baseUrl + c.asset_url : "";
  return (
    <div className="cite-card" id={"cite-" + hit.rank}>
      <div className="cite-head">
        <span className="cite-src">{c.source}</span>
        <span className="cite-score">{hit.score.toFixed(3)}</span>
      </div>
      {src ? <img className="cite-img" src={src} alt={c.text} loading="lazy" /> : null}
      <p className="cite-caption">{c.text}</p>
    </div>
  );
}
```

### 6.4 统一入口 + `[n]` 跳转高亮

```jsx
function HitCard(props) {
  switch (props.hit.chunk.modality) {
    case "table":
      return <TableCard {...props} />;
    case "image":
      return <ImageCard {...props} />;
    default:
      return <TextCard {...props} />;
  }
}

/** 把答案里的 [n] 变成可点击锚点；点击后滚动到对应卡片并高亮 1.2s */
function AnswerWithCitations({ answer, sources, activeRank }) {
  const jump = (n) => {
    const el = document.getElementById("cite-" + n);
    if (!el) return;
    el.scrollIntoView({ behavior: "smooth", block: "center" });
    el.classList.add("cite-flash");
    setTimeout(() => el.classList.remove("cite-flash"), 1200);
  };

  const parts = answer.split(/(\[\d+\])/g);
  return (
    <p className="answer">
      {parts.map((part, i) => {
        const m = part.match(/^\[(\d+)\]$/);
        if (!m) return <span key={i}>{part}</span>;
        const n = Number(m[1]);
        const hit = sources[n - 1];
        return (
          <a
            key={i}
            className={"cite-anchor" + (activeRank === n ? " active" : "")}
            href={"#cite-" + n}
            title={hit ? hit.chunk.source + " " + (hit.chunk.locator || "") : "无对应出处"}
            onClick={(e) => {
              e.preventDefault();
              jump(n);
            }}
          >
            [{n}]
          </a>
        );
      })}
    </p>
  );
}
```

```css
.cite-anchor { color: #2563eb; cursor: pointer; text-decoration: none; }
.cite-anchor.active { font-weight: 700; }
.cite-card.cite-flash { outline: 2px solid #f59e0b; transition: outline-color .3s; }
```

---

## 7. 完整会话示例

流程：**上传 → 入库 → 轮询进度 → 流式提问 → 渲染引用**。

### 7.1 curl / PowerShell

```powershell
$base = "http://127.0.0.1:8080/api/v1"
# 若服务端配置了 auth_token，取消注释并填入 token
# $headers = @{ Authorization = "Bearer <token>" }

# ---- 1) 上传文件并立即入库 ----
curl.exe -s -X POST "$base/assets" `
  -F "file=@D:\Deepseek\RAG\docs\示例-员工手册.md" `
  -F "ingest=true"
# => {"code":0,"message":"ok","data":{"asset_id":"9a8b...","doc_id":"9a8b...","source":"示例-员工手册.md","size":4821,"job_id":"j_7f3a91c2"},"request_id":"..."}

# ---- 2) 轮询入库进度，直到 status != running ----
curl.exe -s "$base/ingest/jobs/j_7f3a91c2"
# => data.status: running -> succeeded，data.progress.percent: 0 -> 100

# ---- 3) 流式提问（-N 关闭输出缓冲，否则看不到打字机效果）----
curl.exe -sN -X POST "$base/chat" `
  -H "Content-Type: application/json" `
  -d "{\"question\":\"年假有几天？\",\"stream\":true,\"answer_format\":\"text\",\"top_k\":4}"
# => event: meta / sites / sources / delta ... / done
```

> PowerShell 用反引号换行；若开了鉴权，给每个请求加 `-H "Authorization: Bearer <token>"`。

### 7.2 JavaScript

```js
const base = "http://127.0.0.1:8080/api/v1";
const token = ""; // 服务端配置了 auth_token 时填这里
const auth = token ? { Authorization: "Bearer " + token } : {};

async function fullSession(file, question) {
  // ---- 1) 上传 ----
  const fd = new FormData();
  fd.append("file", file);
  fd.append("ingest", "true");

  const up = await fetch(base + "/assets", { method: "POST", headers: auth, body: fd }).then((r) => r.json());
  if (up.code !== 0) throw new Error(up.message);

  const jobId = up.data.job_id;
  console.log("已上传", up.data.source, "文档", up.data.doc_id, "任务", jobId);

  // ---- 2) 轮询进度 ----
  let job;
  for (;;) {
    const body = await fetch(base + "/ingest/jobs/" + jobId, { headers: auth }).then((r) => r.json());
    if (body.code !== 0) throw new Error(body.message);
    job = body.data;
    console.log(job.status, (job.progress && job.progress.percent) + "%", job.message);
    if (job.status !== "running") break;
    await new Promise((r) => setTimeout(r, 800)); // 500~1000ms
  }
  if (job.status !== "succeeded") throw new Error("入库失败：" + job.error);

  // ---- 3) 流式提问 + 渲染 ----
  const result = await ragChatStream(
    { question: question, topK: 4, token: token },
    function (ev, data) {
      if (ev === "sources") console.log("出处", data.sources.map((h) => h.chunk.source));
      else if (ev === "delta") { /* 实时更新 UI */ }
      else if (ev === "structured") renderStructured(data);
      else if (ev === "error") console.error("生成失败", data.code, data.message);
    }
  );
  console.log("TTFT", result.ttftMs, "ms，总耗时", result.elapsedMs, "ms");

  // ---- 4) 渲染引用 ----
  return { answer: result.answer, sources: result.sources, html: result.sources.map(renderHit).join("") };
}

function renderHit(hit) {
  const c = hit.chunk;
  const score = hit.score.toFixed(3);
  if (c.modality === "image") {
    return '<figure id="cite-' + hit.rank + '"><img src="http://127.0.0.1:8080' + c.asset_url + '" alt=""><figcaption>' + c.text + "</figcaption></figure>";
  }
  if (c.modality === "table") {
    const rows = c.text.split("\n").map((line) => line.split("|").map((s) => s.trim()));
    return "<table>" + rows.map((cells) => "<tr>" + cells.map((cell) => "<td>" + cell + "</td>").join("") + "</tr>").join("") + "</table>";
  }
  return '<div class="cite" id="cite-' + hit.rank + '"><b>' + c.source + "</b> <i>" + (c.locator || "") + "</i> <span>" + score + "</span><p>" + c.text.slice(0, 200) + "</p></div>";
}
```

---

## 8. 过滤与能力门禁

### 8.1 AND / OR 语义

- **不同维度 AND**：`dynasty: ["唐"]` 且 `place: ["乐山"]`。
- **同一维度 OR**：`dynasty: ["唐","宋"]` 命中任一。

```js
// 唐代 + 乐山 + 地方志：三个维度同时满足
await fetch(base + "/api/v1/search", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    query: "大佛高度",
    top_k: 6,
    filters: {
      dynasty: ["唐"],          // AND
      place: ["乐山"],          // AND
      source_type: ["地方志"],  // AND
      modalities: ["text", "table"], // 同维度内 OR
    },
  }),
}).then((r) => r.json());
```

### 8.2 abilities 门禁

- 过滤集**非空**时：
  - **没有标注 `abilities` 的块 = 通用内容**，始终可检索；
  - **标注了 `abilities` 的块**必须与过滤集**有交集**才可检索。

```js
// 玩家已解锁「水利」「文物」：未标注 abilities 的通用资料 + 命中这两个能力的碎片可检索，
// 只标注了「诗词」的碎片被挡在检索之外，不会进入上下文。
const res = await fetch(base + "/api/v1/chat", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    question: "乐山大佛是谁主持开凿的？",
    stream: true,
    abilities: ["水利", "文物"],           // 便捷写法，等价于 filters.abilities
    filters: { dynasty: ["唐"] },           // 可与 abilities 叠加
  }),
});
```

> 前端可先用 `GET /api/v1/stats` 的 `facets.abilities` 渲染"已解锁能力"选择器，把结果放进 `abilities`。

---

## 9. 错误处理、重试与排障

### 9.1 分层处理

- **非流式**：HTTP 状态码与信封 `code` 一致，先看 `code` 再看 `message`。
- **流式**：HTTP 恒为 200。建连前错误（401/400）走普通 JSON；建连后错误走 `error` 事件，**务必处理**，否则前端会一直转圈。

| 类别 | code | 处理方式 |
| --- | --- | --- |
| 用户可修正 | 40001 | 直接展示 `message`。 |
| 认证 | 40100 | 提示配置 / 刷新 token，不要盲目重试。 |
| 冲突 | 40900 | 提示已有任务在跑，转为轮询现有任务。 |
| 限流 | 42900 | 退避重试。 |
| 上游 / 内部 | 50200、50000 | 退避重试 1~2 次；仍失败则展示 `request_id`。 |
| 依赖未就绪 | 50300 | 提示先入库 / 拉模型，附 `/health` 结果。 |

### 9.2 重试建议

- **幂等接口**（`GET`、`/search`、`/retrieve`、非流式 `/chat`）可安全重试；`/ingest`、`/assets`、`/cancel` 重试前先确认状态。
- 指数退避 + 抖动：第 n 次重试等待 `min(2^n * 500ms, 8s) + random(0~300ms)`，最多 2~3 次。

```js
async function withRetry(fn, tries = 3) {
  let lastErr;
  for (let n = 0; n < tries; n++) {
    try {
      return await fn();
    } catch (e) {
      lastErr = e;
      // 只对 42900 / 50200 / 50000 退避重试；40001 / 40100 / 40900 重试无意义
      if (![42900, 50200, 50000].includes(e.code)) throw e;
      const wait = Math.min(Math.pow(2, n) * 500, 8000) + Math.random() * 300;
      await new Promise((r) => setTimeout(r, wait));
    }
  }
  throw lastErr;
}
```

- **SSE 流中断**：没有断点续传协议，不要"续传"；若已收到 `sources` 但未收到 `done`，提示用户重试（重试会重新检索并重新生成）。
- 收到 `error` 事件后**不要自动重试**，多数是配置问题（Key / 模型）。

### 9.3 限流

- 服务端按 `server.rate_limit_per_min`（默认 120/分钟/IP）限流，超限 **HTTP 429 + `code=42900`**。
- 不要用短于 500ms 的间隔轮询入库任务；同一问题避免连点；多标签页共用一个会话时合并请求。

### 9.4 用 request_id 排障

每个响应（含错误响应与响应头 `X-Request-Id`）都带 `request_id`，SSE 的 `meta` 事件里也有：

```js
const res = await fetch(url, { headers: auth });
const rid = res.headers.get("X-Request-Id");
if (!res.ok) {
  const body = await res.json().catch(() => ({}));
  console.error("请求失败", { rid: body.request_id || rid, code: body.code, message: body.message });
}
```

排障步骤：① 前端捕获 `data.request_id` 或响应头；② 在服务端日志搜该 id 串起整条链路；③ 反馈问题时附 `request_id` + `code` + `message` + 时间点。跨域读取该头需服务端 CORS 暴露，已默认暴露。

### 9.5 traces 接口

`GET /api/v1/traces` 返回最近 100 条问答留痕（倒序），用于复现"当时检索到了什么、prompt 多长、模型回了什么"：

```js
const { data } = await fetch(base + "/api/v1/traces", { headers: auth }).then((r) => r.json());
const t = data.traces.find((x) => x.request_id === rid);
if (t) {
  console.log("问题", t.question, "→ 改写", t.rewritten_question);
  console.log("命中", t.hits.map((h) => h.source + " " + h.score.toFixed(3) + " (" + h.from + ")"));
  console.log("prompt 字符数", t.prompt_chars, "答案字符数", t.answer_chars, "TTFT", t.ttft_ms, "ms");
  if (t.error) console.warn("该次请求出错：", t.error);
}
```

- 留痕只在**内存**中保留最近 100 条，重启即清空，不要当作持久审计日志。

