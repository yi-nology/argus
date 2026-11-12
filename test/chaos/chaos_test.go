// Package chaos_test 混沌矩阵实弹回归（P2-01，#24）：规范矩阵（internal/chaos
// Matrix）逐格落到可执行探针——sqlite 真存储跑 Kill/租约过期/写幂等恢复语义，
// 打桩 LLM/Git 面跑断网/429/超时/空输出/非结构化输出的重试与失败分类语义。
//
// 每格 = 注入故障 → 驱动被测路径 → 断言恢复语义（不是只断言报错）。
// 报告落 test/chaos/results/（gitignore，CI artifact 面），口径与 golden eval
// results 一致：全量数据归档，不只留 pass/fail 信号。
//
// 容器级（testcontainers pg/MySQL）与 k3s Pod Kill 属部署侧手动 job，驱动契约
// 同为本包的 Injector/Report（分层见 internal/chaos 包文档）。
package chaos_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/chaos"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

// leaseWindow / staleAfter 租约实验参数：心跳停摆 staleAfter 即判定失租。
// 测试追求确定性，窗口压到毫秒级（生产为 90s 量级，语义同构）。
const (
	chaosStaleAfter = 30 * time.Millisecond
	chaosLeaseGap   = 60 * time.Millisecond
)

// pairStore 同库双实例（runner-a / runner-b）：Kill/租约用例的跨实例复位与
// fencing 断言面。
type pairStore struct {
	a, b *storage.Store
	dir  string
}

func openPair(t *testing.T) *pairStore {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "chaos.db")
	a, err := storage.Open(dsn)
	if err != nil {
		t.Fatalf("open store a: %v", err)
	}
	b, err := storage.Open(dsn)
	if err != nil {
		t.Fatalf("open store b: %v", err)
	}
	a.RunnerID = "chaos-runner-a"
	b.RunnerID = "chaos-runner-b"
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return &pairStore{a: a, b: b, dir: dir}
}

// mkWebhookTask 带幂等键的 webhook 任务（db.write 幂等用例）。
func mkWebhookTask(eventID, sha string) *storage.Task {
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "chaos", "42")
	task.EventID = &eventID
	task.HeadSHA = sha
	return task
}

// chaosTestOp 注入点坐标构造器。
func chaosTestOp(f chaos.Family, k chaos.Kind, target string) chaos.Op {
	return chaos.Op{Family: f, Kind: k, Target: target}
}

// ---- 五族十格探针 ----

