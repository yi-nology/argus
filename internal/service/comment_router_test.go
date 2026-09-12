package service

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"argus/internal/config"
)

// ---------- 评论意图路由测试（router 架构原语，v3.4） ----------

// routeFakeModel 分类桩：Generate 返回预设分类 JSON（router.Classify 走 GenerateJSON）。
type routeFakeModel struct {
	decision string // 返回给分类调用的 JSON
	fail     bool
}

func (f *routeFakeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	if f.fail {
		return nil, errReply("router model down")
	}
	return &schema.Message{Role: schema.Assistant, Content: f.decision}, nil
}

func (f *routeFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errReply("not implemented")
}

type errReply string

func (e errReply) Error() string { return string(e) }

func routeService(decision string, minCF float64) *Service {
	return &Service{
		Cfg:         &config.Config{RouterMinConfidence: minCF},
		RouterModel: &routeFakeModel{decision: decision},
	}
}

// 关键词快路径：短命令直达 review，长句不误触发；否定守门不误伤。
func TestMatchReviewKeyword(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"重新审查", true},
		{"请重新审查", true},
		{"帮我 re-review 一下", true},
		{"RERUN", true},
		{"不要重新审查，先修这个 bug", false}, // 否定前置守门
		{"这个重新审查的逻辑对吗？我想讨论一下我们目前的复审流程设计是怎么考虑的", false}, // 长句进 LLM 分类
		{"这个函数的边界条件是什么", false},
		{"", false},
	}
	for _, c := range cases {
		if got := matchReviewKeyword(c.in); got != c.want {
			t.Errorf("matchReviewKeyword(%q)=%v, want %v", c.in, got, c.want)
		}
	}
}

// 高置信 review → review；低置信 review → ask（宁可多答，不误触发重审）。
func TestClassifyIntent_ConfidenceGate(t *testing.T) {
	svc := routeService(`{"route":"review","confidence":0.92,"reason":"明确要求重审"}`, 0.6)
	intent, d, err := svc.classifyIntent(context.Background(), "麻烦再看一遍这个 PR")
	if err != nil || intent != commentIntentReview || d.Confidence != 0.92 {
		t.Fatalf("高置信应路由 review，got intent=%s err=%v d=%+v", intent, err, d)
	}

	svc = routeService(`{"route":"review","confidence":0.3,"reason":"可能想重审"}`, 0.6)
	intent, _, err = svc.classifyIntent(context.Background(), "看看吧")
	if err != nil || intent != commentIntentAsk {
		t.Fatalf("低置信应回落 ask，got intent=%s err=%v", intent, err)
	}
}

// ask / none / 未知词表 → 一律 ask。
func TestClassifyIntent_NonReviewFallsToAsk(t *testing.T) {
	for _, dec := range []string{
		`{"route":"ask","confidence":0.9,"reason":"提问"}`,
		`{"route":"none","confidence":0.5,"reason":"不确定"}`,
		`{"route":"hack","confidence":0.99,"reason":"词表外"}`,
	} {
		svc := routeService(dec, 0.6)
		intent, _, err := svc.classifyIntent(context.Background(), "这段代码为什么这样写")
		if err != nil || intent != commentIntentAsk {
			t.Fatalf("dec=%s 应路由 ask，got intent=%s err=%v", dec, intent, err)
		}
	}
}

// 分类模型故障 → 返回 error（由 routeAndReply 回落 Q&A，不丢响应）。
func TestClassifyIntent_ModelError(t *testing.T) {
	svc := &Service{
		Cfg:         &config.Config{RouterMinConfidence: 0.6},
		RouterModel: &routeFakeModel{fail: true},
	}
	_, _, err := svc.classifyIntent(context.Background(), "再看一遍")
	if err == nil {
		t.Fatal("模型故障应返回 error")
	}
}

// 分类模型回退次序：RouterModel → QAModel → Model。
func TestRouterChatModel_FallbackOrder(t *testing.T) {
	mm, qm, rm := &routeFakeModel{}, &routeFakeModel{}, &routeFakeModel{}
	svc := &Service{Model: mm, QAModel: qm, RouterModel: rm}
	if svc.routerChatModel() != model.BaseChatModel(rm) {
		t.Fatal("应优先 RouterModel")
	}
	svc.RouterModel = nil
	if svc.routerChatModel() != model.BaseChatModel(qm) {
		t.Fatal("应回退 QAModel")
	}
	svc.QAModel = nil
	if svc.routerChatModel() != model.BaseChatModel(mm) {
		t.Fatal("应回退 Model")
	}
}
