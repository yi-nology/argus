// Package reposettings 服务侧按仓配置（v3.187.0）：部署方经 admin API 写入的
// 仓库级覆盖（键 platform/owner/repo，值 JSON 落 repo_settings 表）。与仓库内
// .reviewagents.yaml（半可信、只能收窄）分层：本包是 admin token 写入的可信面，
// 覆盖值可直接上调或下调部署默认（预算/上限类旋钮），不碰供应链红线（SAST/
// OCR/LLM endpoint 永远只来自部署 env）。
package reposettings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// maxKnobValue 旋钮防呆上限（int 溢出防线；1e9 token/字符在真实部署下不可能触达）。
const maxKnobValue = 1_000_000_000

// Settings 按仓覆盖集。指针字段区分「未设置」（落全局默认）与显式值；0 与负数
// 在 Parse 拒绝（预算/上限旋钮取 0 无意义，删除覆盖走 DELETE API 而非写 0）。
type Settings struct {
	// MonthlyTokenBudget 覆盖全局 ARGUS_REPO_MONTHLY_TOKEN_BUDGET（仓库月度
	// token 预算闸；runner repoBudgetGate 消费）。
	MonthlyTokenBudget *int `json:"monthly_token_budget,omitempty"`
	// TaskTokenBudget 覆盖 EffectiveTaskTokenBudget()（单任务 token 预算，
	// runner Execute 消费——显式值绕过窗口公式派生）。
	TaskTokenBudget *int `json:"task_token_budget,omitempty"`
	// MaxDiffChars 覆盖 MaxDiffCharsResolved()（diff 总量上限；runner
	// prepareContext 与 service describe 共用）。
	MaxDiffChars *int `json:"max_diff_chars,omitempty"`
}

// Parse 解析并校验按仓配置 JSON。DisallowUnknownFields：拼写错误（如
// maxDiffChars）直接报错而不是静默无效——配置写错了比不生效更危险；
// 尾随内容一并拒绝（防手滑拼接两段 JSON 只生效第一段的误读）。
func Parse(data []byte) (*Settings, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Settings
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("解析 repo_settings: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("repo_settings 含尾随内容（只允许单个 JSON 对象）")
	}
	for name, v := range map[string]*int{
		"monthly_token_budget": s.MonthlyTokenBudget,
		"task_token_budget":    s.TaskTokenBudget,
		"max_diff_chars":       s.MaxDiffChars,
	} {
		if v == nil {
			continue
		}
		if *v <= 0 || *v > maxKnobValue {
			return nil, fmt.Errorf("%s 须为 1~%d 的正整数，得到 %d", name, maxKnobValue, *v)
		}
	}
	return &s, nil
}

// Normalize 规范形态落库（omitempty 收敛后 marshal；读取侧 Parse 单源）。
func (s *Settings) Normalize() ([]byte, error) { return json.Marshal(s) }

// IsZero 全部字段未设置（调用方应拒收——空覆盖无意义，语义等同 DELETE）。
func (s *Settings) IsZero() bool {
	return s == nil || (s.MonthlyTokenBudget == nil && s.TaskTokenBudget == nil && s.MaxDiffChars == nil)
}

// MonthlyTokenBudgetOr 生效值：未设置落 def（调用方传全局缺省）。nil receiver
// 安全（nil Settings = 全默认）。
func (s *Settings) MonthlyTokenBudgetOr(def int) int {
	if s == nil {
		return def
	}
	return or(s.MonthlyTokenBudget, def)
}

// TaskTokenBudgetOr 生效值：未设置落 def。
func (s *Settings) TaskTokenBudgetOr(def int) int {
	if s == nil {
		return def
	}
	return or(s.TaskTokenBudget, def)
}

// MaxDiffCharsOr 生效值：未设置落 def。
func (s *Settings) MaxDiffCharsOr(def int) int {
	if s == nil {
		return def
	}
	return or(s.MaxDiffChars, def)
}

func or(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}
