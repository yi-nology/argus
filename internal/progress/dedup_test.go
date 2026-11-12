package progress

import "testing"

// TestDedupAddFirstSeen 首见 true / 重复 false / FIFO 淘汰。
func TestDedupAddFirstSeen(t *testing.T) {
	d := NewDedup(3)
	if !d.Add("a") {
		t.Fatal("a 首见应返回 true")
	}
	if d.Add("a") {
		t.Fatal("a 重复应返回 false")
	}
	_ = d.Add("b")
	_ = d.Add("c")
	if !d.Add("d") {
		t.Fatal("容量满后 d 应入环（a 被 FIFO 淘汰）→ true")
	}
	if !d.Add("a") {
		t.Fatal("a 已被 FIFO 淘汰，应重新首见")
	}
}

// TestDedupEmptyID 空 ID 视为已见（坏行防御）。
func TestDedupEmptyID(t *testing.T) {
	d := NewDedup(0)
	if d.Add("") {
		t.Fatal("空 ID 应返回 false")
	}
	if !d.Seen("") {
		t.Fatal("空 ID Seen 应为 true")
	}
}

// TestDedupConcurrent 并发 Add 同 ID 恰一次成功。
func TestDedupConcurrent(t *testing.T) {
	d := NewDedup(100)
	const n = 50
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() { results <- d.Add("same") }()
	}
	first := 0
	for i := 0; i < n; i++ {
		if <-results {
			first++
		}
	}
	if first != 1 {
		t.Fatalf("并发 50 次 Add 应恰 1 次成功, got %d", first)
	}
}
