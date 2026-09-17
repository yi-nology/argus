package ocrshim

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"git.example.com/enjoydream/agentkit/logredact"

	"argus/internal/plugin"
)

const version = "0.1.0"

// 缺省硬上限（Config 零值时生效）：单次 OCR 10 分钟、unshallow 5 分钟。
const (
	defaultTimeout          = 10 * time.Minute
	defaultUnshallowTimeout = 5 * time.Minute
)

// Config shim 运行配置（cmd/ocrshim 从 flags/env 装配）。
type Config struct {
	// Listen MCP streamable HTTP 监听地址（如 :8090）。
	Listen string
	// OcrBin OCR 可执行文件绝对路径（供应链 §8：安装期 pin + checksum，运行期只执行此路径）。
	OcrBin string
	// OcrEnv 按名从 shim 进程透传给 OCR 子进程的环境变量白名单（LLM 凭证在此声明）。
	OcrEnv []string
	// Timeout 单次 OCR 执行硬上限（请求 ctx 取消先行生效）。
	Timeout time.Duration
	// UnshallowTimeout 浅克隆补全历史的独立上限。
	UnshallowTimeout time.Duration
	// MaxStdout OCR stdout 采集上限（0 = acpx 缺省 8MB）。
	MaxStdout int
	// HistoryDir OCR 原始输出留档目录（空 = 不留档）。人工回溯用：argus DB 只存
	// 合并后 findings，本目录落盘 OCR 原始 stdout JSON（每次审查一个文件）。
	HistoryDir string
	// AuthToken 共享密钥（空 = 不鉴权，保持单机/纯 loopback 部署的兼容）。
	// compose 形态 sidecar 必须绑非 loopback（argus 经 docker 网络访问），
	// 网络隔离不足以限定"只有 argus"——配置后 /mcp 要求 Authorization: Bearer。
	AuthToken string
}

// Handler 装配 MCP streamable HTTP server（/mcp）+ 就绪探针（/healthz）。
func Handler(cfg Config) http.Handler {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.UnshallowTimeout <= 0 {
		cfg.UnshallowTimeout = defaultUnshallowTimeout
	}
	s := server.NewMCPServer("ocrshim", version)
	s.AddTool(mcp.NewTool("review",
		mcp.WithDescription("对工作副本执行 OCR（open-code-review）审查，返回 argus findings JSON")),
		handleReview(cfg))
	mux := http.NewServeMux()
	var mcpHandler http.Handler = server.NewStreamableHTTPServer(s)
	if cfg.AuthToken != "" {
		mcpHandler = authBearer(cfg.AuthToken, mcpHandler)
	}
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		// OCR 二进制缺失不挡探针（部署时序：shim 可先于 OCR 安装就绪），只标注
		if _, err := os.Stat(cfg.OcrBin); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("ocr binary missing: " + cfg.OcrBin))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// authBearer 共享密钥门：Authorization: Bearer <token> 不匹配一律 401。
// /healthz 刻意留在门外（探针不携带凭证）。非常量时间比较防时序侧信道。
func authBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		ok := false
		if strings.HasPrefix(auth, prefix) {
			ok = subtle.ConstantTimeCompare([]byte(auth[len(prefix):]), []byte(token)) == 1
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ocrshim"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleReview 工具入口：payload（JSON 字符串）→ 校验 → unshallow → OCR → 归一化。
// 业务失败走 ToolResultError（argus 侧转 Invoke error，Merger 部分失败容忍）。
func handleReview(cfg Config) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw, _ := req.GetArguments()["payload"].(string)
		var p shimPayload
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("payload 非法 JSON: %v", err)), nil
		}
		switch {
		case p.WorkcopyDir == "":
			return mcp.NewToolResultError("payload 缺少 workcopy_dir（插件需声明 needs: [working-copy]）"), nil
		case p.BaseBranch == "":
			return mcp.NewToolResultError("payload 缺少 base_branch"), nil
		}
		if st, err := os.Stat(p.WorkcopyDir); err != nil || !st.IsDir() {
			return mcp.NewToolResultError("workcopy_dir 不存在: " + p.WorkcopyDir), nil
		}
		if err := unshallowIfNeeded(ctx, p.WorkcopyDir, cfg.UnshallowTimeout); err != nil {
			// 错误串回传 argus 前统一脱敏（纵深：run.go 已在源头处理 stderr，
			// 这里兜住未来新增的错误路径——此文本会进 argus 日志与报告降级注明）
			return mcp.NewToolResultError(logredact.Redact(err.Error())), nil
		}
		stdout, err := runOCR(ctx, cfg, p.WorkcopyDir, p)
		if err != nil {
			return mcp.NewToolResultError(logredact.Redact(err.Error())), nil
		}
		findings, err := normalizeFindings(stdout)
		if err != nil {
			return mcp.NewToolResultError("OCR 输出解析失败: " + logredact.Redact(err.Error())), nil
		}
		if cfg.HistoryDir != "" {
			writeHistory(cfg.HistoryDir, p, stdout, findings) // 失败仅记日志，不影响审查
		}
		if findings == nil {
			findings = []plugin.Finding{} // 零意见输出空数组而非 null（契约稳定）
		}
		b, err := json.Marshal(struct {
			Findings []plugin.Finding `json:"findings"`
		}{Findings: findings})
		if err != nil {
			return mcp.NewToolResultError("findings 序列化失败: " + err.Error()), nil
		}
		return mcp.NewToolResultText(string(b)), nil
	}
}

// shimPayload argus remote/mcp 适配器下发的任务 payload（adapter_remote.remotePayload
// 的消费侧镜像：只取 shim 用到的字段，其余忽略）。
type shimPayload struct {
	Platform    string `json:"platform"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	Number      string `json:"number"`
	Title       string `json:"title"`
	BaseBranch  string `json:"base_branch"`
	HeadSHA     string `json:"head_sha"`
	WorkcopyDir string `json:"workcopy_dir"`
}

// writeHistory OCR 原始 stdout（嵌入为合法 JSON：原文本就是 JSON，异常时降级为
// 字符串字段）+ 归一化 findings 落盘。文件名纳秒时间戳防同秒互覆。
func writeHistory(dir string, p shimPayload, rawStdout string, findings []plugin.Finding) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[warn] OCR 历史目录创建失败: %v", err)
		return
	}
	var raw json.RawMessage = json.RawMessage(rawStdout)
	if !json.Valid(raw) {
		b, err := json.Marshal(rawStdout)
		if err != nil {
			log.Printf("[warn] OCR 历史序列化失败: %v", err)
			return
		}
		raw = b
	}
	// 标识消毒：GitLab 嵌套组 owner 含 "/"、number 无格式校验——原样拼文件名
	// 会导致留档写入失败甚至目录穿越（v3.9.1，与 ocrdeleg 同款）。
	name := fmt.Sprintf("%s-%s.json",
		time.Now().UTC().Format("20060102-150405.000000000"),
		plugin.SanitizeFileStem(p.Owner+"-"+p.Repo+"-PR"+p.Number))
	b, err := json.MarshalIndent(map[string]any{
		"pr":       p.Platform + "/" + p.Owner + "/" + p.Repo + "#" + p.Number + "@" + p.HeadSHA,
		"ocr_raw":  raw,
		"findings": findings,
	}, "", "  ")
	if err != nil {
		log.Printf("[warn] OCR 历史序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		log.Printf("[warn] OCR 历史写入失败: %v", err)
	}
}
