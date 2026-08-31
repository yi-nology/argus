# Argus 构建与运行（学习期本机/内网；集群部署走 buildkit+ArgoCD）
BINARY  := argus
VERSION ?= v3.0.2
GOFILES := $(shell find . -name "*.go")

.PHONY: help build run test vet fmt e2e it docker compose-up compose-down k8s-apply clean

help:
	@echo "make build   - 编译二进制（静态，可跨平台）"
	@echo "make run     - 本地运行（自动加载 lacus/.env 与 ./.env）"
	@echo "make test    - 单元测试（不联网）"
	@echo "make e2e     - E2E 双打桩九场景（不联网，mock LLM）"
	@echo "make it      - 真实 LLM 集成测试（ARGUS_IT=1，走 lacus/.env 的 OPENAI_*）"
	@echo "make check   - vet + fmt 检查"
	@echo "make docker  - 构建镜像 argus:$(VERSION)"
	@echo "make compose-up / compose-down - docker-compose 部署（deploy/docker-compose）"
	@echo "make k8s-apply - kustomize 部署到当前集群（deploy/k8s）"
	@echo "make clean   - 清理本地产物"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/$(BINARY) .

run: build
	./bin/$(BINARY)

test:
	go test ./... -count=1

e2e:
	go test ./test/e2e/ -count=1 -v

it:
	ARGUS_IT=1 go test ./test/e2e/ -run TestIntegration -count=1 -v

vet:
	go vet ./...

fmt:
	gofmt -l -w $(shell find . -name "*.go" -not -path "./biz/model/*")

check: vet
	@out=$$(gofmt -l . 2>/dev/null | grep -v -e '^biz/model/' || true); \
	if [ -n "$$out" ]; then echo "未格式化文件:"; echo "$$out"; exit 1; fi; echo "fmt ok"

docker:
	# go mod 模式（v3.1）：依赖经 module proxy / SSH 直拉（见 Dockerfile），不再 vendor
	DOCKER_BUILDKIT=1 docker build --ssh default -t argus:$(VERSION) .

compose-up:
	docker compose -f deploy/docker-compose/compose.yaml -p argus up -d --build

compose-down:
	docker compose -f deploy/docker-compose/compose.yaml -p argus down

k8s-apply:
	kubectl apply -k deploy/k8s

clean:
	rm -rf bin argus.db argus.db-shm argus.db-wal