// scenarios 矩阵全量对位：每格一条脚本化回归。
func scenarios(t *testing.T) []chaos.Scenario {
	t.Helper()
	ctx := context.Background()
	var out []chaos.Scenario

	// ---- FamilyProcessKill / worker.process ----
	ps := openPair(t)
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "chaos", "1")
	if err := ps.a.CreateTask(ctx, task); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	// kill 前任务已被 runner-a 认领（running + 心跳新鲜）
	if _, ok, err := ps.a.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := ps.a.TouchHeartbeatsFor(ctx, []string{task.ID}); err != nil {
		t.Fatalf("认领后续租: %v", err)
	}
	out = append(out, chaos.NewScenario(
		chaos.Cell{
			Family: chaos.FamilyProcessKill, Kind: chaos.KillTaskWorker, Point: "worker.process",
			Recovery: "kill -9 后心跳冻结，staleAfter 窗口内复位回 pending 并被新实例重新认领；死亡实例僵尸写入被 fencing 拒绝",
		},
		func(context.Context) error {
			// kill -9：runner-a 死亡——不再续心跳，任务停在 running
			time.Sleep(chaosLeaseGap)
			if n, err := ps.b.ResetRunningToPending(ctx, chaosStaleAfter); err != nil || n != 1 {
				return fmt.Errorf("复位: n=%d err=%v", n, err)
			}
			reclaimed, ok, err := ps.b.ClaimNextPending(ctx)
			if err != nil || !ok {
				return fmt.Errorf("复位后重新认领: ok=%v err=%v", ok, err)
			}
			if fenced, err := ps.b.FinishTaskFenced(ctx, reclaimed.ID, storage.StatusDone, "approve", "ok", "{}", "", 10, 0); err != nil || fenced {
				return fmt.Errorf("新执行体落终态: fenced=%v err=%v", fenced, err)
			}
			// 死亡实例的僵尸写入：fencing 必须拒绝
			fenced, err := ps.a.FinishTaskFenced(ctx, reclaimed.ID, storage.StatusFailed, "", "", "", "zombie-write", 0, 0)
			if err != nil {
				return fmt.Errorf("僵尸写入调用: %w", err)
			}
			if !fenced {
				return errors.New("死亡执行体的僵尸写入未被 fencing 拒绝")
			}
			final, err := ps.b.GetTask(ctx, reclaimed.ID)
			if err != nil {
				return err
			}
			if final.Status != storage.StatusDone || final.FailReason != "" {
				return fmt.Errorf("终态被僵尸写入污染: status=%s fail_reason=%s", final.Status, final.FailReason)
			}
			return nil
		},
	))

	// ---- FamilyLeaseExpiry / db.finish ----
	pl := openPair(t)
	leaseTask := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "chaos", "2")
	if err := pl.a.CreateTask(ctx, leaseTask); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	if _, ok, err := pl.a.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := pl.a.TouchHeartbeatsFor(ctx, []string{leaseTask.ID}); err != nil {
		t.Fatalf("续租: %v", err)
	}
	out = append(out, chaos.NewScenario(
		chaos.Cell{
			Family: chaos.FamilyLeaseExpiry, Kind: chaos.LeaseTaskExpired, Point: "db.finish",
			Recovery: "租约过期复位后旧执行体 FinishTaskFenced 被 fencing 拒绝（不覆盖新执行体结果），新执行体正常落终态",
		},
		func(context.Context) error {
			// 租约停摆：心跳不再续期，跨过 stale 窗口
			time.Sleep(chaosLeaseGap)
			if n, err := pl.b.ResetRunningToPending(ctx, chaosStaleAfter); err != nil || n != 1 {
				return fmt.Errorf("失租复位: n=%d err=%v", n, err)
			}
			reclaimed, ok, err := pl.b.ClaimNextPending(ctx)
			if err != nil || !ok {
				return fmt.Errorf("新执行体认领: ok=%v err=%v", ok, err)
			}
			// 旧执行体（失租）先写：必须被 fencing 拒绝
			fenced, err := pl.a.FinishTaskFenced(ctx, reclaimed.ID, storage.StatusDone, "approve", "stale-runner-report", "{}", "", 5, 0)
			if err != nil {
				return fmt.Errorf("失租执行体写入: %w", err)
			}
			if !fenced {
				return errors.New("失租执行体的终态写入未被 fencing 拒绝")
			}
			if fenced, err := pl.b.FinishTaskFenced(ctx, reclaimed.ID, storage.StatusDone, "approve", "current-report", "{}", "", 10, 0); err != nil || fenced {
				return fmt.Errorf("新执行体落终态: fenced=%v err=%v", fenced, err)
			}
			final, err := pl.b.GetTask(ctx, reclaimed.ID)
			if err != nil {
				return err
			}
			if final.Report != "current-report" {
				return fmt.Errorf("报告被旧执行体覆盖: %q", final.Report)
			}
			return nil
		},
	))

	// ---- FamilyDBTimeout / db.write ----
	pdb := openPair(t)
	injDB := chaos.NewScriptedInjector()
	injDB.Script("db.write", chaos.Script{Fault: chaos.ErrDeadline, FailFirst: 1})
	eventID := "delivery-chaos-db-1"
	out = append(out, chaos.NewScenario(
		chaos.Cell{
			Family: chaos.FamilyDBTimeout, Kind: chaos.DBWriteTimeout, Point: "db.write",
			Recovery: "写路径超时/回滚后重试成功且不产生重复任务（webhook event_id 幂等键去重兜底）",
		},
		func(context.Context) error {
			// 第一次尝试被注入超时（事务回滚模拟），重试后成功
			err := chaos.Retry(ctx, injDB, chaosTestOp(chaos.FamilyDBTimeout, chaos.DBWriteTimeout, "db.write"), 3, 0,
				func(ctx context.Context) error {
					_, _, err := pdb.a.CreateTaskForWebhook(ctx, mkWebhookTask(eventID, "sha-1"))
					return err
				})
			if err != nil {
				return fmt.Errorf("写超时重试后建任务: %w", err)
			}
			// 幂等重放：同 event_id 不同 sha → event_id 去重，不得产生第二行
			dup, existingID, err := pdb.a.CreateTaskForWebhook(ctx, mkWebhookTask(eventID, "sha-2"))
			if err != nil {
				return err
			}
			if dup != storage.DupEventID {
				return fmt.Errorf("幂等重放应命中 event_id 去重: %q", dup)
			}
			first, err := pdb.a.GetTask(ctx, existingID)
			if err != nil {
				return err
			}
			if first.HeadSHA != "sha-1" {
				return fmt.Errorf("existingID 应指向首个任务: sha=%s", first.HeadSHA)
			}
			all, err := pdb.a.ListTasks(ctx, storage.StatusPending, 10)
			if err != nil {
				return err
			}
			if len(all) != 1 {
				return fmt.Errorf("幂等键应保证仅一行任务: %d 行", len(all))
			}
			return nil
		},
	))

	// ---- FamilyNetwork / git.api ----
	injNet := chaos.NewScriptedInjector()
	injNet.Script("git.api", chaos.Script{Fault: chaos.ErrUnreachable, Always: true})
	gitCalls := 0
	out = append(out, chaos.NewScenario(
		chaos.Cell{
			Family: chaos.FamilyNetwork, Kind: chaos.NetGitUnreachable, Point: "git.api",
			Recovery: "Git API 不可达为瞬态故障：有界重试后有界失败（不 hammer）；网络恢复后同调用成功",
		},
		func(context.Context) error {
			// 阶段一：持续断网 → 有界失败
			err := chaos.Retry(ctx, injNet, chaosTestOp(chaos.FamilyNetwork, chaos.NetGitUnreachable, "git.api"), 3, 0,
				func(context.Context) error { gitCalls++; return nil })
			if !errors.Is(err, chaos.ErrUnreachable) {
				return fmt.Errorf("持续断网应返回 ErrUnreachable: %v", err)
			}
			if injNet.Calls("git.api") != 3 {
				return fmt.Errorf("重试应有界（3 次）: %d", injNet.Calls("git.api"))
			}
			if gitCalls != 0 {
				return fmt.Errorf("断网期间业务体不应执行: %d", gitCalls)
			}
			// 阶段二：网络恢复（剧本改回 FailFirst）→ 同调用成功
			injNet.Script("git.api", chaos.Script{Fault: chaos.ErrUnreachable, FailFirst: 1})
			if err := chaos.Retry(ctx, injNet, chaosTestOp(chaos.FamilyNetwork, chaos.NetGitUnreachable, "git.api"), 3, 0,
				func(context.Context) error { gitCalls++; return nil }); err != nil {
				return fmt.Errorf("网络恢复后应成功: %v", err)
			}
			if gitCalls != 1 {
				return fmt.Errorf("恢复后业务体应执行一次: %d", gitCalls)
			}
			return nil
		},
	))

	// ---- FamilyNetwork / llm.review（LLM 网关不可达 → 失败分类 + 退避复活）----
	pll := openPair(t)
	llmTask := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "chaos", "3")
	if err := pll.a.CreateTask(ctx, llmTask); err != nil {
		t.Fatalf("建任务: %v", err)
	}
	if _, ok, err := pll.a.ClaimNextPending(ctx); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	injLLMNet := chaos.NewScriptedInjector()
	injLLMNet.Script("llm.review", chaos.Script{Fault: chaos.ErrUnreachable, Always: true})
	out = append(out, chaos.NewScenario(
		chaos.Cell{
			Family: chaos.FamilyNetwork, Kind: chaos.NetLLMUnreachable, Point: "llm.review",
			Recovery: "LLM 网关不可达分类为瞬态：有界重试后任务按失败落 fail_reason，ResetFailedForRetry 退避后复活回 pending",
		},
		func(context.Context) error {
			err := chaos.Retry(ctx, injLLMNet, chaosTestOp(chaos.FamilyNetwork, chaos.NetLLMUnreachable, "llm.review"), 2, 0,
				func(context.Context) error { return nil })
			if !errors.Is(err, chaos.ErrUnreachable) || !chaos.IsTransient(err) {
				return fmt.Errorf("LLM 不可达应为瞬态分类: err=%v transient=%v", err, chaos.IsTransient(err))
			}
			// 失败分类落库：attempt+1、退避时间入账
			if fenced, err := pll.a.FinishTaskFenced(ctx, llmTask.ID, storage.StatusFailed, "", "", "", "llm_gateway_unreachable", 0, 0); err != nil || fenced {
				return fmt.Errorf("失败分类落库: fenced=%v err=%v", fenced, err)
			}
			// 退避到期后复活（attempt=1 < maxRetries=3）
			if n, err := pll.a.ResetFailedForRetry(ctx, 3, time.Now().Add(10*time.Minute)); err != nil || n != 1 {
				return fmt.Errorf("退避复活: n=%d err=%v", n, err)
			}
			got, err := pll.a.GetTask(ctx, llmTask.ID)
			if err != nil {
				return err
			}
			if got.Status != storage.StatusPending {
				return fmt.Errorf("复活后应回 pending: %s", got.Status)
			}
			return nil
		},
	))

	// ---- FamilyRateLimit / git.api（429 退避重试 + 非瞬态 fail-fast 对照）----
	injRL := chaos.NewScriptedInjector()
	injRL.Script("git.api", chaos.Script{Fault: chaos.ErrRateLimited, FailFirst: 2})
	rlCalls := 0
	out = append(out, chaos.NewScenario(
		chaos.Cell{
			Family: chaos.FamilyRateLimit, Kind: chaos.GitRateLimited, Point: "git.api",
			Recovery: "Git API 429 退避重试至成功；非瞬态故障（进程死亡）不重试（fail-fast）",
		},
		func(context.Context) error {
			if err := chaos.Retry(ctx, injRL, chaosTestOp(chaos.FamilyRateLimit, chaos.GitRateLimited, "git.api"), 5, 0,
				func(context.Context) error { rlCalls++; return nil }); err != nil {
				return fmt.Errorf("429 退避后应成功: %v", err)
			}
			if rlCalls != 1 || injRL.Calls("git.api") != 3 {
				return fmt.Errorf("429 后成功: calls=%d inj=%d, want 1/3", rlCalls, injRL.Calls("git.api"))
			}
			// 对照：kill -9 非瞬态 → fail-fast 单次
			injRL.Script("git.api", chaos.Script{Fault: chaos.ErrKilled, FailFirst: 10})
			err := chaos.Retry(ctx, injRL, chaosTestOp(chaos.FamilyRateLimit, chaos.GitRateLimited, "git.api"), 5, 0,
				func(context.Context) error { rlCalls++; return nil })
			if !errors.Is(err, chaos.ErrKilled) || injRL.Calls("git.api") != 4 {
				return fmt.Errorf("非瞬态应 fail-fast: err=%v calls=%d", err, injRL.Calls("git.api"))
			}
			return nil
		},
	))

	// ---- FamilyLLM × 4 格 ----
	review := func(inj *chaos.ScriptedInjector, attempts int, body func(context.Context) error) (int, error) {
		calls := 0
		err := chaos.Retry(ctx, inj, chaosTestOp(chaos.FamilyLLM, "", "llm.review"), attempts, 0,
			func(c context.Context) error { calls++; return body(c) })
		return calls, err
	}

	// llm_timeout：瞬态，重试后成功
	injTimeout := chaos.NewScriptedInjector()
	injTimeout.Script("llm.review", chaos.Script{Fault: chaos.ErrDeadline, FailFirst: 1})
	out = append(out, chaos.NewScenario(
		chaos.Cell{
			Family: chaos.FamilyLLM, Kind: chaos.LLMTimeout, Point: "llm.review",
			Recovery: "LLM 超时为瞬态故障：有界重试；恢复后产出结果",
		},
		func(context.Context) error {
			calls, err := review(injTimeout, 3, func(context.Context) error { return nil })
			if err != nil || calls != 1 || injTimeout.Calls("llm.review") != 2 {
				return fmt.Errorf("超时重试恢复: calls=%d inj=%d err=%v", calls, injTimeout.Calls("llm.review"), err)
			}
			return nil
		},
	))

	// llm_429：瞬态，退避后成功
	injLLMRL := chaos.NewScriptedInjector()
	injLLMRL.Script("llm.review", chaos.Script{Fault: chaos.ErrRateLimited, FailFirst: 1})
	out = append(out, chaos.NewScenario(
		chaos.Cell{
			Family: chaos.FamilyLLM, Kind: chaos.LLMRateLimited, Point: "llm.review",
			Recovery: "LLM 429 为瞬态故障：退避重试至成功",
		},
		func(context.Context) error {
			calls, err := review(injLLMRL, 3, func(context.Context) error { return nil })
			if err != nil || calls != 1 || injLLMRL.Calls("llm.review") != 2 {
				return fmt.Errorf("429 退避恢复: calls=%d inj=%d err=%v", calls, injLLMRL.Calls("llm.review"), err)
			}
			return nil
		},
	))

	// llm_empty_output / llm_malformed_output：契约故障，fail-fast + 失败分类 + 复活。
	// 两格共用一个真实任务面：各自独立 store，互不串线。
	for _, tc := range []struct {
		kind       chaos.Kind
		fault      error
		failReason string
	}{
		{chaos.LLMEmptyOutput, chaos.ErrEmptyOutput, "llm_empty_output"},
		{chaos.LLMMalformedOutput, chaos.ErrMalformedOutput, "llm_malformed_output"},
	} {
		pf := openPair(t)
		ft := storage.NewTask(domain.TaskKindCodeReview, "gitea", "acme", "chaos", "4")
		if err := pf.a.CreateTask(ctx, ft); err != nil {
			t.Fatalf("建任务: %v", err)
		}
		if _, ok, err := pf.a.ClaimNextPending(ctx); err != nil || !ok {
			t.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		injFault := chaos.NewScriptedInjector()
		injFault.Script("llm.review", chaos.Script{Fault: tc.fault, Always: true})
		kind, fault, failReason := tc.kind, tc.fault, tc.failReason
		out = append(out, chaos.NewScenario(
			chaos.Cell{
				Family: chaos.FamilyLLM, Kind: kind, Point: "llm.review",
				Recovery: "契约故障（非瞬态）不盲目重试：单次即返回，任务按失败分类落 fail_reason，ResetFailedForRetry 退避后复活",
			},
			func(context.Context) error {
				op := chaosTestOp(chaos.FamilyLLM, kind, "llm.review")
				err := chaos.Retry(ctx, injFault, op, 3, 0, func(context.Context) error { return fault })
				if !errors.Is(err, fault) {
					return fmt.Errorf("契约故障应透传: %v", err)
				}
				if chaos.IsTransient(err) {
					return fmt.Errorf("%s 不应分类为瞬态", kind)
				}
				if injFault.Calls("llm.review") != 1 {
					return fmt.Errorf("契约故障不应重试: %d 次", injFault.Calls("llm.review"))
				}
				// 失败分类落库 + 退避复活
				if fenced, err := pf.a.FinishTaskFenced(ctx, ft.ID, storage.StatusFailed, "", "", "", failReason, 0, 0); err != nil || fenced {
					return fmt.Errorf("失败分类落库: fenced=%v err=%v", fenced, err)
				}
				if n, err := pf.a.ResetFailedForRetry(ctx, 3, time.Now().Add(10*time.Minute)); err != nil || n != 1 {
					return fmt.Errorf("退避复活: n=%d err=%v", n, err)
				}
				got, err := pf.a.GetTask(ctx, ft.ID)
				if err != nil {
					return err
				}
				if got.Status != storage.StatusPending {
					return fmt.Errorf("复活后应回 pending: %s", got.Status)
				}
				return nil
			},
		))
	}

	return out
}

