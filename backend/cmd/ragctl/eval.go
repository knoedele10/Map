package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"ragkb/internal/eval"
)

// cmdEval 跑黄金集评测，打真实 HTTP 服务（黑盒，不 import 内部实现）。
//
// 先启动服务：go run ./cmd/server -config configs/config.yaml
func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	golden := fs.String("golden", "eval/golden.yaml", "黄金集文件")
	base := fs.String("base", "http://127.0.0.1:8080", "服务地址")
	token := fs.String("token", "", "鉴权 token（服务开了 auth_token 时必填）")
	k := fs.Int("k", 0, "检索条数，0 表示用默认值")
	useChat := fs.Bool("chat", false, "同时测生成（会花 token）；默认只测检索")
	asJSON := fs.Bool("json", false, "输出 JSON 而不是人类可读报告")
	_ = fs.String("config", "", "（评测不需要，仅为参数一致性保留）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	gs, err := eval.LoadGolden(*golden)
	if err != nil {
		return err
	}
	client := eval.NewHTTPClient(strings.TrimRight(*base, "/"), *token)

	fmt.Printf("黄金集 %s：%d 条用例，目标服务 %s，模式 %s\n\n",
		*golden, len(gs.Cases), *base, map[bool]string{true: "检索+生成", false: "仅检索"}[*useChat])

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	report, err := eval.Run(ctx, client, gs, eval.Options{TopK: *k, UseChat: *useChat})
	if err != nil {
		return err
	}
	if *asJSON {
		out, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(out))
		return nil
	}
	fmt.Println(report.Text())
	return nil
}
