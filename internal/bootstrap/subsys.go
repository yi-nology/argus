package bootstrap

import (
	"context"
	"time"

	agentkitworker "github.com/oagentkit/queue/worker"
	ekitbootstrap "github.com/oekit/bootstrap"
	"github.com/oekit/logx"

	"argus/internal/config"
	"argus/internal/dream"
	"argus/internal/health"
	"argus/internal/obs"
	"argus/internal/poller"
	"argus/internal/storage"
	argusworker "argus/internal/worker"
)

// StartSubsystems 后台子系统组装与启动（v3.63.0 收口：main 只剩进程生命周期）。
// 此前选主/worker 池/运维 ticker/dream/health/poller 散在 main.runApp 手工组装，
// 组合根单点原则被破坏——现在全部构造收敛于此，main 只做资源注册后的 HTTP 接线。
// instanceID 为进程身份（claim 归属/心跳/选主），由 main 生成后贯穿 store 与本函数。
//
// 资源注册顺序（AddResource 逆序关闭）：
//
//	[store(main) →] poll-leader → worker → github-app-refresher → comments(main) → [http(main)]
//	停机：HTTP 先停（新任务入口关闭）→ 评论排水 → worker 两阶段排水 60s
//	（在途自然完成，超时硬取消保持 running 待下次启动重置）→ 选主让位 → 最后关库。
func StartSubsystems(app *ekitbootstrap.App, cfg *config.Config, log logx.Logger,
	store *storage.Store, sys *System, instanceID string) {
	ctx := app.Context()

	// ---- 多副本控制面选主（agentkit/worker.LeaderElector）----
	// 任务面（worker pool）靠 ClaimNextPending 原子抢占天然分片，无需选主；
	// 轮询器是"只能跑一份"的控制面组件——租约收敛，失联 ≤ttl 自动换主。
	pollElector := agentkitworker.NewLeaderElector(store, "argus/poller", instanceID,
		30*time.Second, 10*time.Second, agentkitworker.WithLeaderLogger(log))
	pollElector.Start(ctx)
	app.AddResource("poll-leader", func(context.Context) error {
		pollElector.Stop() // 优雅停机主动让位，缩短换主窗口
		return nil
	})

	// ---- worker pool（§10.4 DB 即队列；两阶段优雅停机）----
	wp := &agentkitworker.Pool{Queue: &argusworker.StoreQueue{Store: store},
		N: cfg.MaxConcurrentTasks, Log: log, Run: sys.Runner.Execute}
	wp.Start(ctx)
	// 排水宽限可配（v3.8.0 ARGUS_WORKER_GRACE，默认 60s）：grace < 典型任务时长时
	// 每次发布必打断重跑；与 ShutdownTimeout 的余量关系见 runbook。
	app.AddResource("worker", func(context.Context) error {
		wp.Stop(cfg.WorkerGrace)
		return nil
	})
	// GitHub App token refresher 停机收编（v3.9.2）：此前 Stop 全仓无调用方，
	// 优雅排水期间仍可能触发 provider 重建（未启用 GitHub App 时为 no-op）。
	app.AddResource("github-app-refresher", func(context.Context) error {
		sys.SDK.Stop()
		return nil
	})

	// ---- 运维加固：任务保留清理 + 队列深度采样（WorkerManager 管理生命周期）----
	// ctx 取消时所有 ticker 自动退出（cancel 先于 close），无需显式 Close
	wm := ekitbootstrap.NewWorkerManager(ctx)
	wm.AddTicker("workcopy-scavenger", time.Hour, func(context.Context) { sys.WC.Sweep(time.Hour) })
	if cfg.TaskRetentionDays > 0 {
		retention := time.Duration(cfg.TaskRetentionDays) * 24 * time.Hour
		pruneOnce := func(ctx context.Context) {
			if n, err := store.PruneTasks(ctx, time.Now().Add(-retention)); err != nil {
				log.Warn("argus.boot.prune_failed", "error", err.Error())
			} else if n > 0 {
				log.Info("argus.boot.pruned", "removed", n, "retention_days", cfg.TaskRetentionDays)
			}
		}
		pruneOnce(ctx)
		wm.AddTicker("task-prune", 24*time.Hour, pruneOnce)
	} else {
		log.Warn("argus.boot.retention_disabled", "hint",
			"ARGUS_TASK_RETENTION_DAYS=0（永久保留）——tasks 报告/调试产物每任务可达 MB 级单调增长；默认 30 天，显式关闭请确认磁盘监控到位（deploy/ops-runbook.md 场景 3）")
	}
	// 会话与指纹的保留清理（深挖修复：qa_sessions/finding_prints 此前永不清理，
	// history_json/comment 单调膨胀拖慢全表扫描与备份）。
	// qa_sessions：上下文 TTL 24h，7 天前会话无回放价值；finding_prints：长期
	// 未复现（last_seen_at）的指纹遗忘——最坏效果是同型意见重新从头计数，不会
	// 复活误报（保留期给足 180 天起）。
	qaCutoff := 7 * 24 * time.Hour
	printCutoff := 180 * 24 * time.Hour
	if cfg.TaskRetentionDays > 0 {
		r := time.Duration(cfg.TaskRetentionDays) * 24 * time.Hour
		if r > qaCutoff {
			qaCutoff = r
		}
		if r*3 > printCutoff {
			printCutoff = r * 3
		}
	}
	pruneAuxOnce := func(ctx context.Context) {
		if n, err := store.PruneQASessions(ctx, time.Now().Add(-qaCutoff)); err != nil {
			log.Warn("argus.boot.qa_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.qa_pruned", "removed", n)
		}
		if n, err := store.PruneStalePrints(ctx, time.Now().Add(-printCutoff)); err != nil {
			log.Warn("argus.boot.print_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.print_pruned", "removed", n)
		}
		// 评论投递幂等记录（v3.8.0 随幂等下沉 DB 而来）：7 天前的重试已无幂等意义
		if n, err := store.PruneCommentDeliveries(ctx, time.Now().Add(-7*24*time.Hour)); err != nil {
			log.Warn("argus.boot.delivery_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.delivery_pruned", "removed", n)
		}
		// 模式信号（v3.9.2）：与指纹同保留期（长期未复现即遗忘，重新从头计数）
		if n, err := store.PruneStalePatterns(ctx, time.Now().Add(-printCutoff)); err != nil {
			log.Warn("argus.boot.pattern_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.pattern_pruned", "removed", n)
		}
	}
	pruneAuxOnce(ctx)
	wm.AddTicker("session-print-prune", 24*time.Hour, pruneAuxOnce)
	// failed 任务到期重试（v3.8.0 ARGUS_TASK_MAX_RETRIES，默认 2；0=禁用）：
	// 指数退避到期后重置 pending；多副本同时执行幂等（RowsAffected 至多一方非零）。
	if cfg.TaskMaxRetries > 0 {
		wm.AddTicker("failed-retry", time.Minute, func(ctx context.Context) {
			if n, err := store.ResetFailedForRetry(ctx, cfg.TaskMaxRetries, time.Now()); err != nil {
				log.Warn("argus.boot.failed_retry_failed", "error", err.Error())
			} else if n > 0 {
				log.Info("argus.boot.failed_retried", "reset", n, "max_retries", cfg.TaskMaxRetries)
			}
		})
	}
	wm.AddTicker("queue-depth", 30*time.Second, func(ctx context.Context) {
		if pending, running, err := store.TaskCounts(ctx); err == nil {
			obs.SetTaskQueueDepth(pending, running)
		}
	})
	// golden 评测结果指标（v3.14.0，G1/G6）：eval-real 离线落盘 latest.json，
	// 这里周期加载暴露 argus_eval_*——审查质量趋势与运行时指标同面抓取。
	// 文件不存在（未跑过评测）与增量快照（评测进行中）静默跳过。
	refreshEvalMetrics := func() {
		if err := obs.RefreshEvalResults(cfg.EvalResultsPath); err != nil &&
			!obs.IsNotExistRefreshErr(err) && !obs.IsIncompleteRefreshErr(err) {
			log.Warn("argus.boot.eval_metrics_refresh_failed", "error", err.Error())
		}
	}
	refreshEvalMetrics()
	wm.AddTicker("eval-results", 5*time.Minute, func(ctx context.Context) { refreshEvalMetrics() })

	// ---- dream 长期记忆整合（v3.5.1）：去重 pattern_signals + 导出低置信模式到知识库 ----
	// 仅在学习环开启且显式配置周期时启动（默认 0=关闭）。
	if cfg.RegretLearning && cfg.DreamInterval > 0 {
		dm := &dream.Dream{
			Store:        store,
			Knowledge:    sys.Knowledge,
			KnowledgeDir: cfg.KnowledgeDir,
			MemoryDir:    cfg.MemoryDir,
			Kernel:       sys.MemKernel,        // v3.151.0 opt-in 双写（nil = 只写文件面）
			Learning:     cfg.LearningConfig(), // 前置门已保证 RegretLearning=true
			ExportTopN:   cfg.RegretInjectTopN,
			Log:          log,
		}
		wm.AddTicker("dream", cfg.DreamInterval, func(ctx context.Context) {
			_, _, err := dm.RunOnce(ctx)
			if err != nil {
				log.Warn("argus.dream.run_failed", "error", err.Error())
			}
		})
		log.Info("argus.dream.enabled", "interval", cfg.DreamInterval.String())
	}

	// ---- 审查健康报告（v3.11.0，G5）：周期聚合学习环状态，IM 推送给人看 ----
	// 噪声处置建议（人工门控）+ guidelines 增量建议（建议文本，绝不自动改仓库文件）。
	if cfg.HealthReportInterval > 0 {
		hr := &health.Runner{
			Store:  store,
			Notify: sys.Notify, // nil 安全：NotifyRaw 空实现仅日志
			Cfg: health.Config{
				Learning: cfg.LearningConfig(),
				TopN:     cfg.RegretInjectTopN,
			},
			Log: log,
		}
		wm.AddTicker("health-report", cfg.HealthReportInterval, func(ctx context.Context) {
			if err := hr.RunOnce(ctx); err != nil {
				log.Warn("argus.health.report_failed", "error", err.Error())
			}
		})
		log.Info("argus.health.enabled", "interval", cfg.HealthReportInterval.String())
	}

	// ---- 轮询兜底任务源（v3.0.1 改造：平台入站 webhook 不可达环境）----
	if cfg.PollInterval > 0 && cfg.PollRepos != "" {
		targets, bad := poller.ParseTargets(cfg.PollRepos)
		for _, b := range bad {
			log.Warn("argus.poller.bad_target", "item", b, "format", "platform:owner/repo")
		}
		if len(targets) > 0 {
			pl := &poller.Poller{SDK: sys.SDK, Store: store, Targets: targets,
				Interval: cfg.PollInterval, SkipDraft: cfg.SkipDraft, Log: log, Bus: sys.Bus,
				IsLeader: pollElector.IsLeader}
			pl.Start(ctx)
		}
	}
}
