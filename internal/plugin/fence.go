package plugin

import (
	"strings"

	"github.com/oagentkit/guardx/fence"
	"github.com/oekit/pkg/shellx"
)

// DataFence 数据区包裹（防线 #1 的统一实现；v3.5.6 自 requirement/prompts.go 上提——
// workflow、builtin 审查插件、Q&A 引擎三处提示词组装方共用同一防线，canonical
// 归属应是插件契约层而非某个编排包）。
// 内容中出现的围栏标记序列会被中和（插入空格破坏 token），
// 防止 PR 内容伪造"数据区结束"把后续注入文本抬出数据区。
// 中和发生即注入特征信号：经 ReportFenceNeutralized 留痕（深挖修复：防线此前
// 纯字符串函数零观测，与反思闸门有指标形成观测不对称）。
// v3.13.0：围栏实现下沉 agentkit fence 包（多产品复用的输入卫生原语）；
// 本包保留门面并接观测钩子。
func DataFence(title, content string) string {
	out, neutralized := fence.Data(title, content)
	if neutralized > 0 {
		ReportFenceNeutralized(neutralized)
	}
	return out
}

// ResolveShellTemplate 沙箱验证命令模板的 {dir} 占位符替换（fixverifier 与
// sandbox_verify 同源单点，v5.3.2 起 shellx.Quote 转义）：模板本身是部署方声明
// 的可信 shell，唯一外部输入是 dir——路径含空格/引号/$ 等元字符时未转义会被
// shell 拆词或展开，验证命令静默跑错目录。单引号包裹后 `-v {dir}:/w` 形态的
// 空格不再拆词，语义与直传一致。
func ResolveShellTemplate(tmpl, dir string) string {
	return strings.ReplaceAll(tmpl, "{dir}", shellx.Quote(dir))
}
