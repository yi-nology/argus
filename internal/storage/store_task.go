// store_task.go —— tasks 表操作面：创建/认领/围栏收尾/状态迁移/清单查询。
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/oekit/data/gormx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"argus/internal/domain"
)

// ResetRunningToPending 把心跳过期的 running 任务重置回 pending
// （§10.4：崩溃任务否则静默丢失）。staleAfter 取心跳间隔的 ≥3 倍：
// 只有 heartbeat_at 已死（或遗留的 NULL 心跳旧行）才重置——多副本共库时，
// 一个实例滚动重启不会把其他存活实例正在执行的任务也重置（那会双执行、双回帖）。
func (s *Store) ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (int64, error) {
	s.writeLock()
	defer s.writeUnlock()
	cutoff := time.Now().Add(-staleAfter)
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("status = ? AND (heartbeat_at IS NULL OR heartbeat_at < ?)", StatusRunning, cutoff).
		Update("status", StatusPending)
	return res.RowsAffected, res.Error
}

// TouchRunningHeartbeats 续本实例全部 running 任务的心跳（worker 池周期调用）。
func (s *Store) TouchRunningHeartbeats(ctx context.Context) error {
	if s.RunnerID == "" {
		// 显式失败拒绝执行：空 RunnerID 的续期无法归属实例，静默 no-op 会让
		// 本实例任务被 reaper 反复重置重跑（装配纪律：main 先赋 RunnerID 再启动 worker）。
		return errors.New("storage: RunnerID 未装配，心跳续期拒绝执行")
	}
	s.writeLock()
	defer s.writeUnlock()
	return s.db.WithContext(ctx).Model(&Task{}).
		Where("status = ? AND runner_id = ?", StatusRunning, s.RunnerID).
		Update("heartbeat_at", time.Now()).Error
}

