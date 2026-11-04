package context

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	datacache "github.com/oekit/data/cache"
	"github.com/oekit/logx"

	// 各平台 backend 在 init() 里注册到 provider 工厂
	_ "github.com/yi-nology/go-git-platform/backends/all"
	"github.com/yi-nology/go-git-platform/provider"

	"argus/internal/config"
)

// SDKCtx L5 上下文服务：git-platform-sdk 的薄封装（diff/issue/文件/webhook 解析）。
// design §10.2：SDK 侧 RetryConfig 重试 1 次，仍败上抛（不吞错）。
type SDKCtx struct {
	provsMu        sync.RWMutex
	provs          map[string]provider.Provider
	log            logx.Logger
	appTokens      *installationTokenSource // GitHub App token 源（nil = 未启用）
	sdkCallTimeout time.Duration            // 单次 SDK 调用兜底超时（<=0 = 默认 120s；<0 = 关闭）

	branchMu  sync.Mutex                    // branchCch 懒建护栏
	branchCch *datacache.LocalCache[string] // 默认分支缓存（v5.3.3，TTL 5 分钟）
}

// defaultSDKCallTimeout ARGUS_SDK_TIMEOUT 未配置时的兜底值。
// 弱网/VPN 下 TCP 连接可能停滞且不报错，无超时的调用会永久挂起（worker/poster 卡死）。
const defaultSDKCallTimeout = 120 * time.Second

// bound 为 SDK 调用兜底超时：入参 ctx 已有 deadline 则原样透传（尊重调用方语义），
// 否则按 sdkCallTimeout（<=0 取默认）限时。取消时 SDK 内部请求随 ctx 中止。
func (s *SDKCtx) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	t := s.sdkCallTimeout
	if t == 0 {
		t = defaultSDKCallTimeout
	}
	if t < 0 {
		return ctx, func() {} // 显式关闭兜底
	}
	return context.WithTimeout(ctx, t)
}

// WithSDKCallTimeout 覆盖兜底超时（测试/运维调优用）。
func (s *SDKCtx) WithSDKCallTimeout(d time.Duration) *SDKCtx {
	s.sdkCallTimeout = d
	return s
}

// appTokenManager/Stop 已随 v5.3.2 TokenSource 迁移删除：installation token 走
// installationTokenSource 每请求取值 + 临期懒续期，无后台 goroutine、无停机面。

// NewSDKCtx 按配置为每个已配 token 的平台构造 provider。
// GitHub 平台支持两种认证方式：
//   - PAT（ARGUS_GITHUB_TOKEN）：静态 token，无需刷新
//   - GitHub App（ARGUS_GITHUB_APP_ID + PRIVATE_KEY + INSTALLATION_ID）：installation
//     token，TokenSource 每请求取值 + 临期懒续期（v5.3.2 起，无后台刷新循环）
func NewSDKCtx(cfg *config.Config, log logx.Logger) (*SDKCtx, error) {
	s := &SDKCtx{provs: map[string]provider.Provider{}, log: log, sdkCallTimeout: cfg.SDKCallTimeout}
	retry := &provider.RetryConfig{
		MaxRetries: 1, BaseDelay: 500 * time.Millisecond, MaxDelay: 5 * time.Second,
	}
	for name, pc := range cfg.Platforms {
		// GitHub App 认证：三者全配时优先于 PAT
		if name == "github" && pc.GitHubAppEnabled() {
			s.initGitHubApp(name, pc, retry, log)
			continue
		}

		// 常规 PAT 认证
		if pc.Token == "" {
			continue
		}
		if pc.Secret == "" {
			log.Warn("argus.sdkctx.no_webhook_secret", "platform", name,
				"hint", "未配置 SECRET：入站 webhook 将跳过签名校验（SDK 语义），生产必须配置")
		}
		if pc.SkipTLS {
			log.Warn("argus.sdkctx.skip_tls", "platform", name,
				"hint", "已跳过 TLS 证书校验（内网自建实例），请勿在公网环境开启")
		}
		p, err := newProviderFor(name, pc, pc.Token, retry, log, nil)
		if err != nil {
			// 平台不可达不阻塞启动（学习期/内网常态）；readyz 与请求期报错可见
			log.Error("argus.sdkctx.platform_init_failed", "platform", name, "error", err.Error())
			continue
		}
		s.provs[name] = p
		log.Info("argus.sdkctx.platform_ready", "platform", name)
	}
	return s, nil
}

