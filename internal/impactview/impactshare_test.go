package impactview

import "testing"

func TestImpactShareTokenDeterministicPerTask(t *testing.T) {
	a1 := ImpactShareToken("s3cret", "t-1")
	a2 := ImpactShareToken("s3cret", "t-1")
	b := ImpactShareToken("s3cret", "t-2")
	if a1 == "" || a1 != a2 {
		t.Fatalf("同任务令牌应恒定: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Fatal("不同任务令牌必须不同")
	}
}

func TestImpactShareEmptySecretDisables(t *testing.T) {
	if ImpactShareToken("", "t-1") != "" || ImpactShareToken("s", "") != "" {
		t.Fatal("secret/taskID 空 = 机制未启用, 应返回空串")
	}
	if ImpactShareValid("", "t-1", "x") {
		t.Fatal("空 secret 一律无效")
	}
}

func TestImpactShareValid(t *testing.T) {
	tok := ImpactShareToken("s3cret", "t-1")
	if !ImpactShareValid("s3cret", "t-1", tok) {
		t.Fatal("正确令牌应通过")
	}
	if ImpactShareValid("s3cret", "t-2", tok) {
		t.Fatal("令牌绑定任务, 换任务应拒绝")
	}
	if ImpactShareValid("wrong", "t-1", tok) {
		t.Fatal("错误 secret 应拒绝")
	}
	if ImpactShareValid("s3cret", "t-1", tok+"x") {
		t.Fatal("篡改应拒绝")
	}
}