// GetTask 按 ID 查询。无此任务返回 ErrNotFound 哨兵。
func (s *Store) GetTask(ctx context.Context, id string) (*Task, error) {
	var t Task
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// GetTaskByEventID webhook 幂等查询：命中 = 平台重发。
func (s *Store) GetTaskByEventID(ctx context.Context, eventID string) (*Task, error) {
	var t Task
	err := s.quietDB().WithContext(ctx).Where("event_id = ?", eventID).First(&t).Error
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTask 新建任务（CLI/TaskAPI 提交；无幂等键）。
func (s *Store) CreateTask(ctx context.Context, t *Task) error {
	s.writeLock()
	defer s.writeUnlock()
	return s.db.WithContext(ctx).Create(t).Error
}

// NewTask 建任务工厂（v3.109.0）：ID/Status/CreatedAt 三个不变量此前在
// taskapi/webhook×2/poller 四处字面量各自组装，漏一处即幻影 ID/缺时间戳的
// 形态漂移且无从测红。调用方补 kind 专属字段（HeadSHA/EventType/Priority/
// EventID/ArtifactsJSON）。
func NewTask(kind, platform, owner, repo, number string) *Task {
	return &Task{
		ID:        NewTaskID(),
		Kind:      kind,
		Platform:  platform,
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		Status:    StatusPending,
		CreatedAt: time.Now(),
	}
}

// 任务去重原因词表（CreateTaskForWebhook 返回值；webhook 侧按此分发——
// 此前生产与比较两侧共 8 处裸字面量）。
const (
	DupLastHeadSHA = "last_head_sha"
	DupEventID     = "event_id"
	DupInFlight    = "in_flight"
)

// CreateTaskForWebhook 同步完成"同 SHA 去重检查 + 落库"（同一事务，§5.1 规则 4 的 TOCTOU 防线）。
// 返回 dupReason 非空表示命中去重（Dup* 词表），existingID 给出可指向的任务。
func (s *Store) CreateTaskForWebhook(ctx context.Context, t *Task) (dupReason, existingID string, err error) {
	s.writeLock()
	defer s.writeUnlock()
	txErr := gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
		// 规则 4a：review_targets.last_head_sha == 本次 head。
		// MySQL 下对 target 行 FOR UPDATE：同 PR 的并发建任务事务在此串行化，
		// 封多副本租约过期窗口的 TOCTOU（深挖 P0；SQLite 单写串行无需锁）。
		// 首审 PR 的 target 行尚不存在、无锚可锁——残余竞窗仅影响 PR 首审，可接受。
		var target ReviewTarget
		targetQ := wherePR(tx, t.Platform, t.Owner, t.Repo, t.Number)
		if !s.sqlite {
			targetQ = targetQ.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		err := targetQ.First(&target).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err // 瞬时 DB 错误不能静默跳过去重检查
		}
		// 同 SHA 去重只对审查类任务生效（domain.IsReviewKind：写作面/ci_analysis
		// 不写也不继承 LastHeadSHA，误挡会丢 CI 归因任务）。
		if err == nil && domain.IsReviewKind(t.Kind) && target.LastHeadSHA != "" && target.LastHeadSHA == t.HeadSHA {
			dupReason = DupLastHeadSHA
			return nil
		}
		// 规则 4b：存在同 head_sha 的 pending/running 任务（防 in-flight 竞态）
		var cnt int64
		err = tx.Model(&Task{}).
			Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND head_sha = ? AND status IN ?",
				t.Platform, t.Owner, t.Repo, t.Number, t.Kind, t.HeadSHA, []string{StatusPending, StatusRunning}).
			Count(&cnt).Error
		if err != nil {
			return err
		}
		if cnt > 0 {
			dupReason = DupInFlight
			var existing Task
			_ = tx.Select("id").Where(
				"platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND head_sha = ? AND status IN ?",
				t.Platform, t.Owner, t.Repo, t.Number, t.Kind, t.HeadSHA,
				[]string{StatusPending, StatusRunning}).First(&existing).Error
			existingID = existing.ID
			return nil
		}
		return tx.Create(t).Error
	})
	if txErr != nil {
		// event_id 唯一冲突（同 delivery 并发重发：幂等预查与落库之间存在窗口）——
		// 回查既有任务按幂等重复处理，避免以 500 暴露（GitHub 对连续 500 会禁用 webhook）。
		if t.EventID != nil {
			if exist, e := s.GetTaskByEventID(ctx, *t.EventID); e == nil && exist != nil {
				return DupEventID, exist.ID, nil
			}
		}
		// 在飞同 SHA 唯一防线冲突（跨实例并发建同 SHA 任务）：与 in_flight 同语义
		// 回查；查不到在飞再回退最近历史任务（last_head_sha 语义）。
		if isDuplicateKeyErr(txErr) {
			var existing Task
			if e := s.quietDB().WithContext(ctx).Where(
				"platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND head_sha = ? AND status IN ?",
				t.Platform, t.Owner, t.Repo, t.Number, t.HeadSHA,
				[]string{StatusPending, StatusRunning}).Order("created_at DESC").First(&existing).Error; e == nil {
				return DupInFlight, existing.ID, nil
			}
			if e := s.quietDB().WithContext(ctx).Where(
				"platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ?",
				t.Platform, t.Owner, t.Repo, t.Number, t.HeadSHA).Order("created_at DESC").First(&existing).Error; e == nil {
				return DupLastHeadSHA, existing.ID, nil
			}
		}
		return "", "", txErr
	}
	return dupReason, existingID, nil
}

// CreateTaskDirectIfIdle TaskAPI 直接触发路径的幂等建任务：同 platform/owner/repo/number
// 已存在 pending/running 任务时不新建，返回既有任务（created=false）。
// 与 CreateTaskForWebhook 的 in_flight 去重语义对齐，防手工触发与轮询/webhook 竞态双审。
// done 任务不拦——手工强制重审是 TaskAPI 的合法用途。
func (s *Store) CreateTaskDirectIfIdle(ctx context.Context, t *Task) (existing *Task, created bool, err error) {
	s.writeLock()
	defer s.writeUnlock()
	txErr := s.quietDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// MySQL 下先对 review_targets 行 FOR UPDATE：与 CreateTaskForWebhook 同一
		// 串行化锚点——此前 count-then-insert 无锁，跨副本可双建任务双烧 LLM
		//（v3.8.0 修复）。首审无锚行时残余竞窗由下方 duplicate 回查兜底。
		if !s.sqlite {
			var anchor ReviewTarget
			aq := wherePR(tx, t.Platform, t.Owner, t.Repo, t.Number).
				Clauses(clause.Locking{Strength: "UPDATE"}).First(&anchor)
			if aq.Error != nil && !errors.Is(aq.Error, gorm.ErrRecordNotFound) {
				return aq.Error
			}
		}
		var cnt int64
		if err := tx.Model(&Task{}).
			Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND status IN ?",
				t.Platform, t.Owner, t.Repo, t.Number, t.Kind, []string{StatusPending, StatusRunning}).
			Count(&cnt).Error; err != nil {
			return err
		}
		if cnt > 0 {
			existing = &Task{}
			if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND status IN ?",
				t.Platform, t.Owner, t.Repo, t.Number, t.Kind, []string{StatusPending, StatusRunning}).
				Order("created_at DESC, id DESC").First(existing).Error; err != nil {
				return err
			}
			return nil
		}
		if err := tx.Create(t).Error; err != nil {
			// SQLite 并发窗口撞 samesha 部分唯一索引 → 回查 in-flight 任务返回
			// 幂等语义；此前直接上抛 500，rerun 评论回帖"处理失败"（v3.8.0 修复）。
			if isDuplicateKeyErr(err) {
				existing = &Task{}
				if qerr := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND kind = ? AND status IN ?",
					t.Platform, t.Owner, t.Repo, t.Number, t.Kind, []string{StatusPending, StatusRunning}).
					Order("created_at DESC, id DESC").First(existing).Error; qerr != nil {
					return err // 回查也失败：暴露原始错误
				}
				created = false
				return nil
			}
			return err
		}
		created = true
		return nil
	})
	if txErr != nil {
		return nil, false, txErr
	}
	return existing, created, nil
}

