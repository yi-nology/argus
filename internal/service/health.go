// health.go —— G5 健康报告查询面（v3.13.0）：GET /reports/health 的 service 层。
// 与周期 IM 推送（main.go health-report ticker）同数据源：按需拉取飞轮状态，
// 供报告页/副驾类消费方轮询；无写操作，Bearer 门控由路由装配层挂。

package service

import (
	"context"

	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/yi-nology/argus/internal/health"
)

// HealthReport 学习环飞轮状态快照（噪声处置建议 + guidelines 增量建议 + 计数）。
// 返回 (report, httpStatus)；learning 环关闭时返回空报告（Empty=true）而非错误——
// 调用方可据此展示「学习环未开启」而非报错。
func (s *Service) HealthReport(ctx context.Context) (*health.Report, int) {
	if s.Store == nil {
		return nil, consts.StatusInternalServerError
	}
	rep, err := health.Build(ctx, s.Store, health.Config{
		Learning: s.Cfg.LearningConfig(),
		TopN:     s.Cfg.RegretInjectTopN,
	})
	if err != nil {
		return nil, consts.StatusInternalServerError
	}
	return rep, consts.StatusOK
}
