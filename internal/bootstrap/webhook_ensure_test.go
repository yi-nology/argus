package bootstrap

import (
	"context"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"argus/internal/config"
	"argus/internal/poller"
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

// TestEnsureWebhooksCreatesAndPatchesSecret 无既有钩子 → EnsureCreated + secret
// 无条件补写（write-only 不可比对，轮换自愈的关键，v5.4.1）。
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
	if f.updateN != 1 || f.patched == nil || f.patched.Secret == nil || *f.patched.Secret != "s1" {
		t.Fatalf("secret 应补写一次: updateN=%d patched=%v", f.updateN, f.patched)
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
