# 常见误报记录

## 已知误报模式

### go.sum 变更
go.sum 文件的 hash 变更是正常依赖更新，不应报 finding。
Argus 已在 diffprep 阶段自动过滤 go.sum（噪声路径规则）。

### 生成代码变更
.pb.go / _pb2.py / .min.js 等生成文件不应审查。
Argus 已在 diffprep 阶段自动过滤（噪声路径规则）。

### 测试文件中的安全问题
测试文件中的硬编码密码/token 是测试桩，不是安全漏洞。
建议：测试文件中的凭证使用明显的假值（如 `test-password-123`）。

### vendor 目录变更
vendor/ 目录是第三方代码，不应审查。
Argus 已在 diffprep 阶段自动过滤。

## 误报反馈机制

通过 POST /findings/suppress 指定指纹标记误报。
被标记的 finding 在后续审查中自动抑制。
误报记录存储在 finding_prints 表（status=suppressed）。

## 评审注意事项

diff 中找不到实现 ≠ 没有实现（可能在其他文件/分支）。
R3 会用 get_file(目标分支) 核实，避免误判。
unverifiable 不是拒绝理由，是"需要人工确认"。
