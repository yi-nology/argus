package obs

// RefreshEvalResults 单测（v3.14.0）：完整结果计分、增量快照不计分不覆盖、
// 文件缺失返回 fs.ErrNotExist 供调用方静默。

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func writeEvalFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "latest.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const evalFixtureDone = `{
  "generated_at": "2026-09-18T12:00:00+08:00",
  "model": "mimo-v2.5",
  "stage_models": {"r3": "mimo-v2.5-pro", "reviewer": "mimo-v2.5-pro", "r35": ""},
  "total": 26, "passed": 21, "pass_rate": 0.8077, "done": true,
  "cases": []
}`

func TestRefreshEvalResultsDone(t *testing.T) {
	path := writeEvalFixture(t, evalFixtureDone)
	if err := RefreshEvalResults(path); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	labels := map[string]string{"model": "mimo-v2.5", "route": "r3=mimo-v2.5-pro,reviewer=mimo-v2.5-pro"}
	if got := testutil.ToFloat64(evalMatchRatio.With(labels)); got < 0.8076 || got > 0.8078 {
		t.Fatalf("match_ratio = %v, want ~0.8077", got)
	}
	if got := testutil.ToFloat64(evalCasesTotal.With(labels)); got != 26 {
		t.Fatalf("cases_total = %v, want 26", got)
	}
	if got := testutil.ToFloat64(evalCasesPassed.With(labels)); got != 21 {
		t.Fatalf("cases_passed = %v, want 21", got)
	}
	// 时间戳 = 2026-09-18T12:00:00+08:00 的 unix 秒
	if got := testutil.ToFloat64(evalResultsTimestamp); got != 1789704000 {
		t.Fatalf("timestamp = %v, want 1789704000", got)
	}
}

func TestRefreshEvalResultsIncomplete(t *testing.T) {
	// 包级 gauge 跨测试共享状态：先清场，保证「未写入」断言成立
	evalMatchRatio.Reset()
	evalCasesTotal.Reset()
	evalCasesPassed.Reset()
	path := writeEvalFixture(t, `{"generated_at":"2026-09-18T12:00:00+08:00","model":"m","total":14,"done":false,"cases":[]}`)
	err := RefreshEvalResults(path)
	if !IsIncompleteRefreshErr(err) {
		t.Fatalf("want ErrEvalIncomplete, got %v", err)
	}
	// 增量快照不得写入任何 gauge（此前完整结果保持原值——用零值序列断言未新建）
	if got := testutil.CollectAndCount(evalMatchRatio); got != 0 {
		t.Fatalf("增量快照写入了 %d 个 match_ratio 序列", got)
	}
}

func TestRefreshEvalResultsMissing(t *testing.T) {
	err := RefreshEvalResults(filepath.Join(t.TempDir(), "absent.json"))
	if !IsNotExistRefreshErr(err) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want fs.ErrNotExist, got %v", err)
	}
}

func TestRefreshEvalResultsResetStaleSeries(t *testing.T) {
	// 先写入 v2.5 全局，再刷新为 pro 全局——旧标签序列必须整体消失
	path := writeEvalFixture(t, `{"generated_at":"2026-09-18T12:00:00+08:00","model":"mimo-v2.5","stage_models":{"r3":"","reviewer":"","r35":""},"total":26,"passed":20,"pass_rate":0.77,"done":true,"cases":[]}`)
	if err := RefreshEvalResults(path); err != nil {
		t.Fatal(err)
	}
	path = writeEvalFixture(t, evalFixtureDone)
	if err := RefreshEvalResults(path); err != nil {
		t.Fatal(err)
	}
	if got := testutil.CollectAndCount(evalMatchRatio); got != 1 {
		t.Fatalf("刷新后残留 %d 个序列, want 1（旧组标签不残留）", got)
	}
}

func TestEvalRouteLabel(t *testing.T) {
	cases := []struct {
		in   map[string]string
		want string
	}{
		{map[string]string{"r3": "", "reviewer": "", "r35": ""}, "global"},
		{map[string]string{}, "global"},
		{map[string]string{"r3": "pro", "reviewer": "pro", "r35": ""}, "r3=pro,reviewer=pro"},
		{map[string]string{"r35": "pro"}, "r35=pro"},
	}
	for _, c := range cases {
		if got := evalRouteLabel(c.in); got != c.want {
			t.Errorf("evalRouteLabel(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
