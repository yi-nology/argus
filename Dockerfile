# Argus 镜像（design §6：学习期本机/内网；集群部署推 Harbor 走 buildkit+ArgoCD）
# 依赖处理：本地 replace（ekit / git-platform-sdk）已由 `go mod vendor` 收进 vendor/，
# 构建自包含（-mod=vendor），不需要私有 Gitea 凭证。vendor 由 `make docker` 自动生成。
# glebarez/sqlite 为纯 Go 驱动，CGO_ENABLED=0 静态编译；
# 运行层需要 git（workcopy 浅克隆）与 ca-certificates（平台/LLM HTTPS）。

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -ldflags "-s -w" -o /out/argus .

FROM alpine:3.20
RUN apk add --no-cache git ca-certificates tzdata && \
    addgroup -S argus && adduser -S argus -G argus
WORKDIR /app
COPY --from=builder /out/argus /app/argus
COPY plugins.example.yaml /app/plugins.yaml
COPY skills /app/skills
# 工作副本沙箱与 SQLite 库放卷上，容器重建不丢任务
RUN mkdir -p /data && chown -R argus:argus /app /data
USER argus

ENV ARGUS_ADDR=:8899 \
    ARGUS_DB_DSN=/data/argus.db \
    ARGUS_WORK_ROOT=/data/work \
    ARGUS_PLUGINS=/app/plugins.yaml \
    ARGUS_SKILLS_DIR=/app/skills
EXPOSE 8899
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8899/healthz || exit 1

ENTRYPOINT ["/app/argus"]
