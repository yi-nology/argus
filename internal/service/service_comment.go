package service

import (
	"context"
	"fmt"
	"time"

	datacache "github.com/oekit/data/cache"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/oagentkit/rag"

	api "github.com/yi-nology/argus/biz/model/api"
	"github.com/yi-nology/argus/internal/commentcmd"
	"github.com/yi-nology/argus/internal/obs"
)

// admitComment 评论事件准入守卫：模型在位 → 负载归一化 → 解析评论要素 →
// bot 自身评论防循环 → delivery 幂等 → 仓库存世。任一不通过返回 ok=false
// （调用方静默忽略）。delivery 幂等 v3.8.0 下沉 DB（comment_deliveries 主键
// 冲突=已处理）：纯内存 map 在多副本共库与单实例重启场景下全部失效。落库失败
// fail-open（继续处理）——回帖重复可容忍，丢单不可容忍。
func (s *Service) admitComment(ctx context.Context, ev *provider.NormalizedEvent,
	delivery string) (commentcmd.Meta, bool) {
	if s.Model == nil {
		s.log().Warn("argus.qa.no_model", "hint", "LLM 未配置，Q&A 评论忽略")
		return commentcmd.Meta{}, false
	}
	commentcmd.NormalizeCommentEvent(ev)
	meta, ok := commentcmd.ParseCommentMeta(ev)
	if !ok {
		return commentcmd.Meta{}, false // 解析失败或非 PR 评论（系统注释/commit/普通 issue）
	}
	// bot 自身评论防循环（Info 留痕：静默丢弃曾让评论链路排查无从下手——
	// "事件到没到、进了哪条路" 全靠推断）。
	if ev.Actor != nil && ev.Actor.Username != "" && ev.Actor.Username == s.Cfg.BotUsername {
		s.log().Info("argus.comment.self_skipped", "author", ev.Actor.Username)
		return commentcmd.Meta{}, false
	}
	if delivery != "" {
		fresh, err := s.Store.MarkCommentDelivery(ctx, delivery, time.Now())
		if err != nil {
			s.log().Warn("argus.qa.delivery_mark_failed", "delivery", delivery, "error", err.Error())
		} else if !fresh {
			return commentcmd.Meta{}, false
		}
	}
	if ev.Repo == nil {
		return commentcmd.Meta{}, false
	}
	return meta, true
}

// dispatchSuppression 抑制类评论指令（restore/suppress）：免 Q&A 节流——误报
// 反馈与恢复是低频运营动作且结果回流知识库，节流窗口把它们挡掉会丢反馈。
// 返回 true 表示已消费该评论。
func (s *Service) dispatchSuppression(ctx context.Context, ev *provider.NormalizedEvent,
	meta commentcmd.Meta, question string) bool {
	// /argus restore fp:xxxxxxxx —— 误抑制恢复（suppress 反向，误杀可撤销）：
	// 将 suppressed 指纹置回 open，后续轮次重新可见。评论轻量路径，不建任务。
	if fp, ok := commentcmd.ExtractRestoreFP(question); ok {
		s.handleRestoreComment(ctx, ev, meta.PRNumber, fp)
		return true
	}
	// /argus suppress fp:xxxxxxxx —— 误报反馈（v3.3 知识飞轮入口）：
	// 抑制该指纹后续轮次 + 结论回流知识库。评论轻量路径，不建任务。
	if fp, ok := commentcmd.ExtractSuppressFP(question); ok {
		s.handleSuppressComment(ctx, ev, meta.PRNumber, fp, meta.Body)
		return true
	}
	return false
}

// dispatchWriting 写作面与修复类评论指令（describe/testgen/fix，v3.15.0/v3.85.0）：
// 显式命令免意图路由，与 Q&A 同享节流与 delivery 幂等（节流已在上一步取得）。
// 返回 true 表示已消费该评论。
func (s *Service) dispatchWriting(ctx context.Context, ev *provider.NormalizedEvent,
	question, platform, prNum, prRef, delivery string) bool {
	if hint, ok := commentcmd.ExtractDescribe(question); ok {
		s.handleDescribe(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, hint, delivery)
		return true
	}
	if hint, ok := commentcmd.ExtractChangelog(question); ok {
		s.handleChangelog(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, hint, delivery)
		return true
	}
	if target, ok := commentcmd.ExtractTestGen(question); ok {
		s.handleTestGen(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, target, delivery)
		return true
	}
	// @argus fix fp:短码（v3.85.0）：acpx 编码 agent 沙箱修复，异步执行。
	if fp, ok := commentcmd.ExtractFix(question); ok {
		s.handleFix(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, fp, delivery)
		return true
	}
	return false
}

