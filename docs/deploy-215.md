# deploy-215 —— 试点环境部署 runbook（10.0.0.215）

> 2026-10-03 固化：当时 10.44 段对开发机断路（v3.105.0 未部署，215 停在 v3.75.3-215）。
> 网络恢复后按本节机械执行。SSH 别名 `215`；跳板不可达时 `-o ProxyJump=none` 直连。

## 0. 前置检查

```bash
ssh -o ProxyJump=none 215 'echo REACHABLE'
ssh 215 'docker ps --format "{{.Names}} {{.Image}} {{.Status}}" | grep -i argus'  # 部署前必须实拍当前版本
```

## 1. 构建新镜像（推荐：本地交叉编译 + FROM 旧镜像，免拉基底/免私有依赖）

本地：`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.version=v<ver>-215 -X github.com/oekit/core/version.BuildCommit=$(git rev-parse --short HEAD)" -o bin/argus-v<ver>-215 .`
（v3.147.0 起 `/version` 端点读 oekit core/version：main.version 由进程启动时同步过去，BuildCommit 需单独注入）

```bash
scp -o ProxyJump=none bin/argus-v<ver>-215 215:/opt/argus/bin/
ssh 215 'chmod +x /opt/argus/bin/argus-v<ver>-215
  printf "FROM argus:v<旧ver>-215\nCOPY argus-v<ver>-215 /app/argus\n" > /opt/argus/bin/Dockerfile.new
  cd /opt/argus/bin && docker build -f Dockerfile.new -t argus:v<ver>-215 .'
```

FROM 旧镜像继承 /app/skills、git、ca-certificates——基底镜像拉不到也没关系。
（备选：源码归档 scp 到 215 远程构建，需 BuildKit SSH 挂载拉私有依赖，见文末历史。）

## 2. 切换容器（先打回滚 tag + 导出 env，再 stop/备份/rm/run）

```bash
ssh 215 'docker tag argus:v<旧ver>-215 argus:rollback-$(date +%Y%m%d)
  docker inspect argus --format "{{range .Config.Env}}{{println .}}{{end}}" | grep -E "^(ARGUS_|OPENAI_|REVIEW_TOKEN)" > /opt/argus/argus.env
  docker stop argus
  cp /opt/argus/data/argus.db /opt/argus/data/argus.db.bak-<旧ver>-$(date +%Y%m%d)
  docker rm argus
  docker run -d --name argus --restart always \
    --network 1panel-network -p 8899:8899 \
    --add-host git.example.com:10.0.0.22 \
    -v /opt/argus/data/.opencodereview:/home/argus/.opencodereview \
    -v /opt/argus/data:/data \
    -v /opt/argus/plugins.yaml:/app/plugins.yaml \
    -v /opt/argus/bin:/opt/argus/bin \
    --env-file /opt/argus/argus.env \
    argus:v<ver>-215'
```

`--network 1panel-network` 勿漏（默认 bridge 断内网 DNS/sink，2026-09-20 教训）；
`--add-host` 勿漏（0926 DNS 修复）；挂载/参数以 docker inspect 实拍为准逐项复刻。
**env 导出模式必须含裸名 `REVIEW_TOKEN`**（2026-10-04 实测：`^(ARGUS_|OPENAI_)`
过滤会把它滤掉、每次部署复现 §4 漂移——review-service 插件静默禁用）。

## 3. 验证面

```bash
ssh 215 'docker ps --format "{{.Image}} {{.Status}}" | grep argus'          # Up + 镜像版本
ssh 215 'curl -s localhost:8899/readyz'                                     # {"code":0,"data":{"status":"ok","checks":[{"name":"db","status":"ok"},{"name":"llm","status":"ok"}]},...}；任一失败 503 + code 503
ssh 215 'curl -s localhost:8899/version'                                    # data.version=v<ver>-215（v3.147.0 起；healthz 不再带 version）
ssh 215 'docker logs argus 2>&1 | grep -c "\"level\":\"ERROR\""'            # 0（过 1~2 个 poller 周期再看）
ssh 215 'docker logs argus 2>&1 | grep -o "\"version\":\"v[^\"]*\""'        # ver 头
# API 探针（Bearer=ARGUS_APITOKEN；路由是根路径，无 /api/v1 前缀）：
ssh 215 'curl -s -H "Authorization: Bearer <token>" localhost:8899/stats/overview?since_days=30 | head -c 200'
```

端到端：poller 每 2m 自动轮 5 仓；大 MR 重审 ~15min 属正常。

## 4. 配置漂移检查（v3.105.0 部署实测踩过）

对比 plugins.yaml 的 `auth_token_env` 声明与 argus.env：2026-10-03 发现 REVIEW_TOKEN
从 env 丢失导致 review-service 插件静默禁用（v3.94 期已漂移）。修复：从 mcp-server
容器 `docker inspect mcp-server` 抓同名 env 对拷进 argus.env 重建。每次部署后
核对 `docker logs argus | grep remote_auth_env_missing` 应为零条。

## 5. 网络恢复后的连带项（同一次窗口内做掉）

- **GitLab note-events checkbox**：om-gitlab 项目 webhook 设置勾选 Note events
  （评论触发链路生产配置，代码 v3.5.5 已 live-verified，仅差生产开关）。
- **MR144 行内评论最终确认**：查看行内评论是否正常落位（R4 路径纪律修复的最终回归）。
- **eval-real**：核对 lacus/.env 的 OPENAI_MODEL（勿残留 pro 前缀）后跑
  `make eval-real`（~2.5h；CI 每周二 05:20 也会自动跑）。
