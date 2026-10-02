package context

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.example.com/enjoydream/ekit/concurrency/async"
	"git.example.com/enjoydream/ekit/observability/logx"
	// 各平台 backend 在 init() 里注册到 provider 工厂
	_ "github.com/yi-nology/go-git-platform/backends/all"
	"github.com/yi-nology/go-git-platform/provider"

	"argus/internal/config"
	"argus/internal/plugin"
)

// SDKCtx L5 上下文服务：git-platform-sdk 的薄封装（diff/issue/文件/webhook 解析）。
// design §10.2：SDK 侧 RetryConfig 重试 1 次，仍败上抛（不吞错）。
type SDKCtx struct {
	provsMu        sync.RWMutex
	provs          map[string]provider.Provider
	log            logx.Logger
	appTokens      *appTokenManager // GitHub App token 管理（nil = 未启用）
	sdkCallTimeout time.Duration    // 单次 SDK 调用兜底超时（<=0 = 默认 120s；<0 = 关闭）
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

// Stop 停止后台组件（GitHub App token refresher，v3.9.2 收编进停机序列）：
// 此前 refresher 以裸 goroutine 启动且 Stop 全仓无调用方——90s 优雅排水期间
// 仍可能触发 provider 重建。重复调用安全（仅停机序列单点调用）。
func (s *SDKCtx) Stop() {
	s.provsMu.RLock()
	defer s.provsMu.RUnlock()
	if s.appTokens != nil && s.appTokens.refresh != nil {
		s.appTokens.refresh.Stop()
	}
}

// appTokenManager 管理 GitHub App installation token 的生命周期。
// token 由 githubAppRefresher 周期刷新，Current() 返回最新值供 workcopy 使用。
type appTokenManager struct {
	mu      sync.RWMutex
	token   string
	refresh *githubAppRefresher
}

func (m *appTokenManager) Current() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.token
}

func (m *appTokenManager) set(token string) {
	m.mu.Lock()
	m.token = token
	m.mu.Unlock()
}

// NewSDKCtx 按配置为每个已配 token 的平台构造 provider。
// GitHub 平台支持两种认证方式：
//   - PAT（ARGUS_GITHUB_TOKEN）：静态 token，无需刷新
//   - GitHub App（ARGUS_GITHUB_APP_ID + PRIVATE_KEY + INSTALLATION_ID）：installation token，50min 自动刷新
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
		p, err := newProviderFor(name, pc, pc.Token, retry, log)
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

// newProviderFor 平台 provider 构造单一出处（PAT / GitHub App / App 刷新重建三路共用）。
func newProviderFor(name string, pc config.PlatformConfig, token string,
	retry *provider.RetryConfig, log logx.Logger) (provider.Provider, error) {
	platform := provider.Platform(name)
	if name == "github" && pc.GitHubAppEnabled() {
		platform = provider.PlatformGitHub
	}
	return provider.NewProvider(provider.Config{
		Platform:    platform,
		BaseURL:     pc.BaseURL,
		Token:       token,
		SkipTLS:     pc.SkipTLS,
		TokenStyle:  pc.TokenStyle,
		RetryConfig: retry,
		Logger:      log,
	})
}

