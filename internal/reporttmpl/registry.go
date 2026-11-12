// Package reporttmpl 报告契约模板注册表（design spec 2026-11-12 §4.1）。
// L0 纯底座：零 internal 依赖。模板词表（standard|analyst|minimal）与分析师
// 综合段提示词的唯一出处；plugin（sanitize 校验）与 config（服务级默认解析）
// 运行时消费本包词表——三方钉死由构造保证，防词表漂移（platformVocab 先例）。
// 渲染分发不在本包：reportview.Render 按 Report.Template 内部分发（reportview
// 是既有渲染单源），本包只管「选择面 + 综合段提示词」，避免 reportview 反向
// 依赖本包成环。
package reporttmpl

// 模板词表（白名单唯一出处）。仓库 .reviewagents.yaml 只能「选」不能自定义
// 内容——提示注入面控制（spec §5）。
const (
	// Standard 现状报告（缺省）：验收清单 + findings + 结论 + meta，零行为漂移。
	Standard = "standard"
	// Analyst 分析师深度报告：声明对照表（五态）+ 风险链（chain 证据链）
	// + 结构维度 + 测试缺口 + 待确认清单（spec §4.5/§4.6）。
	Analyst = "analyst"
	// Minimal 极简报告：结论先行 + 仅 high/critical findings + 一行 meta。
	Minimal = "minimal"
)

// Template 单枚模板定义。SynthesisPrompt 仅 analyst 非空（综合段系统提示词，
// workflow merge 后条件调用）；standard/minimal 无综合段，为空串。
type Template struct {
	Key             string
	SynthesisPrompt string
}

// registry 词表 → 模板定义。新增模板必须同步：此处条目、Keys 顺序、
// plugin sanitize 白名单消费、文档契约面计数。
var registry = map[string]Template{
	Standard: {Key: Standard},
	Minimal:  {Key: Minimal},
	Analyst:  {Key: Analyst, SynthesisPrompt: analystSynthesisPrompt},
}

// Lookup 按键取模板；未知键返回 false。
func Lookup(key string) (Template, bool) {
	t, ok := registry[key]
	return t, ok
}

// IsValid 键是否在白名单词表内。
func IsValid(key string) bool {
	_, ok := registry[key]
	return ok
}

// Keys 有序词表（字母序：[analyst minimal standard]）。测试钉 + sanitize
// 遍历校验用；顺序即文档枚举顺序。
func Keys() []string {
	return []string{Analyst, Minimal, Standard}
}

// Resolve 模板优先级裁决：repo（按仓 yaml）> service（env 默认）> standard。
// 任一层给非法/空值都向下一层回退（sanitize 已挡非法值到不了这里，这里再做
// 防御——配置故障不阻断审查，与 loadRepoConfig 同纪律）。
func Resolve(repoLevel, serviceLevel string) string {
	for _, c := range []string{repoLevel, serviceLevel} {
		if IsValid(c) {
			return c
		}
	}
	return Standard
}
