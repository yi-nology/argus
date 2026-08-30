// Package e2e 双打桩端到端（design §7-9）：mock OpenAI + mock Gitea（fake provider）+
// mock OCR CLI + fake AgentPlugin，验证八场景。
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
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/yi-nology/git-platform-sdk/provider"

	bizapi "argus/biz/handler/api"
	routerapi "argus/biz/router/api"
	"argus/internal/bootstrap"
	"argus/internal/config"
	argusctx "argus/internal/context"
	"argus/internal/knowledge/skill"
	"argus/internal/plugin"
	"argus/internal/plugin/adapter_builtin"
	"argus/internal/poster"
	"argus/internal/runner"
	"argus/internal/service"
	"argus/internal/storage"
	"argus/internal/worker"
)

// ---------- fake Gitea provider ----------

// fakeProvider 假平台：只实现 Argus 会调用的方法，其余借嵌入接口 panic 兜底。
type fakeProvider struct {
	provider.Provider     // 未显式实现的方法（不该被调用）走 nil panic
	provider.IssueManager // 能力接口断言需要完整方法集（嵌入补齐）
	provider.ReviewManager

	mu          sync.Mutex
	cr          *provider.ChangeRequest
	repo        *provider.PlatformRepo
	diff        *provider.MergeDiff
	issues      map[string]*provider.Issue
	files       map[string]string
	comments    []*provider.IssueComment
	nextID      int64
	secret      string
	ev          *provider.NormalizedEvent
	webhookMode string // "ok" | "badsig"
	createdN    int
	updatedN    int
	reviews     []provider.CreateReviewOptions
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		nextID: 100,
		issues: map[string]*provider.Issue{},
		files:  map[string]string{},
		cr: &provider.ChangeRequest{
			Number: "7", Title: "添加登录功能",
			Description:  "实现账号密码登录，见 issue #12",
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
	f.createdN++
	f.nextID++
	c := &provider.IssueComment{ID: f.nextID, Body: body, CreatedAt: time.Now()}
	f.comments = append(f.comments, c)
	return c, nil
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
}

func newMockLLM(r1, r3, correctness string) *mockLLM {
	m := &mockLLM{R1JSON: r1, R3JSON: r3, Correctn: correctness}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
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
		m.reply(w, m.Correctn)
	case strings.Contains(text, "【Argus R2 变更理解】"):
		m.reply(w, "本 PR 添加了账号密码登录功能。")
	default:
		m.reply(w, `{"findings":[],"notes":""}`)
	}
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
	cfg    *config.Config
	store  *storage.Store
	sdk    *argusctx.SDKCtx
	fake   *fakeProvider
	llm    *mockLLM
	runner *runner.Runner
	reg    *plugin.Registry
	engine *server.Hertz
	cancel context.CancelFunc
}

type stackOpt func(*stackOpts)

type stackOpts struct {
	extraAgents []plugin.AgentDecl
	workers     int // 0 = 不启动 worker（场景⑧手动控制执行序）
	r1          string
	r3          string
	correctness string
	withOCR     bool
	realLLM     bool              // true = 用环境变量 OPENAI_* 的真实模型（集成测试），不用 mock
	skills      map[string]string // skillRef name → content
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
	agents = append(agents, opt.extraAgents...)
	pluginsFile := tmp + "/plugins.yaml"
	mustWrite(t, pluginsFile, marshalAgents(agents))

	// 配置
	cfg := &config.Config{
		Addr: "127.0.0.1:0", DBDSN: tmp + "/argus.db", APIToken: "e2e-token",
		BotUsername: "argus-bot", SkipDraft: true, MaxConcurrentTasks: 2,
		TaskTokenBudget: 200000, ReportMeta: true, PluginsFile: pluginsFile,
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
	sdkCtx := argusctx.NewSDKCtxStubbed(log, map[string]provider.Provider{"gitea": fake})

	// workcopy：指向本地 file:// fixture（CLI 插件场景用）
	wc := argusctx.NewWorkcopy(cfg.WorkRoot, log)
	if opt.withOCR {
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
	merger := plugin.NewMerger(store, log)
	posterSvc := &poster.Poster{SDK: sdkCtx, Store: store, Version: "v3.0.0-e2e", Log: log}
	runnerRef = &runner.Runner{
		Cfg: cfg, Store: store, SDK: sdkCtx, Model: model, ModelName: "mock",
		Dispatcher: dispatcher, Merger: merger, Poster: posterSvc, Log: log,
		Version: "v3.0.0-e2e", SkillProvider: skills, SkillRefs: skillRefs,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if opt.workers > 0 {
		wp := &worker.Pool{Store: store, N: opt.workers, Log: log, Run: runnerRef.Execute}
		wp.Start(ctx)
	}

	// HTTP
	svc := &service.Service{Cfg: cfg, Store: store, SDK: sdkCtx, Registry: reg, Log: log, Version: "e2e"}
	bizapi.SetService(svc)
	routerapi.SetAuth = svc.BearerMiddleware()
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	routerapi.Register(h)
	_ = ctx

	return &stack{cfg: cfg, store: store, sdk: sdkCtx, fake: fake, llm: llmMock,
		runner: runnerRef, reg: reg, engine: h, cancel: cancel}
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
func (s *stack) setEvent(action, headSHA string) {
	s.fake.mu.Lock()
	defer s.fake.mu.Unlock()
	s.fake.ev = &provider.NormalizedEvent{
		ID: "evt", Type: "cr." + action, Source: "gitea",
		Actor: &provider.CRUser{Username: "alice"},
		Repo:  &provider.EventRepo{Owner: "acme", Name: "app"},
		CR: &provider.ChangeRequest{Number: "7", HeadSHA: headSHA,
			SourceBranch: "feat/login", TargetBranch: "main", Draft: false},
	}
}

// postWebhook 通过 hertz engine 发 webhook。
func (s *stack) postWebhook(t TestingT, delivery string) (int, webhookResp) {
	body := `{"action":"x"}`
	headers := []utHeader{
		{Key: "Content-Type", Value: "application/json"},
		{Key: "X-Test-Signature", Value: "e2e-sign-secret"},
	}
	if delivery != "" {
		headers = append(headers, utHeader{Key: "X-Gitea-Delivery", Value: delivery})
	}
	rec := performRequest(s.engine, "POST", "/webhook/gitea", body, headers)
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