// answerComment Q&A 终态分派：意图路由关闭时单轮直答（与 v3.3 行为一致）；
// 开启时先分类再分派——review 建全量复审任务，其余一律 Q&A（Fallback 也是 Q&A）。
func (s *Service) answerComment(ctx context.Context, ev *provider.NormalizedEvent,
	question, platform, prNum, prRef, delivery string) {
	owner, repo := ev.Repo.Owner, ev.Repo.Name
	if !s.Cfg.CommentRouter {
		answer, err := s.qa().Answer(ctx, platform, owner, repo, prNum, prRef, question)
		if err != nil {
			obs.IncrQAFailure("llm")
			s.log().Error("argus.qa.llm_failed", "pr", prRef, "error", err.Error())
			s.postQAError(ctx, platform, owner, repo, prNum, "LLM 调用失败: "+err.Error())
			return
		}
		s.postCommentReply(ctx, platform, owner, repo, prNum, prRef,
			"> "+question+"\n\n"+answer+"\n\n<!-- argus:qa delivery="+delivery+" -->",
			question, len(answer))
		return
	}

	reply, err := s.routeAndReply(ctx, platform, owner, repo, prNum, prRef, question, delivery)
	if err != nil {
		obs.IncrQAFailure("llm")
		s.log().Error("argus.qa.llm_failed", "pr", prRef, "error", err.Error())
		s.postQAError(ctx, platform, owner, repo, prNum, "处理失败: "+err.Error())
		return
	}
	s.postCommentReply(ctx, platform, owner, repo, prNum, prRef, reply, question, len(reply))
}

// HandleComment 处理 @argus 评论：准入守卫 → 指令分派（抑制类免节流 → 节流 →
// 写作/修复类）→ Q&A 终态。轻量路径：不建 task、不入 DB、不走 worker pool。
func (s *Service) HandleComment(ctx context.Context, ev *provider.NormalizedEvent, delivery string) {
	meta, ok := s.admitComment(ctx, ev, delivery)
	if !ok {
		return
	}

	question := commentcmd.ExtractQuestion(meta.Body, s.Cfg.BotUsername)
	if question == "" {
		return // 不含 @argus 指令，忽略
	}
	if s.dispatchSuppression(ctx, ev, meta, question) {
		return
	}

	prNum := fmt.Sprintf("%d", meta.PRNumber)
	prRef := ev.Repo.Owner + "/" + ev.Repo.Name + "#" + prNum
	platform := string(ev.Source)

	// 信息类指令（help/status）免节流：查询永远畅通，商业产品的"手册随时可查"。
	if s.dispatchInfo(ctx, question, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, delivery) {
		return
	}

	// per-PR 节流（30 秒冷却）。v3.82.0 引入 oekit/data/cache：TTL 过期 + 容量
	// 上限由库接管，取代手写 map+mutex+「超 100 清 1h 前旧条目」驱逐循环。
	// v3.84.0 起实例回归 per-Service（进程级单例曾令 e2e 同进程多 Service
	// 互相污染节流窗口；生产行为不变）。
	// v5.3.0：指令撞冷却不再静默丢弃——回帖告知（有感知），自由文本保持静默防刷屏。
	throttleKey := ev.Repo.Owner + "/" + ev.Repo.Name + "/" + prNum
	if v, _ := s.qaThrottle().Get(ctx, throttleKey); v != nil {
		if commentcmd.MatchAnyCommand(question) {
			s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, commentcmd.ThrottleNoticeText)
		} else {
			s.log().Warn("argus.qa.throttled", "pr", prRef, "question", question)
		}
		return
	}
	_ = s.qaThrottle().Set(ctx, throttleKey, &qaThrottled)

	s.log().Info("argus.qa.received", "pr", prRef, "question", question,
		"actor", ev.Actor.Username, "event_id", delivery)

	// 即时回执 👀（v5.3.0）：评论上先给"已看到"，长任务（重审/复核）不再干等。
	s.ackComment(ctx, platform, ev.Repo.Owner, ev.Repo.Name, meta.CommentID)

	// 烧预算指令门控（v5.3.0）：review/recheck/fix/testgen 需写权限，防路人白烧。
	if commentcmd.IsDriveCommand(question) && !s.actorCanDrive(ctx, platform, ev.Repo.Owner, ev.Repo.Name, actorName(ev)) {
		s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, commentcmd.GateDeniedText)
		return
	}

	// 显式命令优先于自由文本路由：复核（单条 fp）→ 重审（全量）→ 写作/修复 →
	// Q&A/意图分类。确定性命令零 LLM 分类开销，也不受 ARGUS_COMMENT_ROUTER 开关影响。
	if s.dispatchRecheck(ctx, question, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, delivery) {
		return
	}
	if s.dispatchReview(ctx, question, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, delivery) {
		return
	}
	if s.dispatchWriting(ctx, ev, question, platform, prNum, prRef, delivery) {
		return
	}
	s.answerComment(ctx, ev, question, platform, prNum, prRef, delivery)
}

// actorName 事件触发者用户名（nil 安全）。
func actorName(ev *provider.NormalizedEvent) string {
	if ev.Actor == nil {
		return ""
	}
	return ev.Actor.Username
}

