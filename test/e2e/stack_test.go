// Package e2e 双打桩端到端（design §7-9）：mock OpenAI + mock Gitea（fake provider）+
// mock OCR CLI + fake AgentPlugin，双打桩 E2E（21 场景）。
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/yi-nology/git-platform-sdk/provider"

	bizapi "argus/biz/handler/api"
	routerapi "argus/biz/router/api"
	"argus/internal/bootstrap"
	"argus/internal/channel/mcpserver"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/poster"
	"argus/internal/runner"
	"argus/internal/service"
	"argus/internal/storage"
	"git.example.com/enjoydream/agentkit/skill"
	"git.example.com/enjoydream/agentkit/workcopy"
	agentkitworker "git.example.com/enjoydream/agentkit/worker"

	argusworker "argus/internal/worker"
	"github.com/cloudwego/hertz/pkg/app"
)

// ---------- fake Gitea provider ----------

// fakeProvider 假平台：只实现 Argus 会调用的方法，其余借嵌入接口 panic 兜底。
type fakeProvider struct {
	provider.Provider     // 未显式实现的方法（不该被调用）走 nil panic
	provider.IssueManager // 能力接口断言需要完整方法集（嵌入补齐）
	provider.ReviewManager

	mu       sync.Mutex
	cr       *provider.ChangeRequest
	repo     *provider.PlatformRepo
	diff     *provider.MergeDiff
	issues   map[string]*provider.Issue
	files    map[string]string
	comments []*provider.IssueComment
	nextID   int64
	markerN  int // "审查中"占位评论数（runner postStartMarker；不进 created/comments）
	secret   string
	ev       *provider.NormalizedEvent
	createdN int
	updatedN int
	reviews  []provider.CreateReviewOptions
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		nextID: 100,
		issues: map[string]*provider.Issue{},
		files:  map[string]string{},
		cr: &provider.ChangeRequest{
			Number: "7", Title: "添加登录功能",
			Description:  "实现账号密码登录，见 issue #12",
			State:        provider.CRStateOpened, // SDK 契约：非 draft 开启 PR 恒为 opened（poller 防线依赖此值）
			SourceBranch: "feat/login-#12", TargetBranch: "main",
			HeadSHA: "sha-1", Author: &provider.CRUser{Username: "alice"},
		},
		repo: &provider.PlatformRepo{FullName: "acme/app", Owner: "acme", Name: "app", DefaultBranch: "main"},
		diff: &provider.MergeDiff{Files: []*provider.ChangedFile{
			{NewPath: "main.go", Diff: "diff --git a/main.go b/main.go\n+++ b/main.go\n+func login() {}", Additions: 1},
		}},
		secret: "e2e-sign-secret",
	}
}

func (f *fakeProvider) Platform() provider.Platform { return "gitea" }
func (f *fakeProvider) Capabilities() provider.CapabilitySet {
	return provider.CapabilitySet{Issues: true, Reviews: true}
}

func (f *fakeProvider) GetCR(_ context.Context, _, _, _ string) (*provider.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *f.cr
	return &cp, nil
}

func (f *fakeProvider) GetRepo(_ context.Context, _, _ string) (*provider.PlatformRepo, error) {
	return f.repo, nil
}

func (f *fakeProvider) ListCRs(_ context.Context, opts provider.ListCROptions) ([]*provider.ChangeRequest, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cr == nil {
		return nil, 0, nil
	}
	cp := *f.cr
	return []*provider.ChangeRequest{&cp}, 1, nil
}

func (f *fakeProvider) GetCRDiff(_ context.Context, _, _, _ string) (*provider.MergeDiff, error) {
	return f.diff, nil
}

func (f *fakeProvider) GetFileContent(_ context.Context, _, _, path, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.files[path]; ok {
		return c, nil
	}
	return "", fmt.Errorf("x: %w", provider.ErrNotFound)
}

func (f *fakeProvider) GetIssue(_ context.Context, _, _, number string) (*provider.Issue, error) {
	if is, ok := f.issues[number]; ok {
		return is, nil
	}
	return nil, fmt.Errorf("x: %w", provider.ErrNotFound)
}

