// Package chaos 故障注入与混沌测试矩阵（P2-01，#24）：把「任务中途死亡要能恢复、
// 依赖不可达要有界失败、限流要退避、租约失守要 fencing」这些恢复语义从口头约定
// 变成可执行契约。
//
// 分层（与 issue #24 的建议分层对齐）：
//   - 本包 = 注入点抽象（Injector）+ 五族故障词表 + 规范矩阵（Matrix）+ 执行
//     骨架（Run/Report）——纯 stdlib、零 internal 依赖（L0）；
//   - test/chaos = 每格矩阵的实弹回归（sqlite 真存储 + 打桩 LLM/Git 面），
//     CI 常跑；
//   - 容器级（testcontainers pg/MySQL）与 k3s Pod Kill 属部署侧手动 job，本包
//     的 Injector/Report 同样是其驱动契约（接入点见 docs）。
//
// 五族故障（issue #24 审计矩阵）：Kill（进程死亡）/ 断网（Git API、LLM 网关
// 不可达）/ 锁过期（任务租约失租）/ 429（平台限流）/ LLM 超时（超时、429、
// 空输出、非结构化输出）。
package chaos

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Family 故障族：审计矩阵的五大类（矩阵行）。
type Family string

const (
	// FamilyProcessKill Pod Kill（kill -9）：任务中途死亡 → 重启恢复语义。
	FamilyProcessKill Family = "process_kill"
	// FamilyNetwork 网络分区/断网：Git API、LLM 网关不可达。
	FamilyNetwork Family = "network_partition"
	// FamilyLeaseExpiry 租约过期：任务租约失租行为（fencing 回归）。
	FamilyLeaseExpiry Family = "lease_expiry"
	// FamilyRateLimit 平台限流（Git API 429）。
	FamilyRateLimit Family = "rate_limit_429"
	// FamilyLLM LLM 故障：超时 / 429 / 空输出 / 非结构化输出。
	FamilyLLM Family = "llm_fault"
	// FamilyDBTimeout 数据库超时/事务回滚：写路径幂等性（issue #24 审计矩阵行）。
	FamilyDBTimeout Family = "db_timeout"
)

// Kind 具体故障种类（矩阵列；每格至少一条脚本化回归，见 test/chaos）。
type Kind string

const (
	// KillTaskWorker worker 进程被 SIGKILL，任务停在 running，心跳冻结。
	KillTaskWorker Kind = "task_worker_sigkill"
	// NetGitUnreachable Git API 端点不可达（断网/分区）。
	NetGitUnreachable Kind = "git_api_unreachable"
	// NetLLMUnreachable LLM 网关不可达（断网/分区）。
	NetLLMUnreachable Kind = "llm_gateway_unreachable"
	// LeaseTaskExpired 任务租约过期：心跳停摆后任务被复位，旧执行体仍试图写终态。
	LeaseTaskExpired Kind = "task_lease_expired"
	// GitRateLimited Git API 429（平台限流）。
	GitRateLimited Kind = "git_api_429"
	// LLMTimeout LLM 调用超时。
	LLMTimeout Kind = "llm_timeout"
	// LLMRateLimited LLM 网关 429。
	LLMRateLimited Kind = "llm_429"
	// LLMEmptyOutput LLM 返回空输出。
	LLMEmptyOutput Kind = "llm_empty_output"
	// LLMMalformedOutput LLM 返回非结构化输出（无法解析为约定 schema）。
	LLMMalformedOutput Kind = "llm_malformed_output"
	// DBWriteTimeout 数据库写超时/事务回滚（幂等性验证点）。
	DBWriteTimeout Kind = "db_write_timeout"
)

// Family 归族（矩阵行列映射；未知 kind 归 LLM 族之外的独立串不影响矩阵守卫——
// MissingCells 只认规范矩阵内格子）。
func (k Kind) Family() Family {
	switch k {
	case KillTaskWorker:
		return FamilyProcessKill
	case NetGitUnreachable, NetLLMUnreachable:
		return FamilyNetwork
	case LeaseTaskExpired:
		return FamilyLeaseExpiry
	case GitRateLimited:
		return FamilyRateLimit
	case DBWriteTimeout:
		return FamilyDBTimeout
	case LLMTimeout, LLMRateLimited, LLMEmptyOutput, LLMMalformedOutput:
		return FamilyLLM
	default:
		return ""
	}
}

// 哨兵故障（打桩面与断言共用同一值；errors.Is 判别）。
var (
	// ErrKilled 进程死亡（SIGKILL 模拟）：不可本进程内重试，恢复靠重启复位路径。
	ErrKilled = errors.New("chaos: process killed (SIGKILL)")
	// ErrUnreachable 端点不可达（断网/分区）：瞬态，有界重试后失败。
	ErrUnreachable = errors.New("chaos: endpoint unreachable (network partition)")
	// ErrLeaseLost 租约/归属失效：fencing 拒绝，禁止覆盖新执行体结果。
	ErrLeaseLost = errors.New("chaos: lease lost (fenced)")
	// ErrRateLimited 限流（429）：瞬态，按退避重试。
	ErrRateLimited = errors.New("chaos: rate limited (429)")
	// ErrDeadline 超时（含 LLM 超时）：瞬态。
	ErrDeadline = errors.New("chaos: deadline exceeded (timeout)")
	// ErrEmptyOutput LLM 空输出：契约故障，不盲目重试（降级/失败分类处理）。
	ErrEmptyOutput = errors.New("chaos: empty output")
	// ErrMalformedOutput LLM 非结构化输出：契约故障，同上。
	ErrMalformedOutput = errors.New("chaos: malformed output")
)

