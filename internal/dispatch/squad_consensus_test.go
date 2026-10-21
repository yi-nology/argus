package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/oekit/logx"
	"time"
)

// ---------- 编队黑板共识模式测试（blackboard 架构原语，v3.4） ----------

// scriptedPlugin 脚本化插件：每次 Invoke 依次返回预设 findings，并捕获注入的
// 黑板视图（互看通道验证）。
type scriptedPlugin struct {
	manifest AgentManifest
	script   [][]Finding
	boards   []string // 每次 Invoke 看到的 squad_board 视图
	calls    int
}

func (p *scriptedPlugin) Manifest() AgentManifest { return p.manifest }
func (p *scriptedPlugin) Invoke(_ context.Context, t Task) (Result, error) {
	p.boards = append(p.boards, t.Artifacts[ArtifactSquadBoard])
	i := p.calls
	p.calls++
	if i < len(p.script) {
		return Result{Findings: p.script[i]}, nil
	}
	return Result{}, nil
}

// failOnCallPlugin 第 N 次 Invoke 起失败（模拟首轮成功、后续轮次故障）。
type failOnCallPlugin struct {
	manifest AgentManifest
	failAt   int
	calls    int
}

func (p *failOnCallPlugin) Manifest() AgentManifest { return p.manifest }
func (p *failOnCallPlugin) Invoke(_ context.Context, _ Task) (Result, error) {
	i := p.calls
	p.calls++
	if i >= p.failAt {
		return Result{}, errors.New("simulated later-round failure")
	}
	return Result{}, nil
}

// cancelInvokePlugin Invoke 即阻塞至 ctx 取消并返回取消错误（模拟优雅停机
// 打断共识成员执行）。
type cancelInvokePlugin struct {
	manifest AgentManifest
}

func (p *cancelInvokePlugin) Manifest() AgentManifest { return p.manifest }
func (p *cancelInvokePlugin) Invoke(ctx context.Context, _ Task) (Result, error) {
	<-ctx.Done()
	return Result{}, fmt.Errorf("reAct 中断: %w", ctx.Err())
}

func regWith(p AgentPlugin) *Registration {
	return &Registration{Manifest: p.Manifest(), Plugin: p, Enabled: true}
}

func findingOf(file, comment, sev string, line int) Finding {
	return Finding{File: file, Line: line, Severity: sev, Comment: comment}
}

// newScriptedMember 构造编队成员：触发器刻意不命中（排除 base 选择路径，
// 确保测试只观察 squad/consensus 语义）。
func newScriptedMember(name string, script ...[]Finding) *scriptedPlugin {
	return &scriptedPlugin{
		manifest: AgentManifest{Name: name, Skills: []string{"code-review"},
			Triggers: Trigger{Paths: []string{"excluded/*"}}},
		script: script,
	}
}