func (f *fakeProvider) ListIssueComments(_ context.Context, _, _, _ string) ([]*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]*provider.IssueComment(nil), f.comments...)
	return out, nil
}

func (f *fakeProvider) CreateIssueComment(_ context.Context, _, _, _ string, body string) (*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// "审查中"占位（runner WIP postStartMarker，锚点 argus:start）：独立计数，
	// 不进 comments 流——summary 相关的 created/bodies 断言只针对审查报告评论。
	if strings.Contains(body, "argus:start") {
		f.markerN++
		f.nextID++
		return &provider.IssueComment{ID: f.nextID, Body: body, CreatedAt: time.Now()}, nil
	}
	f.createdN++
	f.nextID++
	c := &provider.IssueComment{ID: f.nextID, Body: body, CreatedAt: time.Now()}
	f.comments = append(f.comments, c)
	return c, nil
}

// CreateNote DiffManager 路径（sdkctx v3.0.3 起优先走 MR Notes API）：
// 必须显式实现，否则 DiffManager 断言命中嵌入接口的 promoted method（nil panic 兜底）。
// 语义与 CreateIssueComment 一致：新建一条总结评论，返回字符串 note ID。
func (f *fakeProvider) CreateNote(ctx context.Context, _, _, _ string, body string) (string, error) {
	c, err := f.CreateIssueComment(ctx, "", "", "", body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", c.ID), nil
}

func (f *fakeProvider) UpdateIssueComment(_ context.Context, _, _, _ string, commentID int64, body string) (*provider.IssueComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updatedN++
	for _, c := range f.comments {
		if c.ID == commentID {
			c.Body = body
			c.UpdatedAt = time.Now()
			return c, nil
		}
	}
	return nil, fmt.Errorf("comment %d not found", commentID)
}

func (f *fakeProvider) CreateReview(_ context.Context, _, _, _ string, opts provider.CreateReviewOptions) (*provider.ReviewResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviews = append(f.reviews, opts)
	return &provider.ReviewResult{ID: "rev-1"}, nil
}

// ListReviews 评审幂等锚点查询：回放已提交评审的 body（含 task_id 锚点）。
func (f *fakeProvider) ListReviews(_ context.Context, _, _, _ string) ([]provider.Review, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]provider.Review, 0, len(f.reviews))
	for i, r := range f.reviews {
		out = append(out, provider.Review{ID: int64(i + 1), Body: r.Body})
	}
	return out, nil
}

