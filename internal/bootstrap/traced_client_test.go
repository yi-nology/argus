package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
