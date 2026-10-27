package requirement

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	"github.com/oagentkit/textx/textutil"

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
	// detailMaxBytes 单条详情硬上限（防异常工具输出打爆磁盘与取回面）。
	detailMaxBytes = 200_000
	// detailOverflowMax codegraph 类工具进上下文的截断正文上限。
	detailOverflowMax = 4_000
)

// spillRefRe 详情 ref 白名单：本包生成的存档名形态（d<十进制序号>）。get_detail 的
// ref 由模型提供（不可信），取回面以此挡路径注入——不满足形态直接按「无此 ref」处理。
var spillRefRe = regexp.MustCompile(`^d[1-9][0-9]*$`)

// stashDetail 超限全文落盘存档（v5.0.0 收编 agent-harness/spill 语义：磁盘面取代
// v3.39.0 进程内 map——checkpoint 复活/进程重启后 get_detail 不再落空；会话隔离 =
// 任务 ID 目录，职责分工与 spill 一致：存档/取回判定单源，文案与工具壳归宿主）。
// 写失败返回空串（stashOverflowAt 退纯截断，不阻塞工具输出主链路）。
func (w *Workflow) stashDetail(content string) string {
	if w.SpillDir == "" {
		return ""
	}
	if len(content) > detailMaxBytes {
		content = content[:detailMaxBytes] + "\n…（详情超 200KB 硬上限，尾部丢弃）"
	}
	w.spillMu.Lock()
	defer w.spillMu.Unlock()
	if err := os.MkdirAll(w.SpillDir, 0o755); err != nil {
		w.Log.Warn("argus.r3.spill_mkdir_failed", "error", err.Error())
		return ""
	}
	w.spillSeq++
	ref := fmt.Sprintf("d%d", w.spillSeq)
	if err := os.WriteFile(filepath.Join(w.SpillDir, ref), []byte(content), 0o600); err != nil {
		w.Log.Warn("argus.r3.spill_write_failed", "error", err.Error())
		return ""
	}
	return ref
}

// stashOverflowAt 超限两级化：全文入任务级详情存档，进上下文的只留 max 内正文 +
// 取回指引。未超限原样返回（零开销直通）；存档写失败退纯截断（如实告知全文丢弃，
// 不留取得到不了的指引）。
func (w *Workflow) stashOverflowAt(full string, max int) string {
	if max <= 0 || len(full) <= max {
		return full
	}
	head, _ := textutil.TruncRunes(full, max)
	if ref := w.stashDetail(full); ref != "" {
		return head + fmt.Sprintf("\n…（截断；完整结果 %d 字节已存为详情，用 get_detail 工具 ref=%q 获取全文）", len(full), ref)
	}
	return head + fmt.Sprintf("\n…（截断；完整结果 %d 字节超存档上限，已丢弃）", len(full))
}

// ---------- 小工具 ----------

func truncSummary(s string, n int) string {
	return textutil.TruncEllipsis(strings.TrimSpace(s), n)
}
