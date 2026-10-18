package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (s *Server) restartService(c *gin.Context) {
	if s.OnRestart == nil {
		Fail(c, CodeNotReady, "当前启动方式不支持网页重启，请通过启动项目.cmd 启动服务")
		return
	}
	if running := s.Jobs.Running(); running != nil {
		Fail(c, CodeConflict, "入库任务正在运行，请等任务完成后再重启")
		return
	}
	if err := s.OnRestart(); err != nil {
		Fail(c, CodeInternal, "无法安排服务重启: "+err.Error())
		return
	}
	OK(c, map[string]any{"restarting": true})
}

func (s *Server) stopService(c *gin.Context) {
	if s.OnStop == nil {
		Fail(c, CodeNotReady, "当前启动方式不支持网页停止，请通过启动项目.cmd 启动服务")
		return
	}
	if running := s.Jobs.Running(); running != nil {
		Fail(c, CodeConflict, "入库任务正在运行，请等任务完成后再停止")
		return
	}
	if err := s.OnStop(); err != nil {
		Fail(c, CodeInternal, "无法安排服务停止: "+err.Error())
		return
	}
	OK(c, map[string]any{"stopping": true})
}

type modelSettings struct {
	PendingRestart bool   `json:"pending_restart"`
	APIMode        string `json:"api_mode"`
	Chat           struct {
		Provider         string `json:"provider"`
		BaseURL          string `json:"base_url"`
		Model            string `json:"model"`
		APIKeyConfigured bool   `json:"api_key_configured"`
	} `json:"chat"`
	Embedding struct {
		Provider         string `json:"provider"`
		BaseURL          string `json:"base_url"`
		Model            string `json:"model"`
		Dim              int    `json:"dim"`
		APIKeyConfigured bool   `json:"api_key_configured"`
	} `json:"embedding"`
	Vision struct {
		Enabled          bool   `json:"enabled"`
		Provider         string `json:"provider"`
		BaseURL          string `json:"base_url"`
		Model            string `json:"model"`
		APIKeyConfigured bool   `json:"api_key_configured"`
		RetrievalURL     string `json:"retrieval_url"`
	} `json:"vision"`
}

func (s *Server) getModelSettings(c *gin.Context) {
	var out modelSettings
	out.Chat.Provider, out.Chat.BaseURL, out.Chat.Model = s.Cfg.Chat.Provider, s.Cfg.Chat.BaseURL, s.Cfg.Chat.Model
	root := filepath.Dir(s.Cfg.Path)
	if s.Cfg.Path == "" {
		root, _ = os.Getwd()
	}
	chatFile := readEnvFile(filepath.Join(root, ".env.chat.local"))
	out.PendingRestart = overrideSetting(chatFile, "RAG_CHAT_PROVIDER", &out.Chat.Provider) || out.PendingRestart
	out.PendingRestart = overrideSetting(chatFile, "RAG_CHAT_BASE_URL", &out.Chat.BaseURL) || out.PendingRestart
	out.PendingRestart = overrideSetting(chatFile, "RAG_CHAT_MODEL", &out.Chat.Model) || out.PendingRestart
	if value, ok := chatFile["RAG_CHAT_API_KEY"]; ok && value != s.Cfg.Chat.APIKey {
		out.PendingRestart = true
	}
	out.Chat.APIKeyConfigured = configuredSecret(s.Cfg.Chat.APIKey, "RAG_CHAT_API_KEY", chatFile)
	out.Embedding.Provider, out.Embedding.BaseURL, out.Embedding.Model = s.Cfg.Embedding.Provider, s.Cfg.Embedding.BaseURL, s.Cfg.Embedding.Model
	out.Embedding.Dim = s.Cfg.Embedding.Dim
	embedFile := readEnvFile(filepath.Join(root, ".env.embedding.local"))
	out.PendingRestart = overrideSetting(embedFile, "RAG_EMBEDDING_PROVIDER", &out.Embedding.Provider) || out.PendingRestart
	out.PendingRestart = overrideSetting(embedFile, "RAG_EMBEDDING_BASE_URL", &out.Embedding.BaseURL) || out.PendingRestart
	out.PendingRestart = overrideSetting(embedFile, "RAG_EMBEDDING_MODEL", &out.Embedding.Model) || out.PendingRestart
	if value, ok := embedFile["RAG_EMBEDDING_DIM"]; ok {
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			if out.Embedding.Dim != n {
				out.PendingRestart = true
			}
			out.Embedding.Dim = n
		}
	}
	if value, ok := embedFile["RAG_EMBEDDING_API_KEY"]; ok && value != s.Cfg.Embedding.APIKey {
		out.PendingRestart = true
	}
	out.Embedding.APIKeyConfigured = configuredSecret(s.Cfg.Embedding.APIKey, "RAG_EMBEDDING_API_KEY", embedFile)
	out.Vision.Enabled = s.Cfg.Vision.Enabled
	out.Vision.Provider, out.Vision.BaseURL, out.Vision.Model = s.Cfg.Vision.Provider, s.Cfg.Vision.BaseURL, s.Cfg.Vision.Model
	out.Vision.RetrievalURL = s.Cfg.Recognize.RetrievalURL
	visionFile := readEnvFile(filepath.Join(root, ".env.vision.local"))
	out.APIMode = "three"
	if shared, ok := visionFile["RAG_VISION_SHARED_CHAT"]; ok {
		if enabled, err := strconv.ParseBool(shared); err == nil {
			if enabled {
				out.APIMode = "two"
			}
			if enabled != s.Cfg.Vision.SharedChat {
				out.PendingRestart = true
			}
		}
	} else if s.Cfg.Vision.SharedChat {
		out.APIMode = "two"
	}
	if value, ok := visionFile["RAG_VISION_ENABLED"]; ok {
		if enabled, err := strconv.ParseBool(value); err == nil {
			if out.Vision.Enabled != enabled {
				out.PendingRestart = true
			}
			out.Vision.Enabled = enabled
		}
	}
	out.PendingRestart = overrideSetting(visionFile, "RAG_VISION_PROVIDER", &out.Vision.Provider) || out.PendingRestart
	out.PendingRestart = overrideSetting(visionFile, "RAG_VISION_BASE_URL", &out.Vision.BaseURL) || out.PendingRestart
	out.PendingRestart = overrideSetting(visionFile, "RAG_VISION_MODEL", &out.Vision.Model) || out.PendingRestart
	if value, ok := visionFile["RAG_RECOGNIZE_RETRIEVAL_URL"]; ok {
		if out.Vision.RetrievalURL != value {
			out.PendingRestart = true
		}
		out.Vision.RetrievalURL = value
	}
	if value, ok := visionFile["RAG_VISION_API_KEY"]; ok && value != s.Cfg.Vision.APIKey {
		out.PendingRestart = true
	}
	out.Vision.APIKeyConfigured = configuredSecret(s.Cfg.Vision.APIKey, "RAG_VISION_API_KEY", visionFile)
	OK(c, out)
}

