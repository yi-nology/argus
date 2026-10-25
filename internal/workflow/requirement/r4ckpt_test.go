package requirement

import (
	"encoding/json"
	"sort"
	"sync"
	"testing"

	"argus/internal/dispatch"
	"argus/internal/plugin"
)

func r4sel(names ...string) []dispatch.Selection {
	out := make([]dispatch.Selection, 0, len(names))
	for _, n := range names {
		name := n
		out = append(out, dispatch.Selection{Reg: &dispatch.Registration{
			Manifest: plugin.AgentManifest{Name: name},
			Enabled:  true,
		}})
	}
	return out
}

func TestRestoreR4Outcomes_HitMissCorrupt(t *testing.T) {
	good := plugin.Result{Notes: "cached", Findings: []plugin.Finding{{Comment: "f1"}}}
	goodJSON, _ := json.Marshal(good)
	arts := map[string]string{
		plugin.ArtifactCkptR4Prefix + "sec":     string(goodJSON),
		plugin.ArtifactCkptR4Prefix + "corrupt": "{bad json",
		plugin.ArtifactCkptR1Spec:               "{}", // 非 R4 键不参与
	}
	exec, restoredSel, restored := restoreR4Outcomes(arts, r4sel("sec", "perf", "corrupt"))
	if len(exec) != 2 {
		t.Fatalf("待执行 = %d, want 2（perf 未命中 + corrupt 损坏）", len(exec))
	}
	if len(restored) != 1 || len(restoredSel) != 1 {
		t.Fatalf("复用 = %d/%d, want 1/1", len(restored), len(restoredSel))
	}
	if restored[0].Name != "sec" || restored[0].Result.Notes != "cached" {
		t.Fatalf("复用产物错位：%+v", restored[0])
	}
	if restored[0].Manifest.Name != "sec" {
		t.Fatalf("Manifest 应取自当前选中集，got %q", restored[0].Manifest.Name)
	}
}

func TestRestoreR4Outcomes_EmptyArtifacts(t *testing.T) {
	exec, restoredSel, restored := restoreR4Outcomes(nil, r4sel("sec"))
	if len(exec) != 1 || len(restoredSel) != 0 || len(restored) != 0 {
		t.Fatal("无 checkpoint 时应全量执行")
	}
}

// TestRestoreR4Outcomes_FanOutOfOrderTrusted 收编核心语义：R4 插件彼此独立
// （Isolated），乱序完成各自独立可信——「第 1 个没完成、第 3 个完成」的合法完成集
// 不再被旧「连续前缀」规则作废。此处 sec 未落键（未完成）、qa 已落键（乱序完成）：
// qa 应被复用（可信），不因 sec 空洞而重跑。
func TestRestoreR4Outcomes_FanOutOfOrderTrusted(t *testing.T) {
	good := plugin.Result{Notes: "restored"}
	goodJSON, _ := json.Marshal(good)
	arts := map[string]string{
		plugin.ArtifactCkptR4Prefix + "qa": string(goodJSON),
		// sec 故意不落键（未完成）——fan-out 下不应作废 qa 的完成
	}
	exec, restoredSel, restored := restoreR4Outcomes(arts, r4sel("sec", "perf", "qa"))
	if len(restored) != 1 || restored[0].Name != "qa" {
		t.Fatalf("乱序完成的 qa 应被复用，got %+v", restored)
	}
	if len(exec) != 2 {
		t.Fatalf("未完成的 sec/perf 应待执行，got %d", len(exec))
	}
	// 关键：不因 sec 空洞报「失序」而整批重跑——qa 仍在复用集
	if len(restoredSel) != 1 || restoredSel[0].Reg.Manifest.Name != "qa" {
		t.Fatalf("restoredSel 应只含 qa，got %+v", restoredSel)
	}
}

func TestR4CkptHook_WritesEncodedResult(t *testing.T) {
	var got map[string]string
	w := &Workflow{CheckpointSink: func(a map[string]string) { got = a }}
	w.r4CkptHook(dispatch.Outcome{Name: "sec", Result: &plugin.Result{Notes: "n1"}})
	raw, ok := got[plugin.ArtifactCkptR4Prefix+"sec"]
	if !ok {
		t.Fatal("hook 未落键")
	}
	var res plugin.Result
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Notes != "n1" {
		t.Fatalf("落键内容往返失败：err=%v notes=%q", err, res.Notes)
	}
	// nil Result 不落
	got = nil
	w.r4CkptHook(dispatch.Outcome{Name: "bad"})
	if got != nil {
		t.Fatal("nil Result 不应落键")
	}
}

// TestR4CkptHook_ConcurrentNoLostUpdate fan-out 并发落键串行化：
// MergeTaskStageArtifacts 是读-改-写，锁在 r4CkptHook 内——并发 N 个插件
// 同时落键，最终键集必须全到齐。
func TestR4CkptHook_ConcurrentNoLostUpdate(t *testing.T) {
	var mu sync.Mutex
	saved := map[string]string{}
	w := &Workflow{CheckpointSink: func(a map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		for k, v := range a {
			saved[k] = v
		}
	}}
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			w.r4CkptHook(dispatch.Outcome{Name: n, Result: &plugin.Result{Notes: n}})
		}(name)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(saved) != 8 {
		keys := make([]string, 0, len(saved))
		for k := range saved {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Fatalf("并发落键丢失：got %d keys %v", len(saved), keys)
	}
}
