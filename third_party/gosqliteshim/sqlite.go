// Package sqlite 是 github.com/glebarez/go-sqlite 的 replace 挡板（v3.151.0）。
//
// 背景：glebarez/go-sqlite 与 modernc.org/sqlite 是同一驱动的两份拷贝，
// 两侧 init 都 sql.Register("sqlite")——agent-memory（storage/sqlite 直引
// modernc.org/sqlite 驱动）与 glebarez/sqlite（GORM dialector，import
// go-sqlite 仅取 Error 类型做约束翻译）同链一个二进制时
// 「Register called twice for driver sqlite」直接 panic。
//
// 本挡板：类型别名到 modernc.org/sqlite（错误类型同一性保持：dialector 的
// Translate 捕获的正是 modernc 驱动返回的 *sqlite.Error），不携带任何 init
// 注册——全进程唯一的 "sqlite" 驱动注册由 modernc.org/sqlite 完成。
// glebarez/sqlite v1.11.0 对 go-sqlite 的全部引用面 = 仅 gosqlite.Error
// 一个类型（已逐符号核实）；dialector 开路经字符串驱动名 sql.Open("sqlite")，
// 不经 go-sqlite 的 Driver 类型，故无需 shim 其他符号。
//
// 上游若把 dialector 对 go-sqlite 的引用面扩大，编译即断（显式失败，不静默）。
package sqlite

import "modernc.org/sqlite"

// Error 驱动错误（唯一被 glebarez/sqlite 引用的符号）。
type Error = sqlite.Error
