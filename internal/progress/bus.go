// Package progress Argus 进度事件定义 + agentkit 泛型总线适配。
// Event 类型是 Argus 领域特有的；Bus 委托给 agentkit/progress.Bus[T]。
package progress

import (
	agentkitprogress "github.com/oagentkit/runtime/progress"
)

// 事件 Type 词表（v3.109.0 常量化——此前 5 处发布点裸字面量，词表只活在
// 注释里；SSE 无类型过滤，新增类型对既有消费者透明）。
const (
	EventTaskCreated  = "task.created"
	EventTaskRunning  = "task.running"
	EventTaskStage    = "task.stage"
	EventTaskFinished = "task.finished"
)

// Event 词表：Type ∈ EventTask* 常量。task.stage（v3.40.0）：阶段级进度
// （Stage ∈ R1/R2/R3…），消费者可展示「现在跑到哪一步」。
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
