// Command server 启动 RAG 知识库 HTTP 服务（开发用）。
//
// 用法：
//
//	go run ./cmd/server                     # 用默认配置（检索走本地 Ollama，生成走 API Key）
//	go run ./cmd/server -config configs/config.yaml
//	go run ./cmd/server -ingest             # 启动前先跑一次增量入库
//
// 需要单文件发行版请用 cmd/shuji。
package main

import (
	"flag"
	"fmt"
	"os"

	"ragkb/internal/app"
)

func main() {
	configPath := flag.String("config", "", "配置文件路径（默认 configs/config.yaml，不存在则用默认值）")
	doIngest := flag.Bool("ingest", false, "启动服务前先执行一次增量入库")
	rebuild := flag.Bool("rebuild", false, "配合 -ingest：清空索引后全量重建")
	flag.Parse()

	if err := app.Run(app.Options{ConfigPath: *configPath, Ingest: *doIngest, Rebuild: *rebuild, ControlMarker: os.Getenv("RAG_CONTROL_MARKER")}); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败: "+err.Error())
		os.Exit(1)
	}
}
