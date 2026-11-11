# 竞品分析

> 调研快照 **2026-09 ~ 2026-11**（业界格局引自仓内对标调研存档；各家产品迭代快，以官网为准）。
> 立场声明：Argus 是自托管开源方案，对比侧重**能力形态**而非商业优劣；下表 ✅/⚠️/— 均按
> 「该产品是否有此能力形态」标注，不代表质量高低。

## 业界格局一览

| 产品 | 形态 | 定位抓手 |
|---|---|---|
| **CodeRabbit** | SaaS（商业标杆） | AST 级分析、learnings 交互记忆、one-click suggestion + Fix with AI、review metrics 仪表盘 |
| **Qodo Merge（PR-Agent）** | 开源 + SaaS | /describe /review /improve /ask 命令族、PR compression、LiteLLM 全模型、五平台覆盖 |
| **GitHub Copilot code review** | SaaS（平台原生） | 与 GitHub 深度集成、diff 内 suggestion、IDE 同源模型 |
| **Claude Code Action / Codex reviews** | agent 进 CI 新形态 | headless agent 可跑测试/push commit/追问，完整 agent loop |
| **Cursor Bugbot** | SaaS | 低噪声 confidence 卖点 |
| **Greptile / Sourcegraph** | SaaS | 全库上下文检索增强 |

## 能力矩阵

<table class="vsmatrix">
<tr><th>能力</th><th>Argus</th><th>CodeRabbit</th><th>PR-Agent</th><th>Copilot</th><th>Agent-CI 形态</th></tr>
<tr><td><b>需求复核</b>（AC 清单逐条判定）</td><td class="yes">✅ R1–R5 独有</td><td class="no">—</td><td class="no">—</td><td class="no">—</td><td class="no">—</td></tr>
<tr><td><b>确定性裁决</b>（真值表非 LLM）</td><td class="yes">✅ R5 真值表</td><td class="no">—</td><td class="no">—</td><td class="no">—</td><td class="no">—</td></tr>
<tr><td><b>多 agent 编队互盲 + 共识</b></td><td class="yes">✅ 黑板共识</td><td class="no">—</td><td class="no">—</td><td class="no">—</td><td class="part">⚠️ 单 agent loop</td></tr>
<tr><td><b>报告反思自纠</b>（R3.5）</td><td class="yes">✅</td><td class="no">—</td><td class="no">—</td><td class="no">—</td><td class="part">⚠️ 隐式</td></tr>
<tr><td>行内意见 + suggestion 一键应用</td><td class="yes">✅</td><td class="yes">✅ +Fix with AI</td><td class="yes">✅</td><td class="yes">✅</td><td class="yes">✅</td></tr>
<tr><td>增量审查（只审变更）</td><td class="yes">✅ diff-of-diffs + stale 跳过</td><td class="yes">✅</td><td class="yes">✅</td><td class="yes">✅</td><td class="yes">✅</td></tr>
<tr><td>PR 描述/changelog 生成</td><td class="yes">✅ /describe /changelog</td><td class="no">—</td><td class="yes">✅</td><td class="part">⚠️</td><td class="yes">✅</td></tr>
<tr><td>问答追问（基于报告/代码）</td><td class="yes">✅ ask_argus 多轮</td><td class="yes">✅ chat</td><td class="yes">✅ /ask</td><td class="part">⚠️</td><td class="yes">✅</td></tr>
<tr><td>改动影响面可视化</td><td class="yes">✅ 图谱+函数级 diff+共变</td><td class="no">—</td><td class="no">—</td><td class="no">—</td><td class="no">—</td></tr>
<tr><td>CI 失败归因</td><td class="yes">✅</td><td class="no">—</td><td class="part">⚠️</td><td class="no">—</td><td class="yes">✅ 可跑测试</td></tr>
<tr><td>自动修复闭环（push commit）</td><td class="part">⚠️ 沙箱验证在，push 待拍板</td><td class="part">⚠️ Fix with AI</td><td class="part">⚠️</td><td class="no">—</td><td class="yes">✅ 天然形态</td></tr>
<tr><td>交互记忆/学习环</td><td class="yes">✅ 误报抑制+学习环</td><td class="yes">✅ learnings</td><td class="part">⚠️</td><td class="no">—</td><td class="no">—</td></tr>
<tr><td>评测体系进 CI</td><td class="yes">✅ 68+ golden + 定时真实评测</td><td class="no">—</td><td class="part">⚠️ 少量用例</td><td class="no">—</td><td class="no">—</td></tr>
<tr><td>自托管 / 内网离线</td><td class="yes">✅ 单二进制/Docker/k3s</td><td class="no">— SaaS</td><td class="yes">✅ 开源</td><td class="no">— SaaS</td><td class="part">⚠️ 依赖模型服务</td></tr>
<tr><td>MCP 原生工具面</td><td class="yes">✅ 24 工具双向</td><td class="no">—</td><td class="no">—</td><td class="part">⚠️</td><td class="part">⚠️</td></tr>
<tr><td>多平台（GitLab/Gitea/…）</td><td class="yes">✅ 七平台</td><td class="yes">✅ 多平台</td><td class="yes">✅ 五平台</td><td class="no">— GitHub 系</td><td class="part">⚠️ 视 CI 而定</td></tr>
<tr><td>开源协议</td><td class="yes">Apache-2.0</td><td class="no">闭源商业</td><td class="yes">Apache-2.0</td><td class="no">闭源商业</td><td class="part">工具开源/模型闭源</td></tr>
</table>

## Argus 的差异化位置

1. **唯一同时做「需求复核 + 代码审查」的**：别家都在回答「这段代码有没有 bug」，
   Argus 先回答「这个 PR 做没做完需求」——对带验收标准的团队，这比多找两个 warning 值钱。
2. **确定性裁决**：结论由真值表产出而非模型自由发挥，可审计、可复现——合规场景刚需。
3. **影响面视图**：把「改这里会炸哪里」画出来（图谱+函数级 diff+历史共变），业界没有对标物。
4. **评测文化**：68+ golden 用例进 CI + 定时真实模型回归——审查质量可度量、可回滚。
5. **数据主权**：全链自托管，LLM 接任意 OpenAI 兼容网关（含内网网关）；代码不出域。

## 诚实的差距（谁更适合你的场景）

- **SaaS 开箱体验**：CodeRabbit/Copilot 零部署点一下就用；Argus 需要自托管与配置（一次成本，长期自由）。
- **生态与知名度**：商业产品有成熟支持/文档/集成市场；Argus 是新晋开源项目，社区规模小。
- **agent 自动修复闭环**：Claude Code Action/Codex 形态天然带完整 agent loop；Argus 的自动 push 尚在授权模型拍板中（沙箱验证环已在）。
- **模型生态**：SaaS 绑定自家强模型；Argus 模型任配——上限取决于你的网关里有什么。

**一句话选型**：要零部署、找 bug 够用 → SaaS 产品；要**需求级把关 + 确定性结论 + 影响面视图 + 数据不出内网** → Argus。

::: tip 反馈
对比事实有出入？欢迎 [提 issue](https://github.com/yi-nology/argus/issues) 指正——本页按证据更新。
:::
