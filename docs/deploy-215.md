# deploy-215 —— 试点环境部署 runbook（10.0.0.215）

> 2026-10-03 固化：当时 10.44 段对开发机断路（v3.105.0 未部署，215 停在 v3.75.3-215）。
> 网络恢复后按本节机械执行。SSH 别名 `215`；跳板不可达时 `-o ProxyJump=none` 直连。

## 0. 前置检查

```bash
ssh -o ProxyJump=none 215 'echo REACHABLE'
ssh 215 'docker ps --format "{{.Names}} {{.Image}} {{.Status}}" | grep -i argus'  # 部署前必须实拍当前版本
```

## 1. 远程构建（本地 Docker Hub 不可达，勿在开发机构建）

```bash
git archive --format=tar.gz -o /tmp/argus-src.tar.gz HEAD
scp /tmp/argus-src.tar.gz 215:~/argus-src.tar.gz
ssh 215 'mkdir -p ~/argus-build && tar -xzf ~/argus-src.tar.gz -C ~/argus-build'
```

Dockerfile 基底镜像走 daocloud 镜像（golang:1.26-alpine 拉不到时）：
`docker pull docker.m.daocloud.io/library/golang:1.26-alpine` 并在 build 前 tag 回 `golang:1.26-alpine`。
私有依赖（git.example.com/ekit、agentkit）走 BuildKit SSH 挂载（`DOCKER_BUILDKIT=1`，215 上
ssh-agent 需装 forgejo key；e2e 失败先看是否 replace 指向本地路径）。

```bash
ssh 215 'cd ~/argus-build && DOCKER_BUILDKIT=1 docker build -t argus:v3.105.0-215 .'
```

## 2. 切换容器（约定名/参数勿省）

```bash
ssh 215 'docker stop argus && docker rm argus'
ssh 215 'docker run -d --name argus --restart unless-stopped \
  --network 1panel-network \
  --add-host git.example.com:10.0.0.22 \
  -p 8899:8899 -v /opt/argus/data:/data argus:v3.105.0-215'
```

`--network 1panel-network` 勿漏（默认 bridge 断内网 DNS/sink，2026-09-20 教训）；
`--add-host` 勿漏（0926 DNS 修复）。

## 3. 验证面

```bash
ssh 215 'curl -s localhost:8899/readyz'                       # 健康面
ssh 215 'docker logs argus 2>&1 | grep -m1 "ver="'            # 报告头 ver=/启动日志
```

端到端：TaskAPI 触发一次审查（内网 webhook 不可用时走 TaskAPI），确认报告回帖与
ver 头。大 MR 重审 ~15min 属正常。

## 4. 网络恢复后的连带项（同一次窗口内做掉）

- **GitLab note-events checkbox**：om-gitlab 项目 webhook 设置勾选 Note events
  （评论触发链路生产配置，代码 v3.5.5 已 live-verified，仅差生产开关）。
- **MR144 行内评论最终确认**：查看行内评论是否正常落位（R4 路径纪律修复的最终回归）。
- **eval-real**：核对 lacus/.env 的 OPENAI_MODEL（勿残留 pro 前缀）后跑
  `make eval-real`（~2.5h；CI 每周二 05:20 也会自动跑）。
