// risk.go —— 确定性风险评分：分数与理由一并落快照（可复现、可解释），LLM
// 只做语义描述不参与打分。启发式阈值刻意保守（宁可 medium 不虚 high），
// 加分面见 scoreRisk 内注释。

package impactview

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// entrypointPathFrag 入口面路径启发式（HTTP handler / 路由 / CLI 入口所在的
// 常见目录词根，大小写不敏感子串匹配）。
var entrypointPathFrag = []string{"handler", "router", "route", "controller", "transport", "apiserver", "middleware", "cmd/"}

// entrypointNameSuffix 入口面函数名启发式后缀/全名。
var entrypointNameSuffix = []string{"handler", "handle", "main"}

// scoreRisk 确定性评分。输入为 Build 各层中间产物的聚合面（避免与 Snapshot
// 相互依赖形成渲染耦合）。
func scoreRisk(in riskInput) Risk {
	r := Risk{Level: RiskLow}
	score := 10 // 基础分：任何进入审查的变更都有非零影响
	reasons := []string{}

	pkgs := in.AffectedPackages
	if pkgs > 0 {
		add := minInt(pkgs*6, 24)
		score += add
		reasons = append(reasons, strconv.Itoa(add)+"分：波及 "+strconv.Itoa(pkgs)+" 个依赖方包")
	}
	if in.CallSites > 0 {
		add := minInt(in.CallSites*2, 16)
		score += add
		reasons = append(reasons, strconv.Itoa(add)+"分：命中 "+strconv.Itoa(in.CallSites)+" 处调用方")
	}
	if in.ScriptAffected > 0 {
		add := minInt(in.ScriptAffected*2, 12)
		score += add
		reasons = append(reasons, strconv.Itoa(add)+"分：波及 "+strconv.Itoa(in.ScriptAffected)+" 个脚本/用表方（词法图）")
	}
	if in.ExportedSymbols > 0 {
		score += 10
		reasons = append(reasons, "10分：改动含导出符号（对外契约面）")
	}
	if in.SignatureChanged {
		score += 8
		reasons = append(reasons, "8分：函数签名变更（func 声明行在 ± diff 中）")
	}
	if in.TestsMissing {
		score += 10
		reasons = append(reasons, "10分：受影响包均无 *_test.go 覆盖")
	}
	if in.EntrypointTouched {
		score += 6
		reasons = append(reasons, "6分：触达入口面（handler/路由/main 启发式）")
	}
	if in.StrongCoupling > 0 {
		add := minInt(in.StrongCoupling*3, 9)
		score += add
		reasons = append(reasons, strconv.Itoa(add)+"分：历史强共变文件 "+strconv.Itoa(in.StrongCoupling)+" 个（import 图外的隐性耦合）")
	}
	if in.DepsChanged {
		score += 8
		reasons = append(reasons, "8分：依赖清单变更（go.mod/go.sum）——传递面超出本仓静态图")
	}
	if in.TestsOnlyChanged {
		add := -8
		score += add
		reasons = append(reasons, "-8分：改动仅为测试文件（不达产线，影响面收敛）")
	}

	r.Score = maxInt(minInt(score, 100), 0)
	switch {
	case r.Score >= 55:
		r.Level = RiskHigh
	case r.Score >= 25:
		r.Level = RiskMedium
	}
	sort.Strings(reasons)
	r.Reasons = reasons
	return r
}

// riskInput 评分输入（Build 内组装）。
type riskInput struct {
	AffectedPackages  int  // 反向闭包包数（不含改动包自身）
	CallSites         int  // 精化后调用位总数
	ExportedSymbols   int  // 改动触达的导出符号数
	SignatureChanged  bool // diff 中有 func 声明行增删
	TestsMissing      bool // 受影响包全部无测试
	EntrypointTouched bool
	StrongCoupling    int  // ≥MinCoupleCount 的共变对数
	DepsChanged       bool // go.mod/go.sum 在改动清单（依赖面变更）
	TestsOnlyChanged  bool // 改动全部为测试文件（降分项）
	ScriptAffected    int  // v5.6.0 词法图波及文件数（非 Go 仓 source/血缘传导）
}

// detectSignatureChange diff 文本里是否有 func 声明行的增/删（近似判定：
// 展开多行签名不可见，参数表变化但声明行整体重写时同样命中）。
func detectSignatureChange(diffText string) bool {
	for _, line := range strings.Split(diffText, "\n") {
		if len(line) > 0 && (line[0] == '+' || line[0] == '-') && !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
			if funcDeclRevised(line[1:]) {
				return true
			}
		}
	}
	return false
}

// funcDeclRevised 行内容是否为 Go func 声明头（含注释行误报可能——保守接受）。
func funcDeclRevised(s string) bool {
	t := strings.TrimLeft(s, " \t")
	return strings.HasPrefix(t, "func ")
}

// touchesEntrypoint 入口面启发式：改动文件路径词根命中，或触达符号名后缀命中。
func touchesEntrypoint(changed []string, syms []symHits) bool {
	for _, f := range changed {
		lf := strings.ToLower(filepath.ToSlash(f))
		for _, frag := range entrypointPathFrag {
			if strings.Contains(lf, frag) {
				return true
			}
		}
		base := strings.ToLower(filepath.Base(f))
		if base == "main.go" {
			return true
		}
	}
	for _, s := range syms {
		ln := strings.ToLower(s.Name)
		for _, suf := range entrypointNameSuffix {
			if strings.HasSuffix(ln, suf) {
				return true
			}
		}
	}
	return false
}

// testsMissing 受影响包（含改动包）是否全部无测试文件。
func testsMissing(pkgs []PkgImpact) bool {
	if len(pkgs) == 0 {
		return false // 无包信息（L1 降级）不判缺测试
	}
	for _, p := range pkgs {
		if p.Tests > 0 {
			return false
		}
	}
	return true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
