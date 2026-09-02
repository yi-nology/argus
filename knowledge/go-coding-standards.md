# Go 编码规范

## 错误处理

所有 error 返回值必须显式处理，禁止用 `_` 忽略。
错误应向上层传递并添加上下文信息（`fmt.Errorf("xxx: %w", err)`）。
不得在循环中使用 `panic`/`os.Exit`，应返回 error。

## 函数设计

单个函数不超过 50 行（不含注释）。
函数参数超过 4 个时应使用结构体封装。
优先返回 `error` 而非 `bool` 表示失败。

## 并发安全

共享状态必须用 `sync.Mutex` 或 `sync.RWMutex` 保护。
`context.Context` 作为函数第一个参数传递。
goroutine 必须有退出机制（ctx.Done / channel close）。
禁止裸 `go func()`——使用 `async.GoSafe` 或等效 panic 恢复。

## 命名规范

包名小写单词，不带下划线（`strconv` 不是 `str_conv`）。
接口名以 `er` 结尾（`Reader`, `Writer`）或描述行为（`Pool`, `Store`）。
布尔变量/函数以 `Is/Has/Can/Should` 前缀。
常量使用 `CamelCase` 或 `SCREAMING_SNAKE_CASE`（视包约定）。

## 测试

测试文件 `_test.go` 与被测文件同包。
表驱动测试优先（`cases := []struct{...}`）。
测试名 `TestXxx_Yyy` 格式，覆盖正常/边界/错误场景。
