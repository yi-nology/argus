package requirement

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	"git.example.com/enjoydream/agentkit/textutil"

	"argus/internal/obs"
	"argus/internal/plugin"
)

// 缺省需求文本截断上限（字符）——Workflow 未装配 config 派生值时的兜底。
const (
	defaultReqBodyMaxChars  = 24_000
	defaultReqIssueMaxChars = 8_000
)

// SummarizeChange R2：变更理解 → ChangeSummary（R3/R4 复用的 diff 公共前缀，省 token）。
func (w *Workflow) SummarizeChange(ctx context.Context, task plugin.Task) (string, error) {
	start := time.Now()
	defer func() {
		plugin.ReportStageDuration(plugin.LayerR2, time.Since(start))
		plugin.ReportLayerExec(plugin.LayerR2)
	}()
	w.onStage(plugin.LayerR2)

	if task.Diff == nil || strings.TrimSpace(task.Diff.Text) == "" {
		return "（无有效 diff 内容）", nil
	}
	msgs := []*schema.Message{
		schema.SystemMessage(r2System),
		schema.UserMessage(DataFence("git diff（已清洗）", task.Diff.Text)),
	}
	resp, err := w.LLM.Generate(ctx, plugin.LayerR2, msgs)
	if err != nil {
		return "", err
	}
	summary := truncSummary(resp.Content, 1200)
	if rc := utf8.RuneCountInString(strings.TrimSpace(summary)); rc < r2MinValidRunes {
		// 软失败路径（v3.42.0）：R2 是自由文本输出、无结构化失败信号——这是
		// 「R2 失效无失败路径可钉」（v3.26.0 在案）的根因。最小有效性校验
		//（空/过短）给路径钉上失败语义：带反馈重试一次，仍无效则上抛错误走
		// 调用方既有降级（summary=""，R3/R4 携带原始 diff 不受影响；不写
		// checkpoint，任务重试时 R2 可重来）。
		obs.IncrR2SoftFailure()
		w.Log.Warn("argus.r2.soft_failure", "runes", rc, "action", "带反馈重试一次")
		retryMsgs := append(msgs, schema.UserMessage(plugin.ReminderNote("retry-feedback",
			"上一轮变更摘要输出为空或过短，无法用作变更理解。请基于 diff 输出一段完整摘要：概述改动了哪些文件、每个文件改了什么（不少于两句话）。")))
		if resp2, e2 := w.LLM.Generate(ctx, plugin.LayerR2, retryMsgs); e2 == nil {
			if s2 := truncSummary(resp2.Content, 1200); utf8.RuneCountInString(strings.TrimSpace(s2)) >= r2MinValidRunes {
				return s2, nil
			}
		}
		return "", fmt.Errorf("R2 变更摘要两次输出均无效（空/过短）")
	}
	return summary, nil
}

// r2MinValidRunes R2 摘要最小有效长度——少于此值视为模型输出失效（空转/拒答）。
const r2MinValidRunes = 20

// onStage 阶段进度上报（nil 安全；runner 接线 SSE 发布）+ 进度标记落库
// （v3.132.0）：每阶段开头持久化 stage_progress，get_report 轮询据此显示
// 「当前在跑哪一步」——此前只有 checkpoint/debug 产物可推导，RunReviewOnly
// 跳过 R1/R3/R35 时被推导成"没做到"，轮询恒显示 R1。
func (w *Workflow) onStage(stage string) {
	if w.OnStage != nil {
		w.OnStage(stage)
	}
	w.checkpointSave(map[string]string{plugin.ArtifactStageProgress: stage})
}

const (
	// detailMaxBytes 单条详情硬上限（防异常工具输出打爆内存）。
	detailMaxBytes = 200_000
	// detailMaxRefs 详情库条数上限（超出整体换——最新详情优先保）。
	detailMaxRefs = 8
	// detailOverflowMax codegraph 类工具进上下文的截断正文上限。
	detailOverflowMax = 4_000
)

// stashDetail 暂存超限全文，返回引用编号（get_detail 凭 ref 取回）。
func (w *Workflow) stashDetail(content string) string {
	w.detailMu.Lock()
	defer w.detailMu.Unlock()
	if w.detailStore == nil {
		w.detailStore = map[string]string{}
	}
	if len(content) > detailMaxBytes {
		content = content[:detailMaxBytes] + "\n…（详情超 200KB 硬上限，尾部丢弃）"
	}
	if len(w.detailStore) >= detailMaxRefs {
		w.detailStore = map[string]string{}
		w.detailSeq = 0
	}
	w.detailSeq++
	ref := fmt.Sprintf("d%d", w.detailSeq)
	w.detailStore[ref] = content
	return ref
}

// stashOverflowAt 超限两级化：全文入任务级详情库，进上下文的只留 max 内正文 +
// 取回指引。未超限原样返回（零开销直通）。
func (w *Workflow) stashOverflowAt(full string, max int) string {
	if max <= 0 || len(full) <= max {
		return full
	}
	ref := w.stashDetail(full)
	head, _ := textutil.TruncRunes(full, max)
	return head + fmt.Sprintf("\n…（截断；完整结果 %d 字节已存为详情，用 get_detail 工具 ref=%q 获取全文）", len(full), ref)
}

// ---------- 小工具 ----------

func truncSummary(s string, n int) string {
	return textutil.TruncEllipsis(strings.TrimSpace(s), n)
}
