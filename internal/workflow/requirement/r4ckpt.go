// r4ckpt.go —— R4 逐插件 checkpoint（v3.154.0）：插件产物一经落定即落库
// （stage_artifacts 键 ckpt_r4/<plugin>），同任务重试时命中键的插件直接复用
// 产物不再重跑——R4 是 LLM 主体成本，进程崩溃续跑不再整批重来。
//
// 与 harness checkpoint 的关系：checkpoint.BuildPlan 原「连续前缀」语义为有序
// 步骤链设计，R4 fan-out 插件彼此独立、乱序完成——前缀规则会把「第 1 个没完成
// 第 3 个完成」的合法完成集作废。harness v0.13.0 的 Isolated/DependsOn 支持 fan-out
// 后这一障碍消除：**决策层**（哪些插件可信可跳）收编到 BuildPlan+Isolated（每插件
// 一步、Isolated=无前驱、乱序完成各自独立可信）。**产物层仍存 stage_artifacts**
// （Isolated/DependsOn 是决策字段不是存储，plugin.Result 需真实存储，与 R1/R2 同构）。
//
// 语义边界（与 R1/R2 checkpoint 一致）：
//   - 只在任务内有效：键落在 tasks.stage_artifacts，新任务永远全量重跑；
//   - 失败/跳过/取消的插件不落键——重试自然再跑（G4 补审语义不变）；
//   - 复用的插件本轮零 token 消耗：预算是「本轮账」，复用不回灌
//     （方向是低估，对预算闸是安全侧）。
package requirement

import (
	"encoding/json"

	"github.com/yi-nology/agent-harness/checkpoint"

	"argus/internal/dispatch"
	"argus/internal/plugin"
)

// restoreR4Outcomes 同任务重试回流：命中 ckpt_r4/<plugin> 且产物可解析的插件，
// 经 BuildPlan 判定为「可信完成」后从执行集剔除、产物直接复用。返回
// （待执行集, 复用选中集, 复用产物）。
//
// 决策层走 BuildPlan+Isolated（fan-out 语义）：每插件一步、Isolated=无前驱，
// 乱序完成各自独立可信。键值损坏（JSON 解不出）= 不进 Done，回执行集重跑——
// checkpoint 是成本优化，永不做正确性决策。
func restoreR4Outcomes(artifacts map[string]string, sel []dispatch.Selection) (exec, restoredSel []dispatch.Selection, restored []dispatch.Outcome) {
	// 组图 + 可信完成集（Done 只收产物可解析的插件——键损坏当没命中）。
	graph := make([]checkpoint.StepDef, 0, len(sel))
	done := make([]string, 0, len(sel))
	results := make(map[string]plugin.Result, len(sel))
	for _, s := range sel {
		name := s.Reg.Manifest.Name
		graph = append(graph, checkpoint.StepDef{ID: name, Isolated: true})
		raw, ok := artifacts[plugin.ArtifactCkptR4Prefix+name]
		if !ok || raw == "" {
			continue
		}
		var res plugin.Result
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			continue // 键值损坏 = 当没命中，回执行集重跑
		}
		done = append(done, name)
		results[name] = res
	}
	// Isolated 全独立 → BuildPlan 的可信边界 = 各自独立完成即可信（fan-out），
	// 不再要求连续前缀；Skip = 可信完成，Run = 其余。
	plan := checkpoint.BuildPlan(graph, &checkpoint.Snapshot{RunID: "r4", Done: done})
	trusted := make(map[string]bool, len(plan.Skip))
	for _, name := range plan.Skip {
		trusted[name] = true
	}
	for _, s := range sel {
		name := s.Reg.Manifest.Name
		if !trusted[name] {
			exec = append(exec, s)
			continue
		}
		restoredSel = append(restoredSel, s)
		res := results[name]
		restored = append(restored, dispatch.Outcome{
			Name:     name,
			Manifest: s.Reg.Manifest,
			Result:   &res,
		})
	}
	return exec, restoredSel, restored
}

// r4CkptHook 逐插件落定落库（Dispatcher OutcomeHook 实现方）：
// 成功产物序列化后经 checkpointSave 合并写（锁在调用方 hook 闭包内）。
func (w *Workflow) r4CkptHook(o dispatch.Outcome) {
	if o.Result == nil {
		return
	}
	b, err := json.Marshal(o.Result)
	if err != nil {
		return
	}
	w.ckptMu.Lock()
	defer w.ckptMu.Unlock()
	w.checkpointSave(map[string]string{plugin.ArtifactCkptR4Prefix + o.Name: string(b)})
}
