package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/commentcmd"
	"github.com/yi-nology/argus/internal/config"
)

type testLogger struct{}

func (testLogger) Info(string, ...any)  {}
func (testLogger) Warn(string, ...any)  {}
func (testLogger) Error(string, ...any) {}

func bearerEngine(token string) *server.Hertz {
	svc := &Service{Cfg: &config.Config{APIToken: token}, Log: testLogger{}}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/agents", append([]app.HandlerFunc{svc.BearerMiddleware()}, func(ctx context.Context, c *app.RequestContext) {
		c.String(http.StatusOK, "ok")
	})...)
	return h
}

func doGet(h *server.Hertz, auth string) *ut.ResponseRecorder {
	headers := []ut.Header{{Key: "Content-Type", Value: "application/json"}}
	if auth != "" {
		headers = append(headers, ut.Header{Key: "Authorization", Value: auth})
	}
	return ut.PerformRequest(h.Engine, http.MethodGet, "/agents", nil, headers...)
}

// v3.0.2 回归：平台 secret 未配置时 webhook fail-closed 503（SDK 空 secret 会跳过
// 签名校验，此前请求期照常放行等于无鉴权）。
func TestWebhookNoSecretFailsClosed(t *testing.T) {
	svc := &Service{Cfg: &config.Config{Platforms: map[string]config.PlatformConfig{
		"gitea": {Token: "tk", Secret: ""}, // 有 token 无 secret：升级后必须拒绝
	}}, Log: testLogger{}}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.POST("/webhook/:platform", func(ctx context.Context, c *app.RequestContext) {
		_, code := svc.Webhook(ctx, c, c.Param("platform"))
		c.SetStatusCode(code)
	})
	resp := ut.PerformRequest(h.Engine, http.MethodPost, "/webhook/gitea", nil)
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("空 secret 应 503 fail-closed，got %d", resp.Code)
	}
	// 对照：配置了 secret 的平台放行进入后续流程（签名会失败→401，而不是 503）
	svc.Cfg.Platforms["gitea"] = config.PlatformConfig{Token: "tk", Secret: "s"}
	h2 := server.New(server.WithHostPorts("127.0.0.1:0"))
	h2.POST("/webhook/:platform", func(ctx context.Context, c *app.RequestContext) {
		_, code := svc.Webhook(ctx, c, c.Param("platform"))
		c.SetStatusCode(code)
	})
	resp2 := ut.PerformRequest(h2.Engine, http.MethodPost, "/webhook/gitea", nil,
		ut.Header{Key: "Content-Type", Value: "application/json"})
	if resp2.Code == http.StatusServiceUnavailable {
		t.Fatal("配了 secret 不应 503（应走到签名校验 400/401）")
	}
}

// 已配置 token：401 / 401 / 200。
func TestBearerWithToken(t *testing.T) {
	h := bearerEngine("secret-1")
	if code := doGet(h, "").Code; code != http.StatusUnauthorized {
		t.Fatalf("无 header 应 401，got %d", code)
	}
	if code := doGet(h, "Bearer wrong").Code; code != http.StatusUnauthorized {
		t.Fatalf("错 token 应 401，got %d", code)
	}
	if code := doGet(h, "Bearer secret-1").Code; code != http.StatusOK {
		t.Fatalf("对 token 应 200，got %d", code)
	}
}

// v3.0.1 修复回归：token 未配置时 🔒 路由 fail-closed（503），不再放行。
func TestBearerNoTokenFailsClosed(t *testing.T) {
	h := bearerEngine("")
	resp := doGet(h, "")
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 token 应 503 拒绝服务（原实现放行是鉴权裸奔），got %d", resp.Code)
	}
	if resp.Code == http.StatusOK {
		t.Fatal("fail-open 回归")
	}
}