// 共识收敛 + 指纹互看去重：B 轮内看到 A 的发现后不重复报告，只补新问题。
func TestRunConsensus_ConvergeAndDedup(t *testing.T) {
	pa := newScriptedMember("expert-a",
		[]Finding{findingOf("a.go", "空指针解引用", "high", 10)},
		[]Finding{findingOf("a.go", "空指针解引用", "high", 10)}, // 第 2 轮重复报告 → 黑板去重
	)
	pb := newScriptedMember("expert-b",
		[]Finding{findingOf("a.go", "空指针解引用", "high", 10), findingOf("b.go", "goroutine 泄漏", "medium", 22)},
		[]Finding{}, // 第 2 轮无补充 → 共识信号
	)

	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	_ = d.reg.Register(regWith(pa))
	_ = d.reg.Register(regWith(pb))
	d = d.WithSquads([]SquadDecl{{Name: "duo", Mode: SquadModeConsensus, Paths: []string{"**"}, Members: []string{"expert-a", "expert-b"}}})

	task := Task{PR: PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"},
		Diff: &DiffContext{Files: []DiffFile{{Path: "a.go"}, {Path: "b.go"}}}}

	// Select：consensus 成员不进 fan-out
	sel := d.Select(&task.PR, []string{"a.go", "b.go"}, []string{"code-review"}, nil)
	if len(sel) != 0 {
		t.Fatalf("consensus 成员不应进 fan-out，got %v", selNames(sel))
	}
	// ActiveSquadMembers：consensus 编队不参与分歧标注
	if m := d.ActiveSquadMembers(&task.PR, []string{"a.go"}); len(m) != 0 {
		t.Fatalf("consensus 成员不应参与分歧标注，got %v", m)
	}
	// ConsensusSquads：命中触发条件
	sqs := d.ConsensusSquads(&task.PR, []string{"a.go", "b.go"})
	if len(sqs) != 1 || sqs[0].Name != "duo" {
		t.Fatalf("应命中 1 个 consensus 编队，got %+v", sqs)
	}

	outcomes := d.RunConsensus(context.Background(), task, sqs[0], nil)
	byName := map[string]*Result{}
	for _, o := range outcomes {
		if o.Err != nil || o.SkippedReason != "" {
			t.Fatalf("成员 %s 不应失败/跳过（err=%v skipped=%s）", o.Name, o.Err, o.SkippedReason)
		}
		byName[o.Name] = o.Result
	}
	if got := len(byName["expert-a"].Findings); got != 1 {
		t.Fatalf("expert-a 应有 1 条发现，got %d", got)
	}
	if got := len(byName["expert-b"].Findings); got != 1 || byName["expert-b"].Findings[0].Comment != "goroutine 泄漏" {
		t.Fatalf("expert-b 应只剩互看后的新增 1 条，got %+v", byName["expert-b"].Findings)
	}

	// 互看验证：expert-b 第 1 轮就应看到 expert-a 的黑板行
	if len(pb.boards) < 1 || !containsAll(pb.boards[0], "expert-a", "a.go", "空指针解引用") {
		t.Fatalf("expert-b 首轮应看到 expert-a 的黑板发现，got %q", pb.boards[0])
	}

	// Merger 集成：共识 outcomes 进既有合并管线，By 归属成员名
	merger := NewMerger(nil, logx.NewSlogLogger("dispatch-test"))
	merged, err := merger.Merge(context.Background(), task.PR, "t1", outcomes, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Findings) != 2 {
		t.Fatalf("合并后应有 2 条（指纹去重后），got %d", len(merged.Findings))
	}
	seenBy := map[string]string{}
	for _, f := range merged.Findings {
		seenBy[f.Comment] = f.By
	}
	if seenBy["空指针解引用"] != "expert-a" || seenBy["goroutine 泄漏"] != "expert-b" {
		t.Fatalf("By 应归属成员名，got %v", seenBy)
	}
}

// 成员失败不中止召集：失败者降级注明，其余成员照常互看。
func TestRunConsensus_MemberFailureDegrades(t *testing.T) {
	pb := newScriptedMember("expert-b",
		[]Finding{findingOf("b.go", "goroutine 泄漏", "medium", 22)})

	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	_ = d.reg.Register(regWith(&failPlugin{manifest: AgentManifest{Name: "expert-a", Skills: []string{"code-review"}}}))
	_ = d.reg.Register(regWith(pb))

	sq := SquadDecl{Name: "duo", Mode: SquadModeConsensus, Paths: []string{"**"}, Members: []string{"expert-a", "expert-b"}}
	task := Task{PR: PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}}
	outcomes := d.RunConsensus(context.Background(), task, sq, nil)

	var aFailed, bOK bool
	for _, o := range outcomes {
		switch o.Name {
		case "expert-a":
			aFailed = o.Err != nil && o.Result == nil
		case "expert-b":
			bOK = len(o.Result.Findings) == 1
		}
	}
	if !aFailed || !bOK {
		t.Fatalf("A 应失败降级、B 应正常产出，got aFailed=%v bOK=%v", aFailed, bOK)
	}
}