// ClaimNextPending worker 原子抢占一个 pending 任务（pending→running）。
// 返回 ok=false 表示当前无任务。
// MySQL 走 FOR UPDATE SKIP LOCKED 单语句取+占（多副本无空转）；SQLite 保持
// 两步条件更新（单写模型下无竞争）。抢锁失败少量重试而非直接报"无任务"——
// 此前败者直接 ok=false，worker 睡 500ms，队列有任务也空转（深挖修复）。
func (s *Store) ClaimNextPending(ctx context.Context) (*Task, bool, error) {
	s.writeLock()
	defer s.writeUnlock()
	const maxAttempts = 3
	var claimed *Task
	err := s.quietDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for attempt := 0; attempt < maxAttempts; attempt++ {
			var t Task
			// 优先级队列（v3.40.0）：人类在等的任务（评论重审/API 手工）先于
			// poller 批量；同优先级保持 FIFO
			q := tx.Where("status = ?", StatusPending).Order("priority DESC, created_at ASC, id ASC")
			if !s.sqlite {
				q = q.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
			}
			err := q.First(&t).Error
			if err != nil {
				return err // gorm.ErrRecordNotFound → 无任务
			}
			now := time.Now()
			res := tx.Model(&Task{}).Where("id = ? AND status = ?", t.ID, StatusPending).
				Updates(map[string]any{"status": StatusRunning, "started_at": &now,
					"runner_id": s.RunnerID, "heartbeat_at": now})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				continue // 被并发抢走：重试取下一条（SKIP LOCKED 下基本不发生）
			}
			t.Status = StatusRunning
			t.StartedAt = &now
			t.RunnerID = s.RunnerID
			t.HeartbeatAt = &now
			claimed = &t
			return nil
		}
		return gorm.ErrRecordNotFound
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return claimed, claimed != nil, nil
}

// ListTasksByHead 查询同 PR 同 head 的任务（E2E 幂等断言/诊断用）。
func (s *Store) ListTasksByHead(ctx context.Context, platform, owner, repo, number, headSHA string) ([]Task, error) {
	var out []Task
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ?",
		platform, owner, repo, number, headSHA).Order("created_at ASC").Find(&out).Error
	return out, err
}

// UpdateTaskRunningStatus 局部更新状态字段（任务执行中的落点）。
func (s *Store) FinishTask(ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
	tokensUsed int, summaryCommentID int64) error {
	_, err := s.FinishTaskFenced(ctx, id, status, verdict, report, resultJSON, failReason, tokensUsed, summaryCommentID)
	return err
}