// Content-Length 预检：超大 body 返回 413（在 body 读取前拒绝）。
func TestWebhook_BodyTooLarge(t *testing.T) {
	svc := &Service{
		Cfg: &config.Config{
			WebhookMaxBody: 1024, // 1KB 限制
			Platforms: map[string]config.PlatformConfig{
				"gitea": {Token: "tk", Secret: "s"},
			},
		},
		Log: testLogger{},
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.POST("/webhook/:platform", func(ctx context.Context, c *app.RequestContext) {
		_, code := svc.Webhook(ctx, c, c.Param("platform"))
		c.SetStatusCode(code)
	})

	// Content-Length 超限 → 413（body 读取前即拒绝，不依赖 SDK）
	resp := ut.PerformRequest(h.Engine, http.MethodPost, "/webhook/gitea", nil,
		ut.Header{Key: "Content-Type", Value: "application/json"},
		ut.Header{Key: "Content-Length", Value: "9999"},
	)
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大 body 应 413，got %d", resp.Code)
	}

	// WebhookMaxBody=0 时跳过预检（不返回 413）
	svc.Cfg.WebhookMaxBody = 0
	resp2 := ut.PerformRequest(h.Engine, http.MethodPost, "/webhook/gitea", nil,
		ut.Header{Key: "Content-Type", Value: "application/json"},
		ut.Header{Key: "Content-Length", Value: "9999"},
	)
	if resp2.Code == http.StatusRequestEntityTooLarge {
		t.Fatal("WebhookMaxBody=0 时不应 413")
	}
}

