# Argus 审查标准

## 严重级别定义

### HIGH（高危）
安全漏洞（SQL 注入/XSS/硬编码凭证/路径遍历）。
数据丢失风险（未处理 error 导致数据不一致）。
并发竞态（共享状态无锁保护）。
资源泄漏（goroutine/连接/文件句柄未关闭）。

### MEDIUM（中等）
错误处理不当（error 被忽略但不影响核心逻辑）。
性能问题（N+1 查询/不必要的内存分配/阻塞调用在热路径）。
代码重复（3 处以上相同逻辑）。
缺少输入校验（用户输入直接使用）。

### LOW（低）
命名不规范（不影响可读性）。
缺少注释（复杂逻辑无说明）。
代码风格（行过长/缩进不一致）。
未使用的变量/导入。

## 审查流程

R1 需求解析：从 PR 描述和关联 issue 提取验收清单。
R2 变更理解：分析 diff，理解变更范围。
R3 完成度判定：逐条 AC 核对实现状态。
R4 专家委派：并行调用各插件（security/ocr 等）。
R5 汇总裁决：确定性真值表，不走 LLM。

## 裁决规则

R5.1：有 missing/partial → REQUEST_CHANGES。
R5.2：有 high finding → REQUEST_CHANGES。
R5.3：有 unverifiable/ambiguous → COMMENT（需人工确认）。
R5.4：有 medium/low finding → COMMENT。
R5.5：全部 implemented 且零 finding → APPROVE。