// initGitHubApp GitHub App 认证初始化：installation token 获取 → provider 构造 →
// 后台 50min 刷新循环（过期时重建 provider 原子替换）。初始化失败仅告警跳过（该
// 平台不可用，不阻塞其他平台）。
func (s *SDKCtx) initGitHubApp(name string, pc config.PlatformConfig,
	retry *provider.RetryConfig, log logx.Logger) {
	tokenMgr := &appTokenManager{}
	// 首次获取 token 并构造 provider
	token, expires, err := generateInstallationToken(
		pc.GitHubAppID, pc.GitHubAppPrivateKey, pc.GitHubAppInstallationID)
	if err != nil {
		log.Error("argus.githubapp.init_failed", "error", err.Error())
		return
	}
	tokenMgr.set(token)
	p, err := newProviderFor(name, pc, token, retry, log)
	if err != nil {
		log.Error("argus.sdkctx.platform_init_failed", "platform", name, "error", err.Error())
		return
	}
	s.provs[name] = p

	// 启动后台刷新：token 过期时重建 provider 替换到 map
	refresher := newGitHubAppRefresher(pc, log, func(newToken string) {
		if newP, err := newProviderFor(name, pc, newToken, retry, log); err == nil {
			s.provsMu.Lock()
			s.provs[name] = newP
			s.provsMu.Unlock()
			tokenMgr.set(newToken)
		} else {
			log.Error("argus.githubapp.rebuild_provider_failed", "error", err.Error())
		}
	})
	refresher.initWithToken(token, expires)
	tokenMgr.refresh = refresher
	s.appTokens = tokenMgr

	// 启动后台刷新循环（非阻塞；GoSafe 防 Start 期 panic 拖垮进程——
	// v3.9.2 收编，Stop 经 main 的 AddResource 停机序列调用）
	async.GoSafe(func() {
		if err := refresher.Start(); err != nil {
			log.Error("argus.githubapp.refresher_start_failed", "error", err.Error())
		}
	})

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

// GitHubAppToken 返回当前 GitHub App installation token（workcopy clone 用）。
// 未启用 GitHub App 时返回空串。
func (s *SDKCtx) GitHubAppToken() string {
	if s.appTokens == nil {
		return ""
	}
	return s.appTokens.Current()
}

// ---------- webhook ----------

// DeliveryHeaders 各平台 delivery ID 请求头（§5.2：幂等键 = 平台 delivery ID，
// 在 ParseWebhookEvent 之前读取；SDK 的 NormalizedEvent.ID 是接收时刻 UnixNano，不可用）。
var DeliveryHeaders = []string{
	"X-Gitea-Delivery",
	"X-GitHub-Delivery",
	"X-Gitlab-Event-UUID",
}

// ExtractDeliveryID 从请求头提取平台 delivery ID；缺失返回空串（调用方 event_id 置 NULL + 告警）。
func (s *SDKCtx) ExtractDeliveryID(headerGetter func(key string) string) string {
	for _, h := range DeliveryHeaders {
		if v := headerGetter(h); v != "" {
			return v
		}
	}
	return ""
}

// ParseWebhook 解析平台原生 webhook（内部先做签名校验；secret 为空时 SDK 会跳过校验，
// 由调用方在配置侧保证 secret 语义）。r 可为从 hertz 原始字节重建的 *http.Request。
func (s *SDKCtx) ParseWebhook(platform string, r *http.Request, secret string) (*provider.NormalizedEvent, error) {
	p, err := s.Provider(platform)
	if err != nil {
		return nil, err
	}
	return p.ParseWebhookEvent(r, secret)
}

// ---------- CR / diff / issue / file ----------

// call 平台调用的单源样板（v3.64.0，此前 12 个方法各写一遍）：Provider 解析 →
// bound ctx（SDK 调用超时）→ 执行。
func call[T any](s *SDKCtx, ctx context.Context, platform string,
	fn func(ctx context.Context, p provider.Provider) (T, error)) (T, error) {
	p, err := s.Provider(platform)
	if err != nil {
		var zero T
		return zero, err
	}
	ctx, cancel := s.bound(ctx)
	defer cancel()
	return fn(ctx, p)
}

// callIface 带能力接口断言的调用：断言失败 = unsupportedErr（断言在 bound 之后，
// 与既有方法顺序一致）。
func callIface[I any, T any](s *SDKCtx, ctx context.Context, platform, iface string,
	fn func(ctx context.Context, cap I) (T, error)) (T, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (T, error) {
		cap, ok := p.(I)
		if !ok {
			var zero T
			return zero, unsupportedErr(platform, iface)
		}
		return fn(ctx, cap)
	})
}

// call2 call 的双返回值变体（如 ListCRs 的 (items, total)）。
func call2[T1, T2 any](s *SDKCtx, ctx context.Context, platform string,
	fn func(ctx context.Context, p provider.Provider) (T1, T2, error)) (T1, T2, error) {
	p, err := s.Provider(platform)
	if err != nil {
		var z1 T1
		var z2 T2
		return z1, z2, err
	}
	ctx, cancel := s.bound(ctx)
	defer cancel()
	return fn(ctx, p)
}

// GetCR 拉取 PR 详情。
func (s *SDKCtx) GetCR(ctx context.Context, platform, owner, repo, number string) (*provider.ChangeRequest, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.ChangeRequest, error) {
		return p.GetCR(ctx, owner, repo, number)
	})
}

// GetCRDiff 拉取 PR diff（结构化文件 + 原始 unified diff）。
func (s *SDKCtx) GetCRDiff(ctx context.Context, platform, owner, repo, number string) (*provider.MergeDiff, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.MergeDiff, error) {
		return p.GetCRDiff(ctx, owner, repo, number)
	})
}