// TestChaosMatrix 全矩阵执行：覆盖完整（无缺格无越格）+ 全格恢复语义成立 +
// 报告双形态落盘（CI artifact 面）。
func TestChaosMatrix(t *testing.T) {
	scs := scenarios(t)
	if missing := chaos.MissingCells(scs); len(missing) > 0 {
		t.Fatalf("矩阵缺格（每格至少一条脚本化回归）: %+v", missing)
	}
	if extra := chaos.ExtraCells(scs); len(extra) > 0 {
		t.Fatalf("越格场景（规范矩阵外）: %+v", extra)
	}

	rep := chaos.Run(context.Background(), scs)
	for _, c := range rep.Cells {
		t.Logf("[%s] %s %s → %s %s", c.Status, c.Family, c.Kind, c.Point, c.Error)
	}
	if !rep.Pass() {
		t.Fatalf("混沌矩阵存在失败格: %d/%d", rep.Failed, rep.Total)
	}

	// 报告落盘：JSON + Markdown（results/ gitignore，CI upload-artifact 抓取）。
	if err := os.MkdirAll("results", 0o755); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join("results", "chaos-latest.json")
	jsonBytes, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, jsonBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join("results", "chaos-latest.md")
	mdFile, err := os.Create(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mdFile.Close()
	if err := rep.WriteMarkdown(mdFile); err != nil {
		t.Fatal(err)
	}
	t.Logf("混沌矩阵报告已落盘: %s / %s", jsonPath, mdPath)
}