// IsTransient 瞬态分类：真（网络/限流/超时）→ 有界重试是正确恢复动作；
// 假（进程死亡/租约失守/空输出/非结构化）→ 重试无意义或有害，走失败分类/
// 复位/降级路径。这是注入面与恢复面共同的语义契约，test/chaos 逐格断言。
func IsTransient(err error) bool {
	return errors.Is(err, ErrUnreachable) ||
		errors.Is(err, ErrRateLimited) ||
		errors.Is(err, ErrDeadline) ||
		errors.Is(err, context.DeadlineExceeded)
}

// Op 注入点描述：一次外部调用（LLM/Git/DB/租约）在注入器视角的坐标。
type Op struct {
	Family Family `json:"family"`
	Kind   Kind   `json:"kind"`
	// Target 注入点目标名（调用方约定：llm.review / git.api / db.write /
	// lease.heartbeat …）。脚本按 target 装配。
	Target string `json:"target"`
	// Call 该 target 第几次调用（从 1 计），供 FailFirst 类脚本使用。
	Call int `json:"call"`
}

// Injector 注入点抽象：宿主（打桩 LLM 客户端 / Git 客户端 / 存储包装层）在
// 每次外部调用前询问是否注入故障。nil 返回 = 放行。
type Injector interface {
	Fault(ctx context.Context, op Op) error
}

// InjectorFunc 适配器。
type InjectorFunc func(ctx context.Context, op Op) error

// Fault 实现 Injector。
func (f InjectorFunc) Fault(ctx context.Context, op Op) error { return f(ctx, op) }

// Noop 恒放行注入器（对照组/恢复后态）。
func Noop() Injector { return InjectorFunc(func(context.Context, Op) error { return nil }) }

// Script 故障剧本：一个 target 的注入编排。
type Script struct {
	// Fault 要注入的故障值（errors.Is 判别用哨兵）。
	Fault error
	// FailFirst 前 N 次调用注入 Fault，之后放行（故障→恢复的双相剧本）。
	// 0 且 Always=false = 该 target 不注入。
	FailFirst int
	// Always 永远注入（断网不恢复类场景）；优先于 FailFirst。
	Always bool
}

// ScriptedInjector 按 target 装配剧本的确定性注入器（混沌回归的主用形态：
// 同一用例先注入后恢复，断言恢复语义而非只断言报错）。
type ScriptedInjector struct {
	mu      sync.Mutex
	scripts map[string]Script
	calls   map[string]int
}

// NewScriptedInjector 空剧本注入器（所有调用放行，逐步装配 Script）。
func NewScriptedInjector() *ScriptedInjector {
	return &ScriptedInjector{scripts: map[string]Script{}, calls: map[string]int{}}
}

// Script 装配/覆盖一个 target 的剧本。
func (inj *ScriptedInjector) Script(target string, s Script) {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	inj.scripts[target] = s
}

// Fault 实现 Injector：计数并按剧本裁决。
func (inj *ScriptedInjector) Fault(_ context.Context, op Op) error {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	inj.calls[op.Target]++
	call := inj.calls[op.Target]
	s, ok := inj.scripts[op.Target]
	if !ok {
		return nil
	}
	if s.Always || call <= s.FailFirst {
		return s.Fault
	}
	return nil
}

// Calls 某 target 至今被询问次数（断言重试次数用）。
func (inj *ScriptedInjector) Calls(target string) int {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	return inj.calls[target]
}

// Retry 有界重试骨架：按 IsTransient 分类决定重试（非瞬态立即返回原错误），
// 共至多 attempts 次调用（attempts<1 视为 1），每次调用恰好询问注入器一次
// （Calls 计数 = 实际尝试次数，断言口径单源）。base 为线性退避基步长（第 i 次
// 重试前等 base*i；骨架不引第三方退避库——混沌用例追求确定性，生产路径的
// 退避策略归各自的客户端所有）。
func Retry(ctx context.Context, inj Injector, op Op, attempts int, base time.Duration, fn func(ctx context.Context) error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := range attempts {
		if i > 0 {
			sleep(ctx, backoff(base, i))
		}
		if ferr := inj.Fault(ctx, op); ferr != nil {
			err = ferr
		} else {
			err = fn(ctx)
		}
		if err == nil {
			return nil
		}
		if !IsTransient(err) {
			return err
		}
	}
	return err
}

// backoff 第 attempt 次重试前的等待（线性）。
func backoff(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	return base * time.Duration(attempt)
}

// sleep 可取消的等待；ctx 结束立即返回。
func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