// 预算耗尽：成员全程跳过并降级注明，不产生发现。
func TestRunConsensus_BudgetExhaustedSkips(t *testing.T) {
	pa := newScriptedMember("expert-a",
		[]Finding{findingOf("a.go", "空指针解引用", "high", 10)})
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	_ = d.reg.Register(regWith(pa))
	_ = d.reg.Register(regWith(newScriptedMember("expert-b")))

	sq := SquadDecl{Name: "duo", Mode: SquadModeConsensus, Paths: []string{"**"}, Members: []string{"expert-a", "expert-b"}}
	task := Task{PR: PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}}
	outcomes := d.RunConsensus(context.Background(), task, sq, NewTokenBudget(0))

	for _, o := range outcomes {
		if o.SkippedReason == "" {
			t.Fatalf("预算耗尽时成员 %s 应被跳过", o.Name)
		}
		if o.Result != nil && len(o.Result.Findings) > 0 {
			t.Fatalf("预算耗尽时成员 %s 不应有发现", o.Name)
		}
	}
}

// 未注册/未启用成员 → 降级注明；全部无效时返回空 outcomes 不 panic。
func TestRunConsensus_UnregisteredMember(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	sq := SquadDecl{Name: "ghost", Mode: SquadModeConsensus, Paths: []string{"**"}, Members: []string{"nobody"}}
	task := Task{PR: PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}}
	outcomes := d.RunConsensus(context.Background(), task, sq, nil)
	if len(outcomes) != 1 || outcomes[0].SkippedReason == "" {
		t.Fatalf("未注册成员应降级注明，got %+v", outcomes)
	}
}

// 独立模式编队不受影响：成员照常进 fan-out（经 squad 追加）且参与分歧标注。
func TestSelect_IndependentSquadUnchanged(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	_ = d.reg.Register(regWith(newScriptedMember("expert-a")))
	_ = d.reg.Register(regWith(newScriptedMember("expert-b")))
	d = d.WithSquads([]SquadDecl{{Name: "duo", Paths: []string{"**"}, Members: []string{"expert-a", "expert-b"}}})

	pr := &PRContext{Platform: "gitea"}
	sel := d.Select(pr, []string{"a.go"}, []string{"code-review"}, nil)
	if len(sel) != 2 {
		t.Fatalf("独立编队成员应进 fan-out，got %v", selNames(sel))
	}
	if m := d.ActiveSquadMembers(pr, []string{"a.go"}); len(m) != 2 {
		t.Fatalf("独立编队成员应参与分歧标注，got %v", m)
	}
	if sqs := d.ConsensusSquads(pr, []string{"a.go"}); len(sqs) != 0 {
		t.Fatalf("独立编队不应出现在 consensus 名单，got %+v", sqs)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// 编队共识的重复召集边界：同一 dispatcher 多轮召集不串状态（冒烟）。
func TestRunConsensus_RepeatedConvene(t *testing.T) {
	// 每次召集消耗 2 轮脚本（首轮贡献 + 次轮重复靠指纹去重收敛），3 次召集深度给足 6
	sameA := []Finding{findingOf("a.go", "空指针解引用", "high", 10)}
	sameB := []Finding{findingOf("b.go", "goroutine 泄漏", "medium", 22)}
	scriptA := make([][]Finding, 6)
	scriptB := make([][]Finding, 6)
	for i := 0; i < 6; i++ {
		scriptA[i] = sameA
		scriptB[i] = sameB
	}
	pa := newScriptedMember("expert-a", scriptA...)
	pb := newScriptedMember("expert-b", scriptB...)
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	_ = d.reg.Register(regWith(pa))
	_ = d.reg.Register(regWith(pb))
	sq := SquadDecl{Name: "duo", Mode: SquadModeConsensus, Paths: []string{"**"}, Members: []string{"expert-a", "expert-b"}}
	task := Task{PR: PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}}

	for i := 0; i < 3; i++ {
		outcomes := d.RunConsensus(context.Background(), task, sq, nil)
		total := 0
		for _, o := range outcomes {
			if o.Result != nil {
				total += len(o.Result.Findings)
			}
		}
		if total != 2 {
			t.Fatalf("第 %d 次召集应产出 2 条，got %d", i+1, total)
		}
	}
}

// 深挖修复回归：第 1 轮成功（无发现）、第 2 轮失败的成员此前静默落成空 Result
// （"参与了但没新发现"），lastErr 被吞、Degraded 无注明。现在按失败终态回填。
func TestRunConsensus_LateRoundFailureNotSwallowed(t *testing.T) {
	lateFail := &failOnCallPlugin{
		manifest: AgentManifest{Name: "expert-a", Skills: []string{"code-review"},
			Triggers: Trigger{Paths: []string{"excluded/*"}}},
		failAt: 1, // 第 2 次 Invoke（第 2 轮）起失败
	}
	pb := newScriptedMember("expert-b",
		[]Finding{findingOf("b.go", "goroutine 泄漏", "medium", 22)},
		[]Finding{},
	)

	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	_ = d.reg.Register(regWith(lateFail))
	_ = d.reg.Register(regWith(pb))

	sq := SquadDecl{Name: "duo", Mode: SquadModeConsensus, Paths: []string{"**"}, Members: []string{"expert-a", "expert-b"}}
	task := Task{PR: PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}}
	outcomes := d.RunConsensus(context.Background(), task, sq, nil)

	var aFailed bool
	for _, o := range outcomes {
		if o.Name == "expert-a" {
			aFailed = o.Err != nil && o.Result == nil
		}
	}
	if !aFailed {
		t.Fatalf("后续轮次失败的成员应回填 Err，got %+v", outcomes)
	}
}

