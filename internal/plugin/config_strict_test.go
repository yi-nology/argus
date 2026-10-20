package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadPluginsFileStrict 严格解码：未知键拒载（自有配置的拼写错误响亮失败，
// 不再静默忽略让插件悄悄不生效——v3.159.0）。
func TestLoadPluginsFileStrict(t *testing.T) {
	dir := t.TempDir()

	// 合法配置照常加载
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte(`
agents:
  - name: sec
    kind: builtin
    skills: [code-review]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPluginsFile(good); err != nil {
		t.Fatalf("合法配置应加载：%v", err)
	}

	// 未知键（typo）拒载且错误指向字段
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte(`
agents:
  - name: sec
    kind: builtin
    skils: [code-review]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPluginsFile(bad)
	if err == nil || !strings.Contains(err.Error(), "skils") {
		t.Fatalf("typo 键应拒载且点名：%v", err)
	}
	// v3.160.0：oekit core v0.11.0 严格模式诊断面——嵌套路径 + 拼写建议 +
	// 该层合法键清单（回退 KnownFields 即丢这三样，此断言防退化）
	for _, want := range []string{"agents.skils", `"skills"`, "合法键"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("诊断面缺 %q：%v", want, err)
		}
	}
}

// TestLoadPluginsExampleShipsClean 随仓分发的 plugins.example.yaml 必须过严格
// 解码——它是用户抄写起点，示例自身带未知键=严格模式上线即破坏所有复制者。
func TestLoadPluginsExampleShipsClean(t *testing.T) {
	_, err := LoadPluginsFile("../../plugins.example.yaml")
	if err != nil {
		t.Fatalf("示例配置须过严格解码（漂移即修）：%v", err)
	}
}
