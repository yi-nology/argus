// service_repomap.go —— 仓库级代码地图数据面（v5.6.2，图谱可视化 D 项）：
// 按需浅克隆工作副本 → BuildRepoMap → 进程内 TTL 缓存。MCP get_repo_map 与
// GET /repos/:owner/:repo/map 双出口共用。
//
// 克隆面纪律：owner/repo 白名单字符集校验（路径/URL 注入防线）；凭据走
// Cfg.Platforms[platform]（与 workcopy.CredentialOf 同源）；URL 含 token 仅落
// pod 内 .git/config（与任务工作副本同水位）；日志零凭据。

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yi-nology/argus/internal/impactview"
)

// repoMapTTL 快照缓存有效期：目录图对"几分钟前的 main"不敏感，30 分钟足够新。
const repoMapTTL = 30 * time.Minute

// repoMapScanTimeout 单次克隆+扫描预算。
const repoMapScanTimeout = 60 * time.Second

// repoMapNameChars owner/repo 路径安全字符集（子群 owner 允许 "/"）。
var repoMapNameChars = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

type repoMapEntry struct {
	at     time.Time
	snap   []byte // nil = 生成中（in-flight 占位，并发请求直接让路）
	errMsg string // 生成失败短因（缓存半程 TTL，防坏仓反复打克隆）
	empty  bool   // 空图（纯文档仓）缓存
	dir    string // 工作副本目录（freshness 探测用）
}

// repoMaps 进程内缓存（Service 单例同生命周期；包级变量与 Bearer 同模式）。
var repoMaps = struct {
	mu    sync.Mutex
	items map[string]repoMapEntry
}{items: map[string]repoMapEntry{}}

// repoMapShareID 仓库地图 share 令牌域（与任务 share 同 HMAC 机制、域串不同
// ——任务令牌开不了地图页，反之亦然）。
func repoMapShareID(platform, owner, repo string) string {
	return "repo-map:" + platform + "/" + owner + "/" + repo
}

// RepoMapShareVerifier main 装配进 routerapi.SetRepoMapShare：?share= 命中
// repo-map 域令牌免 Bearer 放行该仓地图页，否则回落 Bearer。
func (s *Service) RepoMapShareVerifier() func(*app.RequestContext) bool {
	secret := s.Cfg.APIToken
	return func(c *app.RequestContext) bool {
		return impactview.ImpactShareValid(secret,
			repoMapShareID(c.Query("platform"), c.Query("owner"), c.Query("repo")),
			c.Query("share"))
	}
}

// RepoMapPlatform 解析地图请求的平台：显式 query 优先；缺省=唯一已配置平台，
// 多平台歧义报错（提示带 platform 参数）。
func (s *Service) RepoMapPlatform(explicit string) (string, error) {
	if explicit != "" {
		if _, ok := s.Cfg.Platforms[explicit]; !ok {
			return "", fmt.Errorf("未知平台 %q", explicit)
		}
		return explicit, nil
	}
	set := make([]string, 0, 2)
	for p, pc := range s.Cfg.Platforms {
		if pc.Token != "" {
			set = append(set, p)
		}
	}
	switch len(set) {
	case 1:
		return set[0], nil
	case 0:
		return "", fmt.Errorf("未配置任何平台凭据")
	default:
		return "", fmt.Errorf("多平台已配置（%s），请以 ?platform= 指定", strings.Join(set, "/"))
	}
}

// RepoMapView MCP 面：返回快照 JSON 串；ok=false 时首返回值携带短因文本
// （生成中/凭据缺失/克隆失败——工具输出直接透出给调用方）。
func (s *Service) RepoMapView(ctx context.Context, platform, owner, repo string) (string, bool) {
	b, err := s.repoMap(ctx, platform, owner, repo)
	if err != nil {
		return err.Error(), false
	}
	return string(b), true
}

// RepoMapHTML HTTP 面：自包含交互 HTML；错误经 error 返回（handler 出 4xx/5xx）。
func (s *Service) RepoMapHTML(ctx context.Context, platform, owner, repo string) (string, error) {
	b, err := s.repoMap(ctx, platform, owner, repo)
	if err != nil {
		return "", err
	}
	var snap impactview.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return "", fmt.Errorf("地图快照损坏: %w", err)
	}
	htmlOut := impactview.RenderRepoHTML(&snap, owner+"/"+repo+"（"+platform+"）")
	if htmlOut == "" {
		return "", fmt.Errorf("无可视化节点（空图）")
	}
	return htmlOut, nil
}