// FinishTaskFenced 终态落库，带执行归属 fencing（v3.8.0）：
// WHERE 追加 status='running'（+ runner_id 归属校验）——stale 重置后旧执行体
// 的僵尸写入直接失效，不再覆盖新执行体的结果/引发双评论。此前是无守卫的
// Where("id = ?")，后写者全列覆盖先写者。返回 fenced=true 表示本次写入因
// 归属失效被拒（调用方告警；属预期行为而非错误）。
// 例外：RunnerID 未装配（单测桩）时只校验 status。
func (s *Store) FinishTaskFenced(ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
	tokensUsed int, summaryCommentID int64) (fenced bool, err error) {
	s.writeLock()
	defer s.writeUnlock()
	now := time.Now()
	updates := map[string]any{
		"status":      status,
		"verdict":     verdict,
		"report":      report,
		"result_json": resultJSON,
		"fail_reason": failReason,
		"tokens_used": tokensUsed,
		"finished_at": &now,
	}
	if summaryCommentID > 0 {
		updates["summary_comment_id"] = summaryCommentID
	}
	if status == StatusFailed {
		// failed 退避（v3.8.0）：attempt+1，指数退避（5m×3^n，封顶 1h）后由
		// ResetFailedForRetry 重置重试；成功/stale 路径不动 attempt。
		var cur Task
		if err := s.db.WithContext(ctx).Select("attempt").Where("id = ?", id).First(&cur).Error; err == nil {
			updates["attempt"] = cur.Attempt + 1
			nr := now.Add(retryBackoff(5*time.Minute, cur.Attempt+1))
			updates["next_retry_at"] = &nr
		}
	}
	// 例外：RunnerID 未装配（单测桩直接 CreateTask(pending)→finish 的合成路径）
	// 时跳过 fencing——生产装配流程 main 必然先赋 RunnerID 再启动 worker。
	q := s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id)
	if s.RunnerID != "" {
		q = q.Where("status = ? AND runner_id = ?", StatusRunning, s.RunnerID)
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 0, nil
}

// ResetFailedForRetry failed 任务的到期重试（v3.8.0）：failed 且 attempt < maxRetries
// 且退避到期的任务重置回 pending（attempt 保留，防循环）。maxRetries<=0 = 禁用。
// 重启不重置 failed——只有此入口（或 TaskAPI 手工重审）能复活失败任务。
func (s *Store) ResetFailedForRetry(ctx context.Context, maxRetries int, now time.Time) (int64, error) {
	if maxRetries <= 0 {
		return 0, nil
	}
	s.writeLock()
	defer s.writeUnlock()
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("status = ? AND attempt < ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?",
			StatusFailed, maxRetries, now).
		Update("status", StatusPending)
	return res.RowsAffected, res.Error
}

// SetTaskHeadSHA 回填 CLI 任务的 head SHA（执行期从平台获取）。
func (s *Store) SetTaskHeadSHA(ctx context.Context, id, sha string) error {
	s.writeLock()
	defer s.writeUnlock()
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Update("head_sha", sha).Error
}

// SetTaskDebug 留存阶段调试产物（ARGUS_DEBUG=1）。
func (s *Store) SetTaskDebug(ctx context.Context, id, debugJSON string) error {
	s.writeLock()
	defer s.writeUnlock()
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Update("debug_json", debugJSON).Error
}

// MarkReviewPosted 完整评审发成功落标记（v3.36.0）：重跑任务据此跳过
// ListReviews 平台锚点查询（总结评论更新不受影响，照常执行）。
func (s *Store) MarkReviewPosted(ctx context.Context, id string) error {
	s.writeLock()
	defer s.writeUnlock()
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Update("review_posted", true).Error
}

