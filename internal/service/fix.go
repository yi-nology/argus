// fix.go —— @argus fix fp:短码 修复命令（v3.85.0）。
// 链路：评论命中 fix 意图 → 指纹取回入库 finding → 工作副本本地克隆出独立
// 修复沙箱（不碰共享审查工作副本）→ acpx 编码 agent（workspace-write 档）
// 出 patch → 沙箱内确定性验证（ARGUS_FIX_VERIFY_CMD，复用 testgen 的包装
// 命令纪律：argus 自身永不直接执行待审代码，隔离责任在包装命令侧）→ 回帖
// diff。修复成立与否由确定性验证判定，不由 agent 自评；本命令不自动 push。
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/tools/acpx"
	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/concurrency/async"
)

// fix 命令的硬上限（调整阈值须过评测/试点，不拍脑袋）。
const (
	fixDiffMaxRunes  = 6000 // 回帖 diff 截断上限
	fixOutMaxRunes   = 800  // 验证输出摘录上限
	fixExcerptLines  = 40   // finding 邻近代码摘录半窗
	fixDefaultTimout = 10 * time.Minute
)

// extractFix 解析 "fix fp:xxxxxxxx" 指令（词边界 + fp 短码 ≥6 位十六进制）。
// 不匹配时返回 false（落回后续路由，不吞其他意图）。
func extractFix(question string) (string, bool) {
	rest, ok := cutCommand(question, "fix")
	if !ok {
		return "", false
	}
	return parseFPHex(rest)
}

// fixFinding 修复目标（prints 表回查产物；suggested_fix 不入库，修复以
// comment+邻近代码为上下文）。
type fixFinding struct {
	File     string
	Line     int
	Severity string
	Comment  string
}

// handleFix @argus fix 入口：同步段做守卫与受理回执（快路径），重活（沙箱
// 克隆 + agent + 验证）丢后台 goroutine——评论链路超时（默认 10m）与 agent
// 时长同量级，不能占评论并发槽长跑。停机时在途修复随进程终止（交互式命令，
// 可重发），不纳入 DrainComments 等待面。
func (s *Service) handleFix(ctx context.Context, platform, owner, repo, prNum, prRef, fp, delivery string) {
	if s.Cfg.FixAgent == "" {
		s.postQAError(ctx, platform, owner, repo, prNum,
			"修复功能未启用：需服务级配置 ARGUS_FIX_AGENT（建议同时配 ARGUS_FIX_VERIFY_CMD 验证环）。")
		return
	}
	if s.WC == nil {
		s.postQAError(ctx, platform, owner, repo, prNum, "修复不可用：服务未配置工作副本池。")
		return
	}
	rec, ok := s.Store.ResolvePrintRecord(ctx, platform, owner, repo, prNum, fp)
	if !ok {
		s.postQAError(ctx, platform, owner, repo, prNum,
			"未找到匹配的 finding（或短码有歧义），请核对报告中 `fp:` 开头的短码后重试。")
		return
	}
	if rec.File == "" {
		s.postQAError(ctx, platform, owner, repo, prNum, "该 finding 未关联具体文件，无法自动修复。")
		return
	}

	key := platform + "/" + owner + "/" + repo + "#" + prNum + "/" + fp
	if _, busy := s.fixInflight.LoadOrStore(key, true); busy {
		s.postQAError(ctx, platform, owner, repo, prNum, "同一条 finding 的修复已在执行中，请等它完成后回帖。")
		return
	}
	s.Audit.Log(auditFindingFixReq, "platform", platform, "owner", owner, "repo", repo,
		"number", prNum, "fingerprint", fp, "agent", s.Cfg.FixAgent)
	s.postQAError(ctx, platform, owner, repo, prNum,
		fmt.Sprintf("修复任务已受理（fp:%s，agent %s）——异步执行中，完成后回帖结果。\n\n<!-- argus:fix delivery=%s phase=accepted -->",
			shortFP(fp), s.Cfg.FixAgent, delivery))
	// async.GoSafe（v5.4.2）：runFixReply 跑完整 LLM agent + 沙箱链路，裸
	// goroutine 期 panic 即进程死（215 曾炸出 guardPanic 卡死教训）——panic
	// 转结构化留痕，fixInflight 键随进程清理兜底。
	async.GoSafe(func() {
		s.runFixReply(key, platform, owner, repo, prNum, fp, delivery)
	})
}

