# Argus 构建与运行（学习期本机/内网；集群部署走 buildkit+ArgoCD）
BINARY  := argus
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFILES := $(shell find . -name "*.go")

.PHONY: help build build-ocrshim run test vet lint fmt arch e2e it eval eval-real check docker compose-up compose-down k8s-apply clean

help:
	@echo "make build   - 编译二进制（静态，可跨平台）"
	@echo "make build-ocrshim - 编译 ocrshim sidecar 二进制（bin/ocrshim）"
	@echo "make run     - 本地运行（自动加载 lacus/.env 与 ./.env）"
	@echo "make test    - 单元测试（不联网）"
	@echo "make lint    - staticcheck 静态检查（排除 biz/model 生成代码）"
	@echo "make arch    - 可执行架构守卫：分层白名单/组合根单向性/包文档完整性"
	@echo "make e2e     - E2E 双打桩场景集（不联网，mock LLM）"
	@echo "make it      - 真实 LLM 集成测试（ARGUS_IT=1，走 lacus/.env 的 OPENAI_*）"
	@echo "make eval    - golden PR 评测集·打桩回归（编排语义固化，不联网）"
	@echo "make eval-real - golden PR 评测·真实模型 A/B（ARGUS_EVAL=1，verdict 命中率）"
	@echo "make check   - govulncheck 漏洞扫描"
	@echo "make docker  - 构建镜像 argus:$(VERSION)"
	@echo "make compose-up / compose-down - docker-compose 部署（deploy/docker-compose）"
	@echo "make k8s-apply - kustomize 部署到当前集群（deploy/k8s）"
	@echo "make clean   - 清理本地产物"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) .

build-ocrshim:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/ocrshim ./cmd/ocrshim

run: build
	./bin/$(BINARY)

test:
	go test ./... -count=1

e2e:
	go test ./test/e2e/ -count=1 -v

# 可执行架构守卫（v3.62.0）：分层白名单+组合根单向性+包文档完整性；
# 依赖方向变更必须先改 test/arch 白名单（=架构变更显式化）
arch:
	go test ./test/arch/ -count=1 -v

it:
	ARGUS_IT=1 go test ./test/e2e/ -run TestIntegration -count=1 -v

# golden PR 评测集（v3.10.0，G1a）：打桩回归固化编排语义；真实模式做模型 A/B
eval:
	go test ./test/e2e/ -run TestGoldenReviewEval -count=1 -v

eval-real:
	ARGUS_EVAL=1 go test ./test/e2e/ -run TestGoldenReviewEval -count=1 -v -timeout 150m

vet:
	go vet ./...

# staticcheck 钉版本：CI 与本地缺失时安装同一版本，避免 @latest 漂移把
# "新检查器报新问题"冒充代码回归；升级版本是显式动作（改这个变量 + 过一次全量）。
STATICCHECK_VERSION ?= 2026.2.1

lint:
	@command -v staticcheck >/dev/null 2>&1 || GOFLAGS= go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)
	staticcheck $$(go list ./... | grep -v '/biz/model/')
	@unformatted=$$(gofmt -l internal cmd test 2>/dev/null); \
		if [ -n "$$unformatted" ]; then echo "gofmt 未格式化:"; echo "$$unformatted"; exit 1; fi

fmt:
	gofmt -l -w $(shell find internal cmd test -name "*.go" 2>/dev/null)

check:
	@command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

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
