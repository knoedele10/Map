// Command shuji 是「蜀迹 · 成都城脉地图」蓉宝知识库问答的自包含发行版。
//
// 单个 exe 内嵌了 Web 前端、蜀地语料、预建索引与配置；首次运行解包到
// %LOCALAPPDATA%\ShuJiRAG，随后启动本地服务与无地址栏的独立游戏窗口。
//
// 用法：
//
//	蜀迹-蓉宝知识库.exe               # 解包（如需）→ 起服务 → 开游戏窗口
//	蜀迹-蓉宝知识库.exe -reset        # 强制重新解包前端与语料（保留已有索引）
//	蜀迹-蓉宝知识库.exe -no-browser   # 不自动打开浏览器
//	蜀迹-蓉宝知识库.exe -dir D:\x     # 指定解包目录
//	蜀迹-蓉宝知识库.exe -ingest       # 启动前先跑一次增量入库
package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ragkb/internal/app"
)

//go:embed all:assets
var bundle embed.FS

// bundleVersion 由构建脚本通过 -ldflags "-X main.bundleVersion=..." 注入。
var bundleVersion = "dev"

const (
	embedRoot  = "assets"      // 内嵌资源根
	serverDir  = "server"      // 解包后作为工作目录
	indexDir   = "server/data" // 预建索引；用户重新入库后不能被覆盖
	versionTag = ".bundle-version"
)

func main() {
	var (
		reset     = flag.Bool("reset", false, "强制重新解包前端与语料（保留已有索引）")
		noBrowser = flag.Bool("no-browser", false, "仅启动服务，不打开游戏窗口（调试用）")
		dirFlag   = flag.String("dir", "", "解包目录（默认 %LOCALAPPDATA%\\ShuJiRAG）")
		doIngest  = flag.Bool("ingest", false, "启动服务前先执行一次增量入库")
		rebuild   = flag.Bool("rebuild", false, "配合 -ingest：清空索引后全量重建")
	)
	flag.Parse()

	if err := run(*dirFlag, *reset, *noBrowser, *doIngest, *rebuild); err != nil {
		fmt.Fprintln(os.Stderr, "\n启动失败: "+err.Error())
		showStartupError(err)
		os.Exit(1)
	}
}

func run(dirFlag string, reset, noBrowser, doIngest, rebuild bool) error {
	root, err := resolveRoot(dirFlag)
	if err != nil {
		return err
	}

	fmt.Println("==========================================================")
	fmt.Println("  蜀迹 · 成都城脉地图  ---  蓉宝知识库问答")
	fmt.Printf("  版本 %s\n", bundleVersion)
	fmt.Println("==========================================================")
	fmt.Printf("  数据目录: %s\n\n", root)

	if err := extract(root, reset); err != nil {
		return fmt.Errorf("解包内嵌资源: %w", err)
	}
	if err := os.Chdir(filepath.Join(root, serverDir)); err != nil {
		return fmt.Errorf("切换工作目录: %w", err)
	}

	// 独立端口可避免和开发服务器、其他本地网页冲突。
	addr := "127.0.0.1:38180"
	pageURL := "http://" + addr + "/static/shuji/"

	// 已经有一个实例在跑：不重复启动，直接打开独立游戏窗口。
	if healthOK(addr) {
		fmt.Println("[i] 检测到游戏服务已在运行，直接打开窗口。")
		if !noBrowser {
			cmd, err := openGameWindow(pageURL, root)
			if err != nil {
				openBrowser(pageURL)
				return nil
			}
			// Edge 通常会把启动请求交给已经运行的浏览器进程，当前命令随即
			// 退出；这里只负责回收子进程，不让第二个启动器参与服务生命周期。
			go func() { _ = cmd.Wait() }()
			return nil
		}
		fmt.Println("\n    调试服务已在运行，按回车键退出本启动器。")
		waitEnter()
		return nil
	}

	go checkOllama()

	if !noBrowser {
		go func() {
			if waitHealthy(addr, 40*time.Second) {
				cmd, err := openGameWindow(pageURL, root)
				if err != nil {
					fmt.Println("[警告] 无法启动独立窗口，改用系统浏览器:", err)
					openBrowser(pageURL)
					return
				}
				fmt.Printf("\n[OK] 游戏窗口已打开: %s\n", pageURL)
				// Edge 可能把窗口交给既有进程并让当前命令提前结束，不能据此
				// 判断窗口已关闭；服务生命周期由页面心跳决定。
				_ = cmd.Wait()
			}
		}()
	}

	fmt.Printf("[..] 启动服务端 %s\n", addr)
	fmt.Println("----------------------------------------------------------")
	fmt.Printf("  页面:  %s\n", pageURL)
	fmt.Printf("  健康:  http://%s/api/v1/health\n", addr)
	fmt.Println("----------------------------------------------------------")
	fmt.Println()
	fmt.Println("  从序章开始探索，按时间顺序找回五块记忆碎片。")
	fmt.Println("  点击地图地标时，蓉宝会带路；解锁记忆后即可向他提问。")
	fmt.Println("  关闭游戏窗口即可停止后台服务。")
	fmt.Println()

	return app.Run(app.Options{
		Ingest:           doIngest,
		Rebuild:          rebuild,
		ServerAddr:       addr,
		DesktopHeartbeat: !noBrowser,
	})
}

