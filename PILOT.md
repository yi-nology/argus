# Argus 真实 Forgejo/Gitea 试点手册（演进基线 v3.0.1）

> 目标：在真实仓库（git.example.com，Forgejo）上跑通「PR 开/同步 → Argus 审查 → 报告评论」，
> 作为后续演进的验收基线。本机已实测的两个事实已内置处理：
> ① git.example.com 证书已过期 → 用 `ARGUS_GITEA_SKIP_TLS=1`；
> ② 机器上没有现成 API token → 需要你生成一个（约 30 秒，见第 1 步）。

## 0. 网络前提

Forgejo（10.0.0.10）必须能回调到运行 Argus 的机器：

```bash
ipconfig getifaddr en0   # 记下 Mac 内网 IP，webhook 默认回调 http://<该IP>:8899
# 若启用了防火墙，放行 8899/TCP
```

## 1. 生成 Forgejo Access Token（唯一需要人工的一步）

浏览器打开 `https://git.example.com`（过期证书点"继续访问"）→ 右上角头像 → 设置 → 应用 →
生成新令牌：勾选 **repository 读写 + issue 读写** → 生成后复制（`gto_` 开头，只显示一次）。

## 2. 启动 Argus

```bash
cd lacus/argus
export ARGUS_ADDR=:8899
export ARGUS_APITOKEN=pilot-$(date +%s)          # 🔒 路由凭证（v3.0.1 起未配置会拒绝服务）
export ARGUS_GITEA_URL=https://git.example.com
export ARGUS_GITEA_TOKEN=<第 1 步的 gto_...>
export ARGUS_GITEA_SECRET=argus-pilot-secret     # webhook 签名密钥，任意串
export ARGUS_GITEA_SKIP_TLS=1                    # 证书过期，试点期间免校验
make run
```

启动日志确认三行：`platform_ready`（平台探活通过）、`plugin_registered`（插件清单）、
`http_ready`。若看到 `skip_tls` 告警属预期。

## 3. 注册 webhook（一键）

```bash
./scripts/setup-webhook.sh <owner/repo>          # 例：enjoydream/argus-pilot
```

脚本幂等（已有 argus webhook 则更新）。也可以先跳过 webhook，直接走第 4 步的 TaskAPI 路径。

## 4. 触发审查（两条路径）

**路径 A · TaskAPI（不依赖 webhook，先跑通它）**

```bash
# 对任意已有 PR：
curl -s -X POST localhost:8899/tasks \
  -H "Authorization: Bearer $ARGUS_APITOKEN" -H "Content-Type: application/json" \
  -d '{"kind":0,"platform":"gitea","owner":"enjoydream","repo":"<repo>","number":"<PR号>"}'
# 返回 {"data":{"taskId":"t-...","status":"pending"}} 后轮询：
curl -s localhost:8899/tasks/<taskId> -H "Authorization: Bearer $ARGUS_APITOKEN" | python3 -m json.tool
```

**路径 B · webhook（平台自动触发）**

在该仓库开一个 PR（或对已有 PR push 新提交），然后观察：

| 观察点 | 预期 |
|---|---|
| Argus 日志 | `argus.webhook.accepted`（签名通过/未跳过）→ `worker.claimed` → `task_done` |
| PR 页面 | 出现/更新 `## 📋 Argus 需求复核报告` 评论（含 HTML 注释头） |
| 有行内意见时 | CreateReview 行内评论（REQUEST_CHANGES/COMMENT 事件） |
| 平台重发/重复投递 | `argus.webhook.duplicate_delivery`，不重复烧 LLM |
| 同一 PR 再 push | 更新同一条总结评论（不刷新评论流） |

## 5. 验收清单（试点通过的判据）

- [ ] TaskAPI 路径：task 从 pending → done，`report` 字段含验收清单与结论
- [ ] PR 页报告评论：验收清单完成度 / scope creep / 审查意见 / 结论 四段齐全
- [ ] webhook 路径：push 新提交后报告评论被**更新**而非新增
- [ ] staleness：连续两次 push、第一次任务慢完成时总结评论仍是新 SHA 的结果（观察日志 `stale_skip`）
- [ ] `/metrics` 出现 `argus_tasks_total`、`argus_llm_tokens_total` 样本

## 6. 已知试点边界

- 证书过期：`SKIP_TLS=1` 期间凭证走未校验 TLS 通道——试点仅限内网，正式使用应修复证书后关闭。
- draft PR 默认不审（`ARGUS_SKIP_DRAFT=true`）；bot 自身事件自动跳过（`ARGUS_BOT_USERNAME`）。
- 事件词表：只审 `cr.opened / cr.synchronize / cr.reopened`（Forgejo 与 Gitea 同词表，已核）。
- LLM 消耗：一个中等 PR 约 4–7 次调用、3–8k tokens（mimo-v2.5 实测口径）。
