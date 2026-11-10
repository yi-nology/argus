package health

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/learning"
)

type fakeStore struct {
	repos []learning.RepoRef
	rows  map[string][]learning.PatternState
}

func (f *fakeStore) ListPatternRepos(context.Context) ([]learning.RepoRef, error) {
	return f.repos, nil
}

func (f *fakeStore) ListRepoPatternStates(_ context.Context, platform, owner, repo string) ([]learning.PatternState, error) {
	return f.rows[platform+"/"+owner+"/"+repo], nil
}

func TestBuild_Classification(t *testing.T) {
	fs := &fakeStore{
		repos: []learning.RepoRef{{Platform: "gitea", Owner: "acme", Repo: "app"}},
		rows: map[string][]learning.PatternState{
			"gitea/acme/app": {
				// 噪声：样本充足、置信度 < HardFloor(0.20)
				// Confidence(posted=1, regret=5, suppress=0) ≈ 3/(3+12) = 0.20 → 用更大 regret 压到 <0.2
				{Pattern: "k1", PatternText: "错误处理不佳", Plugin: "correctness",
					Posted: 1, Regret: 7},
				// guidelines 候选：posted 高、零 suppress、置信度高
				{Pattern: "k2", PatternText: "缺少 doc comment", Plugin: "style",
					Posted: 5},
				// 已被人工 suppress 的不再进 guidelines（人工已处置）
				{Pattern: "k3", PatternText: "命名风格", Plugin: "style",
					Posted: 4, Suppress: 2},
			},
		},
	}
	rep, err := Build(context.Background(), fs, Config{
		Learning: learning.Config{Enabled: true, MinSamples: 3, SoftFloor: 0.35, HardFloor: 0.20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Repos) != 1 {
		t.Fatalf("应 1 个仓库: %+v", rep.Repos)
	}
	rh := rep.Repos[0]
	if len(rh.Noisy) != 1 || rh.Noisy[0].Sample != "错误处理不佳" {
		t.Fatalf("k1 应进噪声处置: %+v", rh.Noisy)
	}
	if len(rh.Guideline) != 1 || rh.Guideline[0].Sample != "缺少 doc comment" {
		t.Fatalf("k2 应进 guidelines 建议（k3 已被人工 suppress 排除）: %+v", rh.Guideline)
	}
}

func TestRender_NoisyAndGuideline(t *testing.T) {
	rep := &Report{
		GeneratedAt: time.Now().UTC(),
		Repos: []RepoHealth{{
			Platform: "gitea", Owner: "acme", Repo: "app",
			Posted: 6, Regret: 7, Suppress: 2, Patterns: 3,
			Noisy:     []Tip{{Plugin: "correctness", Sample: "错误处理不佳", Confidence: 0.15, Suppress: 0, Regret: 7}},
			Guideline: []Tip{{Plugin: "style", Sample: "缺少 doc comment", Posted: 5}},
		}},
	}
	out := Render(rep, learning.Config{Enabled: true, SoftFloor: 0.35, HardFloor: 0.20, MinSamples: 3})
	for _, want := range []string{"审查健康报告", "acme/app", "噪声处置建议", "guidelines 增量建议", "绝不自动改仓库文件"} {
		if !strings.Contains(out, want) {
			t.Fatalf("报告缺 %q:\n%s", want, out)
		}
	}
	// 人工门控红线：报告必须带人工处置措辞（噪声→人工复核；guidelines→人工加入）
	for _, want := range []string{"请人工复核", "确认后人工加入"} {
		if !strings.Contains(out, want) {
			t.Fatalf("报告缺人工门控措辞 %q:\n%s", want, out)
		}
	}
}

func TestRender_EmptyNoPush(t *testing.T) {
	if out := Render(nil, learning.Config{}); out != "" {
		t.Fatalf("nil 报告应空渲染: %q", out)
	}
	if out := Render(&Report{Empty: true}, learning.Config{}); out != "" {
		t.Fatalf("空报告应空渲染: %q", out)
	}
}

// RunOnce 空数据：不推送、不报错（宁缺勿扰）。
func TestRunOnce_EmptySkipsNotify(t *testing.T) {
	fs := &fakeStore{}
	notified := false
	r := &Runner{
		Store: fs,
		Notify: notifyFunc(func(ctx context.Context, content string) {
			notified = true
		}),
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if notified {
		t.Fatal("空报告不应推送")
	}
}

type notifyFunc func(ctx context.Context, content string)

func (f notifyFunc) NotifyRaw(ctx context.Context, content string) { f(ctx, content) }