// repoMap 缓存主路。in-flight 占位防并发双克隆；失败短因缓存半程 TTL。
func (s *Service) repoMap(ctx context.Context, platform, owner, repo string) ([]byte, error) {
	if !repoMapNameChars.MatchString(owner) || !repoMapNameChars.MatchString(repo) ||
		strings.Contains(owner, "..") || strings.Contains(repo, "..") {
		return nil, fmt.Errorf("owner/repo 含非法字符")
	}
	key := platform + ":" + owner + "/" + repo
	now := time.Now()
	repoMaps.mu.Lock()
	if e, ok := repoMaps.items[key]; ok {
		fresh := now.Sub(e.at) < repoMapTTL
		switch {
		case e.snap != nil && fresh:
			// v5.6.3 freshness：远程 HEAD 与本地副本一致才回缓存。探测失败按
			// 一致处理——探测是优化不是正确性依赖，宁可旧图不打死缓存。
			if s.repoMapHeadMatches(ctx, e.dir) {
				repoMaps.mu.Unlock()
				return e.snap, nil
			}
			// 失配：丢弃旧副本走下方全量重建（cloneForMap 见无 .git 重克隆）
			os.RemoveAll(e.dir)
		case e.snap == nil && e.errMsg == "" && fresh:
			repoMaps.mu.Unlock()
			return nil, fmt.Errorf("地图生成中（浅克隆+扫描），数十秒后重试")
		case e.errMsg != "" && now.Sub(e.at) < repoMapTTL/2:
			repoMaps.mu.Unlock()
			return nil, fmt.Errorf("%s", e.errMsg)
		}
	}
	repoMaps.items[key] = repoMapEntry{at: now} // in-flight 占位
	repoMaps.mu.Unlock()

	var snap *impactview.Snapshot
	dir, err := s.cloneForMap(ctx, platform, owner, repo)
	if err == nil {
		snap = impactview.BuildRepoMap(dir, repoMapScanTimeout)
	}
	b, _ := json.Marshal(snap)
	repoMaps.mu.Lock()
	if err != nil {
		// 失败短因缓存半程 TTL（防坏仓反复打克隆）；in-flight 占位覆盖
		repoMaps.items[key] = repoMapEntry{at: time.Now(), errMsg: err.Error()}
		repoMaps.mu.Unlock()
		return nil, err
	}
	if len(snap.Nodes) == 0 {
		repoMaps.items[key] = repoMapEntry{at: time.Now(), empty: true, snap: b, dir: dir}
		repoMaps.mu.Unlock()
		return b, nil
	}
	repoMaps.items[key] = repoMapEntry{at: time.Now(), snap: b, dir: dir}
	repoMaps.mu.Unlock()
	return b, nil
}

// repoMapHeadMatches 远程默认分支 HEAD 与本地副本是否一致（ls-remote 单往返，
// 10s 超时）。任何失败按一致处理——见上。
func (s *Service) repoMapHeadMatches(ctx context.Context, dir string) bool {
	if dir == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	local, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return true
	}
	remote, err := exec.CommandContext(ctx, "git", "ls-remote", "origin", "HEAD").Output()
	if err != nil {
		return true
	}
	fields := strings.Fields(string(remote))
	return len(fields) > 0 && fields[0] == strings.TrimSpace(string(local))
}

// cloneForMap 浅克隆（复用既有副本且 TTL 内免 fetch）。ctx 贯穿 git 进程取消。
func (s *Service) cloneForMap(ctx context.Context, platform, owner, repo string) (string, error) {
	pc, ok := s.Cfg.Platforms[platform]
	if !ok || pc.Token == "" {
		return "", fmt.Errorf("平台 %s 缺少 clone 凭据", platform)
	}
	dst := filepath.Join(s.Cfg.WorkRoot, "repomap",
		platform+"-"+strings.ReplaceAll(owner, "/", "-")+"-"+repo)
	if fi, err := os.Stat(filepath.Join(dst, ".git")); err == nil && fi.IsDir() {
		return dst, nil // 已有副本：TTL 由快照缓存闸，目录只增不减
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	_ = os.RemoveAll(dst) // 半截目录清理（无 .git 的残留）
	url := repoMapCloneURL(pc.BaseURL, owner, repo, pc.Token)
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--single-branch", url, dst)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if pc.SkipTLS {
		cmd.Env = append(cmd.Env, "GIT_SSL_NO_VERIFY=1")
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("浅克隆失败（平台 %s）: %s", platform, strings.TrimSpace(stderr.String()))
	}
	return dst, nil
}

// repoMapCloneURL 与 workcopy.cloneURL 同纪律：剥 API 后缀、保留原 scheme、
// oauth2 形态注入 token。
func repoMapCloneURL(baseURL, owner, repo, token string) string {
	u := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/api/v1", "/api/v4", "/api/v3", "/api"} {
		u = strings.TrimSuffix(u, suffix)
	}
	scheme := "https://"
	if i := strings.Index(u, "://"); i >= 0 {
		scheme = u[:i+3]
		u = u[i+3:]
	}
	return fmt.Sprintf("%soauth2:%s@%s/%s/%s.git", scheme, token, u, owner, repo)
}
