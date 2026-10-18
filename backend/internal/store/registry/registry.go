// Package registry 按配置构造检索后端。
//
// 单独成包的原因：cmd/server 与 cmd/ragctl 都要用同一套选择逻辑；
// 而 store 包不能 import 各后端实现（会形成循环），所以工厂放在这里。
package registry

import (
	"context"
	"fmt"

	"ragkb/internal/config"
	"ragkb/internal/store"
	"ragkb/internal/store/file"
	"ragkb/internal/store/pgvector"
)

// Open 根据 storage.backend 打开对应后端。
//
// 两个后端实现同一个 store.Backend 接口，RRF 融合在共享的 store.Hybrid 里，
// 所以换后端不改变检索语义，只有"找候选"这一步从内存换成数据库。
func Open(ctx context.Context, cfg *config.Config) (store.Backend, error) {
	switch cfg.Storage.Backend {
	case "", "file":
		st, err := file.Open(cfg.Storage.DataDir, cfg.Embedding.Dim, cfg.Embedding.Normalize)
		if err != nil {
			return nil, fmt.Errorf("打开文件索引: %w", err)
		}
		return st, nil
	case "pgvector":
		// 维度必须与 embedding.dim 一致：Open 会读 pg_attribute 提前校验，
		// 而不是等到第一次写入才失败。
		st, err := pgvector.Open(ctx, cfg.Storage.Pgvector.DSN, cfg.Embedding.Dim, cfg.Storage.Pgvector.MaxConns)
		if err != nil {
			return nil, fmt.Errorf("连接 pgvector: %w", err)
		}
		return st, nil
	default:
		return nil, fmt.Errorf("未知的 storage.backend: %q", cfg.Storage.Backend)
	}
}
