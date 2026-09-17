<!-- 面向 Forgejo/Gitea 与 GitHub 双兼容：.github/ 目录两家都读 -->

## 变更说明

<!-- 做什么 / 为什么；关联 issue、EXPERIMENTS 条目或生产事件 -->

## 变更类型

- [ ] 缺陷修复
- [ ] 新功能
- [ ] 文档
- [ ] 重构 / 卫生
- [ ] 其他

## golden 回归门禁（评测文化门禁，v3.14.0）

本 PR 触及以下任一项时，**必须附最近一次 golden 回归数字**（模型/插件/prompt
默认行为变更没有数字不放行——REPORT §5.3 G1 的「质量评估文化」落点）：

- [ ] LLM prompt 模板（R1/R3/R3.5/Reviewer/QA）
- [ ] 模型默认值 / StageRouter 默认路由（含 plugins.example.yaml、deploy env 示例）
- [ ] R5 裁决规则 / 合并去重 / 完备性裁判语义

触及则填写（打桩 `make eval` 必全过；真实 `make eval-real` 命中率随附）：

```
make eval    : __/__ 通过
make eval-real: __/26（模型：____，route：____，已登记 eval/EXPERIMENTS.md）
```

- [ ] 本 PR 不触及上述项，无需附数字

## 自检

- [ ] `make test`、`make lint` 通过（CI 会跑 golden-eval 门禁）
- [ ] 行为变更已补测试
- [ ] 用户可见变更已记 CHANGELOG