// newProviderFor 平台 provider 构造单一出处（PAT / GitHub App 两路共用）；
// ts 非 nil 时交给上游 TokenSource（每请求取值，优先于静态 token）。
func newProviderFor(name string, pc config.PlatformConfig, token string,
	retry *provider.RetryConfig, log logx.Logger, ts provider.TokenSource) (provider.Provider, error) {
	platform := provider.Platform(name)
	if name == "github" && pc.GitHubAppEnabled() {
		platform = provider.PlatformGitHub
	}
	return provider.NewProvider(provider.Config{
		Platform:            platform,
		BaseURL:             pc.BaseURL,
		Token:               token,
		SkipTLS:             pc.SkipTLS,
		TokenSource:         ts,
		TokenStyle:          pc.TokenStyle,
		RetryConfig:         retry,
		Logger:              log,
		ConditionalRequests: pc.ConditionalRequests,
	})
}

// initGitHubApp GitHub App 认证初始化：installation token 启动期首取（fail-fast：
// 凭据坏 ⇒ 该平台启动即判死，不拖到首请求）→ provider 构造挂 TokenSource（v5.3.2：
// 每请求取值 + 临期懒续期，替代旧的 50min ticker + provider 重建——过期 token 在飞
// 请求的 401 窗口归零）。初始化失败仅告警跳过（该平台不可用，不阻塞其他平台）。
func (s *SDKCtx) initGitHubApp(name string, pc config.PlatformConfig,
	retry *provider.RetryConfig, log logx.Logger) {
	token, expires, err := generateInstallationToken(
		pc.GitHubAppID, pc.GitHubAppPrivateKey, pc.GitHubAppInstallationID)
	if err != nil {
		log.Error("argus.githubapp.init_failed", "error", err.Error())
		return
	}
	ts := newInstallationTokenSource(pc, token, expires, log)
	p, err := newProviderFor(name, pc, "", retry, log, ts)
	if err != nil {
		log.Error("argus.sdkctx.platform_init_failed", "platform", name, "error", err.Error())
		return
	}
	s.provs[name] = p
	s.appTokens = ts

	log.Info("argus.sdkctx.platform_ready", "platform", name,
		"auth", "github_app", "app_id", pc.GitHubAppID,
		"expires_at", expires.Format(time.RFC3339))
}

// RegisterProvider 测试/e2e 注入口：覆盖某平台的 provider（fake 后端）。
func (s *SDKCtx) RegisterProvider(platform string, p provider.Provider) {
	if s.provs == nil {
		s.provs = map[string]provider.Provider{}
	}
	s.provs[strings.ToLower(platform)] = p
}

// NewSDKCtxStubbed 直接以给定 providers 构造（测试/e2e 打桩用，跳过真实客户端初始化）。
func NewSDKCtxStubbed(log logx.Logger, provs map[string]provider.Provider) *SDKCtx {
	copied := make(map[string]provider.Provider, len(provs))
	for k, v := range provs {
		copied[strings.ToLower(k)] = v
	}
	return &SDKCtx{provs: copied, log: log}
}

// Provider 取平台客户端。
func (s *SDKCtx) Provider(platform string) (provider.Provider, error) {
	s.provsMu.RLock()
	p, ok := s.provs[strings.ToLower(platform)]
	s.provsMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("sdkctx: 平台 %q 未配置（检查 ARGUS_%s_TOKEN）",
			platform, strings.ToUpper(platform))
	}
	return p, nil
}

// Ready 哪些平台可用（readyz）。
func (s *SDKCtx) Ready() []string {
	s.provsMu.RLock()
	defer s.provsMu.RUnlock()
	out := make([]string, 0, len(s.provs))
	for k := range s.provs {
		out = append(out, k)
	}
	return out
}

// HasGitHubApp 是否启用 GitHub App 认证（installationTokenSource 已就绪）。
func (s *SDKCtx) HasGitHubApp() bool {
	return s.appTokens != nil
}

// GitHubAppToken 返回当前 GitHub App installation token（workcopy clone 用）。
// 临期时经 TokenSource 同步续期（clone 路径无上游 ctx ⇒ Background，客户端自身
// 15s 兜底超时）；未启用或获取失败返回空串，调用方回落 PAT。
func (s *SDKCtx) GitHubAppToken(ctx context.Context) string {
	if s.appTokens == nil {
		return ""
	}
	tok, err := s.appTokens.Token(ctx)
	if err != nil {
		s.log.Warn("argus.githubapp.token_for_clone_failed", "error", err.Error(),
			"hint", "回落 PAT 凭证")
		return ""
	}
	return tok
}