// ListCRs 列出仓库 PR（轮询兜底任务源用）。
func (s *SDKCtx) ListCRs(ctx context.Context, platform string, opts provider.ListCROptions) ([]*provider.ChangeRequest, int, error) {
	return call2(s, ctx, platform, func(ctx context.Context, p provider.Provider) ([]*provider.ChangeRequest, int, error) {
		return p.ListCRs(ctx, opts)
	})
}

// GetRepo 拉取仓库信息（默认分支）。
func (s *SDKCtx) GetRepo(ctx context.Context, platform, owner, repo string) (*provider.PlatformRepo, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.PlatformRepo, error) {
		return p.GetRepo(ctx, owner, repo)
	})
}

// unsupportedErr 平台不支持某接口的统一错误。
func unsupportedErr(platform, iface string) error {
	return fmt.Errorf("sdkctx: 平台 %s 不支持 %s", platform, iface)
}

// GetFileContent 读仓库文件（ref=分支/tag/SHA）。仓库配置只从默认分支读（§3.4 红线 1）。
func (s *SDKCtx) GetFileContent(ctx context.Context, platform, owner, repo, path, ref string) (string, error) {
	return callIface(s, ctx, platform, "FileManager",
		func(ctx context.Context, fm provider.FileManager) (string, error) {
			return fm.GetFileContent(ctx, owner, repo, path, ref)
		})
}

// GetIssue 读 issue（需求源）。
func (s *SDKCtx) GetIssue(ctx context.Context, platform, owner, repo, number string) (*provider.Issue, error) {
	return callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) (*provider.Issue, error) {
			return im.GetIssue(ctx, owner, repo, number)
		})
}

// ListIssueComments 列 issue/PR 评论。v3.92.0：NoteManager（MR Notes API）
// 优先——GitLab 的 MR 与 issue 是两个 iid 命名空间，IssueManager 的评论方法
// 对 MR 必 404；GitHub/Gitea 等 issue 即 PR 的平台无 NoteManager，走原路。
func (s *SDKCtx) ListIssueComments(ctx context.Context, platform, owner, repo, number string) ([]*provider.IssueComment, error) {
	if c, err := callIface(s, ctx, platform, "NoteManager",
		func(ctx context.Context, nm provider.NoteManager) ([]*provider.IssueComment, error) {
			return nm.ListNotes(ctx, owner, repo, number)
		}); err == nil {
		return c, nil
	}
	return callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) ([]*provider.IssueComment, error) {
			return im.ListIssueComments(ctx, owner, repo, number)
		})
}

// CreateIssueComment 发总结评论。
// 优先使用 DiffManager.CreateNote（MR Notes API），因为 Argus 任务对象全部是 CR/PR，
// GitLab 的 IssueManager.CreateIssueComment 走 Issues Notes API 会 404。
// 回退到 IssueManager 仅当平台不实现 DiffManager（如纯 Issue 场景）。
func (s *SDKCtx) CreateIssueComment(ctx context.Context, platform, owner, repo, number, body string) (*provider.IssueComment, error) {
	return call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.IssueComment, error) {
		// 优先走 DiffManager.CreateNote（MR Notes API），兼容所有 CR 场景。
		if dm, ok := p.(provider.DiffManager); ok {
			noteID, err := dm.CreateNote(ctx, owner, repo, number, body)
			if err == nil {
				// 解析失败留痕（深挖修复）：Comment.ID=0 会破坏按评论 ID 幂等更新的锚点
				id, perr := strconv.ParseInt(noteID, 10, 64)
				if perr != nil {
					s.log.Warn("argus.sdkctx.note_id_unparsable", "platform", platform,
						"note_id", noteID, "error", perr.Error())
				}
				return &provider.IssueComment{ID: id, Body: body}, nil
			}
			s.log.Warn("argus.sdkctx.create_note_fallback", "platform", platform, "error", err.Error())
		}
		// 回退到 IssueManager（Issue 评论 API）
		im, ok := p.(provider.IssueManager)
		if !ok {
			return nil, fmt.Errorf("sdkctx: 平台 %s 不支持 DiffManager 也不支持 IssueManager", platform)
		}
		return im.CreateIssueComment(ctx, owner, repo, number, body)
	})
}

