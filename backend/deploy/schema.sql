-- =============================================================================
-- ragkb · pgvector 存储后端 DDL（可选后端；默认后端是内置文件索引）
--
-- 用法：
--   psql "postgres://postgres:rag@127.0.0.1:55432/rag?sslmode=disable" -f deploy/schema.sql
--   集成测试 internal/store/pgvector/pgvector_test.go 也会先执行本文件再 Open。
--
-- 本文件可重复执行：所有对象都是 IF NOT EXISTS。
-- 依赖 pgvector 扩展（镜像 pgvector/pgvector:pg17 自带）。
-- =============================================================================

CREATE EXTENSION IF NOT EXISTS vector;

-- -----------------------------------------------------------------------------
-- 向量维度：vector(1024)
--
-- 1024 是本地嵌入模型 bge-m3 的输出维度。**换嵌入模型就要改这里**：
--   1) 改下面 rag_chunks.embedding 的 vector(N)；
--   2) Go 侧 Open(ctx, dsn, dim) 传入的 dim 必须与 N 一致 —— Open 会读
--      pg_attribute 拿到真实列类型来校验，不一致直接返回错误，
--      避免"写进去才发现维度不对"；
--   3) 已建好的向量索引必须 DROP 后重建（HNSW / ivfflat 都不支持改维度）。
-- DDL 里没有变量，所以维度只能写死在类型上；把维度集中在这一处、由 Go 侧校验，
-- 比用字符串模板拼 DDL 更不容易出错。
-- -----------------------------------------------------------------------------