// ParseWebhookEvent 假签名校验：X-Test-Signature == secret；然后回放预置事件。
func (f *fakeProvider) ParseWebhookEvent(r *http.Request, secret string) (*provider.NormalizedEvent, error) {
	if r.Header.Get("X-Test-Signature") != f.secret {
		return nil, fmt.Errorf("gitea: %w", provider.ErrWebhookValidation)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ev != nil && f.ev.CR != nil {
		// 平台状态随事件推进（新 push 改 head）
		f.cr.HeadSHA = f.ev.CR.HeadSHA
	}
	return f.ev, nil
}

// snapshot 评论/审查调用快照（断言用）。
func (f *fakeProvider) snapshot() (created, updated int, reviews []provider.CreateReviewOptions, bodies []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	created, updated = f.createdN, f.updatedN
	reviews = append(reviews, f.reviews...)
	for _, c := range f.comments {
		bodies = append(bodies, c.Body)
	}
	return
}

// ---------- mock OpenAI ----------

type mockLLM struct {
	mu       sync.Mutex
	srv      *httptest.Server
	r1calls  int
	blockR1  chan struct{} // 非空：第一个 R1 请求阻塞直至关闭
	R1JSON   string
	R3JSON   string
	Correctn string // correctness reviewer 输出
	r4calls  int    // correctness 人设调用次数（共识场景断言单次执行）
	// R35 drafts：反思「终审编辑」轮的修订 findings（按 draft 轮次取，超出取末个）。
	R35Drafts []string
	r35calls  int
	// RouterReply 评论意图分类桩响应（stackOpts.routerReply 注入）。
	// 分类请求的 user 消息携带评论原文——routerHit 按测试设定标记句匹配。
	RouterReply string
	routerMark  string
	// PersonaSeq 按人设标记（"【Argus <插件名>】"）的序列化回复：第 i 次调用取
	// 第 i 项（越界取末项）；非空时优先于 Correctn。best-of-N 场景需要逐采样
	// 可变的 reviewer 输出（复现计数依赖两次同题 + 一次不同）。
	PersonaSeq   map[string][]string
	personaCalls map[string]int
}

func newMockLLM(r1, r3, correctness string) *mockLLM {
	m := &mockLLM{R1JSON: r1, R3JSON: r3, Correctn: correctness, personaCalls: map[string]int{}}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

// routerHit 分类请求识别：消息含测试标记句（评论原文进分类 prompt）。
func (m *mockLLM) routerHit(text string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.routerMark != "" && strings.Contains(text, m.routerMark)
}

func (m *mockLLM) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	fmt.Printf("[mockLLM] %s %s raw(%d bytes): %.200s\n", r.Method, r.URL.Path, len(raw), string(raw))
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(raw, &req)
	var sb strings.Builder
	for _, msg := range req.Messages {
		var c string
		_ = json.Unmarshal(msg.Content, &c)
		if c == "" {
			c = string(msg.Content)
		}
		sb.WriteString(c)
		sb.WriteString("\n")
	}
	text := sb.String()

	switch {
	case strings.Contains(text, "【Argus R1 需求解析】"):
		m.mu.Lock()
		m.r1calls++
		block := m.blockR1
		m.mu.Unlock()
		if block != nil {
			select {
			case <-block:
			case <-time.After(15 * time.Second):
			}
		}
		m.reply(w, m.R1JSON)
	case strings.Contains(text, "【Argus R3 完成度判定】"):
		m.reply(w, m.R3JSON)
	case strings.Contains(text, "【Argus correctness】"):
		m.mu.Lock()
		m.r4calls++
		m.mu.Unlock()
		m.reply(w, m.Correctn)
	case strings.Contains(text, "【Argus R2 变更理解】"):
		m.reply(w, "本 PR 添加了账号密码登录功能。")
	case strings.Contains(text, "苛刻但公正的评审"):
		// R3.5 critique：首轮即通过（draft 已由测试脚本钉死）
		m.reply(w, `{"pass":true,"issues":[]}`)
	case strings.Contains(text, "【Argus R3.5 报告反思】"):
		m.mu.Lock()
		i := m.r35calls
		m.r35calls++
		m.mu.Unlock()
		if len(m.R35Drafts) == 0 {
			m.reply(w, `[]`)
			return
		}
		if i >= len(m.R35Drafts) {
			i = len(m.R35Drafts) - 1
		}
		m.reply(w, m.R35Drafts[i])
	case m.routerHit(text):
		// 评论意图分类（router GenerateJSON 的 user 消息携带评论原文，按测试
		// 预设的标记句识别）：按测试桩指定路由回
		m.mu.Lock()
		r := m.RouterReply
		m.mu.Unlock()
		if r == "" {
			r = `{"route":"ask","confidence":0.9,"reason":"default"}`
		}
		m.reply(w, r)
	default:
		if m.personaReply(w, text) {
			return
		}
		m.reply(w, `{"findings":[],"notes":""}`)
	}
}

// personaReply PersonaSeq 命中时按调用序回复（返回是否命中）。
func (m *mockLLM) personaReply(w http.ResponseWriter, text string) bool {
	for marker, seq := range m.PersonaSeq {
		if !strings.Contains(text, marker) {
			continue
		}
		m.mu.Lock()
		i := m.personaCalls[marker]
		m.personaCalls[marker] = i + 1
		m.mu.Unlock()
		if i >= len(seq) {
			i = len(seq) - 1
		}
		m.reply(w, seq[i])
		return true
	}
	return false
}

func (m *mockLLM) reply(w http.ResponseWriter, content string) {
	fmt.Printf("[mockLLM] reply(%d bytes): %.120s\n", len(content), content)
	resp := map[string]any{
		"id": "cmpl-e2e", "object": "chat.completion", "created": 1, "model": "mock",
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ---------- e2e 栈 ----------

type stack struct {
	cfg     *config.Config
	store   *storage.Store
	sdk     *argusctx.SDKCtx
	fake    *fakeProvider
	llm     *mockLLM
	mcpBase string
	runner  *runner.Runner
	reg     *plugin.Registry
	engine  *server.Hertz
	cancel  context.CancelFunc
}

type stackOpts struct {
	extraAgents []plugin.AgentDecl
	workers     int // 0 = 不启动 worker（场景⑧手动控制执行序）
	r1          string
	r3          string
	correctness string
	withOCR     bool
	withACPX    bool              // acpx 插件（假 kimi CLI 进 PATH，验证 bootstrap→R4→合并全链路）
	realLLM     bool              // true = 用环境变量 OPENAI_* 的真实模型（集成测试），不用 mock
	debug       bool              // ARGUS_DEBUG：留存阶段产物
	skills      map[string]string // skillRef name → content
	// v3.5 闭环
	reflectRounds   int    // R3.5 反思轮数（0=关闭）
	reflectEvidence string // off|soft|strict
	regretLearning  bool   // 模式级后悔学习环
	r35Drafts       []string
	commentRouter   bool                // @argus 评论意图路由（含 GitLab note 场景的 gitlab 平台装配）
	squads          []plugin.SquadDecl  // 编队声明（共识/互盲场景）
	routerReply     string              // 评论意图分类桩响应（路由 LLM 分支场景；空=回落 ask）
	routerMark      string              // 分类请求标记句（= 测试评论原文，命中即回 routerReply）
	personaSeq      map[string][]string // 按人设标记的序列化回复（best-of-N 场景）

	// ---- golden 评测夹具注入（golden_test.go 用；nil/空 = 默认场景）----
	diffFiles     []provider.ChangedFile     // PR diff 文件集
	repoFiles     map[string]string          // get_file 可读文件内容
	prTitle       string                     // PR 标题（覆盖默认）
	prBody        string                     // PR 描述（覆盖默认）
	prLabels      []string                   // PR 标签
	issues        map[string]*provider.Issue // R1 引用的 issue 内容（number → issue）
	stripDefaults bool                       // 移除默认插件集（R5.0 零插件守门场景）

	// G4 完备性裁判（golden 夹具用）
	completenessPolicy string // annotate | demote（空 = annotate）
	completenessRetry  bool   // 受限补审开关
}

func defaultOpts() stackOpts {
	return stackOpts{
		workers:     0,
		r1:          `{"source":"issue #12","acceptance_criteria":[{"id":"AC-1","text":"支持账号密码登录"},{"id":"AC-2","text":"登录失败三次锁定"}],"ambiguous":[]}`,
		r3:          `{"checks":[{"id":"AC-1","status":"implemented","evidence":"main.go:3","detail":"ok"},{"id":"AC-2","status":"implemented","evidence":"main.go:5","detail":"ok"}],"scope_creep":[],"summary":"完成"}`,
		correctness: `{"findings":[],"notes":""}`,
		skills:      map[string]string{},
	}
}

// newStack 组装与 main() 等价的完整栈（SDK/LLM/OCR 全打桩）。
func newStack(t TestingT, opt stackOpts) *stack {
	defs := defaultOpts()
	if opt.r1 == "" {
		opt.r1 = defs.r1
	}
	if opt.r3 == "" {
		opt.r3 = defs.r3
	}
	if opt.correctness == "" {
		opt.correctness = defs.correctness
	}
	if opt.skills == nil {
		opt.skills = defs.skills
	}
	tmp := t.TempDir()
	log := logx.NewSlogLogger("argus-e2e")

	// LLM：mock 或真实（集成测试走环境变量 OPENAI_*，由 config.LoadDotEnv 加载 lacus/.env）
	var llmMock *mockLLM
	llmCfg := config.LLMConfig{BaseURL: "unused", APIKey: "k", Model: "mock",
		MaxTokens: 8192, ContextToken: 128000, Timeout: 180 * time.Second}
	if !opt.realLLM {
		llmMock = newMockLLM(opt.r1, opt.r3, opt.correctness)
		llmMock.R35Drafts = opt.r35Drafts
		llmMock.RouterReply = opt.routerReply
		llmMock.routerMark = opt.routerMark
		llmMock.PersonaSeq = opt.personaSeq
		t.Cleanup(llmMock.srv.Close)
		llmCfg.BaseURL = llmMock.srv.URL
	} else {
		config.LoadDotEnv()
		llmCfg = config.LLMConfig{
			BaseURL: os.Getenv("OPENAI_BASE_URL"), APIKey: os.Getenv("OPENAI_API_KEY"),
			Model: os.Getenv("OPENAI_MODEL"), MaxTokens: 8192, ContextToken: 128000,
			Timeout: 180 * time.Second,
		}
		if llmCfg.BaseURL == "" || llmCfg.APIKey == "" || llmCfg.Model == "" {
			t.Fatal("ARGUS_IT=1 需要 OPENAI_BASE_URL / OPENAI_API_KEY / OPENAI_MODEL（lacus/.env）")
		}
	}
	_ = llmMock

	// skill 文件
	skillsDir := tmp + "/skills"
	if len(opt.skills) > 0 {
		mustMkdir(t, skillsDir)
		for name, content := range opt.skills {
			mustWrite(t, skillsDir+"/"+name+".md", content)
		}
	}

	// plugins.yaml
	agents := []plugin.AgentDecl{
		{
			Name: "requirement-compliance", Kind: "builtin",
			Skills: []string{plugin.SkillRequirementCompliance},
			Needs:  []string{"diff", "repo-file-read", "requirement-spec"},
			Cost:   plugin.CostHeavy, Timeout: "120s", LockRepoConfig: true,
		},
		{
			Name: "correctness", Kind: "builtin",
			Skills: []string{plugin.SkillCodeReview}, Needs: []string{"diff"},
			Cost: plugin.CostStandard, Timeout: "60s",
		},
		{
			Name: "security", Kind: "builtin",
			Skills: []string{plugin.SkillCodeReview}, Needs: []string{"diff"},
			Cost: plugin.CostHeavy, Timeout: "60s", LockRepoConfig: true,
		},
		{
			// 与生产默认一致：remote 占位（endpoint 未配置 → 注册为不可用，可观测）
			Name: "frontend-visual", Kind: "remote", Protocol: "a2a",
			Skills: []string{plugin.SkillVisualCheck}, Needs: []string{"preview-url"},
		},
	}
	if opt.withOCR {
		script := writeOCRScript(t, tmp)
		agents = append(agents, plugin.AgentDecl{
			Name: "ocr", Kind: "cli",
			Skills: []string{plugin.SkillCodeReview}, Needs: []string{"working-copy"},
			Cmd:  []string{script, "diff", "--format", "json"},
			Mode: "full", Cost: plugin.CostHeavy, Timeout: "60s",
		})
	}
	if opt.withACPX {
		writeFakeKimi(t, tmp)
		if tt, ok := t.(*testing.T); ok {
			// 假 kimi 必须在 bootstrap LookPath 之前进 PATH
			tt.Setenv("PATH", tmp+"/bin:"+os.Getenv("PATH"))
		}
		agents = append(agents, plugin.AgentDecl{
			Name: "coding-agent-kimi", Kind: "acpx", Agent: "kimi",
			Skills: []string{plugin.SkillCodeReview}, Needs: []string{"diff", "working-copy"},
			Cost: plugin.CostHeavy, Timeout: "60s",
		})
	}
	if opt.stripDefaults {
		agents = nil // golden 场景：零插件守门（R5.0）等需要空注册表
	}
	agents = append(agents, opt.extraAgents...)
	pluginsFile := tmp + "/plugins.yaml"
	if len(opt.squads) > 0 {
		mustWrite(t, pluginsFile, marshalPluginsFile(agents, opt.squads))
	} else {
		mustWrite(t, pluginsFile, marshalAgents(agents))
	}

	// 配置
	cfg := &config.Config{
		Addr: "127.0.0.1:0", DBDSN: tmp + "/argus.db", APIToken: "e2e-token",
		BotUsername: "argus-bot", SkipDraft: true, MaxConcurrentTasks: 2,
		RouterMinConfidence: 0.6, // 与生产默认对齐（分类低置信门），路由场景依赖
		TaskTokenBudget:     200000, ReportMeta: true, PluginsFile: pluginsFile,
		Debug:     opt.debug,
		SkillsDir: skillsDir, WorkRoot: tmp + "/work",
		Platforms: map[string]config.PlatformConfig{
			"gitea": {BaseURL: "http://gitea.test", Token: "tok", Secret: "e2e-sign-secret"},
		},
		LLM: llmCfg,
	}

	// 存储 + SDK
	store, err := storage.Open(cfg.DBDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fake := newFakeProvider()
	// golden 夹具注入：非空时覆盖默认 PR/diff/文件/issue
	if len(opt.diffFiles) > 0 {
		dfs := make([]*provider.ChangedFile, len(opt.diffFiles))
		for i := range opt.diffFiles {
			dfs[i] = &opt.diffFiles[i]
		}
		fake.diff = &provider.MergeDiff{Files: dfs}
	}
	if opt.repoFiles != nil {
		fake.files = opt.repoFiles
	}
	if opt.prTitle != "" {
		fake.cr.Title = opt.prTitle
	}
	if opt.prBody != "" {
		fake.cr.Description = opt.prBody
	}
	if len(opt.prLabels) > 0 {
		fake.cr.Labels = opt.prLabels
	}
	if len(opt.issues) > 0 {
		fake.issues = opt.issues
	}
	provs := map[string]provider.Provider{"gitea": fake}
	cfg.CommentRouter = opt.commentRouter
	if opt.commentRouter {
		// GitLab note 评论场景：webhook 路由需要 cfg.Platforms 有 secret 的 gitlab 条目
		provs["gitlab"] = fake
		cfg.Platforms["gitlab"] = config.PlatformConfig{
			BaseURL: "http://gitlab.test", Token: "tok", Secret: "e2e-sign-secret"}
	}
	sdkCtx := argusctx.NewSDKCtxStubbed(log, provs)

	// workcopy：指向本地 file:// fixture（CLI 插件场景用）
	wc := workcopy.NewPool(cfg.WorkRoot, log)
	if opt.withOCR || opt.withACPX {
		fixture := makeFixtureRepo(t, tmp)
		wc.CredentialOf = func(string) (string, string, bool) { return "file://" + fixture, "x", true }
	} else {
		wc.CredentialOf = func(string) (string, string, bool) { return "http://gitea.test", "tok", true }
	}

	// LLM 模型
	model, err := openai.NewChatModel(context.Background(), &openai.ChatModelConfig{
		BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
		MaxTokens: &llmCfg.MaxTokens, Timeout: llmCfg.Timeout,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 注册表 + 编排（runner 延迟绑定）
	var runnerRef *runner.Runner
	skills := skill.NewFileProvider(skillsDir)
	reg, skillRefs, err := bootstrap.BuildRegistry(&bootstrap.Deps{
		Cfg: cfg, Log: log, WC: wc, Model: model, Skills: skills,
		WorkflowFactory: func(budget *plugin.TokenBudget) adapter_builtin.WorkflowRunner {
			if runnerRef == nil {
				t.Fatal("runner 未装配")
			}
			return runnerRef.WorkflowFactory()(budget)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := plugin.NewDispatcher(reg, log)
	// 编队声明（收尾批修复）：e2e 装配此前漏了 WithSquads——plugins.yaml squads
	// 段在 e2e 从未生效（生产 bootstrap.Assemble 有此步）
	if pf, perr := plugin.LoadPluginsFile(cfg.PluginsFile); perr == nil && len(pf.Squads) > 0 {
		dispatcher.WithSquads(pf.Squads)
	}
	merger := plugin.NewMerger(store, log)
	if opt.regretLearning {
		// e2e 阈值放宽：MinSamples=1 便于单轮/两轮内触发消费。SoftFloor=0.55：
		// v3.9.1 记账口径修正后，轮 2 发布使真实模式行置信度恰达 1+1 计数的
		// 0.50（旧错账把它记进影子行才低于阈值）——放宽到 0.55 保住边界余量。
		merger.WithLearning(store, plugin.LearningConfig{
			Enabled: true, MinSamples: 1, SoftFloor: 0.55, HardFloor: 0.30,
			InjectFloor: 0.50, InjectTopN: 10,
		})
	}
	posterSvc := &poster.Poster{SDK: sdkCtx, Store: store, Version: "v3.0.0-e2e", Log: log}
	runnerRef = &runner.Runner{
		Cfg: cfg, Store: store, SDK: sdkCtx, Model: model, ModelName: "mock",
		Dispatcher: dispatcher, Merger: merger, Poster: posterSvc, Log: log,
		Version: "v3.0.0-e2e", SkillProvider: skills, SkillRefs: skillRefs,
		ReflectRounds:      opt.reflectRounds,
		ReflectEvidence:    opt.reflectEvidence,
		CompletenessPolicy: opt.completenessPolicy,
		CompletenessRetry:  opt.completenessRetry,
	}
	if opt.regretLearning {
		runnerRef.Learning = plugin.LearningConfig{
			Enabled: true, MinSamples: 1, SoftFloor: 0.55, HardFloor: 0.30,
			InjectFloor: 0.50, InjectTopN: 10,
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if opt.workers > 0 {
		wp := &agentkitworker.Pool{Queue: &argusworker.StoreQueue{Store: store}, N: opt.workers, Log: log, Run: runnerRef.Execute}
		wp.Start(ctx)
	}

	// HTTP（Model 接入：HandleComment 的 LLM 门需要非 nil；评论重审关键词路径不实际调用 LLM）
	svc := &service.Service{Cfg: cfg, Store: store, SDK: sdkCtx, Registry: reg, Log: log, Version: "e2e", Model: model}
	bizapi.SetService(svc)
	routerapi.SetAuth = svc.BearerMiddleware()
	hostPort := freeTCPPort()
	h := server.New(server.WithHostPorts("127.0.0.1:" + hostPort))
	routerapi.Register(h)
	// MCP Server 挂载（与 main 一致：Bearer 鉴权 + streamable http 桥接）
	mcpBridge := mcpserver.HertzBridge(mcpserver.New(svc, "v3.0.1-e2e"))
	auth := svc.BearerMiddleware()
	for _, m := range []string{"GET", "POST", "DELETE"} {
		switch m {
		case "GET":
			h.GET("/mcp", append([]app.HandlerFunc{auth}, mcpBridge)...)
		case "POST":
			h.POST("/mcp", append([]app.HandlerFunc{auth}, mcpBridge)...)
		case "DELETE":
			h.DELETE("/mcp", append([]app.HandlerFunc{auth}, mcpBridge)...)
		}
	}
	t.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer scancel()
		_ = h.Shutdown(sctx)
	})
	go func() { _ = h.Run() }()
	waitHTTPReady("127.0.0.1:" + hostPort)
	_ = ctx

	return &stack{cfg: cfg, store: store, sdk: sdkCtx, fake: fake, llm: llmMock,
		runner: runnerRef, reg: reg, engine: h, mcpBase: "http://127.0.0.1:" + hostPort + "/mcp",
		cancel: cancel}
}

// countTasksByHead 统计某 head 的任务数（poller 幂等断言用）。
func (s *stack) countTasksByHead(head string) int {
	tasks, err := s.store.ListTasksByHead(context.Background(), "gitea", "acme", "app", "7", head)
	if err != nil {
		return -1
	}
	return len(tasks)
}

// anyTaskDoneByHead 某 head 是否已有 done 任务。
func (s *stack) anyTaskDoneByHead(head string) bool {
	tasks, err := s.store.ListTasksByHead(context.Background(), "gitea", "acme", "app", "7", head)
	if err != nil {
		return false
	}
	for _, t := range tasks {
		if t.Status == storage.StatusDone {
			return true
		}
	}
	return false
}

// setEvent 预置 webhook 事件。
// v3.8.0：action 先过 provider.NormalizeCRAction——与真实 SDK 链路一致（此前
// fake 直接拼原生词绕过归一化，掩盖了白名单与归一化词表脱节的 P0 断流）。
func (s *stack) setEvent(action, headSHA string) {
	s.fake.mu.Lock()
	defer s.fake.mu.Unlock()
	s.fake.ev = &provider.NormalizedEvent{
		ID: "evt", Type: "cr." + provider.NormalizeCRAction(action, false), Source: "gitea",
		Actor: &provider.CRUser{Username: "alice"},
		Repo:  &provider.EventRepo{Owner: "acme", Name: "app"},
		CR: &provider.ChangeRequest{Number: "7", HeadSHA: headSHA,
			SourceBranch: "feat/login", TargetBranch: "main", Draft: false},
	}
}

// postWebhook 通过 hertz engine 发 webhook（默认 gitea 平台）。
func (s *stack) postWebhook(t TestingT, delivery string) (int, webhookResp) {
	return s.postWebhookOn(t, "gitea", delivery)
}

// postWebhookOn 指定平台发 webhook：路径与 delivery 头按平台区分（与生产一致：
// gitea → X-Gitea-Delivery，gitlab → X-Gitlab-Event-UUID）。
func (s *stack) postWebhookOn(t TestingT, platform, delivery string) (int, webhookResp) {
	body := `{"action":"x"}`
	headers := []utHeader{
		{Key: "Content-Type", Value: "application/json"},
		{Key: "X-Test-Signature", Value: "e2e-sign-secret"},
	}
	if delivery != "" {
		h := "X-" + platform + "-Delivery"
		if platform == "gitlab" {
			h = "X-Gitlab-Event-UUID"
		}
		headers = append(headers, utHeader{Key: h, Value: delivery})
	}
	rec := performRequest(s.engine, "POST", "/webhook/"+platform, body, headers)
	var env struct {
		Code int             `json:"code"`
		Data webhookRespData `json:"data"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&env)
	return rec.Code, webhookResp{Status: env.Data.Status, Reason: strv(env.Data.Reason),
		EventId: strv(env.Data.EventId), TaskId: strv(env.Data.TaskId)}
}

type webhookResp struct {
	Status  string
	Reason  string
	EventId string
	TaskId  string
}

type webhookRespData struct {
	Status  string  `json:"status"`
	Reason  *string `json:"reason"`
	EventId *string `json:"eventId"`
	TaskId  *string `json:"taskId"`
}

func strv(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// waitForStatus 轮询任务状态至期望值。
func (s *stack) waitForStatus(t TestingT, taskID, want string) storage.Task {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		task, err := s.store.GetTask(context.Background(), taskID)
		if err == nil && task.Status == want {
			return *task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未到达状态 %s（超时）", taskID, want)
	return storage.Task{}
}

// createTaskViaAPI 走 TaskAPI 提交任务。
func (s *stack) createTaskViaAPI(t TestingT, kind int) string {
	body := fmt.Sprintf(`{"kind":%d,"platform":"gitea","owner":"acme","repo":"app","number":"7"}`, kind)
	rec := performRequest(s.engine, "POST", "/tasks", body, []utHeader{
		{Key: "Content-Type", Value: "application/json"},
		{Key: "Authorization", Value: "Bearer e2e-token"},
	})
	if rec.Code != 200 {
		t.Fatalf("CreateTask HTTP %d", rec.Code)
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			TaskId string `json:"taskId"`
			Status string `json:"status"`
		} `json:"data"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&env)
	return env.Data.TaskId
}

// TestingT 测试接口（*testing.T 满足）。
type TestingT interface {
	TempDir() string
	Cleanup(func())
	Helper()
	Fatal(...any)
	Fatalf(string, ...any)
}
