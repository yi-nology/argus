---
home: true
heroText: 给一个 PR，还你一份带证据的审查报告。
tagline: Argus 读完代码、对照需求、派出多个审查专家，把逐条带 file:line 证据的报告与 APPROVE / COMMENT / REQUEST_CHANGES 结论回帖到 PR 上。
actions:
  - text: ⭐ GitHub 仓库
    link: https://github.com/yi-nology/argus
    type: primary
  - text: 快速开始
    link: /#quickstart
    type: secondary
features:
  - icon: 📋
    title: 需求复核
    details: 把 PR 描述/需求 issue 解析成验收清单，逐条判定 implemented / partial / unverifiable，堵 scope creep。
  - icon: 🎯
    title: 影响面视图
    details: blast radius 一图看清：import 闭包、调用方、测试面、函数级图 diff（Go/shell/Python/Rust/JS/TS）、跨轮演化对比、审查意见挂图。
  - icon: 🔬
    title: 消歧符号图
    details: 目录限定键根治同名假边；get_trace 查两符号调用链、get_symbol_context 看定义+调用方全景。
  - icon: 🌐
    title: 多语言仓库
    details: Go/Python/JS/TS/shell/SQL/Rust/Java/C++ 全进审查；符号图、影响面、函数级 diff 分层覆盖（能力×语言矩阵见 README）。
  - icon: 💬
    title: MR 上的体验
    details: 执行期进度条原地滚动；完成后 TL;DR 结论先行 + 完整报告置底；行内意见带 suggestion 一键应用。
---

<div class="chips" style="justify-content:center; margin-top:-8px">
<code>Gitea · Forgejo · GitHub · GitLab · Gitee · GitCode · 腾讯工蜂</code>
<code>单二进制 · 本地化部署</code>
<code>MCP 原生 · 24 工具</code>
<code>Apache-2.0</code>
</div>

## 它怎么审查

多 agent 编排，确定性裁决——不是「一问一答」，是一条有反思、有证据链的流水线。

<div class="pipeline">
<div class="step"><div class="no">1</div><b>需求解析</b><small>R1 · AC 清单化</small></div>
<div class="step"><div class="no">2</div><b>变更理解</b><small>R2 · diff 深读</small></div>
<div class="step"><div class="no">3</div><b>完成度判定</b><small>R3 · 逐条核实</small></div>
<div class="step"><div class="no">4</div><b>报告反思</b><small>R3.5 · 自纠</small></div>
<div class="step"><div class="no">5</div><b>专家插件</b><small>R4 · 安全/OCR/…</small></div>
<div class="step"><div class="no">6</div><b>确定性裁决</b><small>R5 · 结论回帖</small></div>
</div>

## 审查长什么样

结论先行，证据随行——以下全部是**真实回帖截图**（测试仓冒烟 PR 与生产实弹；生产图内部标识已脱敏），markdown 由 GitHub / GitLab 官方渲染器渲染。

<div class="gallery">
<figure><img src="./img/tldr-production.png" alt="TL;DR 先行的审查结论回帖"><figcaption><b>TL;DR 结论先行</b>——结论 + 意见分级计数 + 重点关注 top3，先给判断再给细节（生产实弹）</figcaption></figure>
<figure><img src="./img/report-checklist.png" alt="需求复核报告与验收清单"><figcaption><b>完整报告</b>——验收清单逐条 implemented / missing，意见带 file:line 证据与修复建议</figcaption></figure>
<figure><img src="./img/inline-suggestion.png" alt="行内意见与 suggestion 一键应用"><figcaption><b>行内意见 + 一键应用</b>——意见钉到 diff 具体行，<code>suggestion</code> 原生渲染可直接采纳</figcaption></figure>
<figure><img src="./img/verdict-receipt.png" alt="确定性裁决回执"><figcaption><b>确定性裁决</b>——R5 真值表产出 APPROVE / COMMENT / REQUEST_CHANGES 回执</figcaption></figure>
<figure><img src="./img/progress.png" alt="执行期进度回执"><figcaption><b>执行期进度</b>——审查启动即回执，报告随同步更新（不刷屏）</figcaption></figure>
<figure><img src="./img/impact-graph.png" alt="改动影响面图"><figcaption><b>影响面视图</b>——影响图 nodes/edges、风险分、共变面板（自检任务实拍）</figcaption></figure>
<figure><img src="./img/funcdiff-card.png" alt="函数级图 diff 卡片"><figcaption><b>函数级图 diff</b>——签名变更/函数增删/调用边 ±（自检任务实拍）</figcaption></figure>
<figure><img src="./img/mermaid-diff.png" alt="mermaid 图 diff 回帖渲染"><figcaption><b>图 diff 回帖</b>——mermaid 直接贴在 MR 评论里渲染</figcaption></figure>
</div>

## MCP 原生 · 24 个工具

审查能力全部以 MCP 工具暴露，任意 agent / IDE 直接驱动。

<div class="chips">
<div class="grp">审查发起</div>
<code>review_pr</code><code>requirement_compliance</code><code>review_diff</code><code>review_diff_async</code>
<div class="grp">结果消费</div>
<code>get_report</code><code>ask_argus</code><code>list_tasks</code><code>list_findings</code><code>get_task_stages</code><code>get_suggestion</code>
<div class="grp">代码检索</div>
<code>search_code</code><code>search_sem</code><code>search_knowledge</code><code>search_findings</code><code>get_trace</code><code>get_symbol_context</code>
<div class="grp">影响与配置</div>
<code>get_impact</code><code>get_repo_map</code><code>get_repo_config</code><code>get_capabilities</code><code>list_agents</code><code>fix_finding</code><code>suppress_finding</code><code>restore_finding</code>
</div>

<h2 id="quickstart">快速开始</h2>

单二进制，内网可离线运行；配置即环境变量。

```bash
# 1. 获取（go install 或 Release 二进制）
go install github.com/yi-nology/argus@latest

# 2. 配置平台与模型（环境变量）
export ARGUS_API_TOKENS=tok:admin
export ARGUS_GITHUB_URL=https://your-git.example.com
export ARGUS_GITHUB_TOKEN=xxxx
# 平台同构 ARGUS_<平台>_*：gitea/forgejo/github/gitlab/gitee/gitcode/tencent_code
# （GitLab 即 ARGUS_GITLAB_*；腾讯工蜂前缀 ARGUS_TENCENT_CODE_*）
export OPENAI_BASE_URL=https://your-llm-gateway/v1   # LLM 网关（OpenAI 兼容协议）
export OPENAI_API_KEY=sk-xxxx
export OPENAI_MODEL=your-model

# 3. 启动；PR 触发即自动审查，报告回帖到平台
./argus serve
```

更多部署形态（k3s / Docker / OCR 插件 / 按仓配置 / 语言能力矩阵）见 [README](https://github.com/yi-nology/argus/blob/main/README.md)。

---

Argus · [github.com/yi-nology/argus](https://github.com/yi-nology/argus) · Apache License 2.0 · © 2026 yi-nology