-- -----------------------------------------------------------------------------
-- rag_docs：文档登记 + 结构化元数据 + fingerprint + status
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS rag_docs (
    id          text        PRIMARY KEY,                    -- 文档 id（上传时 asset_id 与它同值）
    title       text        NOT NULL DEFAULT '',
    source      text        NOT NULL DEFAULT '',            -- 来源相对路径，如 "四川地方志/乐山大佛.md"
    modality    text        NOT NULL DEFAULT 'text',        -- text | table | image
    size        bigint      NOT NULL DEFAULT 0,             -- 原文件字节数
    chunks      integer     NOT NULL DEFAULT 0,             -- 切块数
    assets      text[]      NOT NULL DEFAULT '{}',          -- 关联资产文件名
    status      text        NOT NULL DEFAULT 'pending',     -- indexed | failed | pending
    error       text        NOT NULL DEFAULT '',
    dynasty     text        NOT NULL DEFAULT '',
    place       text        NOT NULL DEFAULT '',
    topic       text        NOT NULL DEFAULT '',
    source_type text        NOT NULL DEFAULT '',
    credibility text        NOT NULL DEFAULT '',            -- high | medium | low
    abilities   text[]      NOT NULL DEFAULT '{}',
    tags        text[]      NOT NULL DEFAULT '{}',
    fingerprint text        NOT NULL DEFAULT '',            -- 文件内容 sha256 前缀，增量更新用
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS rag_docs_source_idx    ON rag_docs (source);
CREATE INDEX IF NOT EXISTS rag_docs_abilities_idx ON rag_docs USING gin (abilities);

-- -----------------------------------------------------------------------------
-- rag_chunks：检索的最小单元（块 + 向量 + 过滤维度）
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS rag_chunks (
    id          text        PRIMARY KEY,
    doc_id      text        NOT NULL DEFAULT '',
    source      text        NOT NULL DEFAULT '',
    idx         integer     NOT NULL DEFAULT 0,             -- 块在文档内的序号
    text        text        NOT NULL DEFAULT '',
    -- text_tokens：text 的**预分词**结果，空格连接。
    -- PostgreSQL 没有中文分词器，所以分词放在 Go 侧
    -- （internal/store/bm25.Tokenize：汉字 unigram + bigram，非汉字词整段小写）。
    -- 检索时对查询做同样的分词，再 to_tsvector('simple', text_tokens) @@ ...
    -- 这是**近似 BM25 的稀疏召回**；精确 BM25（IDF + 文档长度归一化 + k1/b）
    -- 由内置文件后端 internal/store/bm25 提供。
    text_tokens text        NOT NULL DEFAULT '',
    modality    text        NOT NULL DEFAULT 'text',        -- text | table | image
    asset_url   text        NOT NULL DEFAULT '',            -- 仅 modality='image' 时非空
    locator     text        NOT NULL DEFAULT '',            -- 人可读定位，如 "第 3 块"
    dynasty     text        NOT NULL DEFAULT '',
    place       text        NOT NULL DEFAULT '',
    topic       text        NOT NULL DEFAULT '',
    source_type text        NOT NULL DEFAULT '',
    credibility text        NOT NULL DEFAULT '',
    abilities   text[]      NOT NULL DEFAULT '{}',          -- 能力门禁维度，见 types.Filter.Match
    tags        text[]      NOT NULL DEFAULT '{}',
    hash        text        NOT NULL DEFAULT '',            -- 块内容指纹，幂等去重用
    meta        jsonb       NOT NULL DEFAULT '{}'::jsonb,   -- 零散属性：页码 / 工作表名 / 图注模型
    embedding   vector(1024),                               -- ← 换维度改这里（见文件头说明）
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- (doc_id, idx) 是块在文档里的自然键：唯一约束同时给 RemoveDoc 的
    -- "WHERE doc_id = $1" 提供前缀索引，所以不需要再单独建 doc_id 索引。
    CONSTRAINT rag_chunks_doc_idx_uniq UNIQUE (doc_id, idx)
);

-- 向量索引：HNSW + 余弦距离。
-- 选 HNSW 而不是 ivfflat：HNSW 对增量写入更友好（ivfflat 需要先有代表性数据、
-- lists 也要调参），且不需要训练。小表下查询计划可能直接走顺序扫描，结果一样正确。
-- 若 pgvector < 0.5（没有 hnsw），改成：
--   CREATE INDEX ... ON rag_chunks USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
CREATE INDEX IF NOT EXISTS rag_chunks_embedding_hnsw_idx
    ON rag_chunks USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

-- hash 唯一索引（幂等去重）。**必须是部分索引**：没标注 hash 的块 hash = ''，
-- 不加 WHERE 条件的话所有空 hash 的块会互相冲突，只能写进第一条。
CREATE UNIQUE INDEX IF NOT EXISTS rag_chunks_hash_uniq_idx
    ON rag_chunks (hash) WHERE hash <> '';

CREATE INDEX IF NOT EXISTS rag_chunks_source_idx ON rag_chunks (source);
CREATE INDEX IF NOT EXISTS rag_chunks_abilities_gin_idx ON rag_chunks USING gin (abilities);
CREATE INDEX IF NOT EXISTS rag_chunks_tags_gin_idx      ON rag_chunks USING gin (tags);
-- 稀疏检索用的表达式索引，必须和查询里的 to_tsvector('simple', text_tokens) 完全一致。
CREATE INDEX IF NOT EXISTS rag_chunks_tokens_gin_idx
    ON rag_chunks USING gin (to_tsvector('simple', text_tokens));

-- -----------------------------------------------------------------------------
-- 业务表骨架（"景点 / 碎片 / 对话"）。pgvector 存储后端不读写这几张表，
-- 它们是给业务侧用的：景点结构化数据、可解锁碎片、会话与消息。
-- 外键只在这些业务表内部使用；rag_chunks.doc_id **故意不建外键** ——
-- Add 是幂等热路径，不应该因为文档登记行还没写好而失败（Add 会顺手补一行
-- 骨架 doc，DeleteDoc/RemoveDoc 各自显式清理块，不依赖级联）。
-- -----------------------------------------------------------------------------

-- sites：结构化景点数据（对应 GET /api/v1/sites，以及生成前的只读工具）
CREATE TABLE IF NOT EXISTS sites (
    id             text PRIMARY KEY,                        -- 稳定 slug，如 'leshan-buddha'
    name           text NOT NULL DEFAULT '',
    aliases        text[] NOT NULL DEFAULT '{}',            -- 别名/旧称，按任一名称都能查到
    dynasty        text NOT NULL DEFAULT '',
    place          text NOT NULL DEFAULT '',
    topic          text NOT NULL DEFAULT '',
    ability        text NOT NULL DEFAULT '',                -- 关联玩法能力，进 Filter.Abilities
    summary        text NOT NULL DEFAULT '',                -- 一句话简介
    description    text NOT NULL DEFAULT '',                -- 正文
    facts          jsonb NOT NULL DEFAULT '{}'::jsonb,      -- 结构化字段：高度/年代/坐标…
    interpretation text NOT NULL DEFAULT '',                -- 演绎说明（与史实分开，产品红线）
    sources        jsonb NOT NULL DEFAULT '[]'::jsonb,      -- [{title,url,license,note}]
    lat            double precision,
    lng            double precision,
    tags           text[] NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sites_aliases_gin_idx ON sites USING gin (aliases);
CREATE INDEX IF NOT EXISTS sites_tags_gin_idx    ON sites USING gin (tags);

-- fragments：可解锁碎片。ability + unlocked 是玩法门禁的核心。
CREATE TABLE IF NOT EXISTS fragments (
    id          text PRIMARY KEY,
    site_id     text REFERENCES sites(id) ON DELETE SET NULL,  -- 归属景点；景点删除后碎片保留
    ability     text NOT NULL DEFAULT '',                      -- 该碎片属于哪个玩法能力
    title       text NOT NULL DEFAULT '',
    text        text NOT NULL DEFAULT '',
    asset_url   text NOT NULL DEFAULT '',
    -- unlocked 语义：**未解锁的碎片不进检索上下文**。
    -- 检索层的门禁由 types.Filter.Match 的 abilities 规则实现（过滤集非空时，
    -- 没标注 abilities 的块视为通用内容始终可检索，标注了的必须与过滤集有交集）；
    -- 这里的 unlocked 是业务侧状态，两者配合使用：解锁后才把该 ability 放进 Filter。
    unlocked    boolean NOT NULL DEFAULT false,
    unlock_hint text NOT NULL DEFAULT '',                      -- 解锁提示文案
    unlock_cost integer NOT NULL DEFAULT 0,                    -- 解锁消耗（0 = 免费/剧情解锁）
    rarity      text NOT NULL DEFAULT 'common',
    sort_order  integer NOT NULL DEFAULT 0,
    chunk_id    text,                                          -- 可选：指向 rag_chunks.id，便于回到原文
    tags        text[] NOT NULL DEFAULT '{}',
    meta        jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT fragments_rarity_chk CHECK (rarity IN ('common','rare','epic','legendary'))
);
CREATE INDEX IF NOT EXISTS fragments_site_idx            ON fragments (site_id);
CREATE INDEX IF NOT EXISTS fragments_ability_unlocked_idx ON fragments (ability, unlocked);
CREATE INDEX IF NOT EXISTS fragments_tags_gin_idx        ON fragments USING gin (tags);

-- conversations：会话（一轮探索的稳定标识）
CREATE TABLE IF NOT EXISTS conversations (
    id         text PRIMARY KEY,
    session_id text NOT NULL UNIQUE,                           -- 前端传的 session_id
    title      text NOT NULL DEFAULT '',
    user_id    text NOT NULL DEFAULT '',
    site_id    text REFERENCES sites(id) ON DELETE SET NULL,   -- 当前所在景点
    ability    text NOT NULL DEFAULT '',                       -- 当前解锁到的能力
    meta       jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS conversations_user_idx ON conversations (user_id);

-- messages：会话里的消息（user / assistant / system / tool）
CREATE TABLE IF NOT EXISTS messages (
    id              bigserial PRIMARY KEY,
    conversation_id text NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    role            text NOT NULL,
    content         text NOT NULL DEFAULT '',
    sources         jsonb NOT NULL DEFAULT '[]'::jsonb,        -- 该轮引用的 Hit 列表
    structured      jsonb,                                     -- answer_format=structured 时的结构化答案
    model           text NOT NULL DEFAULT '',
    tokens          integer NOT NULL DEFAULT 0,
    elapsed_ms      integer NOT NULL DEFAULT 0,
    error           text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT messages_role_chk CHECK (role IN ('user','assistant','system','tool'))
);
CREATE INDEX IF NOT EXISTS messages_conversation_idx ON messages (conversation_id, id);
