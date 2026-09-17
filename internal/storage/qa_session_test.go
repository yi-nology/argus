// QA 会话乐观锁（SaveQASessionCas）行为测试（v3.9.2 补课）：「读历史→追加→
// 覆盖写」在并发评论下的丢轮次防线——expect 过期必须拒绝写入，重读重放后成功。
package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSaveQASessionCasConflictReplay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	key := "gitea:acme/app#1"

	// 首轮：无条件写建行
	first := &QASession{SessionKey: key, HistoryJSON: `[{"question":"q1","answer":"a1"}]`}
	if ok, err := s.SaveQASessionCas(ctx, first, time.Time{}, false); err != nil || !ok {
		t.Fatalf("建行应成功: ok=%v err=%v", ok, err)
	}

	// 读者 A 读到当前状态（expect=该行的 updated_at）
	cur, err := s.GetQASession(ctx, key, time.Hour)
	if err != nil || cur == nil {
		t.Fatalf("应读到会话: %+v err=%v", cur, err)
	}

	// 并发写者 B 抢先无条件覆盖（updated_at 前移）
	b := &QASession{SessionKey: key, HistoryJSON: `[{"question":"q1","answer":"a1"},{"question":"qB","answer":"aB"}]`}
	if ok, err := s.SaveQASessionCas(ctx, b, time.Time{}, false); err != nil || !ok {
		t.Fatalf("并发写应成功: ok=%v err=%v", ok, err)
	}

	// 读者 A 用过期 expect 条件写 → 必须拒绝（written=false），B 的轮次不丢
	a := &QASession{SessionKey: key, HistoryJSON: `[{"question":"q1","answer":"a1"},{"question":"q2","answer":"a2"}]`}
	ok, err := s.SaveQASessionCas(ctx, a, cur.UpdatedAt, true)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expect 过期的条件写必须被拒绝（否则并发丢轮次）")
	}
	got, _ := s.GetQASession(ctx, key, time.Hour)
	if got == nil || !strings.Contains(got.HistoryJSON, "qB") {
		t.Fatalf("B 的写入不得被 A 的过期写覆盖: %+v", got)
	}

	// 重读-重放：A 拿到最新 updated_at 再写 → 成功且内容落库
	latest, _ := s.GetQASession(ctx, key, time.Hour)
	latest.HistoryJSON = `[{"question":"q1","answer":"a1"},{"question":"qB","answer":"aB"},{"question":"q2","answer":"a2"}]`
	ok, err = s.SaveQASessionCas(ctx, latest, latest.UpdatedAt, true)
	if err != nil || !ok {
		t.Fatalf("重放条件写应成功: ok=%v err=%v", ok, err)
	}
	final, _ := s.GetQASession(ctx, key, time.Hour)
	if final == nil || !strings.Contains(final.HistoryJSON, "q2") {
		t.Fatalf("重放内容应落库: %+v", final)
	}

	// 不存在的会话：check=true 条件写不会插行（written=false），建行走 check=false
	missing := &QASession{SessionKey: "gitea:acme/app#2", HistoryJSON: `[]`}
	if ok, _ := s.SaveQASessionCas(ctx, missing, time.Now(), true); ok {
		t.Fatal("check=true 对不存在的行不得误插")
	}
}
