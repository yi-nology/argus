---
home: true
---

<section class="hero2">
<div class="hero2-copy">
<div class="kicker">SELF-HOSTED · APACHE-2.0 · MCP NATIVE</div>
<h1>给一个 PR，<br>还你一份带证据的审查报告。</h1>
<p class="hero2-desc">Argus 读完代码、对照需求、派出多个审查专家，把逐条带 file:line 证据的报告与 APPROVE / COMMENT / REQUEST_CHANGES 结论回帖到 PR 上。</p>
<div class="hero2-actions">
<a class="btn2 primary" href="https://github.com/yi-nology/argus">GitHub 仓库 ↗</a>
<a class="btn2" href="#quickstart">快速开始</a>
</div>
<div class="hero2-meta">
<code>Gitea</code><code>Forgejo</code><code>GitHub</code><code>GitLab</code><code>Gitee</code><code>GitCode</code><code>腾讯工蜂</code>
</div>
</div>
<div class="hero2-visual">
<div class="shot">
<div class="shot-inner">
<div class="shot-bar"><span></span><span></span><span></span><em>argus-bot · 真实审查回帖</em></div>
<img src="./img/tldr-production.png" alt="Argus 真实审查回帖：TL;DR 结论先行">
</div>
</div>
</div>
</section>

<div class="stats">
<div><div class="num">24</div><div class="lbl">MCP 工具</div></div>
<div><div class="num">7</div><div class="lbl">Git 平台</div></div>
<div><div class="num">13+</div><div class="lbl">语言覆盖</div></div>
<div><div class="num">R1–R5</div><div class="lbl">审查流水线</div></div>
<div><div class="num">68+</div><div class="lbl">golden 评测</div></div>
<div><div class="num">100%</div><div class="lbl">开源 · 内网可离线</div></div>
</div>

<div class="sec-head">
<div class="secnum">01</div>
<h2>核心能力</h2>
<p class="section-sub">需求级把关 + 代码级审查双轨；每项能力都在真实 MR 上验证过。</p>
</div>

<div class="hl-grid">
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="8" y="2" width="8" height="4" rx="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><path d="m9 14 2 2 4-4"/></svg></span><h3>需求复核</h3></div>
<p>把 PR 描述/需求 issue 解析成验收清单，逐条判定 implemented / partial / unverifiable——堵 scope creep 与「声称完成但没做」。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><circle cx="12" cy="12" r="1.4" fill="currentColor" stroke="none"/></svg></span><h3>影响面视图</h3></div>
<p>blast radius 一图看清：import 闭包、调用方、测试面、函数级图 diff（Go/shell/Python/Rust/JS/TS）、跨轮演化对比、审查意见挂图。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="6" r="2.5"/><circle cx="18" cy="6" r="2.5"/><circle cx="12" cy="18" r="2.5"/><path d="M7.8 7.8 10.5 16M16.2 7.8 13.5 16M8.5 6h7"/></svg></span><h3>消歧符号图</h3></div>
<p>目录限定键根治同名假边；get_trace 查两符号调用链、get_symbol_context 看定义+调用方全景；可选 go/types 类型解析级边。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="m8 6-6 6 6 6M16 6l6 6-6 6"/></svg></span><h3>多语言仓库</h3></div>
<p>Go / Python / JS / TS / shell / SQL / Rust / Java / C++ 全进审查；符号图、影响面、函数级 diff 分层覆盖，能力×语言矩阵见 README。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a8 8 0 0 1-8 8H4l2.2-2.9A8 8 0 1 1 21 12z"/><path d="M8.5 10.5h7M8.5 13.5h4"/></svg></span><h3>MR 上的体验</h3></div>
<p>执行期进度条原地滚动；完成后 TL;DR 结论先行 + 完整报告置底；行内意见带 suggestion 一键应用。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 4h16v12H8l-4 4z"/><path d="M8 8h8M8 11h5"/></svg></span><h3>评论指令交互</h3></div>
<p>@bot 重审 / 问答 / fix / testgen 一句话触发；意图路由自动分派，30s 节流防刷屏，评论命令与 TaskAPI 同语义。</p>
</div>
</div>

