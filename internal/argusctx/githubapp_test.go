package argusctx

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// TestInstallationTokenSourceFastPath 寿命充裕时直接返回缓存 token，零 API 往返。
func TestInstallationTokenSourceFastPath(t *testing.T) {
	ts := &installationTokenSource{
		token: "tok-1", expiresAt: time.Now().Add(time.Hour), log: logx.Nop(),
	}
	got, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "tok-1" {
		t.Fatalf("got %q, want tok-1", got)
	}
}

// TestInstallationTokenSourceStaleFallback 临期（<5min）触发续期且续期失败时，
// 旧 token 仍在寿命内 ⇒ 沿用旧值（可用性优先）。私钥为空保证刷新必然失败
// （PEM 解码错），全程无网络。
func TestInstallationTokenSourceStaleFallback(t *testing.T) {
	ts := &installationTokenSource{
		token: "old", expiresAt: time.Now().Add(2 * time.Minute), log: logx.Nop(),
	}
	got, err := ts.Token(context.Background())
	if err != nil {
		t.Fatalf("临期且刷新失败应退回旧值，got err: %v", err)
	}
	if got != "old" {
		t.Fatalf("got %q, want old", got)
	}
}

// TestInstallationTokenSourceExpiredNoCreds token 已过期且刷新失败 ⇒ 上抛错误
// （无旧值可退，调用方拿到明确失败）。
func TestInstallationTokenSourceExpiredNoCreds(t *testing.T) {
	ts := &installationTokenSource{
		token: "old", expiresAt: time.Now().Add(-time.Minute), log: logx.Nop(),
	}
	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("过期且刷新失败应返回错误")
	}
}