// resolveRoot 决定解包目录：优先 -dir，其次 %LOCALAPPDATA%ShuJiRAG。
func resolveRoot(flagVal string) (string, error) {
	if flagVal != "" {
		return filepath.Abs(flagVal)
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		d, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("找不到可写的数据目录，请用 -dir 指定")
		}
		base = d
	}
	return filepath.Join(base, "ShuJiRAG"), nil
}

// extract 把内嵌资源解包到 root。
//
// 规则：
//   - 目标不存在        -> 写入
//   - 预建索引已存在    -> 跳过（用户可能重新入库过，不能覆盖）
//   - 其余文件          -> 仅当版本变化或 -reset 时刷新（保留用户对配置/语料的改动）
func extract(root string, force bool) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	fresh := force || readVersion(root) != bundleVersion
	if !fresh {
		return nil
	}

	written := 0
	err := fs.WalkDir(bundle, embedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(embedRoot, filepath.FromSlash(p))
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// 构建时为避免 go:embed 将 assets/server 识别为嵌套 Go 模块，
		// go.mod 临时加了 .bundle 后缀；释放时恢复正式文件名。
		if filepath.ToSlash(rel) == "server/go.mod.bundle" {
			rel = filepath.FromSlash("server/go.mod")
		}
		target := filepath.Join(root, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if fileExists(target) {
			if strings.HasPrefix(filepath.ToSlash(rel), indexDir) {
				return nil // 已有索引：保留
			}
			if !force && readVersion(root) == bundleVersion && fileExists(filepath.Join(root, versionTag)) {
				return nil
			}
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		written++
		return nil
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, versionTag), []byte(bundleVersion), 0o644); err != nil {
		return err
	}
	fmt.Printf("[OK] 已解包 %d 个文件（版本 %s）\n\n", written, bundleVersion)
	return nil
}

func readVersion(root string) string {
	b, err := os.ReadFile(filepath.Join(root, versionTag))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func copyFile(embedded, target string) error {
	in, err := bundle.Open(embedded)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func healthOK(addr string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/v1/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == 200
}

func waitHealthy(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if healthOK(addr) {
			return true
		}
		time.Sleep(400 * time.Millisecond)
	}
	return false
}

func checkOllama() {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:11434/api/tags")
	if err != nil {
		fmt.Println("[警告] 连不上 Ollama 127.0.0.1:11434，检索会失败。")
		fmt.Println("       请另开一个窗口执行:  ollama serve")
		fmt.Println()
		return
	}
	resp.Body.Close()
	fmt.Println("[OK] Ollama 在线")
}

// openGameWindow 使用 Windows 自带 Edge 的应用模式打开页面：没有标签栏、地址栏和
// 浏览器菜单，看起来就是一个独立桌面游戏窗口。服务生命周期由页面心跳决定，因此
// 使用系统现有 Edge 配置即可，避免专用 profile 被残留进程锁住后无法再次启动。
func openGameWindow(url, _ string) (*exec.Cmd, error) {
	edge, err := findEdge()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(edge,
		"--app="+url,
		"--new-window",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-mode",
		"--disable-extensions",
		"--disable-features=msEdgeSidebarV2,msEdgeShoppingAssistant,msEdgeCollections",
		"--start-maximized",
	)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动游戏窗口: %w", err)
	}
	return cmd, nil
}

func findEdge() (string, error) {
	if p, err := exec.LookPath("msedge.exe"); err == nil {
		return p, nil
	}
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "Application", "msedge.exe"),
	}
	for _, p := range candidates {
		if p != "" && fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("未找到 Microsoft Edge（Windows 10/11 通常已自带）")
}

func openBrowser(url string) {
	// 极少数没有 Edge 的环境才退回普通浏览器。
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start(); err != nil {
		_ = exec.Command("cmd", "/c", "start", "", url).Start()
	}
}

func showStartupError(err error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command",
		"Add-Type -AssemblyName PresentationFramework; [System.Windows.MessageBox]::Show($env:SHUJI_STARTUP_ERROR,'蜀迹 Demo') | Out-Null")
	cmd.Env = append(os.Environ(), "SHUJI_STARTUP_ERROR="+err.Error())
	_ = cmd.Run()
}

func waitEnter() {
	fmt.Println("\n按回车键退出…")
	fmt.Scanln()
}
