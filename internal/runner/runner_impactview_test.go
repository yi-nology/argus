// runner_impactview_test.go —— injectImpactView 门控分支单测（v3.185.0）：
// 开关/池缺失/空纳入文件三道早退，裸 Runner 构造（Ensure 之前的分支不触
// Log/Store/池，同 runner_related_test 的 mk 模式）；impactBaseRef 纯函数
// 钉死 base 参照取舍。
package runner

import (
	"context"
	"testing"

	"github.com/oekit/logx"

	"argus/internal/config"
	"argus/internal/domain"
	"argus/internal/plugin"
	"argus/internal/storage"
)

// TestInjectImpactViewGates 门控矩阵：关/无池/空纳入均无产物且不 panic。
func TestInjectImpactViewGates(t *testing.T) {
	log := logx.Nop()
	newRunner := func(cfg *config.Config) *Runner {
		return &Runner{Cfg: cfg, Log: log}
	}
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "o", "r", "7")
	ptask := plugin.Task{Artifacts: map[string]string{}}

	// 1. ARGUS_IMPACTVIEW=0 → 整段跳过
	cfgOff := &config.Config{ImpactView: false}
	newRunner(cfgOff).injectImpactView(context.TODO(), task.ID,
		&plugin.PRContext{BaseBranch: "main"}, nil, &ptask, nil)
	if len(ptask.Artifacts) != 0 {
		t.Fatal("关闭开关不应有产物")
	}

	// 2. 开关开、无工作副本池 → 早退
	cfgOn := &config.Config{ImpactView: true, ImpactCoupling: 0}
	diff := &plugin.DiffContext{Files: []plugin.DiffFile{{Path: "a.go", Included: true}}}
	newRunner(cfgOn).injectImpactView(context.TODO(), task.ID,
		&plugin.PRContext{BaseBranch: "main"}, diff, &ptask, nil)
	if _, ok := ptask.Artifacts[plugin.ArtifactImpactView]; ok {
		t.Fatal("无池不应有产物")
	}

	// 3. 开关开、有池形态（WC 为 nil 同上，改测空纳入文件）
	emptyDiff := &plugin.DiffContext{}
	newRunner(cfgOn).injectImpactView(context.TODO(), task.ID,
		&plugin.PRContext{BaseBranch: "main"}, emptyDiff, &ptask, nil)
	if _, ok := ptask.Artifacts[plugin.ArtifactImpactSummary]; ok {
		t.Fatal("空纳入文件不应有 prompt 段")
	}
}

// TestImpactBaseRef base 参照取舍：PR base 分支优先，回落默认分支，全空为空。
func TestImpactBaseRef(t *testing.T) {
	cases := []struct {
		base, def, want string
	}{
		{"feature-x", "main", "origin/feature-x"},
		{"", "main", "origin/main"},
		{"", "", ""},
	}
	for _, c := range cases {
		pr := plugin.PRContext{BaseBranch: c.base, DefaultBranch: c.def}
		if got := impactBaseRef(&pr); got != c.want {
			t.Fatalf("impactBaseRef(%q,%q) = %q, want %q", c.base, c.def, got, c.want)
		}
	}
}
