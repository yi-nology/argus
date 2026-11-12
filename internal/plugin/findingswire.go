package plugin

import (
	"strings"

	"github.com/oekit/pkg/jsonrepair"
)

// FindingsWire 审查产出的 findings JSON schema 单源（v3.64.0）：builtin ReAct/
// 单轮、remote 三条路径同 schema，此前匿名结构体三处克隆。By 归属策略各调用方
// 自定（builtin 强制 manifest 名、remote 空时补 "remote"），不入本结构。
// v6.10.0 扩展 analyst 综合段字段（claims/design/test_gaps/open_questions）：
// 常规插件路径不产出这些字段（omitempty 零值），仅 workflow 分析师综合段解码
// 消费——同一解码函数宽容解析，旧输出零影响。
type FindingsWire struct {
	Findings []Finding `json:"findings"`
	Notes    string    `json:"notes"`
	// 以下四字段为 analyst 模板综合段产物（spec §4.2），常规路径忽略。
	Claims        []Claim  `json:"claims,omitempty"`
	Design        *Design  `json:"design,omitempty"`
	TestGaps      []string `json:"test_gaps,omitempty"`
	OpenQuestions []string `json:"open_questions,omitempty"`
}

// Claim 声明对照表条目（analyst 模板）：MR 描述中的一条可验收声明 vs 代码证据。
type Claim struct {
	Claim    string `json:"claim"`    // 声明原文一句
	Evidence string `json:"evidence"` // 代码证据 file:line 或差距说明
	Status   string `json:"status"`   // ClaimStatus 五词表
}

// ClaimStatus 声明判定五词表（v6.10.0，参考 mr-review playbook 步骤 5.1）：
// 比 AC 检查四态多 undeclared/inconsistent 两态——"描述没提但对外语义变了"与
// "实现与声明/既有模块口径矛盾"恰是深度评审最高危信号。词表校验见
// NormalizeClaimStatus；渲染图标映射见 reportview。
const (
	ClaimImplemented  = "implemented"  // ✅ 实现且一致
	ClaimPartial      = "partial"      // ⚠️ 部分实现（条件/边界/字段不全）
	ClaimMissing      = "missing"      // ❌ 声明了但代码没有
	ClaimUndeclared   = "undeclared"   // ➕ 超出声明（描述没提但改了对外语义）
	ClaimInconsistent = "inconsistent" // ❗ 口径不一致（与声明/既有模块规则矛盾）
)

// claimStatusVocab 五词表判定集（Normalize 用；新增状态必须同步渲染图标映射）。
var claimStatusVocab = map[string]bool{
	ClaimImplemented:  true,
	ClaimPartial:      true,
	ClaimMissing:      true,
	ClaimUndeclared:   true,
	ClaimInconsistent: true,
}

// NormalizeClaimStatus 归一声明判定词表；未知值返回 false（调用方降级空态，
// 不猜——与 NormalizeSeverity 宽容归 low 不同：判定状态错误比缺失更有害）。
func NormalizeClaimStatus(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if claimStatusVocab[s] {
		return s, true
	}
	return "", false
}

// Design 结构维度产出（analyst 模板，spec §4.5）：30 秒判定命中才产出，
// Triggered=false 时其余字段为空、渲染整段省略。
type Design struct {
	Triggered   bool     `json:"triggered"`    // 30 秒判定是否命中结构类改动
	ObjectGraph []string `json:"object_graph"` // 创建者→持有者→写者→读者→生命周期
	Cohesion    []string `json:"cohesion"`     // 内聚问题（path:line 证据 + 修法）
	Coupling    []string `json:"coupling"`     // 耦合问题（path:line 证据 + 修法）
	Migration   string   `json:"migration"`    // 目标骨架 + P0/P1 + 是否阻塞合入
}

// AnalystSections 综合段产物（workflow → runner → reportview 的载体）：
// wire 的渲染安全子集——已做敌对输入钳制（见 SanitizeAnalyst）。
type AnalystSections struct {
	Claims        []Claim
	Design        *Design
	TestGaps      []string
	OpenQuestions []string
}

// 综合段敌对输入钳制上限（模型输出按不可信数据对待，spec §5）：条数与单条
// runes 双钳——防巨型输出吃报告面/撑爆评论长度。
const (
	analystMaxItems     = 20
	analystMaxItemRunes = 500
)

// SanitizeAnalyst 把 wire 综合段字段钳成渲染安全子集：超长列表截断留痕
// （末条追加"…（截断）"注明，与 extra_instructions 截断同纪律）、单条超 runes
// 截断、design 子列表同钳。纯函数，解码后立即调用。
func SanitizeAnalyst(w *FindingsWire) AnalystSections {
	sec := AnalystSections{
		Claims:        clampClaims(w.Claims),
		TestGaps:      clampStrList(w.TestGaps),
		OpenQuestions: clampStrList(w.OpenQuestions),
	}
	if w.Design != nil {
		d := *w.Design // 拷贝，不持模型产出指针
		d.ObjectGraph = clampStrList(d.ObjectGraph)
		d.Cohesion = clampStrList(d.Cohesion)
		d.Coupling = clampStrList(d.Coupling)
		d.Migration = truncItem(d.Migration)
		sec.Design = &d
	}
	return sec
}

// clampClaims 声明表钳制 + 状态词表校验（非法状态的条目整条丢弃——状态错误
// 的声明比缺失更有害，不渲染猜测）。
func clampClaims(in []Claim) []Claim {
	if len(in) == 0 {
		return nil
	}
	n := min(len(in), analystMaxItems)
	truncated := len(in) > analystMaxItems
	out := make([]Claim, 0, n)
	for _, c := range in {
		status, ok := NormalizeClaimStatus(c.Status)
		if !ok {
			continue
		}
		out = append(out, Claim{
			Claim:    truncItem(c.Claim),
			Evidence: truncItem(c.Evidence),
			Status:   status,
		})
		if len(out) >= n {
			break
		}
	}
	if truncated && len(out) > 0 {
		out[len(out)-1].Evidence += "…（截断）"
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// clampStrList 字符串列表钳制（条数 + 单条 runes）。
func clampStrList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	n := min(len(in), analystMaxItems)
	truncated := len(in) > analystMaxItems
	out := make([]string, 0, n)
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, truncItem(s))
		}
		if len(out) >= n {
			break
		}
	}
	if truncated && len(out) > 0 {
		out[len(out)-1] += "…（截断）"
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// truncItem 单条 runes 钳制（超长在词界截断 + 省略号）。
func truncItem(s string) string {
	const max = analystMaxItemRunes
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return s
	}
	cut := string(r[:max])
	if i := strings.LastIndexAny(cut, " ，。；、,.;"); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ，。；、,.;") + "…"
}

// DecodeFindingsJSON 从模型输出文本宽容解析 findings（jsonrepair 单源：全角标点/
// 尾逗号类损坏不致命）。解析失败的错误包装留给调用方（各路径错误语义不同）。
func DecodeFindingsJSON(text string) (FindingsWire, error) {
	var w FindingsWire
	err := jsonrepair.Unmarshal(NormalizeFence(text), &w)
	return w, err
}
