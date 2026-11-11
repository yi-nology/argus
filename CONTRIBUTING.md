# Contributing

## 开发环境

- Go 版本以 `go.mod` 为准；依赖走 `vendor/`（`go mod vendor` 同步）。
- 本地门禁与 CI 完全一致（命令单源在 `Makefile`，流水线只调 make 目标），提交前跑：

  ```bash
  make test    # 全量测试（-race，与 CI test-sqlite / test-mysql 同参）
  make lint    # 静态门禁三合一：vet + staticcheck（钉版本）+ gofmt
  make arch    # 可执行架构守卫（分层白名单 + 组合根单向性 + 包文档）
  make eval    # golden PR 评测集（打桩，固化编排语义）
  ```

  过 = CI 对应 job 绿。MySQL 路径由 CI `test-mysql` job 覆盖（本地可 `docker compose up mysql` + `make it`）。
  `make check`（govulncheck）按发布窗口跑，不阻塞日常提交。

## 修复与改动准则

- **根因修复**：先定位哪个判断/数据/调用链错了，修源头；不在输出层加 special case、clamp 或伪造默认值遮症状。
- **可观测性随行**：新分支、降级、外部失败必须能从 logs/仪表分辨；不引入吞错误的 default/0 分支。
- **行为契约改前先看真实分布**：阈值与门限改动先跑 `gatecheck` / `make it` 对照生产形态，不拍脑袋调数。
- **命名双语规范**：导出类型/方法 GoDoc 英文 + 同行中文精译；业务行内注释中文；错误串小写、动作明确。

## 代码组织惯例

- **service 域双命名**：`service.go` 及 `service_<域>.go`（service_comment.go 等）=服务门面主文件；`<域>.go`（fix.go/knowledge 相关等）=同域内聚面拆分件。新增文件按「改动归属的内聚面」选名，不强求统一——门面主文件与域拆分件并存是既有形态（v6.3.0 包文档批裁定）。
- **包注释恰一文件**：每包包注释只在 `doc.go`（或唯一持有 Package 注释的文件）；其余文件头注释与 `package` 子句间留空行（go doc 不拼接）。

## 提交

- 单个 commit 聚焦一个语义单元；CHANGELOG 同批记录（沿用 `CHANGELOG.md` 版本段格式）。
- 大版本段落按批次设计记录版本号；不在代码里留死的 design 章节引用（改版号用「design §X」模糊锚）。
