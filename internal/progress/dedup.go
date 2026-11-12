// dedup.go —— 实例级事件去重环（v6.12.0 P1：同实例 at-most-once 投递）。
//
// 背景（外部评估实锤）：publishProgress/publishOutbox 是「本地 Bus 快路径 +
// outbox 落库」双写，fanout 环每 500ms 回读 outbox 回灌本地 Bus。此前 seenRing
// 只登记 fanout 读到的 ID——本地发布侧不登记，同实例 SSE 客户端经「本地广播 +
// 500ms 后回灌」两条路径收到同一事件两次。
//
// 修复：dedup 环提升为实例级共享组件（progress 包单源），本地发布侧
// Bus.Publish 前登记 ID，fanout 回灌时命中环即跳过——同一事件 ID 对同一实例
// 的订阅者最多投递一次（at-most-once）。跨实例投递仍是 at-least-once（outbox
// 轮询语义，SSE 实时面可接受）。
package progress

import "sync"

// Dedup FIFO 去重环：首见返回 true，容量满按 FIFO 淘汰。并发安全。
type Dedup struct {
	mu   sync.Mutex
	ids  map[string]struct{}
	fifo []string
	cap  int
}

// NewDedup 创建容量 cap 的去重环（cap<=0 取 4096——事件峰值 100/s 量级下
// 「本实例 500ms 窗口内发过」的集合上界）。
func NewDedup(cap int) *Dedup {
	if cap <= 0 {
		cap = 4096
	}
	return &Dedup{ids: make(map[string]struct{}, cap), cap: cap}
}

// Add 首见返回 true（应投递）；已见返回 false（应跳过）。
func (d *Dedup) Add(id string) bool {
	if id == "" {
		return false // 空 ID 视为已见（坏行防御，与 fanout 旧语义一致）
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.ids[id]; ok {
		return false
	}
	d.ids[id] = struct{}{}
	d.fifo = append(d.fifo, id)
	if len(d.fifo) > d.cap {
		old := d.fifo[0]
		d.fifo = d.fifo[1:]
		delete(d.ids, old)
	}
	return true
}

// Seen 已见查询（不登记）。
func (d *Dedup) Seen(id string) bool {
	if id == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.ids[id]
	return ok
}