// UpdateIssueComment 更新总结评论。v3.92.0 路由序：NoteManager（MR Notes
// API，GitLab 真·原地更新）→ IssueManager（GitHub/Gitea 等 issue 即 PR 的
// 平台）→ v3.91.2 的 DiffManager 原子换新兜底（先 CreateNote 后 DeleteNote，
// 返回新 ID，调用方经 repointIfSwapped 回写缓存）。
func (s *SDKCtx) UpdateIssueComment(ctx context.Context, platform, owner, repo, number string, commentID int64, body string) (*provider.IssueComment, error) {
	if c, err := callIface(s, ctx, platform, "NoteManager",
		func(ctx context.Context, nm provider.NoteManager) (*provider.IssueComment, error) {
			return nm.UpdateNote(ctx, owner, repo, number, strconv.FormatInt(commentID, 10), body)
		}); err == nil {
		return c, nil
	}
	c, err := callIface(s, ctx, platform, "IssueManager",
		func(ctx context.Context, im provider.IssueManager) (*provider.IssueComment, error) {
			return im.UpdateIssueComment(ctx, owner, repo, number, commentID, body)
		})
	if err == nil {
		return c, nil
	}
	fallback, ferr := call(s, ctx, platform, func(ctx context.Context, p provider.Provider) (*provider.IssueComment, error) {
		dm, ok := p.(provider.DiffManager)
		if !ok {
			return nil, err
		}
		nid, cerr := dm.CreateNote(ctx, owner, repo, number, body)
		if cerr != nil {
			s.log.Warn("argus.sdkctx.update_swap_create_failed", "platform", platform, "error", cerr.Error())
			return nil, err
		}
		newID, perr := strconv.ParseInt(nid, 10, 64)
		if perr != nil {
			// 新评论已建但 ID 不可解析：删旧保新（缓存定位失效属可容忍降级）
			s.log.Warn("argus.sdkctx.note_id_unparsable", "platform", platform, "note_id", nid, "error", perr.Error())
			newID = 0
		}
		if derr := dm.DeleteNote(ctx, owner, repo, number, strconv.FormatInt(commentID, 10)); derr != nil {
			s.log.Warn("argus.sdkctx.update_swap_delete_failed", "platform", platform,
				"comment_id", commentID, "error", derr.Error())
		}
		return &provider.IssueComment{ID: newID, Body: body}, nil
	})
	if ferr == nil {
		return fallback, nil
	}
	return c, err
}

// CreateReview 提交行内评论 review。
func (s *SDKCtx) CreateReview(ctx context.Context, platform, owner, repo, number string, opts provider.CreateReviewOptions) (*provider.ReviewResult, error) {
	return callIface(s, ctx, platform, "ReviewManager",
		func(ctx context.Context, rm provider.ReviewManager) (*provider.ReviewResult, error) {
			return rm.CreateReview(ctx, owner, repo, number, opts)
		})
}

// ListReviews 列出 PR 的评审记录（poster 评审幂等锚点用）。
func (s *SDKCtx) ListReviews(ctx context.Context, platform, owner, repo, number string) ([]provider.Review, error) {
	return callIface(s, ctx, platform, "ReviewManager",
		func(ctx context.Context, rm provider.ReviewManager) ([]provider.Review, error) {
			return rm.ListReviews(ctx, owner, repo, number)
		})
}

// LoadRepoConfig 从默认分支读 .reviewagents.yaml（§3.4 红线 1：绝不读 PR head / 工作副本）。
// 读不到（404）返回 EmptyRepoConfig 而非错误。
func (s *SDKCtx) LoadRepoConfig(ctx context.Context, platform, owner, repo, defaultBranch string) (*plugin.RepoConfig, error) {
	const path = ".reviewagents.yaml"
	content, err := s.GetFileContent(ctx, platform, owner, repo, path, defaultBranch)
	if err != nil {
		if provider.IsNotFound(err) {
			return plugin.EmptyRepoConfig(), nil
		}
		return nil, err
	}
	rc, err := plugin.ParseRepoConfig([]byte(content))
	if err != nil {
		// 仓库配置是敌对输入：解析失败按无配置处理 + 告警，不炸任务
		s.log.Warn("argus.sdkctx.repo_config_invalid", "repo", owner+"/"+repo, "error", err.Error())
		return plugin.EmptyRepoConfig(), nil
	}
	return rc, nil
}