func overrideSetting(file map[string]string, key string, target *string) bool {
	value, ok := file[key]
	if !ok {
		return false
	}
	pending := *target != value
	*target = value
	return pending
}

type saveModelSettingsRequest struct {
	APIMode string `json:"api_mode"`
	Chat    struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		APIKey   string `json:"api_key"`
		ClearKey bool   `json:"clear_api_key"`
	} `json:"chat"`
	Embedding struct {
		Provider string `json:"provider"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		Dim      int    `json:"dim"`
		APIKey   string `json:"api_key"`
		ClearKey bool   `json:"clear_api_key"`
	} `json:"embedding"`
	Vision struct {
		Enabled      bool   `json:"enabled"`
		Provider     string `json:"provider"`
		BaseURL      string `json:"base_url"`
		Model        string `json:"model"`
		APIKey       string `json:"api_key"`
		ClearKey     bool   `json:"clear_api_key"`
		RetrievalURL string `json:"retrieval_url"`
	} `json:"vision"`
}

func (s *Server) saveModelSettings(c *gin.Context) {
	var req saveModelSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, 40001, "配置格式错误: "+err.Error())
		return
	}
	if req.APIMode == "" {
		req.APIMode = "three"
	}
	if req.APIMode != "two" && req.APIMode != "three" {
		Fail(c, 40001, "api_mode 只支持 two 或 three")
		return
	}
	if req.APIMode == "two" {
		req.Vision.Enabled = true
		req.Vision.Provider, req.Vision.BaseURL, req.Vision.Model = req.Chat.Provider, req.Chat.BaseURL, req.Chat.Model
		req.Vision.APIKey, req.Vision.ClearKey = req.Chat.APIKey, req.Chat.ClearKey
	}
	for label, values := range map[string][]string{
		"Chat":      {req.Chat.Provider, req.Chat.BaseURL, req.Chat.Model, req.Chat.APIKey},
		"Embedding": {req.Embedding.Provider, req.Embedding.BaseURL, req.Embedding.Model, req.Embedding.APIKey},
		"Vision":    {req.Vision.Provider, req.Vision.BaseURL, req.Vision.Model, req.Vision.APIKey, req.Vision.RetrievalURL},
	} {
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n\x00") {
				Fail(c, 40001, label+" 配置不能包含换行或空字符")
				return
			}
		}
	}
	if !validProvider(req.Chat.Provider) || !validProvider(req.Embedding.Provider) || !validProvider(req.Vision.Provider) {
		Fail(c, 40001, "服务商只支持 ollama 或 openai 兼容 API")
		return
	}
	if strings.TrimSpace(req.Chat.Model) == "" || strings.TrimSpace(req.Embedding.Model) == "" || req.Embedding.Dim < 1 {
		Fail(c, 40001, "Chat 和检索模型名不能为空，Embedding 维度必须为正数")
		return
	}
	if req.Chat.Provider == "openai" && strings.TrimSpace(req.Chat.BaseURL) == "" || req.Embedding.Provider == "openai" && strings.TrimSpace(req.Embedding.BaseURL) == "" || req.Vision.Enabled && req.Vision.Provider == "openai" && strings.TrimSpace(req.Vision.BaseURL) == "" {
		Fail(c, 40001, "使用 OpenAI 兼容服务时必须填写 Base URL")
		return
	}

	root := filepath.Dir(s.Cfg.Path)
	if s.Cfg.Path == "" {
		root, _ = os.Getwd()
	}
	files := []struct {
		name               string
		values             map[string]string
		secret, configured string
	}{
		{".env.chat.local", map[string]string{"RAG_CHAT_PROVIDER": req.Chat.Provider, "RAG_CHAT_BASE_URL": req.Chat.BaseURL, "RAG_CHAT_MODEL": req.Chat.Model}, "RAG_CHAT_API_KEY", req.Chat.APIKey},
		{".env.embedding.local", map[string]string{"RAG_EMBEDDING_PROVIDER": req.Embedding.Provider, "RAG_EMBEDDING_BASE_URL": req.Embedding.BaseURL, "RAG_EMBEDDING_MODEL": req.Embedding.Model, "RAG_EMBEDDING_DIM": strconv.Itoa(req.Embedding.Dim)}, "RAG_EMBEDDING_API_KEY", req.Embedding.APIKey},
		{".env.vision.local", map[string]string{"RAG_VISION_ENABLED": strconv.FormatBool(req.Vision.Enabled), "RAG_VISION_SHARED_CHAT": strconv.FormatBool(req.APIMode == "two"), "RAG_VISION_PROVIDER": req.Vision.Provider, "RAG_VISION_BASE_URL": req.Vision.BaseURL, "RAG_VISION_MODEL": req.Vision.Model, "RAG_RECOGNIZE_RETRIEVAL_URL": req.Vision.RetrievalURL}, "RAG_VISION_API_KEY", req.Vision.APIKey},
	}
	for i := range files {
		if files[i].configured == "" && !clearRequested(&req, i) {
			fallback := []string{s.Cfg.Chat.APIKey, s.Cfg.Embedding.APIKey, s.Cfg.Vision.APIKey}[i]
			files[i].configured = effectiveSecret(files[i].secret, filepath.Join(root, files[i].name), fallback)
		}
		if clearRequested(&req, i) {
			files[i].configured = ""
		}
		files[i].values[files[i].secret] = files[i].configured
	}
	if req.APIMode == "two" {
		delete(files[2].values, files[2].secret)
	}
	for _, file := range files {
		if err := writeEnvFile(filepath.Join(root, file.name), file.values); err != nil {
			Fail(c, 50000, "保存配置失败: "+err.Error())
			return
		}
	}
	OK(c, gin.H{"saved": true, "restart_required": true})
}

func validProvider(value string) bool { return value == "ollama" || value == "openai" }

func clearRequested(req *saveModelSettingsRequest, index int) bool {
	switch index {
	case 0:
		return req.Chat.ClearKey
	case 1:
		return req.Embedding.ClearKey
	default:
		return req.Vision.ClearKey
	}
}

func effectiveSecret(name, path, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	if value := readEnvFile(path)[name]; value != "" {
		return value
	}
	return fallback
}

func configuredSecret(current, name string, file map[string]string) bool {
	if value, ok := file[name]; ok {
		return value != ""
	}
	if value, ok := os.LookupEnv(name); ok {
		return value != ""
	}
	return current != ""
}

func readEnvFile(path string) map[string]string {
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, raw, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value, err := strconv.Unquote(strings.TrimSpace(raw))
		if err == nil {
			values[key] = value
		}
	}
	return values
}

func writeEnvFile(path string, values map[string]string) error {
	var body strings.Builder
	for _, key := range []string{"RAG_CHAT_PROVIDER", "RAG_CHAT_BASE_URL", "RAG_CHAT_MODEL", "RAG_CHAT_API_KEY", "RAG_EMBEDDING_PROVIDER", "RAG_EMBEDDING_BASE_URL", "RAG_EMBEDDING_MODEL", "RAG_EMBEDDING_DIM", "RAG_EMBEDDING_API_KEY", "RAG_VISION_ENABLED", "RAG_VISION_SHARED_CHAT", "RAG_VISION_PROVIDER", "RAG_VISION_BASE_URL", "RAG_VISION_MODEL", "RAG_VISION_API_KEY", "RAG_RECOGNIZE_RETRIEVAL_URL"} {
		if value, ok := values[key]; ok {
			fmt.Fprintf(&body, "%s=%s\n", key, strconv.Quote(value))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
