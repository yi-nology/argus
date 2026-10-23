package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oagentkit/llm"
	otel "go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestTracedLLMClientProducesSpan 注入面验收（强化版）：内存 exporter 断言
// LLM 出站请求真的产生 OTel span——215 实弹出现「gorm span 有、LLM span 无」
// 的盲区，此测试把「链路挂着」与「span 真生成」分开钉。
func TestTracedLLMClientProducesSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := tracedLLMClient(5 * time.Second)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("traced client GET 失败: %v", err)
	}
	_ = resp.Body.Close()

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("注入客户端的出站请求未产生 OTel span——httpotel 未在请求链上")
	}
	t.Logf("span ops: %v", func() []string {
		var names []string
		for _, s := range spans {
			names = append(names, s.Name)
		}
		return names
	}())
}

// TestTracedLLMCapturesRetryAfter v3.182.0：tracedLLMClient 外层包 RetryAfterTransport
// 后，429 的 Retry-After 头写入 ctx sink（供 agentrun 重试环按服务端建议退避）。
// ReAct 走 eino ChatModel 直调出口（无 CaptureRetryAfter 可设）——此前该路径漏捕获，
// agentrun 注入了 sink 但无人写入；此测试钉住「eino 出口也捕获」。
func TestTracedLLMCapturesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := tracedLLMClient(5 * time.Second)
	ctx := llm.WithRetryAfterSink(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("traced client Do 失败: %v", err)
	}
	_ = resp.Body.Close()

	if got := llm.RetryAfterFrom(ctx); got != 7*time.Second {
		t.Fatalf("Retry-After 未写入 sink: got %v want 7s", got)
	}
}

// TestTracedLLMRetryAfterNoSink 未装 sink 的请求零开销：非 429/无 sink 不 panic、原样透传。
func TestTracedLLMRetryAfterNoSink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := tracedLLMClient(5 * time.Second)
	resp, err := c.Get(srv.URL) // 无 sink 的 ctx
	if err != nil {
		t.Fatalf("无 sink 请求应原样透传: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("响应状态不应被改写: got %d", resp.StatusCode)
	}
}
