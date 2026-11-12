# Argus 构建与运行（学习期本机/内网；集群部署走 buildkit+ArgoCD）
BINARY  := argus
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
# 版本元数据注入（v3.147.0）：main.version（报告头/AgentCard）与 oekit core/version.Version
# （/version 端点）同值；BuildCommit/BuildBranch/BuildTime 只进 core/version。
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
OEKIT_VER  := github.com/oekit/core/version
LDFLAGS    := -s -w -X main.version=$(VERSION) -X $(OEKIT_VER).Version=$(VERSION) -X $(OEKIT_VER).BuildCommit=$(GIT_COMMIT) -X $(OEKIT_VER).BuildBranch=$(GIT_BRANCH) -X $(OEKIT_VER).BuildTime=$(BUILD_TIME)
GOFILES := $(shell find . -name "*.go")

.PHONY: help build build-ocrshim run test vet lint fmt arch e2e it eval eval-real eval-baseline chaos check docker compose-up compose-down k8s-apply clean

help:
	@echo "make build   - 编译二进制（静态，可跨平台）"
	@echo "make build-ocrshim - 编译 ocrshim sidecar 二进制（bin/ocrshim）"
	@echo "make run     - 本地运行（自动加载 lacus/.env 与 ./.env）"
	@echo "make test    - 全量测试（-race，与 CI test job 同参）"
	@echo "make lint    - 静态门禁三合一：vet + staticcheck + gofmt（与 CI lint job 同参）"
	@echo "make arch    - 可执行架构守卫：分层白名单/组合根单向性/包文档完整性"
	@echo "make e2e     - E2E 双打桩场景集（不联网，mock LLM）"
	@echo "make it      - 真实 LLM 集成测试（ARGUS_IT=1，走 lacus/.env 的 OPENAI_*）"
	@echo "make eval    - golden PR 评测集·打桩回归（编排语义固化，不联网）"
	@echo "make eval-real - golden PR 评测·真实模型 A/B（ARGUS_EVAL=1，verdict 命中率）"
	@echo "make eval-baseline - 基准集评测跑批·打桩（P2-02 #25，指标报告样例）"
	@echo "make chaos   - 混沌测试矩阵·五族故障实弹回归（P2-01 #24，报告落盘）"
	@echo "make check   - govulncheck 漏洞扫描"
	@echo "make docker  - 构建镜像 argus:$(VERSION)"
	@echo "make compose-up / compose-down - docker-compose 部署（deploy/docker-compose）"
	@echo "make k8s-apply - kustomize 部署到当前集群（deploy/k8s）"
	@echo "make clean   - 清理本地产物"

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

build-ocrshim:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/ocrshim ./cmd/ocrshim

run: build
	./bin/$(BINARY)

# 与 CI test-sqlite/test-mysql job 同参：-race 抓数据竞争，-timeout 防挂死拖垮流水线。
# 命令口径单源在此；CI 只调 make 目标，不各自拼参数。
test:
	go test ./... -count=1 -race -timeout=300s

e2e:
	go test ./test/e2e/ -count=1 -v

# 可执行架构守卫（v3.62.0）：分层白名单+组合根单向性+包文档完整性；
# 依赖方向变更必须先改 test/arch 白名单（=架构变更显式化）
arch:
	go test ./test/arch/ -count=1 -v

# IDL 再生成（v6.3.0）：改 idl/*.thrift 后必跑，生成物收编进提交；
# CI idl-drift job 用同命令 + git diff 断言同步（防生成物手改失真）。
idl-gen:
	hz update --idl idl/argus.thrift --module github.com/yi-nology/argus
	@command -v goimports >/dev/null 2>&1 || GOFLAGS= go install golang.org/x/tools/cmd/goimports@latest
	goimports -w biz/handler/argus/ # hz 每次 update 机械重写 handler import（stub 视角，可能悬空），goimports 收敛为函数体真实依赖

it:
	ARGUS_IT=1 go test ./test/e2e/ -run TestIntegration -count=1 -v

# golden PR 评测集（v3.10.0，G1a）：打桩回归固化编排语义；真实模式做模型 A/B
eval:
	go test ./test/e2e/ -run TestGoldenReviewEval -count=1 -v

eval-real:
	ARGUS_EVAL=1 go test ./test/e2e/ -run TestGoldenReviewEval -count=1 -v -timeout 150m

# 基准集评测跑批（P2-02，#25）：装载 eval/baseline 打桩跑批出指标报告样例
# （artifact 面 eval/results/baseline-latest.*）；真实模型评测经
# internal/evalq.Reviewer 接口装配（不联网，指标口径单源见 docs/eval-baseline.md）。
eval-baseline:
	go test ./test/evalq/ -run TestEvalPipelineStub -count=1 -v

# 混沌测试矩阵（P2-01，#24）：五族故障 × 注入点逐格实弹回归，
# 报告落 test/chaos/results/（chaos-latest.{json,md}，CI artifact 面）。
chaos:
	go test ./test/chaos/ -run TestChaosMatrix -count=1 -v

vet:
	go vet ./...

# staticcheck 钉版本：CI 与本地缺失时安装同一版本，避免 @latest 漂移把
# "新检查器报新问题"冒充代码回归；升级版本是显式动作（改这个变量 + 过一次全量）。
# 2026.2.2：go toolchain 1.27.2 起 export data v5，2026.2.1 内嵌 x/tools 只认到 v4
# （"export data version 5 is greater than maximum supported version 4"）。
STATICCHECK_VERSION ?= 2026.2.2

# 静态门禁三合一（与 CI lint job 同参）：vet + staticcheck + gofmt。
# 命令口径单源在此——本地 make lint 过 = CI lint 过，不再各自拼参数。
lint: vet
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
