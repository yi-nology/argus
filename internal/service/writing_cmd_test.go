package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
)

// ---------- 单轮写作命令模板测试（v3.110.0 describe/testgen 骨架单源） ----------
// 此前 handleDescribe/handleTestGen 零直接测试锚点，模板抽取后以打桩端到端钉住
// 骨架语义：回执体/marker/失败回帖/装饰段四类不变量。

// writingFakePR 打桩平台：GetCRDiff 供 prDiffText，CreateNote 捕获回帖
// （SDKCtx 写路径的 DiffManager 优先）；其余借嵌入 Provider 的方法集
// （本测试不应触达，触达即 panic 兜底）。
type writingFakePR struct {
	provider.Provider

	mu     sync.Mutex
	diff   *provider.MergeDiff
	bodies []string
}

func (f *writingFakePR) GetCRDiff(context.Context, string, string, string) (*provider.MergeDiff, error) {
	return f.diff, nil
}

// GetRepo 报错：describeLabelVocab 对失败降级（标签词表走"仓库未提供"分支），
// 不覆盖则 nil 嵌入接口的晋升调用 panic。
func (f *writingFakePR) GetRepo(context.Context, string, string) (*provider.PlatformRepo, error) {
	return nil, errReply("writing stub: no repo")
}

// CreateNote 覆盖 DiffManager 优先路径：SDKCtx.CreateIssueComment 的类型断言会
// 命中嵌入接口晋升的方法集（宣告 DiffManager），不覆盖则 nil 嵌入值 panic。
func (f *writingFakePR) CreateNote(_ context.Context, _, _, _ string, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, body)
	return "123", nil
}

func (f *writingFakePR) posted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.bodies...)
}

// writingService 组装可打桩的 Service：QAModel 走 replyModel 优先路径。
func writingService(t *testing.T, mdl *routeFakeModel, plat *writingFakePR) *Service {
	t.Helper()
	return &Service{
		Cfg:     &config.Config{MaxDiffChars: 100_000},
		SDK:     argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{"gitea": plat}),
		QAModel: mdl,
	}
}

func testDiff() *provider.MergeDiff {
	return &provider.MergeDiff{
		RawDiff: "diff --git a/main.go b/main.go\nindex 0000000..1111111 100644\n--- a/main.go\n+++ b/main.go\n@@ -1 +1,2 @@\n+func login() {}\n",
		Files:   []*provider.ChangedFile{{NewPath: "main.go", Additions: 1, Deletions: 0}},
	}
}

// TestWritingCommand_HappyPath 钉回执体拼装：正文 + marker（argus:<cmd> delivery=），
// decorate 段先于 marker。
func TestWritingCommand_HappyPath(t *testing.T) {
	plat := &writingFakePR{diff: testDiff()}
	svc := writingService(t, &routeFakeModel{decision: "正文输出"}, plat)
	svc.handleDescribe(context.Background(), "gitea", "o", "r", "7", "o/r#7", "", "d1")
	bodies := plat.posted()
	if len(bodies) != 1 {
		t.Fatalf("应恰好回帖一次，got %d", len(bodies))
	}
	if !strings.HasPrefix(bodies[0], "正文输出") {
		t.Errorf("回帖应以模型输出开头:\n%s", bodies[0])
	}
	if !strings.HasSuffix(bodies[0], "<!-- argus:describe delivery=d1 -->") {
		t.Errorf("回帖缺 describe marker 尾缀:\n%s", bodies[0])
	}
}

// TestWritingCommand_TestGenFooter 无沙箱配置时固定尾注必须拼接（「未经编译验证」
// 声明是 v1 边界的确定性保障，不依赖模型自觉）。
func TestWritingCommand_TestGenFooter(t *testing.T) {
	plat := &writingFakePR{diff: testDiff()}
	svc := writingService(t, &routeFakeModel{decision: "### 测试"}, plat)
	svc.handleTestGen(context.Background(), "gitea", "o", "r", "7", "o/r#7", "", "d2")
	bodies := plat.posted()
	if len(bodies) != 1 || !strings.Contains(bodies[0], testgenFooter) {
		t.Fatalf("testgen 回帖应含固定尾注:\n%v", bodies)
	}
	if !strings.HasSuffix(bodies[0], "<!-- argus:testgen delivery=d2 -->") {
		t.Errorf("回帖缺 testgen marker 尾缀:\n%s", bodies[0])
	}
}

// TestWritingCommand_BuildReject target 过滤空命中：build 拒绝必须以专属文案回帖，
// 不触达 LLM。
func TestWritingCommand_BuildReject(t *testing.T) {
	plat := &writingFakePR{diff: testDiff()}
	mdl := &routeFakeModel{decision: "不应被消费"}
	svc := writingService(t, mdl, plat)
	svc.handleTestGen(context.Background(), "gitea", "o", "r", "7", "o/r#7", "no/such/path.go", "d3")
	bodies := plat.posted()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "diff 中没有路径命中") {
		t.Fatalf("应回帖 target 未命中文案:\n%v", bodies)
	}
}

// TestWritingCommand_LLMFailure 模型失败：记账 + 失败回帖（用户有感知，不静默）。
func TestWritingCommand_LLMFailure(t *testing.T) {
	plat := &writingFakePR{diff: testDiff()}
	svc := writingService(t, &routeFakeModel{fail: true}, plat)
	svc.handleDescribe(context.Background(), "gitea", "o", "r", "7", "o/r#7", "", "d4")
	bodies := plat.posted()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "变更说明生成失败") {
		t.Fatalf("LLM 失败应回帖错误说明:\n%v", bodies)
	}
}

// TestWritingCommand_DiffUnavailable diff 拉取失败：按命令专属文案回帖。
func TestWritingCommand_DiffUnavailable(t *testing.T) {
	plat := &writingFakePR{} // 无 diff
	svc := writingService(t, &routeFakeModel{decision: "x"}, plat)
	svc.handleTestGen(context.Background(), "gitea", "o", "r", "7", "o/r#7", "", "d5")
	bodies := plat.posted()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "无法获取有效 diff") {
		t.Fatalf("diff 缺失应回帖说明:\n%v", bodies)
	}
}

// TestWritingCommand_NoModel 模型未配置：静默返回，不触达平台、不 panic。
func TestWritingCommand_NoModel(t *testing.T) {
	plat := &writingFakePR{diff: testDiff()}
	svc := &Service{SDK: argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{"gitea": plat})}
	svc.handleDescribe(context.Background(), "gitea", "o", "r", "7", "o/r#7", "", "d6")
	if bodies := plat.posted(); len(bodies) != 0 {
		t.Errorf("无模型不应回帖: %v", bodies)
	}
}