<div class="dark-band">
<div class="sec-head">
<div class="secnum">02</div>
<h2>它怎么审查</h2>
<p class="section-sub">多 agent 编排，确定性裁决——一条有反思、有证据链的流水线。</p>
</div>
<div class="pipeline">
<div class="step"><div class="no">1</div><b>需求解析</b><small>R1 · AC 清单化</small></div>
<div class="step"><div class="no">2</div><b>变更理解</b><small>R2 · diff 深读</small></div>
<div class="step"><div class="no">3</div><b>完成度判定</b><small>R3 · 逐条核实</small></div>
<div class="step"><div class="no">4</div><b>报告反思</b><small>R3.5 · 自纠</small></div>
<div class="step"><div class="no">5</div><b>专家插件</b><small>R4 · 安全/OCR/…</small></div>
<div class="step"><div class="no">6</div><b>确定性裁决</b><small>R5 · 结论回帖</small></div>
</div>
</div>

<div class="sec-head">
<div class="secnum">03</div>
<h2>审查长什么样</h2>
<p class="section-sub">全部为真实回帖截图（冒烟 PR 与生产实弹；生产图内部标识已脱敏），markdown 由 GitHub / GitLab 官方渲染器渲染。</p>
</div>

<div class="gallery">
<figure><img src="./img/tldr-production.png" alt="TL;DR 先行的审查结论回帖"><figcaption><b>TL;DR 结论先行</b>——结论 + 意见分级计数 + 重点关注 top3</figcaption></figure>
<figure><img src="./img/report-checklist.png" alt="需求复核报告与验收清单"><figcaption><b>完整报告</b>——验收清单逐条 implemented / missing，意见带 file:line 证据</figcaption></figure>
<figure><img src="./img/inline-suggestion.png" alt="行内意见与 suggestion 一键应用"><figcaption><b>行内意见 + 一键应用</b>——钉到 diff 具体行，suggestion 可直接采纳</figcaption></figure>
<figure><img src="./img/verdict-receipt.png" alt="确定性裁决回执"><figcaption><b>确定性裁决</b>——R5 真值表产出 APPROVE / COMMENT / REQUEST_CHANGES</figcaption></figure>
<figure><img src="./img/progress.png" alt="执行期进度回执"><figcaption><b>执行期进度</b>——启动即回执，报告随同步更新</figcaption></figure>
<figure><img src="./img/impact-graph.png" alt="改动影响面图"><figcaption><b>影响面视图</b>——nodes/edges、风险分、共变面板（自检实拍）</figcaption></figure>
<figure><img src="./img/funcdiff-card.png" alt="函数级图 diff 卡片"><figcaption><b>函数级图 diff</b>——签名变更/函数增删/调用边 ±</figcaption></figure>
<figure><img src="./img/mermaid-diff.png" alt="mermaid 图 diff 回帖渲染"><figcaption><b>图 diff 回帖</b>——mermaid 直接贴 MR 评论渲染</figcaption></figure>
</div>

<div class="diagram">
<div class="diag-cap"><b>技术底座</b> · 通用能力全部下沉共享库，argus 只做审查领域编排 · <a href="./architecture.html">架构详解 →</a></div>
<div class="eco-mini">
<div class="em-core"><b>Argus</b><small>审查领域层 · R1–R5 / 真值表 / 影响面</small></div>
<div class="em-lib"><b>go-git-platform</b><small>git 平台 SDK</small></div>
<div class="em-lib"><b>oekit</b><small>工程底座 · 12 模块</small></div>
<div class="em-lib"><b>oagentkit</b><small>agent 能力族 · 12 模块</small></div>
<div class="em-lib"><b>goagent-kernel</b><small>agent 执行内核</small></div>
</div>
</div>

