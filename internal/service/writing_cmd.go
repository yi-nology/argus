// writing_cmd.go —— 单轮 LLM 写作命令的模板方法（v3.110.0）：
// describe/testgen 的骨架同构（回复模型守卫 → diff 拉取 → 提示词装配 → LLM
// 直调 → 失败记账回帖 → 截断留痕 → 装饰段 → 评论回执）此前两份拷贝各自演化，
// 任一步漂移都无从测红。差异面全部收进 spec 钩子：build（diff→提示词对，可
// 拒绝）、decorate（截断后正文追加段，如 testgen 沙箱验证）。

package service

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/obs"
)

// writingSpec 单轮写作命令的参数与钩子面。
type writingSpec struct {
	// cmd 命令名：回执名 / marker（argus:<cmd>）/ 日志前缀 / LLM stage（大写化）共用。
	cmd string
	// diffFailText diff 缺失时的回帖文案。
	diffFailText string
	// failText LLM 调用失败的回帖文案前缀（后拼 ": "+err）。
	failText string
	maxRunes int
	// build 由 diff 文本产出提示词对；failMsg 非空表示拒绝（如 testgen 的
	// target 过滤空命中），以该文案回帖返回。
	build func(diff string) (sys, user, failMsg string)
	// decorate 可空：截断后的正文追加段。
	decorate func(out string) string
}

// runWritingCommand 模板方法：骨架单源，命令差异全部在 spec。
func (s *Service) runWritingCommand(ctx context.Context, platform, owner, repo, number,
	prRef, delivery string, spec writingSpec) {
	mdl := s.replyModel()
	if mdl == nil {
		return
	}
	diffText, ok := s.prDiffText(ctx, platform, owner, repo, number)
	if !ok {
		s.postQAError(ctx, platform, owner, repo, number, spec.diffFailText)
		return
	}
	sys, user, failMsg := spec.build(diffText)
	if failMsg != "" {
		s.postQAError(ctx, platform, owner, repo, number, failMsg)
		return
	}
	client := llm.NewClient(mdl, "", nil)
	resp, err := client.Generate(ctx, strings.ToUpper(spec.cmd), []*schema.Message{
		schema.SystemMessage(sys),
		schema.UserMessage(user),
	})
	if err != nil {
		obs.IncrQAFailure("llm")
		s.log().Error("argus."+spec.cmd+".llm_failed", "pr", prRef, "error", err.Error())
		s.postQAError(ctx, platform, owner, repo, number, spec.failText+": "+err.Error())
		return
	}
	out := strings.TrimSpace(resp.Content)
	if t, truncated := textutil.TruncRunes(out, spec.maxRunes); truncated {
		out = t + "\n…（截断）"
	}
	if spec.decorate != nil {
		out = spec.decorate(out)
	}
	body := out + "\n\n<!-- argus:" + spec.cmd + " delivery=" + delivery + " -->"
	s.postCommentReply(ctx, platform, owner, repo, number, prRef, body, spec.cmd, len(out))
}
