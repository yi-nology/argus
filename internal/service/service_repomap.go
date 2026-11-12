// service_repomap.go —— 仓库级代码地图数据面（v5.6.2，图谱可视化 D 项）：
// 按需浅克隆工作副本 → BuildRepoMap → 进程内 TTL 缓存。MCP get_repo_map 与
// GET /repos/:owner/:repo/map 双出口共用。
//
// 克隆面纪律：owner/repo 白名单字符集校验（路径/URL 注入防线）；工作目录按
// 仓库身份三元组哈希派生（v6.7.0 P0-03：owner 内 "/" 不再拼接转义，跨仓碰撞
// 串仓封死）；凭据经 http.extraHeader 注入（v6.7.0 P0-03：token 不落
// .git/config——此前 token 注入 clone URL 会被持久化到 origin remote）；日志
// 零凭据。

package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// repoMapCloneGCTTL 孤儿克隆目录的保留期（P1-04 旧仓治理）：仓库被删/改名后
// 旧目录不再被新身份哈希命中，靠 mtime TTL 在建图未命中路径顺手清理。
const repoMapCloneGCTTL = 7 * 24 * time.Hour

// repoMapWorkDirName 新寻址目录名（platform-<hex12>）：与旧拼接式目录名互斥，
// GC 据此识别旧寻址残留（含 token 落盘 .git/config 的历史目录）立即清理。
var repoMapWorkDirName = regexp.MustCompile(`^[a-z0-9_.-]+-[0-9a-f]{12}$`)

// repoMapNameChars owner/repo 路径安全字符集（子群 owner 允许 "/"）。
var repoMapNameChars = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// repomapCloneURLFn clone/ls-remote URL 派生（测试钩子：本地 file 裸库回归用；
// 生产恒 repoMapCloneURL）。
var repomapCloneURLFn = repoMapCloneURL

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
// v6.7.0 P0-02：仓库维度判定收口——此前 MCP get_repo_map 无 repoAllowedFor，
// 持任意 ci 档 token 可渲染任意仓的代码地图。
func (s *Service) RepoMapView(ctx context.Context, platform, owner, repo string) (string, bool) {
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repos/map")
		return ErrAuthzDenied.Error(), false
	}
	b, err := s.repoMap(ctx, platform, owner, repo)
	if err != nil {
		return err.Error(), false
	}
	return string(b), true
}

