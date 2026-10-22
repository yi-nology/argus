package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
	otel "go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestEinoChatModelSpanExport(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prev)

	// fake openai 端点（chat completions 形态）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	bm, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		BaseURL: srv.URL, APIKey: "k", Model: "m",
		Timeout:    5 * time.Second,
		HTTPClient: tracedLLMClient(5 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bm.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	if err != nil {
		t.Logf("Generate err（端点假没关系，请求已发出）: %v", err)
	}
	names := []string{}
	for _, s := range exporter.GetSpans() {
		names = append(names, s.Name)
	}
	t.Logf("spans: %v", names)
	if len(names) == 0 {
		t.Fatal("eino ChatModel 出站请求未产生 span——注入链断点在此")
	}
}