// fixExec 修复同步执行体（守卫 + 工作副本就绪 + 沙箱运行），评论链与 MCP
// fix_finding 共用。工作副本经池 Ensure（已被 Sweep 回收会自动重建，引用
// 覆盖 agent 全程），修复沙箱再从其本地克隆——源目录零写入。
func (s *Service) fixExec(ctx context.Context, platform, owner, repo, number, fp string) (*fixResult, *fixFinding, error) {
	if s.Cfg == nil || s.Cfg.FixAgent == "" {
		return nil, nil, fmt.Errorf("修复功能未启用（ARGUS_FIX_AGENT 未配置）")
	}
	if s.WC == nil {
		return nil, nil, fmt.Errorf("修复不可用：服务未配置工作副本池")
	}
	rec, ok := s.Store.ResolvePrintRecord(ctx, platform, owner, repo, number, fp)
	if !ok {
		return nil, nil, fmt.Errorf("未找到匹配的 finding（fp:%s），请核对报告短码", fp)
	}
	if rec.File == "" {
		return nil, nil, fmt.Errorf("该 finding 未关联具体文件，无法自动修复")
	}
	ff := &fixFinding{File: rec.File, Line: rec.Line, Severity: rec.Severity, Comment: rec.Comment}
	// head SHA 取不到回落默认分支（降级可修复，摘录行号可能漂移，agent 以意见上下文为准）
	headSHA := ""
	if cr, err := s.SDK.GetCR(ctx, platform, owner, repo, number); err == nil && cr != nil {
		headSHA = cr.HeadSHA
	}
	wcKey := workcopy.WorktreeKey{Platform: platform, Owner: owner, Repo: repo, Number: number, HeadSHA: headSHA}
	dir, err := s.WC.Ensure(ctx, wcKey)
	if err != nil {
		return nil, nil, fmt.Errorf("工作副本不可用: %w", err)
	}
	defer s.WC.Release(wcKey)
	res, err := runFixSandbox(ctx, fixRunConfig{
		SrcDir:    dir,
		WorkRoot:  s.Cfg.WorkRoot,
		AgentName: s.Cfg.FixAgent,
		Verify:    VerifyConfig{Cmd: s.Cfg.FixVerifyCmd, Timeout: s.Cfg.FixVerifyTimeout},
		Finding:   *ff,
		Timeout:   s.fixAgentTimeout(),
		Log:       s.log(),
	})
	if err != nil {
		return nil, nil, err
	}
	return res, ff, nil
}

// runFixReply 评论链后台段：独立 ctx（评论链 ctx 随 handler 返回即取消），
// 结果/失败都回帖，用户永远有感知。回帖走独立短 ctx（v3.91.1）：agent 撞
// 硬上限时执行 ctx 已过期，过期 ctx 会让失败/成功回帖本身也发不出去。
func (s *Service) runFixReply(key, platform, owner, repo, prNum, fp, delivery string) {
	defer s.fixInflight.Delete(key)
	ctx, cancel := context.WithTimeout(context.Background(), s.fixAgentTimeout())
	defer cancel()

	res, ff, err := s.fixExec(ctx, platform, owner, repo, prNum, fp)
	pctx, pcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pcancel()
	if err != nil {
		s.log().Error("argus.fix.failed", "pr", fmt.Sprintf("%s/%s/%s#%s", platform, owner, repo, prNum),
			"fp", shortFP(fp), "error", logredact.Redact(err.Error()))
		s.Audit.Log(auditFindingFixDone, "platform", platform, "owner", owner, "repo", repo,
			"number", prNum, "fingerprint", fp, "ok", false, "error", err.Error())
		s.postQAError(pctx, platform, owner, repo, prNum,
			fmt.Sprintf("修复执行失败（fp:%s）：%s\n\n<!-- argus:fix delivery=%s phase=done -->",
				shortFP(fp), logredact.Redact(err.Error()), delivery))
		return
	}
	s.Audit.Log(auditFindingFixDone, "platform", platform, "owner", owner, "repo", repo,
		"number", prNum, "fingerprint", fp, "ok", res.Verified, "agent", res.AgentName)
	s.postQAError(pctx, platform, owner, repo, prNum, renderFixReply(fp, *ff, res, delivery))
}

// fixRunConfig 修复沙箱执行配置（runFixSandbox 可独立单测）。
type fixRunConfig struct {
	SrcDir    string         // 克隆源（工作副本目录；空 = 不可用）
	WorkRoot  string         // 沙箱父目录（空 = 系统临时目录）
	AgentName string         // acpx agent 名（claude/codex/...）
	Registry  *acpx.Registry // nil = 缺省注册表
	Verify    VerifyConfig   // 空.Cmd = 不验证
	Finding   fixFinding
	Timeout   time.Duration // agent 硬上限（0 = 10m）
	Log       Logger
}

// fixResult 修复沙箱执行产物（渲染 renderFixReply 消费）。
type fixResult struct {
	AgentName string
	Took      time.Duration
	Fixed     bool     // agent 自报修复完成（不作为成立判据，仅展示）
	Summary   string   // agent 一句话变更说明
	Files     []string // agent 自报变更文件
	DiffStat  string
	Diff      string // 截断后的 git diff（相对克隆源 HEAD）
	DiffTrunc bool
	Verified  bool   // 沙箱验证通过（Verify.Cmd 为空 = 未验证）
	VerifyOut string // 验证输出摘录（失败时可见）
}