// MergeTaskStageArtifacts 合并写入任务阶段产物 checkpoint（v3.12.0 G2 断点续审）。
// patch 键覆盖同名字段；同任务由单一 runner 认领执行，read-modify-write 在
// writeMu 下无并发竞争。坏 JSON 当空处理——checkpoint 可丢（重试退化为全量
// 重跑），不可阻塞主流程。v3.185.3：同 MergeTaskArtifacts 补 "null" 钳制
// （Unmarshal("null") 成功且置 nil map,后续赋值 panic 的同型隐患）。
func (s *Store) MergeTaskStageArtifacts(ctx context.Context, id string, patch map[string]string) error {
	s.writeLock()
	defer s.writeUnlock()
	var row Task
	if err := s.db.WithContext(ctx).Select("stage_artifacts").Where("id = ?", id).First(&row).Error; err != nil {
		return err
	}
	cur := map[string]string{}
	if row.StageArtifacts != "" {
		_ = json.Unmarshal([]byte(row.StageArtifacts), &cur)
	}
	if cur == nil {
		cur = map[string]string{}
	}
	for k, v := range patch {
		cur[k] = v
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Update("stage_artifacts", string(b)).Error
}

// MergeTaskArtifacts 合并写入任务 artifacts_json（v3.183.0）：影响面快照在
// 评审早期即产出的机器可读产物（impact_view），不等 finalize 即落库——
// MCP get_impact / 影响视图 API 在任务 running 期就能消费，任务失败也不丢。
// patch 键覆盖同名字段；并发纪律同 MergeTaskStageArtifacts。
// v3.185.3 修复：TaskAPI 建的任务 artifacts_json 为字符串 "null"（nil map
// 序列化产物），Unmarshal("null") 成功且把 cur 置 nil——原实现直接 cur[k]=v
// panic（live 实弹炸出，任务卡死 running）；同型隐患顺修 stage 版。
func (s *Store) MergeTaskArtifacts(ctx context.Context, id string, patch map[string]string) error {
	s.writeLock()
	defer s.writeUnlock()
	var row Task
	if err := s.db.WithContext(ctx).Select("artifacts_json").Where("id = ?", id).First(&row).Error; err != nil {
		return err
	}
	cur := map[string]string{}
	if row.ArtifactsJSON != "" {
		// "null"/坏 JSON 均可能把 cur 反序列化成 nil——统一在下文钳回空表
		_ = json.Unmarshal([]byte(row.ArtifactsJSON), &cur)
	}
	if cur == nil {
		cur = map[string]string{}
	}
	for k, v := range patch {
		cur[k] = v
	}
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
		Update("artifacts_json", string(b)).Error
}

// HasNewerNonFailedTask staleness 单条件判定（§10.2 五轮评审修正）：
// 存在同 PR 更晚创建的非 failed 且带 head 的任务 ⇒ 本任务 stale。
// 只统计 head_sha <> ” 的任务（webhook/poller 来源）：手动任务 head 为空（执行期才回填），
// 若它先压 stale 再失败（手动任务参数/平台错误率高），本轮审查会被永久静默吞掉。
// 数据全来自 tasks 表（head_sha/created_at），不依赖 review_targets.last_head_sha。
func (s *Store) HasNewerNonFailedTask(ctx context.Context, platform, owner, repo, number, taskID string, createdAt time.Time) (bool, error) {
	var cnt int64
	err := s.db.WithContext(ctx).Model(&Task{}).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND id <> ? AND created_at > ? AND status <> ? AND head_sha <> ''",
			platform, owner, repo, number, taskID, createdAt, StatusFailed).
		Count(&cnt).Error
	return cnt > 0, err
}

// TaskCounts 队列深度（pending/running），供指标暴露。
func (s *Store) TaskCounts(ctx context.Context) (pending, running int64, err error) {
	err = s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ?", StatusPending).Count(&pending).Error
	if err != nil {
		return 0, 0, err
	}
	err = s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ?", StatusRunning).Count(&running).Error
	return pending, running, err
}

// ListTasks 任务列表（GET /tasks）：status 过滤（空=全部），created_at 倒序，limit 收口。
// Select 摘要列（深挖修复）：此前 Find 全列——report/result_json/debug_json 均为
// LONGTEXT，单行可达 MB 级，列表 100 条白拉几百 MB IO，还放大 SQLite 单写连接的
// 队头阻塞。报告/调试产物只为详情接口服务。
func (s *Store) ListTasks(ctx context.Context, status string, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := s.quietDB().WithContext(ctx).
		Select("id", "kind", "platform", "owner", "repo", "number", "head_sha", "event_type",
			"status", "verdict", "runner_id", "summary_comment_id", "tokens_used", "artifacts_json",
			"created_at", "started_at", "finished_at", "heartbeat_at", "updated_at").
		Order("created_at DESC, id DESC").Limit(limit)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []Task
	err := q.Find(&out).Error
	return out, err
}

// FindReportByHead 按 head SHA 查最近一次成功的报告（GET /reports/:ref）。
// 无报告返回 ErrNotFound 哨兵。
func (s *Store) FindReportByHead(ctx context.Context, headSHA string) (*Task, error) {
	var t Task
	err := s.quietDB().WithContext(ctx).
		Where("head_sha = ? AND status = ? AND report != ''", headSHA, StatusDone).
		Order("created_at DESC").First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// GetLastReviewedDiff 查询同 PR 最近一次 done 任务的 diff 文本（增量审查 diff-of-diffs）。
// 返回空字符串表示无前置审查记录。
func (s *Store) GetLastReviewedDiff(ctx context.Context, platform, owner, repo, number string) (string, error) {
	var t Task
	err := s.quietDB().WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND status = ?",
			platform, owner, repo, number, StatusDone).
		Order("created_at DESC").First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	if t.ResultJSON == "" {
		return "", nil
	}
	// 从 result_json 中提取 diff_text 字段（轻量解析，不反序列化整个结构）
	var payload struct {
		DiffText string `json:"diff_text"`
	}
	if err := jsonUnmarshal([]byte(t.ResultJSON), &payload); err != nil {
		return "", nil // result_json 格式异常不阻塞审查
	}
	return payload.DiffText, nil
}
