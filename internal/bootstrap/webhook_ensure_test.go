package bootstrap

import (
	"context"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/poller"
)

// hookFake 只实现 WebhookManager 三方法（EnsureWebhook 的消费面）；未覆盖方法借
// 嵌入接口 panic 兜底。
type hookFake struct {
	provider.Provider
	existing   []*provider.PlatformWebhook
	created    provider.CreateWebhookOptions
	createN    int
	patched    *provider.UpdateWebhookOptions
	updateN    int
	nextHookID int64
}

func (f *hookFake) ListWebhooks(_ context.Context, _, _ string) ([]*provider.PlatformWebhook, error) {
	return f.existing, nil
}

func (f *hookFake) CreateWebhook(_ context.Context, opts provider.CreateWebhookOptions) (*provider.PlatformWebhook, error) {
	f.createN++
	f.created = opts
	f.nextHookID++
	return &provider.PlatformWebhook{ID: f.nextHookID, URL: opts.URL}, nil
}

func (f *hookFake) GetWebhook(_ context.Context, _, _ string, _ int64) (*provider.PlatformWebhook, error) {
	return &provider.PlatformWebhook{ID: 1}, nil
}

func (f *hookFake) UpdateWebhook(_ context.Context, _, _ string, _ int64, opts provider.UpdateWebhookOptions) (*provider.PlatformWebhook, error) {
	f.updateN++
	f.patched = &opts
	return &provider.PlatformWebhook{ID: 1}, nil
}

// TestEnsureWebhooksCreatesWithSecret 无既有钩子 → EnsureCreated，secret 随建
// 生效不补写（gp#34 EnsureSecretAlwaysUpdate 语义：仅 match 分支重写，v5.5.1 收编）。
func TestEnsureWebhooksCreatesAndPatchesSecret(t *testing.T) {
	f := &hookFake{}
	cfg := &config.Config{
		PublicURL: "http://argus:8899",
		Platforms: map[string]config.PlatformConfig{
			"gitea": {Token: "t", Secret: "s1"},
		},
	}
	targets := []poller.Target{{Platform: "gitea", Owner: "o", Repo: "r"}}
	ensureWebhooks(context.Background(), stubSDK{"gitea": f}, cfg, logx.Nop(), targets)

	if f.createN != 1 {
		t.Fatalf("应创建 1 次，got %d", f.createN)
	}
	if f.created.URL != "http://argus:8899/webhook/gitea" {
		t.Fatalf("回调 URL 错: %q", f.created.URL)
	}
	if len(f.created.Events) != 2 || f.created.Events[0] != "pull_request_only" || f.created.Events[1] != "issue_comment" {
		t.Fatalf("gitea 默认事件词表错: %v", f.created.Events)
	}
	if f.updateN != 0 {
		t.Fatalf("created 分支不应补写（secret 随建生效）: updateN=%d", f.updateN)
	}
}

// TestEnsureWebhooksMatchRotatesSecret URL+事件集匹配 → AlwaysUpdate 重写 secret
// （轮换收敛进单次 ensure 往返，替代 v5.4.1 时代的后置补写 workaround）。
func TestEnsureWebhooksMatchRotatesSecret(t *testing.T) {
	f := &hookFake{existing: []*provider.PlatformWebhook{
		{ID: 7, URL: "http://argus:8899/webhook/gitea", Events: []string{"pull_request_only", "issue_comment"}},
	}}
	cfg := &config.Config{
		PublicURL: "http://argus:8899",
		Platforms: map[string]config.PlatformConfig{
			"gitea": {Token: "t", Secret: "rotated"},
		},
	}
	targets := []poller.Target{{Platform: "gitea", Owner: "o", Repo: "r"}}
	ensureWebhooks(context.Background(), stubSDK{"gitea": f}, cfg, logx.Nop(), targets)

	if f.createN != 0 {
		t.Fatalf("match 不应新建: createN=%d", f.createN)
	}
	if f.updateN != 1 || f.patched == nil || f.patched.Secret == nil || *f.patched.Secret != "rotated" {
		t.Fatalf("match 应重写 secret（EnsureSecretAlwaysUpdate）: updateN=%d patched=%v", f.updateN, f.patched)
	}
}

// TestEnsureWebhooksSkipsNoSecret 缺 secret：入站 fail-closed 全拒，建了也白建 →
// 跳过且零 API 调用。
func TestEnsureWebhooksSkipsNoSecret(t *testing.T) {
	f := &hookFake{}
	cfg := &config.Config{
		PublicURL: "http://argus:8899",
		Platforms: map[string]config.PlatformConfig{
			"gitea": {Token: "t"},
		},
	}
	ensureWebhooks(context.Background(), stubSDK{"gitea": f}, cfg, logx.Nop(),
		[]poller.Target{{Platform: "gitea", Owner: "o", Repo: "r"}})
	if f.createN != 0 || f.updateN != 0 {
		t.Fatalf("缺 secret 应跳过，got create=%d update=%d", f.createN, f.updateN)
	}
}

// TestEventsForOverride 显式覆盖优先于内置默认。
func TestEventsForOverride(t *testing.T) {
	pc := config.PlatformConfig{WebhookEvents: " pull_request_only , issue_comment , push "}
	got := eventsFor(pc, "gitea")
	if len(got) != 3 || got[2] != "push" {
		t.Fatalf("覆盖词表解析错: %v", got)
	}
	if eventsFor(config.PlatformConfig{}, "unknown-platform") != nil {
		t.Fatal("无默认词表且无覆盖应返回 nil（调用方跳过）")
	}
}

// stubSDK webhookSDK 打桩。
type stubSDK map[string]provider.Provider

func (s stubSDK) Provider(platform string) (provider.Provider, error) {
	if p, ok := s[platform]; ok {
		return p, nil
	}
	return nil, context.Canceled
}

// TestGitlabEventsIncludeNote 钉 GitLab 事件词表含 note（评论）事件——上游
// go-git-platform v0.101.1（#32）修复后 SDK 面可表达；词表漏掉它 = GitLab
// 评论命令链路静默不通（ensure 建出的钩子收不到评论）。
func TestGitlabEventsIncludeNote(t *testing.T) {
	evs := eventsFor(config.PlatformConfig{}, "gitlab")
	found := false
	for _, e := range evs {
		if e == "note" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gitlab 默认词表缺 note 事件: %v", evs)
	}
}

// TestWebhookEventsCoverSupportedPlatforms 事件词表完备性：config.SupportedPlatforms
// 每个平台必须有内置默认词表——缺条目时 eventsFor 返回 nil，该平台 webhook ensure
// 静默跳过（自动注册失效），曾使 forgejo 成为死条目（词表有、config 装载无）。
func TestWebhookEventsCoverSupportedPlatforms(t *testing.T) {
	for _, p := range config.SupportedPlatforms {
		if evs, ok := webhookEvents[p]; !ok || len(evs) == 0 {
			t.Fatalf("webhookEvents 缺平台 %q 的默认词表（config.SupportedPlatforms 有）", p)
		}
	}
}
