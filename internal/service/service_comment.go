package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	ekcache "github.com/oekit/data/cache"

	"github.com/yi-nology/go-git-platform/provider"

	"git.example.com/enjoydream/agentkit/knowledge/rag"

	api "argus/biz/model/api"
	"argus/internal/obs"
)

// qaCommentPayload 解析 issue_comment webhook 原始负载中我们关心的字段。
// 仅用于从 RawPayload 提取评论正文与 PR 信息，不做全量反序列化。
type qaCommentPayload struct {
	Comment struct {
		Body string `json:"body"`
	} `json:"comment"`
	Issue struct {
		Number int `json:"number"`
		// GitHub 与 Gitea/Forgejo 的 pull_request 元数据字段集不完全一致，
		// 只取原始 JSON 判存在性（非空 = 这是 PR，不是普通 issue）。
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
}

// gitlabNotePayload GitLab note webhook 负载（object_kind=note）中我们关心的字段。
// MR 评论带 merge_request 对象（iid 即 MR 序号）；system=true 是平台自动生成的
// 系统注释（改指派/标签/状态等），没有用户意图，必须跳过。
type gitlabNotePayload struct {
	ObjectAttributes struct {
		Note   string `json:"note"`
		System bool   `json:"system"`
	} `json:"object_attributes"`
	MergeRequest struct {
		IID int `json:"iid"`
	} `json:"merge_request"`
}

// commentMeta 评论负载的归一化视图：两种平台形状折叠后的评论要素。
type commentMeta struct {
	Body     string
	PRNumber int
}

// commentEventTypes 可路由为评论命令的事件词表。canonical 为
// provider.EventTypeComment+CommentActionCreated（"comment.created"，SDK v0.72+
// 七后端统一：gitlab/github/gitea/gitee/forgejo/gitcode/tencentcode）；issue_comment
// 与 cr.note 为旧词表，保留兼容防 vendor 回退误判。词表漏配曾让 GitLab 评论
// 命令整条链路静默落白名单 skip（v3.114.0 实测修复，v3.115.0 扩全平台）。
var commentEventTypes = map[string]bool{
	"issue_comment": true,
	"cr.note":       true,
	provider.EventTypeComment + provider.CommentActionCreated: true,
}

// parseCommentMeta 按事件形态解析评论要素。canonical 事件（comment.created）
// 的归一化结构已就绪（Comment=评论体、CR=所在 MR），直接取用不重解 RawPayload；
// 旧词表 cr.note → GitLab payload 形状；issue_comment 与 Gitea/Forgejo（Type 空
// + comment 字段兜底）→ issue payload 形状。ok=false 表示解析失败或非 PR 评论
// （commit/snippet/普通 issue），调用方静默忽略。
func parseCommentMeta(ev *provider.NormalizedEvent) (commentMeta, bool) {
	if ev.Type == provider.EventTypeComment+provider.CommentActionCreated {
		// 只处理新建（created）；CR 缺席 = 普通 issue/commit 评论，无 PR 语义。
		if ev.Comment == nil || ev.CR == nil {
			return commentMeta{}, false
		}
		n, err := strconv.Atoi(ev.CR.Number)
		if err != nil || n <= 0 {
			return commentMeta{}, false
		}
		return commentMeta{Body: ev.Comment.Body, PRNumber: n}, true
	}
	if ev.Type == "cr.note" {
		var gl gitlabNotePayload
		if err := json.Unmarshal(ev.RawPayload, &gl); err != nil {
			return commentMeta{}, false
		}
		if gl.ObjectAttributes.System || gl.MergeRequest.IID == 0 {
			return commentMeta{}, false
		}
		return commentMeta{Body: gl.ObjectAttributes.Note, PRNumber: gl.MergeRequest.IID}, true
	}
	var pl qaCommentPayload
	if err := json.Unmarshal(ev.RawPayload, &pl); err != nil {
		return commentMeta{}, false
	}
	// pull_request 元数据字段非空 = 这是 PR（不是普通 issue）；Gitea 的该对象形状
	// 与 GitHub 不完全一致，因此只判存在性，不依赖具体字段。
	if len(pl.Issue.PullRequest) == 0 || string(pl.Issue.PullRequest) == "null" {
		return commentMeta{}, false
	}
	return commentMeta{Body: pl.Comment.Body, PRNumber: pl.Issue.Number}, true
}

// isCommentEvent 判定 NormalizedEvent 是否为可处理的评论事件。
// canonical：comment.created（SDK v0.72+ 七后端统一，Actor/Repo/Comment/CR 已由
// SDK 归一化填充）；旧词表 issue_comment（GitHub）/cr.note（GitLab）保留兼容；
// Gitea/Forgejo 未识别事件 → ev.Type 为空但 payload 含 comment 字段。
func isCommentEvent(ev *provider.NormalizedEvent) bool {
	if commentEventTypes[ev.Type] {
		return true
	}
	if ev.Type != "" {
		return false
	}
	// Gitea 后端不识别的事件类型 → Type 为空；用 payload 结构兜底判定。
	var probe struct {
		Comment *json.RawMessage `json:"comment"`
	}
	return len(ev.RawPayload) > 0 &&
		json.Unmarshal(ev.RawPayload, &probe) == nil &&
		probe.Comment != nil
}

// normalizeCommentEvent 从原始负载补齐评论事件的 Actor/Repo。
// 背景：git-platform-sdk 的 GitHub 后端对 issue_comment 走 default 分支只置 Type，
// 不填 Actor/Repo（v3.5.5 真机实测发现，评论链路因此静默早退）；Gitea/GitLab 后端
// 则在 switch 前统一填充。此处以负载为准兜底，使评论处理与 SDK 归一化程度解耦。
func normalizeCommentEvent(ev *provider.NormalizedEvent) {
	if len(ev.RawPayload) == 0 {
		return
	}
	if ev.Repo == nil {
		var probe struct {
			Repository struct {
				FullName string `json:"full_name"`
				Name     string `json:"name"`
				Owner    struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repository"`
		}
		if err := json.Unmarshal(ev.RawPayload, &probe); err == nil {
			owner, name := probe.Repository.Owner.Login, probe.Repository.Name
			if owner == "" || name == "" {
				// full_name = "owner/name" 兜底
				if i := strings.IndexByte(probe.Repository.FullName, '/'); i > 0 {
					if owner == "" {
						owner = probe.Repository.FullName[:i]
					}
					if name == "" {
						name = probe.Repository.FullName[i+1:]
					}
				}
			}
			if owner != "" && name != "" {
				ev.Repo = &provider.EventRepo{Owner: owner, Name: name}
			}
		}
	}
	if ev.Actor == nil {
		var probe struct {
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
			User struct {
				Username string `json:"username"`
				Login    string `json:"login"`
			} `json:"user"`
		}
		if err := json.Unmarshal(ev.RawPayload, &probe); err == nil {
			login := probe.Sender.Login
			if login == "" {
				login = probe.User.Login
			}
			if login == "" {
				login = probe.User.Username
			}
			if login != "" {
				ev.Actor = &provider.CRUser{Username: login}
			}
		}
	}
}

// admitComment 评论事件准入守卫：模型在位 → 负载归一化 → 解析评论要素 →
// bot 自身评论防循环 → delivery 幂等 → 仓库存世。任一不通过返回 ok=false
// （调用方静默忽略）。delivery 幂等 v3.8.0 下沉 DB（comment_deliveries 主键
// 冲突=已处理）：纯内存 map 在多副本共库与单实例重启场景下全部失效。落库失败
// fail-open（继续处理）——回帖重复可容忍，丢单不可容忍。
func (s *Service) admitComment(ctx context.Context, ev *provider.NormalizedEvent,
	delivery string) (commentMeta, bool) {
	if s.Model == nil {
		s.log().Warn("argus.qa.no_model", "hint", "LLM 未配置，Q&A 评论忽略")
		return commentMeta{}, false
	}
	normalizeCommentEvent(ev)
	meta, ok := parseCommentMeta(ev)
	if !ok {
		return commentMeta{}, false // 解析失败或非 PR 评论（系统注释/commit/普通 issue）
	}
	// bot 自身评论防循环（Info 留痕：静默丢弃曾让评论链路排查无从下手——
	// "事件到没到、进了哪条路" 全靠推断）。
	if ev.Actor != nil && ev.Actor.Username != "" && ev.Actor.Username == s.Cfg.BotUsername {
		s.log().Info("argus.comment.self_skipped", "author", ev.Actor.Username)
		return commentMeta{}, false
	}
	if delivery != "" {
		fresh, err := s.Store.MarkCommentDelivery(ctx, delivery, time.Now())
		if err != nil {
			s.log().Warn("argus.qa.delivery_mark_failed", "delivery", delivery, "error", err.Error())
		} else if !fresh {
			return commentMeta{}, false
		}
	}
	if ev.Repo == nil {
		return commentMeta{}, false
	}
	return meta, true
}

// dispatchSuppression 抑制类评论指令（restore/suppress）：免 Q&A 节流——误报
// 反馈与恢复是低频运营动作且结果回流知识库，节流窗口把它们挡掉会丢反馈。
// 返回 true 表示已消费该评论。
func (s *Service) dispatchSuppression(ctx context.Context, ev *provider.NormalizedEvent,
	meta commentMeta, question string) bool {
	// /argus restore fp:xxxxxxxx —— 误抑制恢复（suppress 反向，误杀可撤销）：
	// 将 suppressed 指纹置回 open，后续轮次重新可见。评论轻量路径，不建任务。
	if fp, ok := extractRestoreFP(question); ok {
		s.handleRestoreComment(ctx, ev, meta.PRNumber, fp)
		return true
	}
	// /argus suppress fp:xxxxxxxx —— 误报反馈（v3.3 知识飞轮入口）：
	// 抑制该指纹后续轮次 + 结论回流知识库。评论轻量路径，不建任务。
	if fp, ok := extractSuppressFP(question); ok {
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
	if hint, ok := extractDescribe(question); ok {
		s.handleDescribe(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, hint, delivery)
		return true
	}
	if target, ok := extractTestGen(question); ok {
		s.handleTestGen(ctx, platform, ev.Repo.Owner, ev.Repo.Name, prNum, prRef, target, delivery)
		return true
	}
	// @argus fix fp:短码（v3.85.0）：acpx 编码 agent 沙箱修复，异步执行。
	if fp, ok := extractFix(question); ok {
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

	question := extractQuestion(meta.Body, s.Cfg.BotUsername)
	if question == "" {
		return // 不含 @argus 指令，忽略
	}
	if s.dispatchSuppression(ctx, ev, meta, question) {
		return
	}

	prNum := fmt.Sprintf("%d", meta.PRNumber)
	prRef := ev.Repo.Owner + "/" + ev.Repo.Name + "#" + prNum

	// per-PR 节流（30 秒冷却）。v3.82.0 引入 ekit data/cache：TTL 过期 + 容量
	// 上限由库接管，取代手写 map+mutex+「超 100 清 1h 前旧条目」驱逐循环。
	// v3.84.0 起实例回归 per-Service（进程级单例曾令 e2e 同进程多 Service
	// 互相污染节流窗口；生产行为不变）。
	throttleKey := ev.Repo.Owner + "/" + ev.Repo.Name + "/" + prNum
	if v, _ := s.qaThrottle().Get(ctx, throttleKey); v != nil {
		s.log().Warn("argus.qa.throttled", "pr", prRef, "question", question)
		return
	}
	_ = s.qaThrottle().Set(ctx, throttleKey, &qaThrottled)

	s.log().Info("argus.qa.received", "pr", prRef, "question", question,
		"actor", ev.Actor.Username, "event_id", delivery)

	if s.dispatchWriting(ctx, ev, question, string(ev.Source), prNum, prRef, delivery) {
		return
	}
	s.answerComment(ctx, ev, question, string(ev.Source), prNum, prRef, delivery)
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

// extractQuestion 从评论正文提取 @argus ask 后面的问题文本。
// 支持 "@argus ask ..." 和 "@argus ..." 两种格式；返回空串表示不匹配。
// extractSuppressFP 解析 "suppress fp:xxxxxxxx" 指令（fp 短码 ≥6 位十六进制）。
func extractSuppressFP(question string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(question), "suppress"))
	return parseFPHex(rest)
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

func extractQuestion(body, botUsername string) string {
	// 先规整原文再取切片（深挖修复）：此前 lower 基于 TrimSpace 后的串、切片基于
	// 原始 body，前导空白时切点前移——产出乱码前缀甚至切进多字节 rune 中间
	body = strings.TrimSpace(body)
	lower := strings.ToLower(body)
	mention := "@" + strings.ToLower(botUsername)
	if !strings.HasPrefix(lower, mention) {
		return ""
	}
	rest := strings.TrimSpace(body[len(mention):])
	rest = strings.TrimSpace(strings.TrimPrefix(rest, "ask"))
	return strings.TrimSpace(rest)
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

// extractRestoreFP 解析 "restore fp:xxxxxxxx" 指令（fp 短码 ≥6 位十六进制）。
// 与 extractSuppressFP 同形但路由独立：restore 指令不得被 suppress 分支截胡。
func extractRestoreFP(question string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(question), "restore"))
	return parseFPHex(rest)
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
func (s *Service) qaThrottle() *ekcache.LocalCache[bool] {
	s.qaThrottleMu.Lock()
	defer s.qaThrottleMu.Unlock()
	if s.qaThrottleCch == nil {
		s.qaThrottleCch = ekcache.NewLocalCache(ekcache.LocalConfig[bool]{
			TTL:     30 * time.Second,
			MaxSize: 200,
		})
	}
	return s.qaThrottleCch
}

var qaThrottled = true
