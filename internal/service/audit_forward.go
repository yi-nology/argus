// audit_forward.go —— 审计事件外送（v3.137.0，SIEM 对接旁路）：
// ARGUS_AUDIT_WEBHOOK_URL 非空时，audit.event 结构化日志在本地留痕之外再
// POST 一份 JSON 到外送端点（Bearer 可选 ARGUS_AUDIT_WEBHOOK_TOKEN）。
// 旁路语义与 sink 一致：失败只告警、绝不阻断审计主流程；队列满即丢弃并计数
// （宁可丢外送不可拖垮服务）。字段值已由 audit 包统一 logredact 打码——
// 外送载荷天然无明文密钥。
// v3.164.0：POST 收编 pkg/httpjson 单源（与 sink 同一条外送线）——顺修一处
// 实缺陷：旧手写路径不检查响应状态码，SIEM 端 5xx = 审计事件静默丢且无告警；
// 现非 2xx 计入 forward_failed 留痕（重试 1 次吸收瞬态）。
package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/oekit/logx"
	"github.com/oekit/pkg/httpjson"
)

// auditForwardQueue 外送队列深度（满即丢弃，防外送端故障积压）。
const auditForwardQueue = 256

// AuditForwarder 审计外送器（进程级生命周期；worker 随进程退出）。
type AuditForwarder struct {
	url    string
	token  string
	ch     chan map[string]any
	client *http.Client
	drops  atomic.Int64
	log    logx.Logger
}

// NewAuditForwarder 构建外送器并启动 worker。
func NewAuditForwarder(url, token string, log logx.Logger) *AuditForwarder {
	f := &AuditForwarder{
		url:    url,
		token:  token,
		ch:     make(chan map[string]any, auditForwardQueue),
		client: &http.Client{Timeout: 5 * time.Second},
		log:    log,
	}
	go f.run()
	return f
}

func (f *AuditForwarder) run() {
	for ev := range f.ch {
		body, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		// 与 sink 同一条 httpjson 外送线：非 2xx 带状态码与响应摘要报错
		//（旧手写路径不查状态码，SIEM 端 5xx 静默丢事件）；重试 1 次吸收瞬态。
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = httpjson.DoJSONWithRetry(ctx, f.client,
			func() (httpjson.Request, error) {
				return httpjson.Request{
					Method: http.MethodPost,
					URL:    f.url,
					Body:   body,
					Header: func(h http.Header) {
						if f.token != "" {
							h.Set("Authorization", "Bearer "+f.token)
						}
					},
				}, nil
			},
			httpjson.RetryConfig{MaxAttempts: 2, BaseDelay: 500 * time.Millisecond},
			new(map[string]any))
		cancel()
		if err != nil && f.log != nil {
			f.log.Warn("argus.audit.forward_failed", "error", err.Error())
		}
	}
}

// send 入队一条审计事件（非阻塞；满即丢弃计数）。
func (f *AuditForwarder) send(fields ...any) {
	ev := map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano)}
	for i := 0; i+1 < len(fields); i += 2 {
		if k, ok := fields[i].(string); ok {
			ev[k] = fields[i+1]
		}
	}
	select {
	case f.ch <- ev:
	default:
		if n := f.drops.Add(1); n%100 == 1 && f.log != nil {
			f.log.Warn("argus.audit.forward_dropped", "total_dropped", n)
		}
	}
}

// Wrap 包一层 logx.Logger：本地日志照常，msg=="audit.event" 的 Info 额外外送。
// WithService 等 builder 方法保持包装（audit.New 会调 WithService）。
func (f *AuditForwarder) Wrap(inner logx.Logger) logx.Logger {
	return &auditForwardLogger{Logger: inner, hook: f.send}
}

// auditForwardLogger logx.Logger 转发包装：接口嵌入继承全量方法，仅覆写
// Info（audit.event 外送）与 WithService（包装穿透）。
type auditForwardLogger struct {
	logx.Logger
	hook func(fields ...any)
}

func (l *auditForwardLogger) Info(msg string, kv ...any) {
	l.Logger.Info(msg, kv...)
	if msg == "audit.event" && l.hook != nil {
		l.hook(kv...)
	}
}

func (l *auditForwardLogger) WithService(name string) logx.Logger {
	return &auditForwardLogger{Logger: l.Logger.WithService(name), hook: l.hook}
}
