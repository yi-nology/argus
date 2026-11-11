# 获取 · 构建 · 部署

单二进制起步，Docker / k3s 一步到位；CI/CD 全链开源可见。

## 三种获取方式

```bash
# 1) go install（开发者最快路径）
go install github.com/yi-nology/argus@latest

# 2) GitHub Release 二进制（免工具链）
# https://github.com/yi-nology/argus/releases

# 3) GHCR 多架构镜像（linux/amd64 + linux/arm64，与 /version 端点同源版本元数据）
docker pull ghcr.io/yi-nology/argus:latest
```

## Docker 运行

镜像为多阶段静态构建（golang-alpine 构建层 → 运行层仅含 git 与 ca-certificates；
CGO_ENABLED=0 纯 Go，glebarez/sqlite 内嵌）。开箱即跑：

```bash
docker run -d --name argus -p 8899:8899 \
  -e ARGUS_APITOKEN=change-me \
  -e ARGUS_API_TOKENS=change-me:admin \
  -e ARGUS_GITHUB_URL=https://your-git.example.com \
  -e ARGUS_GITHUB_TOKEN=xxxx \
  -e ARGUS_GITHUB_SECRET=webhook-secret \
  -e OPENAI_BASE_URL=https://your-llm-gateway/v1 \
  -e OPENAI_API_KEY=sk-xxxx \
  -e OPENAI_MODEL=your-model \
  -v argus-data:/data \
  ghcr.io/yi-nology/argus:latest
```

- 平台凭证同构 `ARGUS_<平台>_*`（gitea/forgejo/github/gitlab/gitee/gitcode/tencent_code，
  腾讯工蜂前缀 `ARGUS_TENCENT_CODE_*`）；LLM 走任意 OpenAI 兼容网关（含内网网关）。
- 产物形态：`docker-compose`（含 .env.example 模板）与 `deploy/k8s` kustomize 双形态在仓内。

## GitHub Actions：三段流水线（全部在仓内可审计）

| Job | 触发 | 内容 |
|---|---|---|
| `ci` | 每次 push/PR | build / vet / test / arch 守卫四门禁 + **MySQL/PostgreSQL/SQLite 三方言测试矩阵**（生产方言必进矩阵的教训在案）+ golden 评测门禁 |
| `image` | push main / tag v* | GHCR 多架构镜像（amd64+arm64，QEMU+buildx）；tag 策略：main → `latest`+`sha-*`，tag → 版本号+`latest` |
| `pages` | push `web/**` | 宣传页 VuePress 构建 → GitHub Pages（你现在看的这个站） |

版本元数据（`/version` 端点、镜像标签、报告头）三处同源：`git describe` 注入
`main.version` 与 oekit core/version，绝不手填。

## k3s 生产部署（实弹在役形态）

生产（43 k3s ARM64，双副本+PostgreSQL）走「换芯」升级链——不重建镜像，只换二进制层：

```bash
# Mac 交叉编译 arm64 → 推内部 GitLab argus-bin → 一条命令换芯
VERSION=v6.6.0 ./deploy/k8s43/swap-deploy.sh
```

swap-deploy.sh 全程断言（拉取→权限→镜像层叠→rollout→health 双探活），滚动升级零停机；
回滚锚点保留旧镜像层。多副本面：迁移哨兵 + ensure 租约单飞 + local-path 同节点 pin。

## 运行形态速览

| 形态 | 适用 | 入口 |
|---|---|---|
| Webhook | 平台可达 Argus | 七平台签名校验 + delivery 幂等 + 启动期幂等自注册（`ARGUS_ENSURE_WEBHOOK=1`） |
| 轮询兜底 | NAT/VPN 下 webhook 不可达 | `ARGUS_POLL_REPOS` 定时发现 |
| TaskAPI | 手动/脚本/CI | `POST /tasks` |
| MCP | 任意 agent/IDE | `/mcp`（streamable HTTP + Bearer），24 工具，见 [README 工具清单](https://github.com/yi-nology/argus/blob/main/README.md) |

更多细节（环境变量全表/按仓配置/OCR 插件/知识库）见 [仓库 README](https://github.com/yi-nology/argus/blob/main/README.md)。