// postCommentReply 回帖 + 留痕（Q&A 与意图路由分派的共用出口）。
func (s *Service) postCommentReply(ctx context.Context, platform, owner, repo, prNum, prRef,
	body, question string, answerLen int) {
	if _, err := s.SDK.CreateIssueComment(ctx, platform, owner, repo, prNum, body); err != nil {
		obs.IncrCommentPostFailure("reply")
		s.log().Error("argus.qa.reply_failed", "pr", prRef, "error", err.Error())
		s.publishCommentReplied("error", platform, owner, repo, prNum)
		return
	}
	s.publishCommentReplied("ok", platform, owner, repo, prNum)
	s.log().Info("argus.qa.replied", "pr", prRef, "answer_len", answerLen)
	s.Audit.Log("qa_reply", "pr", prRef, "question", question, "answer_len", answerLen)
}

// handleSuppressComment 处理 MR 评论里的误报反馈：短码解析完整指纹 → 抑制入库 →
// 知识回流 → 回帖确认。复用 SuppressFinding 服务路径（同一套幂等与回流逻辑）。
func (s *Service) handleSuppressComment(ctx context.Context, ev *provider.NormalizedEvent,
	number int, fpPrefix, commentBody string) {
	platform := string(ev.Source)
	num := fmt.Sprintf("%d", number)
	req := &api.SuppressFindingRequest{
		Platform: platform, Owner: ev.Repo.Owner, Repo: ev.Repo.Name, Number: num,
		Fingerprint: strPtr(fpPrefix),
		Comment:     strPtr(commentBody),
	}
	resp, status := s.SuppressFinding(ctx, req)
	if resp == nil {
		s.log().Warn("argus.feedback.comment_failed", "pr", ev.Repo.Owner+"/"+ev.Repo.Name+"#"+num, "status", status)
		return
	}
	reply := "已收到误报反馈。"
	if !resp.Matched {
		reply = "未找到匹配的意见指纹，请核对报告中 `fp:` 开头的短码后重试。"
	} else {
		reply = "已标记为误报：后续轮次自动抑制" + s.knowledgeSuffix() + "。"
	}
	s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, reply)
}

// knowledgeSuffix 报告尾注（知识回流是否生效取决于后端模式）。
func (s *Service) knowledgeSuffix() string {
	if _, ok := s.Knowledge.(*rag.MilvusStore); ok {
		return "，误报结论已回流知识库（同型问题检索时将带历史语境）"
	}
	return ""
}

// postQAError 当 Q&A 流程失败时，向 PR 发一条错误说明（避免用户无响应）。
func (s *Service) postQAError(ctx context.Context, platform, owner, repo, number, msg string) {
	body := ":warning: **Argus Q&A 错误**: " + msg +
		"\n\n<!-- argus:qa error -->"
	if _, err := s.SDK.CreateIssueComment(ctx, platform, owner, repo, number, body); err != nil {
		s.log().Error("argus.qa.error_reply_failed", "error", err.Error())
	}
	s.publishCommentReplied("error", platform, owner, repo, number)
}

// handleRestoreComment 处理 MR 评论里的恢复指令：短码解析 → 置回 open → 回帖确认。
// 与 handleSuppressComment 互为反向；不走知识回流（恢复不是误报结论）。
// v3.131.0 起执行体委派 restoreFindingCore 单源（API/MCP 同源）。
func (s *Service) handleRestoreComment(ctx context.Context, ev *provider.NormalizedEvent,
	number int, fpPrefix string) {
	platform := string(ev.Source)
	num := fmt.Sprintf("%d", number)
	matched, err := s.restoreFindingCore(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, fpPrefix)
	if err != nil {
		s.log().Warn("argus.feedback.restore_failed", "pr", ev.Repo.Owner+"/"+ev.Repo.Name+"#"+num, "err", err)
		return
	}
	reply := "已恢复：该意见后续轮次重新可见。"
	if !matched {
		reply = "未找到匹配的已抑制指纹（或短码有歧义），请核对报告中 `fp:` 开头的短码后重试。"
	}
	s.postQAError(ctx, platform, ev.Repo.Owner, ev.Repo.Name, num, reply)
}

// qaThrottle Q&A 评论 per-PR 30s 冷却缓存（per-Service 懒建，v3.84.0 起；
// v3.82.0 的进程级单例曾破坏 e2e 测试隔离。TTL 过期 + MaxSize LRU 上限由
// 库负责；qaThrottleMu 护并发首建）。
func (s *Service) qaThrottle() *datacache.LocalCache[bool] {
	s.qaThrottleMu.Lock()
	defer s.qaThrottleMu.Unlock()
	if s.qaThrottleCch == nil {
		s.qaThrottleCch = datacache.NewLocalCache(datacache.LocalConfig[bool]{
			TTL:     30 * time.Second,
			MaxSize: 200,
		})
	}
	return s.qaThrottleCch
}

var qaThrottled = true
