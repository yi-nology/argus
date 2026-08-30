package context

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.example.com/enjoydream/ekit/observability/logx"

	"argus/internal/plugin"
)

// Workcopy 工作副本沙箱服务（design §3.2 cli 适配器前置）：
// 浅克隆 base 默认分支 + fetch PR head + checkout，供 cli 插件在真实工作树上执行。
// 凭证：复用平台 token（SDK 凭证同一来源），嵌在 clone URL 中，绝不落盘。
// 生命周期：任务结束 Release 删目录；兜底 TTL 扫描（1 小时）清泄漏（§10.4）。
type Workcopy struct {
	Root string
	log  logx.Logger

	// CredentialOf 返回 (cloneBaseURL, token)：由 main 按平台注入。
	CredentialOf func(platform string) (baseURL, token string, ok bool)
	// InsecureTLSOf 平台是否免 TLS 校验（自签/过期证书的内网实例），决定 GIT_SSL_NO_VERIFY。
	InsecureTLSOf func(platform string) bool

	mu      sync.Mutex
	entries map[string]*wcEntry
}

type wcEntry struct {
	dir      string
	refCount int
	lastUsed time.Time
}

func NewWorkcopy(root string, log logx.Logger) *Workcopy {
	return &Workcopy{Root: root, log: log, entries: map[string]*wcEntry{}}
}

func wcKey(pr plugin.PRContext) string {
	return fmt.Sprintf("%s-%s-%s-%s@%s", pr.Platform, pr.Owner, pr.Repo, pr.Number, pr.HeadSHA)
}

// Ensure 返回就绪的工作副本目录（浅克隆 base + checkout PR head）。
// 同 PR+SHA 并发调用共享同一目录（引用计数）。
func (w *Workcopy) Ensure(ctx context.Context, pr plugin.PRContext) (string, error) {
	if w.CredentialOf == nil {
		return "", fmt.Errorf("workcopy: 未配置 CredentialOf")
	}
	baseURL, token, ok := w.CredentialOf(pr.Platform)
	if !ok || token == "" {
		return "", fmt.Errorf("workcopy: 平台 %s 缺少 clone 凭证", pr.Platform)
	}
	if pr.HeadSHA == "" {
		return "", fmt.Errorf("workcopy: PR %s/%s#%s 无 head SHA", pr.Owner, pr.Repo, pr.Number)
	}

	key := wcKey(pr)
	w.mu.Lock()
	if e, ok := w.entries[key]; ok {
		e.refCount++
		e.lastUsed = time.Now()
		dir := e.dir
		w.mu.Unlock()
		return dir, nil
	}
	w.mu.Unlock()

	dir, err := w.prepare(ctx, pr, baseURL, token)
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	// 双检：并发 prepare 同 key 时保留先到者
	if e, ok := w.entries[key]; ok {
		e.refCount++
		w.mu.Unlock()
		_ = os.RemoveAll(dir)
		return e.dir, nil
	}
	w.entries[key] = &wcEntry{dir: dir, refCount: 1, lastUsed: time.Now()}
	w.mu.Unlock()
	return dir, nil
}

// Release 任务结束后释放（引用归零即删沙箱目录）。
func (w *Workcopy) Release(pr plugin.PRContext) {
	key := wcKey(pr)
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.entries[key]
	if !ok {
		return
	}
	e.refCount--
	if e.refCount <= 0 {
		_ = os.RemoveAll(e.dir)
		delete(w.entries, key)
	}
}

// refspec 按 platform 返回 PR head 的 fetch refspec。
func prRefspec(platform, number string) string {
	switch strings.ToLower(platform) {
	case "gitlab":
		return fmt.Sprintf("refs/merge-requests/%s/head", number)
	default: // gitea/github/forgejo 等同 GitHub 习惯
		return fmt.Sprintf("refs/pull/%s/head", number)
	}
}

// cloneURL 由 base URL 推导 https clone 地址并内嵌凭证。
// file:// 前缀直通（e2e 本地 fixture 仓库），不嵌凭证。
func cloneURL(baseURL, owner, repo, token string) string {
	u := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if strings.HasPrefix(u, "file://") {
		return u
	}
	for _, suffix := range []string{"/api/v1", "/api/v4", "/api/v3", "/api"} {
		u = strings.TrimSuffix(u, suffix)
	}
	if !strings.HasPrefix(u, "http") {
		u = "https://" + u
	}
	// https://oauth2:<token>@host/owner/repo.git（Gitea/GitHub/GitLab 通用习惯）
	return fmt.Sprintf("https://oauth2:%s@%s/%s/%s.git", token, stripScheme(u), owner, repo)
}

func stripScheme(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		return u[i+3:]
	}
	return u
}

func (w *Workcopy) prepare(ctx context.Context, pr plugin.PRContext, baseURL, token string) (string, error) {
	if err := os.MkdirAll(w.Root, 0o755); err != nil {
		return "", fmt.Errorf("workcopy: 根目录创建失败: %w", err)
	}
	dir, err := os.MkdirTemp(w.Root, "wc-")
	if err != nil {
		return "", fmt.Errorf("workcopy: 建沙箱失败: %w", err)
	}
	branch := pr.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	url := cloneURL(baseURL, pr.Owner, pr.Repo, token)
	insecureTLS := w.InsecureTLSOf != nil && w.InsecureTLSOf(pr.Platform)

	// 浅克隆默认分支 → fetch PR head → checkout（三步都限定超时由 ctx 控制）
	steps := [][]string{
		{"git", "clone", "--depth", "1", "--branch", branch, url, dir},
		{"git", "-C", dir, "fetch", "--depth", "1", "origin", prRefspec(pr.Platform, pr.Number)},
		{"git", "-C", dir, "checkout", "--force", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if err := runGit(ctx, args, insecureTLS); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("workcopy: %s#%s 准备失败: %w", pr.Owner+"/"+pr.Repo, pr.Number, err)
		}
	}
	w.log.Info("argus.workcopy.ready", "dir", dir, "pr", pr.Owner+"/"+pr.Repo+"#"+pr.Number)
	return dir, nil
}

func runGit(ctx context.Context, args []string, insecureTLS bool) error {
	c, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(c, args[0], args[1:]...)
	if insecureTLS {
		// 平台 SkipTLS=1 时克隆同步免校验（凭证走同一条 TLS 通道，风险已在启动期告警）
		cmd.Env = append(os.Environ(), "GIT_SSL_NO_VERIFY=true")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 400 {
			tail = tail[len(tail)-400:]
		}
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(tail))
	}
	return nil
}

// StartTTLScavenger 兜底 TTL 扫描（每小时）：清引用计数泄漏的沙箱目录（§10.4）。
func (w *Workcopy) StartTTLScavenger(ctx context.Context, ttl time.Duration) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.sweep(ttl)
			}
		}
	}()
}

func (w *Workcopy) sweep(ttl time.Duration) {
	w.mu.Lock()
	var stale []string
	for k, e := range w.entries {
		if e.refCount <= 0 || time.Since(e.lastUsed) > ttl {
			stale = append(stale, e.dir)
			delete(w.entries, k)
		}
	}
	w.mu.Unlock()
	for _, d := range stale {
		_ = os.RemoveAll(d)
		w.log.Warn("argus.workcopy.swept", "dir", filepath.Base(d))
	}
}
