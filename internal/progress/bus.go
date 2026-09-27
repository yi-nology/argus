// Package progress Argus 进度事件定义 + agentkit 泛型总线适配。
// Event 类型是 Argus 领域特有的；Bus 委托给 agentkit/progress.Bus[T]。
package progress

import (
	agentkitprogress "git.example.com/enjoydream/agentkit/progress"
)

// Event 词表：Type ∈ task.created | task.running | task.stage | task.finished。
// task.stage（v3.40.0）：阶段级进度（Stage ∈ R1/R2/R3…），消费者可展示
// 「现在跑到哪一步」；SSE 无类型过滤，新增类型对既有消费者透明。
type Event struct {
	Type     string `json:"type"`
	TaskID   string `json:"task_id"`
	Platform string `json:"platform,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Number   string `json:"number,omitempty"`
	Status   string `json:"status,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	Stage    string `json:"stage,omitempty"`
	At       string `json:"at"`
}

// Bus 类型别名：委托给 agentkit 泛型总线。
type Bus = agentkitprogress.Bus[Event]

// NewBus 创建事件总线。
func NewBus() *Bus {
	return agentkitprogress.NewBus[Event]()
}
