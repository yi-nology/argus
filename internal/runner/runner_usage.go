// runner_usage.go —— 逐调用实销埋点（#32）：execContext UsageHandler 的落账
// sink——每次 LLM 调用一条 usage_event（invocation_id 唯一键幂等），运行中
// 消费即时入账、崩溃窗口收敛到「调用返回→写库」的毫秒级。

package runner

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/storage"
)

// recordUsageEvent 单次 LLM 调用落账（#32 验收①调用点埋点）。
//
// 埋点层选点：execContext 的 llm.NewUsageHandler sink——它是全任务唯一的
// 用量汇聚面（Generate 直连与 ReAct/RawModel 旁路两条路径都到此，llm 侧
// clientAccounted 防重保证同笔物理调用只投递一次），任务上下文（task/attempt/
// 仓库坐标）在 Execute 装配期即在闭包内，无须再穿 context。
//
// 纪律：
//   - invocation_id 稳定键（Phase1③）：任务作用域计数器（execContext 注入
//     llm.WithCallCounter）的 Iteration 铸 u-<taskID>-<attempt>-<iteration>——
//     同一物理调用重复投递（回调重放/写库重试）得同键，由 usage_events
//     唯一索引幂等收敛；Iteration=0（计数器缺席的旁路）退回新 uuid（旧语义），
//     「不可重复计费」单源仍在存储层；
//   - 同步落账 + WithoutCancel：调用已发生、token 已花——任务取消/停机不得
//     抹掉这笔账；5s 超时上界防停机路径挂死；
//   - 写失败只告警不返回（sink 契约）：账本绝不阻断审查，丢笔由任务终态的
//     差额兜底（storage.backfillTaskUsageDelta）补账。
func (r *Runner) recordUsageEvent(ctx context.Context, task *storage.Task, rec llm.UsageRecord) {
	if r.Store == nil || task == nil {
		return
	}
	// 独立超时上下文：不随任务取消（账已发生），停机路径 5s 上界。
	ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	inv := "u-" + uuid.NewString()
	if rec.Iteration > 0 {
		inv = fmt.Sprintf("u-%s-%d-%d", task.ID, task.Attempt, rec.Iteration)
	}
	ev := &storage.UsageEvent{
		TaskID: task.ID, AttemptID: strconv.Itoa(task.Attempt),
		InvocationID: inv,
		Platform:     task.Platform, Owner: task.Owner, Repo: task.Repo,
		Provider:      r.usageProvider(),
		Model:         rec.Model,
		InputTokens:   rec.PromptTokens,
		OutputTokens:  rec.CompletionTokens,
		CachedTokens:  rec.CachedTokens,
		EstimatedCost: r.estimatedUsageCost(rec),
		Status:        storage.UsageSettled,
		OccurredAt:    time.Now(),
	}
	if err := r.Store.RecordUsageEvent(ictx, ev); err != nil && r.Log != nil {
		// logredact：模型名/URL 类字段无凭据面，直接结构化告警。
		r.Log.Warn("argus.runner.usage_record_failed", "task", task.ID, "error", err.Error())
	}
}

// usageProvider 调用方 provider 标注：取 LLM BaseURL 主机名（argus 全链路
// OpenAI 兼容端点，端点即 provider 身份——自建网关/mimo 等以主机区分），
// 解析失败缺省 openai。
func (r *Runner) usageProvider() string {
	if r.Cfg != nil && r.Cfg.LLM.BaseURL != "" {
		if u, err := url.Parse(r.Cfg.LLM.BaseURL); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return "openai"
}

// estimatedUsageCost 单次调用成本估算（美元）：与报告成本面同口径——输入价
// 按缓存命中折扣摊薄（cacheAdjustedInput），输出价原价；未配价（rate 全 0）
// 返回 0（与「0 = 不计价」配置语义一致）。rate 单位 per-1K（配置面），换算
// 到 per-1M 与 llm.RecordPricedCached 对齐。
func (r *Runner) estimatedUsageCost(rec llm.UsageRecord) float64 {
	rate := r.costRate(rec.Model)
	if rate[0] <= 0 && rate[1] <= 0 {
		return 0
	}
	ratio := 0.1
	if r.Cfg != nil {
		ratio = r.Cfg.CacheReadRatio()
	}
	inputPerM := cacheAdjustedInput(rate[0]*1000, rec.PromptTokens, rec.CachedTokens, ratio)
	outputPerM := rate[1] * 1000
	return (inputPerM*float64(rec.PromptTokens) + outputPerM*float64(rec.CompletionTokens)) / 1e6
}
