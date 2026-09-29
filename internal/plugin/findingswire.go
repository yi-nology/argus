package plugin

import "git.example.com/enjoydream/agentkit/jsonrepair"

// FindingsWire 审查产出的 findings JSON schema 单源（v3.64.0）：builtin ReAct/
// 单轮、remote 三条路径同 schema，此前匿名结构体三处克隆。By 归属策略各调用方
// 自定（builtin 强制 manifest 名、remote 空时补 "remote"），不入本结构。
type FindingsWire struct {
	Findings []Finding `json:"findings"`
	Notes    string    `json:"notes"`
}

// DecodeFindingsJSON 从模型输出文本宽容解析 findings（jsonrepair 单源：全角标点/
// 尾逗号类损坏不致命）。解析失败的错误包装留给调用方（各路径错误语义不同）。
func DecodeFindingsJSON(text string) (FindingsWire, error) {
	var w FindingsWire
	err := jsonrepair.Unmarshal(text, &w)
	return w, err
}
