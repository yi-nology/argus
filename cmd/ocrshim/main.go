// Command ocrshim 把 alibaba/open-code-review（OCR）CLI 包装成 MCP server
// （streamable HTTP），供 argus 以 remote/mcp 插件形态调用。
//
// 部署形态：argus 同主机 sidecar（共享工作副本目录卷）。argus 备好工作副本
// 后经 payload.workcopy_dir 下发目录，shim 在其中执行 OCR 并回 findings JSON；
// OCR 的 LLM 凭证只配在 shim 侧（环境白名单透传），不再进入 argus 进程。
//
// 用法：
//
//	ocrshim -listen :8090 -ocr-bin /opt/argus/bin/ocr \
//	  -ocr-env OPENAI_API_KEY,OPENAI_BASE_URL
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	oekitconfig "github.com/oekit/core/config"

	"argus/internal/ocrshim"
)

func main() {
	// env 缺省读取收编 oekit/core/config 单源（v3.164.0；与 argus 主进程
	// config_env.go 同一读取面——envOr 手写助手退役）。
	envOr := oekitconfig.GetStringWithDefault
	listen := flag.String("listen", envOr("OCRSHIM_LISTEN", ":8090"), "MCP 监听地址")
	ocrBin := flag.String("ocr-bin", envOr("OCRSHIM_OCR_BIN", "/opt/argus/bin/ocr"),
		"OCR 可执行文件路径（版本 pin + checksum 由 scripts/install-ocr.sh 负责）")
	ocrEnv := flag.String("ocr-env", envOr("OCRSHIM_OCR_ENV", "OPENAI_API_KEY,OPENAI_BASE_URL"),
		"按名透传给 OCR 子进程的环境变量白名单（逗号分隔；其余一律不继承）")
	timeout := flag.Duration("timeout", 10*time.Minute, "单次 OCR 执行硬上限")
	unshallowTimeout := flag.Duration("unshallow-timeout", 5*time.Minute, "浅克隆补全历史（git fetch --unshallow）上限")
	maxStdout := flag.Int("max-stdout", 0, "OCR stdout 采集上限字节（0 = 缺省 8MB）")
	historyDir := flag.String("history-dir", envOr("OCRSHIM_HISTORY_DIR", ""),
		"OCR 原始输出留档目录（空 = 不留档；人工回溯用）")
	authToken := flag.String("auth-token", envOr("OCRSHIM_TOKEN", ""),
		"共享密钥（空 = 不鉴权；设置后 /mcp 要求 Authorization: Bearer <token>，与 argus 侧 auth_token_env 配对）")
	flag.Parse()

	cfg := ocrshim.Config{
		OcrBin:           *ocrBin,
		OcrEnv:           strings.Split(*ocrEnv, ","),
		Timeout:          *timeout,
		UnshallowTimeout: *unshallowTimeout,
		MaxStdout:        *maxStdout,
		HistoryDir:       *historyDir,
		AuthToken:        *authToken,
	}
	if _, err := os.Stat(cfg.OcrBin); err != nil {
		// 不挡启动：healthz 会持续上报 missing，部署编排可据此告警
		log.Printf("[warn] OCR 二进制不可访问: %s（%v）", cfg.OcrBin, err)
	}
	log.Printf("ocrshim v0.1.0 listening on %s（ocr=%s env=[%s] auth=%s）",
		*listen, cfg.OcrBin, strings.Join(cfg.OcrEnv, ","),
		map[bool]string{true: "enabled", false: "disabled"}[cfg.AuthToken != ""])
	srv := &http.Server{
		Addr:    *listen,
		Handler: ocrshim.Handler(cfg),
		// 慢连接资源边界：只限头部读取与空闲连接——不设 Read/WriteTimeout，
		// MCP streamable 响应是长连接流（SSE），工具调用时长由请求 ctx 超时约束
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "ocrshim:", err)
		os.Exit(1)
	}
}
