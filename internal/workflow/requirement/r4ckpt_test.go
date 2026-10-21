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
