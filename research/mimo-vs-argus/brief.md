# Research brief: Xiaomi MiMo — 怎么做的 / 为什么智能好用 / Argus 差距

- Date: 2026-09-17
- Depth: standard
- Audience: Argus 项目开发者（技术负责人视角，要可落地的差距判断，不是科普）
- Workspace: `/Users/demo-user/my_project/lacus/argus/research/mimo-vs-argus/`

## Refined question

1. 小米 MiMo（模型 + MiMoCode / MiMo Desktop 代理产品）在架构、模型、工具链、产品形态上是怎么做的？
2. 用户体感「这么智能、这么好用」的技术/产品成因是什么？
3. 与本地 Argus（可插拔多 Agent PR 审查平台，v3.8.0）相比，差距在哪些维度？哪些差距是定位差异（不必追），哪些是能力缺口（值得追）？

## Scope

**In**
- Xiaomi MiMo 基础模型公开信息（版本、训练/推理特性、benchmark）
- MiMoCode / MiMo Desktop / 代理运行时公开与可本地验证的能力面（skills、memory、MCP、subagent、workflow、context management）
- 代理产品「好用」的可解释机制（上下文工程、工具编排、反馈闭环、UX）
- Argus 现有能力与已知边界（本地 README/design/docs/CHANGELOG + 代码结构）
- 对比维度：模型底座、代理运行时、编排哲学、上下文/记忆、工具生态、反馈闭环、产品形态、安全边界、可观测

**Out**
- 小米内部未公开实现细节（无法核实则标 speculative）
- 把 Argus 改造成通用 coding agent 的完整方案（报告只给差距与可追方向）
- 商业/财务分析

## Assumptions

- 「Xiaomi MiMo」= 小米 MiMo 模型家族 + 基于其的 MiMoCode/MiMo Desktop 代理产品（当前会话即运行其上）
- Argus 定位是领域化 PR 审查平台，不是通用 agent 宿主；对比时区分「定位差异」与「能力缺口」
- 公开资料优先；本机 mimocode 配置/skill 文档作为产品能力的一手旁证
- 不假设 MiMo 与 Argus 要做成同一形态；报告要能指导 Argus 下一步取舍

## Angles

- F1: Xiaomi MiMo 模型家族 — 版本演进、公开 benchmark、训练/推理宣称特性
- F2: MiMoCode / MiMo Desktop 代理产品 — 架构与能力面（agents/subagents/memory/MCP/skills/workflows/context）
- F3: 「智能 + 好用」的机制拆解 — 上下文工程、工具编排、反馈/自我改进、交互设计
- F4: 公开评价与对比 — 社区/媒体/竞品视角下 MiMo 的优劣势
- F5: Argus 本地能力基线 — 从 README/design/architecture/CHANGELOG/代码提炼可比维度（本地研究，非 web）