func TestExtractSuppressFP(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"suppress fp:abcd1234", "abcd1234", true},
		{"suppress fp:ABCD1234 更正", "abcd1234", true},
		{"suppress fp:abc", "", false},     // 短码过短
		{"suppress fp:xyz123!", "", false}, // 非十六进制
		{"ask 这段代码为什么这样写", "", false},      // 非 suppress 指令
	}
	for _, c := range cases {
		got, ok := commentcmd.ExtractSuppressFP(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("commentcmd.ExtractSuppressFP(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestExtractRestoreFP(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"restore fp:abcd1234", "abcd1234", true},
		{"restore fp:ABCD1234 更正", "abcd1234", true},
		{"restore fp:abc", "", false},       // 短码过短
		{"restore fp:xyz123!", "", false},   // 非十六进制
		{"suppress fp:abcd1234", "", false}, // 反向指令不得命中 restore
		{"ask 这段代码为什么这样写", "", false},       // 非 restore 指令
	}
	for _, c := range cases {
		got, ok := commentcmd.ExtractRestoreFP(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("commentcmd.ExtractRestoreFP(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

// v3.5.3 回归：三种平台的评论事件判定——GitHub issue_comment、GitLab cr.note
// （SDK 归一化产物，此前不认导致生产 GitLab @argus 无响应）、Gitea/Forgejo 兜底探测。
func TestIsCommentEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   provider.NormalizedEvent
		want bool
	}{
		{"github issue_comment", provider.NormalizedEvent{Type: "issue_comment"}, true},
		{"gitlab cr.note", provider.NormalizedEvent{Type: "cr.note"}, true},
		{"gitlab cr.open", provider.NormalizedEvent{Type: "cr.open"}, false},
		{"白名单外已知事件", provider.NormalizedEvent{Type: "cr.comment"}, false},
		{"gitea 未识别+comment 负载", provider.NormalizedEvent{RawPayload: json.RawMessage(`{"comment":{"body":"hi"}}`)}, true},
		{"gitea push（无 comment 负载）", provider.NormalizedEvent{RawPayload: json.RawMessage(`{"ref":"refs/heads/main"}`)}, false},
		{"空事件", provider.NormalizedEvent{}, false},
	}
	for _, c := range cases {
		if got := commentcmd.IsCommentEvent(&c.ev); got != c.want {
			t.Errorf("%s: isCommentEvent = %v, want %v", c.name, got, c.want)
		}
	}
}

// v3.5.3 回归：评论负载归一化——GitLab note 形状（object_attributes/merge_request）
// 与 GitHub/Gitea issue 形状折叠为同一 commentMeta；系统注释/非 MR/普通 issue 静默忽略。
func TestParseCommentMeta(t *testing.T) {
	ev := func(typ string, raw string) *provider.NormalizedEvent {
		return &provider.NormalizedEvent{Type: typ, RawPayload: json.RawMessage(raw)}
	}
	cases := []struct {
		name     string
		ev       *provider.NormalizedEvent
		wantOK   bool
		wantBody string
		wantNum  int
	}{
		{
			"gitlab MR note", ev("cr.note", `{"object_attributes":{"note":"@argus 请重新审查","system":false},"merge_request":{"iid":7}}`),
			true, "@argus 请重新审查", 7,
		},
		{
			"gitlab 系统注释", ev("cr.note", `{"object_attributes":{"note":"assigned to @bob","system":true},"merge_request":{"iid":7}}`),
			false, "", 0,
		},
		{
			"gitlab commit note（非 MR）", ev("cr.note", `{"object_attributes":{"note":"lgtm"},"merge_request":{}}`),
			false, "", 0,
		},
		{
			"github issue_comment", ev("issue_comment", `{"comment":{"body":"@argus ask 为什么"},"issue":{"number":12,"pull_request":{"url":"https://api.github.com/x/pr/12"}}}`),
			true, "@argus ask 为什么", 12,
		},
		{
			// Gitea 的 pull_request 元数据无 url 字段（diff_url/html_url 形状），只判存在性
			"gitea PR 评论", ev("", `{"comment":{"body":"@argus hi"},"issue":{"number":7,"pull_request":{"diff_url":"x","html_url":"y"}}}`),
			true, "@argus hi", 7,
		},
		{
			"普通 issue 评论（无 pull_request）", ev("issue_comment", `{"comment":{"body":"hi"},"issue":{"number":3}}`),
			false, "", 0,
		},
		{
			"负载非法", ev("issue_comment", `{not-json`),
			false, "", 0,
		},
	}
	for _, c := range cases {
		meta, ok := commentcmd.ParseCommentMeta(c.ev)
		if ok != c.wantOK || meta.Body != c.wantBody || meta.PRNumber != c.wantNum {
			t.Errorf("%s: parseCommentMeta = (%+v,%v), want body=%q num=%d ok=%v",
				c.name, meta, ok, c.wantBody, c.wantNum, c.wantOK)
		}
	}
}

// v3.5.5 回归：GitHub SDK 对 issue_comment 不填 Actor/Repo（default 分支只置 Type），
// normalizeCommentEvent 必须从原始负载补齐，否则评论链路在 Repo nil 处静默早退。
func TestNormalizeCommentEvent(t *testing.T) {
	ev := &provider.NormalizedEvent{
		Type: "issue_comment",
		RawPayload: json.RawMessage(`{
			"sender": {"login": "alice"},
			"repository": {"id": 7, "full_name": "acme/app"},
			"comment": {"body": "@argus hi"}
		}`),
	}
	commentcmd.NormalizeCommentEvent(ev)
	if ev.Repo == nil || ev.Repo.Owner != "acme" || ev.Repo.Name != "app" {
		t.Fatalf("Repo 应从 repository.full_name 补齐: %+v", ev.Repo)
	}
	if ev.Actor == nil || ev.Actor.Username != "alice" {
		t.Fatalf("Actor 应从 sender.login 补齐: %+v", ev.Actor)
	}

	// 已有值不覆盖；Actor 缺 sender 时用 user.username 兜底（GitLab 形状）
	ev2 := &provider.NormalizedEvent{
		Type: "issue_comment",
		Repo: &provider.EventRepo{Owner: "keep", Name: "me"},
		RawPayload: json.RawMessage(`{
			"repository": {"name": "n", "owner": {"login": "o"}},
			"user": {"username": "gl-user"}
		}`),
	}
	commentcmd.NormalizeCommentEvent(ev2)
	if ev2.Repo.Owner != "keep" || ev2.Repo.Name != "me" {
		t.Fatalf("已有 Repo 不应被覆盖: %+v", ev2.Repo)
	}
	if ev2.Actor == nil || ev2.Actor.Username != "gl-user" {
		t.Fatalf("Actor 应从 user.username 兜底: %+v", ev2.Actor)
	}

	// 空负载：保持 nil，不 panic
	ev3 := &provider.NormalizedEvent{Type: "cr.note"}
	commentcmd.NormalizeCommentEvent(ev3)
	if ev3.Repo != nil || ev3.Actor != nil {
		t.Fatalf("空负载不应产生补齐: %+v %+v", ev3.Repo, ev3.Actor)
	}
}