// v3.9.1 回归：任务级取消（优雅停机）打断共识成员不得计入熔断——与
// Dispatcher.Run 的 teardown 守卫同语义；否则连续 3 次滚动发布即可把
// security 类成员误熔断 5 分钟。
func TestRunConsensus_CancelNotCountedAsBreakerFailure(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"))
	_ = d.reg.Register(regWith(&cancelInvokePlugin{manifest: AgentManifest{
		Name: "blocker", Skills: []string{"code-review"},
		Triggers: Trigger{Paths: []string{"excluded/*"}}}}))
	_ = d.reg.Register(regWith(newScriptedMember("expert-b", []Finding{}, []Finding{})))
	sq := SquadDecl{Name: "duo", Mode: SquadModeConsensus, Paths: []string{"**"}, Members: []string{"blocker", "expert-b"}}
	task := Task{PR: PRContext{Platform: "gitea", Owner: "o", Repo: "r", Number: "1"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_ = d.RunConsensus(ctx, task, sq, nil)

	// 取消不算失败：blocker 的熔断器必须仍在闭合态（Allow 放行）。
	if _, _, berr := d.BreakerFor("blocker").AllowGen(context.Background()); berr != nil {
		t.Fatal("任务级取消不得计入熔断连续失败")
	}
}

// v3.9.2 回归：半开探测在途时共识成员被取消——teardown 路径经 Abandon 释放
// 探测位，下一轮成员不得因探测滞留被"熔断中"误跳过。
func TestRunConsensus_TeardownAbandonsHalfOpenProbe(t *testing.T) {
	now := time.Now()
	d := newDispatcher(NewRegistry(), logx.NewSlogLogger("dispatch-test"), func() time.Time { return now })
	_ = d.reg.Register(regWith(&canceledPlugin{manifest: AgentManifest{
		Name: "blocker", Skills: []string{"code-review"},
		Triggers: Trigger{Paths: []string{"excluded/*"}}}}))
	_ = d.reg.Register(regWith(newScriptedMember("expert-b", []Finding{}, []Finding{})))
	sq := SquadDecl{Name: "duo", Mode: SquadModeConsensus, Paths: []string{"**"}, Members: []string{"blocker", "expert-b"}}

	bs := d.BreakerFor("blocker")
	for i := 0; i < breakerTripThreshold; i++ {
		_, gen, berr := bs.AllowGen(context.Background())
		if berr != nil {
			break
		}
		bs.FailureGen(context.Background(), gen)
	}
	now = now.Add(6 * time.Minute) // 冷却期（5min）过 → 半开

	_ = d.RunConsensus(context.Background(), Task{}, sq, nil)

	if _, _, berr := bs.AllowGen(context.Background()); berr != nil {
		t.Fatal("共识成员取消后应经 AbandonGen 立即放行新探测")
	}
}
