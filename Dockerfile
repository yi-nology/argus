# Argus 镜像（design §6：学习期本机/内网；集群部署推 Harbor 走 buildkit+ArgoCD）
# 依赖处理（v3.1 起 go mod 模式，不再 vendor）：
#   ekit（git.example.com 私有）走 BuildKit SSH 挂载直拉（insteadOf 重写为 git@），
#   其余公共依赖走 GOPROXY。构建机器需：DOCKER_BUILDKIT=1 且 ssh-agent 装有 forgejo key
#   （make docker 已带 --ssh default）。glebarez/sqlite 为纯 Go 驱动，CGO_ENABLED=0 静态编译；
#   运行层需要 git（workcopy 浅克隆）与 ca-certificates（平台/LLM HTTPS）。
# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git openssh
COPY go.mod go.sum ./
COPY . .
RUN git config --global url."git@git.example.com:".insteadOf "https://git.example.com/" && \
    git config --global --add safe.directory /src
ARG TARGETARCH=amd64
# 版本元数据（v3.147.0）：--build-arg VERSION=v3.x.y GIT_COMMIT=abc123 注入 main.version 与
# oekit core/version（/version 端点）；不传保持 dev/unknown。
ARG VERSION=dev
ARG GIT_COMMIT=unknown
RUN --mount=type=ssh \
    GOPRIVATE=git.example.com/* GOINSECURE=git.example.com/* GOPROXY=https://goproxy.cn,direct \
    CGO_ENABLED=0 GOARCH=${TARGETARCH} \
    go build -mod=mod -trimpath -ldflags "-s -w -X main.version=${VERSION} -X github.com/oekit/core/version.Version=${VERSION} -X github.com/oekit/core/version.BuildCommit=${GIT_COMMIT}" -o /out/argus . && \
    CGO_ENABLED=0 GOARCH=${TARGETARCH} \
    go build -mod=mod -trimpath -ldflags "-s -w" -o /out/ocrshim ./cmd/ocrshim

FROM alpine:3.20
RUN apk add --no-cache git ca-certificates tzdata && \
    addgroup -S argus -g 1000 && adduser -S argus -G argus -u 1000
WORKDIR /app
COPY --from=builder /out/argus /app/argus
COPY --from=builder /out/ocrshim /app/ocrshim
COPY plugins.example.yaml /app/plugins.yaml
COPY skills /app/skills
# 工作副本沙箱与 SQLite 库放卷上，容器重建不丢任务
RUN mkdir -p /data && chown -R argus:argus /app /data
USER 1000:1000

LABEL org.opencontainers.image.title="argus" \
      org.opencontainers.image.description="可插拔多 Agent 审查平台（PR Bot / TaskAPI）" \
      org.opencontainers.image.source="https://git.example.com/demo-user/argus-pilot" \
      org.opencontainers.image.vendor="enjoydream"

ENV ARGUS_ADDR=:8899 \
    ARGUS_DB_DSN=/data/argus.db \
    ARGUS_WORK_ROOT=/data/work \
    ARGUS_PLUGINS=/app/plugins.yaml \
    ARGUS_SKILLS_DIR=/app/skills
EXPOSE 8899
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8899/readyz || exit 1

ENTRYPOINT ["/app/argus"]