<div class="sec-head">
<div class="secnum">04</div>
<h2>Argus 有何不同</h2>
<p class="section-sub">带验收清单的审查、真值表裁决、影响面图——这三件事别家没有。逐项对比见<a href="./vs.html">竞品分析</a>。</p>
</div>

<div class="hl-grid">
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M10 6h10M10 12h10M10 18h10"/><path d="m3 6 1.5 1.5L7 5M3 12l1.5 1.5L7 11M3 18l1.5 1.5L7 17"/></svg></span><h3>不只找 bug，还查「做没做完」</h3></div>
<p>业界审查 bot 都在找 bug；Argus 先把需求解析成验收清单逐条核对——需求复核 + 代码审查双轨，scope creep 和「声称完成但没做」都逃不掉。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v18M7 21h10M5 7l-3 7h6zM19 7l-3 7h6zM5 7h14M8 7l4-4 4 4"/></svg></span><h3>裁决是真值表，不是模型心情</h3></div>
<p>结论由确定性 Go 真值表按序短路产出（R5），可解释、可复现；路由、裁决、合并不走 LLM——同一份 diff 两次审查结论一致。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="9" cy="8" r="3.2"/><path d="M3 20a6 6 0 0 1 12 0"/><circle cx="17" cy="9" r="2.6"/><path d="M16 14.6A5.5 5.5 0 0 1 21 20"/></svg></span><h3>多专家互盲 + 反思自纠</h3></div>
<p>安全/正确性/需求多专家并发互盲审查，黑板共识标注交叉分歧；R3.5 反思轮批判→修订，剔除误报嫌疑。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><path d="M12 12 19 7"/></svg></span><h3>blast radius 一图看清</h3></div>
<p>import 反向闭包、调用方、测试面、历史共变、函数级图 diff——审查意见挂图、mermaid 直贴 MR、交互 HTML 可分享。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2 4 5.5V11c0 5 3.4 9 8 10.5 4.6-1.5 8-5.5 8-10.5V5.5z"/><path d="m8.8 11.8 2.2 2.2 4-4"/></svg></span><h3>内网可离线，数据不出门</h3></div>
<p>单二进制 + Docker/k3s 自托管，LLM 走任意 OpenAI 兼容网关（含内网网关）；代码、报告、记忆全在自己的存储里。</p>
</div>
<div class="hl-card">
<div class="hl-head"><span class="ic"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20V4"/><path d="M4 20h16"/><path d="M8 16v-5M12 16V7M16 16v-8M20 16v-3"/></svg></span><h3>自带评测体系</h3></div>
<p>68+ golden 用例（需求复核/评论交互/编队语义/多语言）常驻 CI 门禁 + 定时真实模型评测，审查质量有回归防线。</p>
</div>
</div>

<div class="dark-band">
<div class="sec-head">
<div class="secnum">05</div>
<h2>MCP 原生 · 24 个工具</h2>
<p class="section-sub">审查能力全部以 MCP 工具暴露——Claude Code、Cursor、ZCode、自建 agent 直接驱动。</p>
</div>
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
</div>

<div class="sec-head">
<div class="secnum">06</div>
<h2 id="quickstart">快速开始</h2>
<p class="section-sub">单二进制，内网可离线运行；配置即环境变量。</p>
</div>

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

<p class="section-sub" style="text-align:left">更多部署形态（k3s / Docker / OCR 插件 / 按仓配置 / 语言能力矩阵）见<a href="./guide.html">部署文档</a>与<a href="https://github.com/yi-nology/argus/blob/main/README.md">仓库 README</a>。</p>

<div class="site-footer">
Argus · <a href="https://github.com/yi-nology/argus">github.com/yi-nology/argus</a> · Apache License 2.0 · © 2026 yi-nology
</div>
