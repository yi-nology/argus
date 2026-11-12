// pii_mask.go —— 出站评论 PII 脱敏（v6.16.0，goagent-kernel/pii 接入，合规新增面）。
//
// 接入口 = SDKCtx 三个评论写方法（CreateIssueComment/UpdateIssueComment/
// CreateReview）——全部平台评论出站的唯一 choke point（poster/service 都经此）。
// 值面检测（手机号/身份证四重校验）在上游 gak pii 单源；logredact 是键名面
// 凭据脱敏，与本面互补不重叠。
//
// 策略（用户拍板合规优先）：ARGUS_PII_MASK 缺省开（=0 关）。脱敏发生即在正文
// 尾部追加一行透明脚注（静默改内容比泄漏更糟——审阅者有权知道文本被处理过）。

package argusctx

import (
	"strings"

	"github.com/yi-nology/go-git-platform/provider"
	"github.com/yi-nology/goagent-kernel/pii"
)

// piiMaskNote 脱敏发生时的透明脚注（计数不入注——避免泄露命中次数推断内容）。
const piiMaskNote = "\n\n<sub>🔒 本评论已启用 PII 脱敏（手机号/身份证号已打码；ARGUS_PII_MASK=0 可关闭）</sub>"

// maskOutboundPII 对出站评论正文施加值面 PII 脱敏；文本被改写时追加透明脚注。
// 返回处理后的正文。策略未启用时原样直通（pii.Apply 内部判 enabled）。
//
// 注意 gak pii.Apply 的 bool 是「阻断」语义（mask 策略恒 false），**不是**「是否
// 改写」——是否命中以输出比对判定（v6.16.0 实测踩坑：按 bool 判会导致脚注永不
// 出现）。幂等：已打码文本无 PII 可检出，输出=输入原样返回；脚注已存在不重复
// 追加（总结评论轮换更新场景——旧脚注随正文带进来）。
func maskOutboundPII(body string) string {
	masked, _ := pii.Apply(pii.SurfaceOutput, body)
	if masked == body {
		return body
	}
	if strings.Contains(masked, piiMaskNote) {
		return masked
	}
	return masked + piiMaskNote
}

// maskReviewOptions 行内 review 的整包脱敏（总评 Body + 每条行内评论 Body）。
func maskReviewOptions(opts provider.CreateReviewOptions) provider.CreateReviewOptions {
	opts.Body = maskOutboundPII(opts.Body)
	for i := range opts.Comments {
		opts.Comments[i].Body = maskOutboundPII(opts.Comments[i].Body)
	}
	return opts
}