// RepoMapHTML HTTP 面：自包含交互 HTML；错误经 error 返回（handler 出 4xx/5xx，
// ErrAuthzDenied 映射 403）。
func (s *Service) RepoMapHTML(ctx context.Context, platform, owner, repo string) (string, error) {
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repos/map")
		return "", ErrAuthzDenied
	}
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
// v6.7.0（P1-04 锁范围）：freshness 探测（git 子进程 + 网络往返，10s 上界）
// 移出全局锁——此前探测持 repoMaps.mu，一个仓库的慢 ls-remote 拖住所有仓库
// 的建图请求。
func (s *Service) repoMap(ctx context.Context, platform, owner, repo string) ([]byte, error) {
	if !repoMapNameChars.MatchString(owner) || !repoMapNameChars.MatchString(repo) ||
		strings.Contains(owner, "..") || strings.Contains(repo, "..") {
		return nil, fmt.Errorf("owner/repo 含非法字符")
	}
	key := platform + ":" + owner + "/" + repo
	now := time.Now()

	// 阶段一：锁内窥探缓存态（不阻塞）。
	repoMaps.mu.Lock()
	e, hit := repoMaps.items[key]
	probeDir := ""
	probeSnap := []byte(nil)
	probedAt := time.Time{}
	if hit && e.snap != nil && now.Sub(e.at) < repoMapTTL {
		probeDir, probeSnap, probedAt = e.dir, e.snap, e.at
	}
	repoMaps.mu.Unlock()

	// 阶段二（锁外）：freshness 探测。任何失败按失配处理——fail-safe 刷新
	//（v6.7.0 P1-04：此前 ls-remote 失败按「一致」处理，HEAD 对比恒 true，
	// 提交更新后代码地图永不刷新）。
	// v6.12 P1（外部评估实锤）：探测失配不自删 probeDir——并发请求 A 可能
	// 正处在阶段三重建扫描中，B 删其工作目录即地图损坏。代际标记（staleDir
	// 保留给 in-flight 重建方在成功落缓存后统一回收）+ 阶段三 fresh 分支
	// 「同代同 dir」命中即返回缓存——多探测者最多一个进入重建（退位语义），
	// 不再互删。
	probedStale := false
	if probeDir != "" {
		if s.repoMapHeadMatches(ctx, probeDir, platform) {
			return probeSnap, nil
		}
		probedStale = true // 不自删——代际标记留给重建方回收
	}

	// 阶段三：锁内复查 + in-flight 占位（check-and-set 原子，防并发双克隆）。
	// 探测期间他请求可能已重建完成（e.at 推进）——TTL 内直接采信；但自己刚
	// 探测失配的同一代快照不得再采信（否则刷新被旧缓存短路，永不生效）。
	// v6.12 P1：同代同 dir 不采信（e.at.Equal(probedAt) 判同代，不论 dir 是否
	// 变化）——并发重建方落缓存时必推进 e.at（at: time.Now()），第二个探测者
	// 进入本阶段看到 e.at 已推进即知重建完成、采信新代。旧代同 dir 残留仅
	// 出现在「自己即将重建」的占位被回收前，不得短路。
	repoMaps.mu.Lock()
	if e, ok := repoMaps.items[key]; ok {
		fresh := now.Sub(e.at) < repoMapTTL
		probedSelf := probedStale && e.at.Equal(probedAt)
		switch {
		case e.snap != nil && fresh && !probedSelf:
			repoMaps.mu.Unlock()
			return e.snap, nil
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
	// v6.12 P1：重建方克隆前清理自己探测失配的那一代目录——cloneForMap 对
	// 同哈希目录已有 .git 直接复用（旧 HEAD 仍在），不清理则「重建」实为空转。
	// 安全性：只有重建方（in-flight 占位持有者）删自己刚探测过的目录；并发
	// 探测者已进入阶段三会命中占位返回「生成中」，不会走到这里；若另一重建
	// 方抢先占位，本请求已在上方 fresh 分支采信/报错返回，亦不会走到这里。
	if probedStale && probeDir != "" {
		_ = os.RemoveAll(probeDir)
	}
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
// 10s 超时）。失败按失配处理（fail-safe 刷新）——探测是正确性依赖不是纯优化
// （v6.7.0 P1-04 语义翻转，见 repoMap 阶段二注）。凭据经 http.extraHeader
// 注入（P0-03：origin 为净 URL，token 不落盘），`-C dir` 锚定副本目录——
// 此前缺 `-C` 在服务进程 CWD 执行，「origin」不存在恒失败。
func (s *Service) repoMapHeadMatches(ctx context.Context, dir, platform string) bool {
	if dir == "" {
		return false
	}
	var token string
	if s.Cfg != nil {
		if pc, ok := s.Cfg.Platforms[platform]; ok {
			token = pc.Token
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	local, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return false
	}
	args := append([]string{"-C", dir, "ls-remote"}, repomapGitAuthArgs(token)...)
	remote, err := exec.CommandContext(ctx, "git", append(args, "origin", "HEAD")...).Output()
	if err != nil {
		return false
	}
	fields := strings.Fields(string(remote))
	return len(fields) > 0 && fields[0] == strings.TrimSpace(string(local))
}

// cloneForMap 浅克隆（复用既有副本且 TTL 内免 fetch）。ctx 贯穿 git 进程取消。
// v6.7.0 P0-03：工作目录按仓库身份三元组哈希派生（碰撞对 g/s1+r 与 g+s1-r
// 不再同目录）；未命中路径顺手清理孤儿目录（旧寻址残留 + 超保留期的失配克隆）。
func (s *Service) cloneForMap(ctx context.Context, platform, owner, repo string) (string, error) {
	pc, ok := s.Cfg.Platforms[platform]
	if !ok || pc.Token == "" {
		return "", fmt.Errorf("平台 %s 缺少 clone 凭据", platform)
	}
	dst := repoMapWorkDir(s.Cfg.WorkRoot, platform, owner, repo)
	if fi, err := os.Stat(filepath.Join(dst, ".git")); err == nil && fi.IsDir() {
		return dst, nil // 已有副本：TTL 由快照缓存闸
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	s.pruneRepomapClones(dst) // 未命中路径顺手 GC（热仓走 .git 命中路径不触发，无清理churn）
	_ = os.RemoveAll(dst)     // 半截目录清理（无 .git 的残留）
	url := repomapCloneURLFn(pc.BaseURL, owner, repo)
	args := append(repomapGitAuthArgs(pc.Token), "clone", "--depth", "1", "--single-branch", url, dst)
	cmd := exec.CommandContext(ctx, "git", args...)
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

// pruneRepomapClones 克隆目录 GC（P0-03/P1-04 旧仓治理）：①旧寻址拼接式目录
// （新寻址是 platform-<hex12> 哈希名，形态可判）立即清理——其中含旧版 token
// 落盘 .git/config 的历史泄露面；②新寻址目录 mtime 超过保留期的（仓库被删/
// 改名后的孤儿克隆）清理。只在建图未命中路径触发；skip 当前正在用的目录。
func (s *Service) pruneRepomapClones(keep string) {
	root := filepath.Join(s.Cfg.WorkRoot, "repomap")
	entries, err := os.ReadDir(root)
	if err != nil {
		return // 根目录不存在 = 无可清理
	}
	now := time.Now()
	for _, en := range entries {
		if !en.IsDir() || en.Name() == filepath.Base(keep) {
			continue
		}
		path := filepath.Join(root, en.Name())
		if !repoMapWorkDirName.MatchString(en.Name()) {
			_ = os.RemoveAll(path) // 旧寻址残留（含 token 落盘形态）：立即清理
			continue
		}
		if fi, ferr := en.Info(); ferr == nil && now.Sub(fi.ModTime()) > repoMapCloneGCTTL {
			_ = os.RemoveAll(path) // 孤儿克隆（仓库已删/改名）：按保留期清理
		}
	}
}

// repoMapWorkDir 仓库 → 克隆目录唯一映射（v6.7.0 P0-03）：platform|owner|repo
// 三元组 SHA-256 前 6 字节 hex 派生，owner 内 "/" 不再拼接转义——旧寻址
// platform+"-"+ReplaceAll(owner,"/","-")+"-"+repo 使 GitLab 子组 g/s1+r 与
// g+s1-r 撞同一目录（跨仓工作目录不隔离 → 代码地图串仓/缓存键串仓）。
func repoMapWorkDir(root, platform, owner, repo string) string {
	sum := sha256.Sum256([]byte(platform + "|" + owner + "|" + repo))
	return filepath.Join(root, "repomap", platform+"-"+hex.EncodeToString(sum[:6]))
}

// repomapGitAuthArgs git -c 参数：Authorization 头携带平台凭据（P0-03）。-c
// 配置只作用于本次调用不落盘——此前 token 注入 clone URL 会持久化到
// .git/config 的 origin remote（目录复用 + 凭据落盘叠加放大泄露面）。token
// 空 = 不带鉴权参数。
func repomapGitAuthArgs(token string) []string {
	if token == "" {
		return nil
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("oauth2:" + token))
	return []string{"-c", "http.extraHeader=Authorization: Basic " + b64}
}

// repoMapCloneURL 与 workcopy.cloneURL 同纪律：剥 API 后缀、保留原 scheme、
// 产出净 URL（不含凭据——凭据走 repomapGitAuthArgs 的 http.extraHeader，
// P0-03）。
func repoMapCloneURL(baseURL, owner, repo string) string {
	u := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	for _, suffix := range []string{"/api/v1", "/api/v4", "/api/v3", "/api"} {
		u = strings.TrimSuffix(u, suffix)
	}
	scheme := "https://"
	if i := strings.Index(u, "://"); i >= 0 {
		scheme = u[:i+3]
		u = u[i+3:]
	}
	return fmt.Sprintf("%s%s/%s/%s.git", scheme, u, owner, repo)
}
